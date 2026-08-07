// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

// Package crypto implements Harbor's client-side, end-to-end note encryption —
// the part the server never sees. Harbor is zero-knowledge: the server stores
// opaque ciphertext and an opaque per-user keystore and never holds, derives, or
// validates any key. All key derivation, wrapping, and field encryption happens
// here, in the client.
//
// This package is the canonical CLI implementation of the interop contract. Two
// formats must match byte-for-byte across every Harbor client:
//
//   - HRBK1 keystore — a JSON blob holding the KEK salt, Argon2id parameters, and
//     the wrapped master key. It is synced as one opaque per-user record.
//   - HRBC2 field envelope — the per-field ciphertext string stored in a note's
//     title and content: "HRBC2" "." base64url(iv12) "." base64url(ct‖tag16).
//
// Key model (envelope encryption): a single passphrase + a per-user random salt
// derive a Key-Encryption-Key (KEK) via Argon2id; a random 256-bit master key is
// generated once and wrapped with the KEK in the keystore; every note field is
// encrypted with the master key (AES-256-GCM, fresh 12-byte nonce per op). This
// is why a passphrase change is cheap — re-wrap the master key, no note re-encrypt.
//
// All base64url is UNPADDED (RawURLEncoding), matching the server's structural
// validator. The AEAD additional-authenticated-data (AAD) for a field is the
// UTF-8 bytes of "<noteID>:<fieldName>" — the note id, a literal colon, then
// "title" or "content" — so an envelope cannot be transplanted to another note
// or field. That exact byte sequence is the cross-client interop contract shared
// with web, macOS/iOS, Android and Windows; see fieldAAD and crypto/README.md.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Canonical format constants. These are part of the cross-client wire contract;
// do not change them without coordinating every Harbor client.
const (
	// KeystoreVersion tags the HRBK1 keystore blob.
	KeystoreVersion = "HRBK1"
	// EnvelopeVersion tags an HRBC2 per-field ciphertext envelope.
	EnvelopeVersion = "HRBC2"
	// kdfName is the only supported KDF.
	kdfName = "argon2id"

	masterKeyLen = 32 // AES-256 master key
	kekLen       = 32 // Argon2id output (also AES-256)
	saltLen      = 16 // KEK salt
	nonceLen     = 12 // AES-GCM nonce / IV
	tagLen       = 16 // AES-GCM tag
)

// Sentinel errors callers branch on to render friendly messages.
var (
	// ErrBadPassphrase means the wrapped master key would not unwrap — almost
	// always a wrong passphrase (GCM authentication failed).
	ErrBadPassphrase = errors.New("incorrect passphrase: could not unwrap the master key")
	// ErrNotEnvelope means a string is not a structurally-valid HRBC2 envelope.
	ErrNotEnvelope = errors.New("not an HRBC2 ciphertext envelope")
	// ErrDecrypt means an envelope failed to decrypt or authenticate (wrong key,
	// wrong AAD, or corrupt data).
	ErrDecrypt = errors.New("decryption failed: wrong key or corrupt data")
	// ErrBadKeystore means the keystore blob is malformed or an unknown version.
	ErrBadKeystore = errors.New("malformed or unsupported keystore")
)

// Argon2Params are the Argon2id cost parameters carried in the keystore so every
// device derives the same KEK from the passphrase.
type Argon2Params struct {
	MemKiB      uint32 // m — memory in KiB
	Iterations  uint32 // t — passes
	Parallelism uint8  // p — lanes
}

// DefaultArgon2Params is the canonical profile from docs/encryption.md
// (RFC 9106): m = 64 MiB, t = 3, p = 1.
var DefaultArgon2Params = Argon2Params{MemKiB: 65536, Iterations: 3, Parallelism: 1}

// Keystore is the parsed HRBK1 blob. The on-disk/on-wire form is exactly this
// JSON object (snake_case field names are part of the contract). Salt and
// WrappedKey are unpadded base64url; WrappedKey decodes to iv(12)‖ct‖tag(16).
type Keystore struct {
	Version     string `json:"version"`
	KDF         string `json:"kdf"`
	KDFMemKiB   uint32 `json:"kdf_mem_kib"`
	KDFIter     uint32 `json:"kdf_iterations"`
	KDFParallel uint8  `json:"kdf_parallelism"`
	Salt        string `json:"salt"`
	WrappedKey  string `json:"wrapped_key"`
}

// b64 is the canonical unpadded base64url codec used for every binary field in
// both the keystore and the HRBC2 envelope.
var b64 = base64.RawURLEncoding

// DeriveKEK derives the 32-byte Key-Encryption-Key from the passphrase and salt
// using Argon2id with the given parameters. The same (passphrase, salt, params)
// always yields the same KEK, which is what lets any device unwrap the keystore.
func DeriveKEK(passphrase string, salt []byte, p Argon2Params) []byte {
	return argon2.IDKey([]byte(passphrase), salt, p.Iterations, p.MemKiB, p.Parallelism, kekLen)
}

// params returns the keystore's Argon2 parameters as an Argon2Params value.
func (k *Keystore) params() Argon2Params {
	return Argon2Params{MemKiB: k.KDFMemKiB, Iterations: k.KDFIter, Parallelism: k.KDFParallel}
}

// ParseKeystore decodes and structurally validates an HRBK1 keystore blob,
// rejecting unknown versions or KDFs so a client never silently mis-derives.
func ParseKeystore(blob string) (*Keystore, error) {
	var k Keystore
	if err := json.Unmarshal([]byte(blob), &k); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKeystore, err)
	}
	if k.Version != KeystoreVersion {
		return nil, fmt.Errorf("%w: version %q (want %q)", ErrBadKeystore, k.Version, KeystoreVersion)
	}
	if k.KDF != kdfName {
		return nil, fmt.Errorf("%w: kdf %q (want %q)", ErrBadKeystore, k.KDF, kdfName)
	}
	if k.KDFMemKiB == 0 || k.KDFIter == 0 || k.KDFParallel == 0 {
		return nil, fmt.Errorf("%w: zero kdf parameter", ErrBadKeystore)
	}
	if _, err := b64.DecodeString(k.Salt); err != nil {
		return nil, fmt.Errorf("%w: bad salt", ErrBadKeystore)
	}
	if _, err := b64.DecodeString(k.WrappedKey); err != nil {
		return nil, fmt.Errorf("%w: bad wrapped_key", ErrBadKeystore)
	}
	return &k, nil
}

// Marshal serializes the keystore to its canonical JSON blob form.
func (k *Keystore) Marshal() (string, error) {
	b, err := json.Marshal(k)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NewKeystore performs first-time setup: it generates a random master key and a
// random salt, derives the KEK from the passphrase, wraps the master key, and
// returns the serialized HRBK1 blob together with the in-memory master key. The
// master key is the long-lived secret; losing the passphrase that wraps it makes
// the key (and all ciphertext) unrecoverable — there is no escrow.
func NewKeystore(passphrase string, p Argon2Params) (blob string, masterKey []byte, err error) {
	masterKey = make([]byte, masterKeyLen)
	if _, err = rand.Read(masterKey); err != nil {
		return "", nil, fmt.Errorf("generating master key: %w", err)
	}
	salt := make([]byte, saltLen)
	if _, err = rand.Read(salt); err != nil {
		return "", nil, fmt.Errorf("generating salt: %w", err)
	}
	kek := DeriveKEK(passphrase, salt, p)
	wrapped, err := gcmSeal(kek, masterKey, nil)
	if err != nil {
		return "", nil, fmt.Errorf("wrapping master key: %w", err)
	}
	k := &Keystore{
		Version:     KeystoreVersion,
		KDF:         kdfName,
		KDFMemKiB:   p.MemKiB,
		KDFIter:     p.Iterations,
		KDFParallel: p.Parallelism,
		Salt:        b64.EncodeToString(salt),
		WrappedKey:  b64.EncodeToString(wrapped),
	}
	blob, err = k.Marshal()
	if err != nil {
		return "", nil, err
	}
	return blob, masterKey, nil
}

// UnwrapMasterKey derives the KEK from the passphrase and the keystore's salt and
// uses it to unwrap (decrypt) the master key. A wrong passphrase surfaces as
// ErrBadPassphrase rather than a raw GCM error.
func UnwrapMasterKey(k *Keystore, passphrase string) ([]byte, error) {
	salt, err := b64.DecodeString(k.Salt)
	if err != nil {
		return nil, fmt.Errorf("%w: bad salt", ErrBadKeystore)
	}
	wrapped, err := b64.DecodeString(k.WrappedKey)
	if err != nil {
		return nil, fmt.Errorf("%w: bad wrapped_key", ErrBadKeystore)
	}
	kek := DeriveKEK(passphrase, salt, k.params())
	masterKey, err := gcmOpen(kek, wrapped, nil)
	if err != nil {
		return nil, ErrBadPassphrase
	}
	if len(masterKey) != masterKeyLen {
		return nil, fmt.Errorf("%w: unexpected master key length", ErrBadKeystore)
	}
	return masterKey, nil
}

// RewrapMasterKey rotates the passphrase: it unwraps the master key with the old
// passphrase, then re-wraps the SAME master key under a new KEK derived from the
// new passphrase and a fresh salt. No note is re-encrypted. Returns the new blob.
func RewrapMasterKey(k *Keystore, oldPass, newPass string, p Argon2Params) (string, error) {
	masterKey, err := UnwrapMasterKey(k, oldPass)
	if err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	kek := DeriveKEK(newPass, salt, p)
	wrapped, err := gcmSeal(kek, masterKey, nil)
	if err != nil {
		return "", fmt.Errorf("wrapping master key: %w", err)
	}
	nk := &Keystore{
		Version:     KeystoreVersion,
		KDF:         kdfName,
		KDFMemKiB:   p.MemKiB,
		KDFIter:     p.Iterations,
		KDFParallel: p.Parallelism,
		Salt:        b64.EncodeToString(salt),
		WrappedKey:  b64.EncodeToString(wrapped),
	}
	return nk.Marshal()
}

// fieldAAD builds the AEAD additional-authenticated-data for a note field: the
// UTF-8 bytes of "<recordID>:<fieldName>". This binds an envelope to exactly one
// note and one field, so a content envelope can never be replayed as that note's
// title nor moved to another note.
//
// The colon is load-bearing and is NOT a local choice: this exact byte sequence
// is the cross-client interop contract, defined canonically by the web client
// (app.harbor.my web/src/lib/crypto/envelope.ts) and matched byte-for-byte by
// macOS/iOS, Android and Windows. Change it and notes sealed here stop opening
// everywhere else. See crypto/README.md and the crossClientVector literal pinned
// in crypto_test.go.
func fieldAAD(recordID, fieldName string) []byte {
	return []byte(recordID + ":" + fieldName)
}

// SealField encrypts a note field's plaintext into an HRBC2 envelope under the
// master key, with a fresh random nonce and AAD bound to (recordID, fieldName).
// fieldName must be the literal "title" or "content".
func SealField(masterKey []byte, recordID, fieldName, plaintext string) (string, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	return sealFieldWithNonce(masterKey, recordID, fieldName, plaintext, nonce)
}

// sealFieldWithNonce is SealField with the nonce supplied rather than generated.
// It exists so tests can reproduce the shared cross-client known-answer vector
// byte for byte, the same way harbor-swift and harbor-windows make their nonce
// injectable. Production code must always use SealField: reusing a nonce under
// the same key breaks AES-GCM catastrophically.
func sealFieldWithNonce(masterKey []byte, recordID, fieldName, plaintext string, nonce []byte) (string, error) {
	if len(nonce) != nonceLen {
		return "", fmt.Errorf("nonce must be %d bytes, got %d", nonceLen, len(nonce))
	}
	aead, err := newAEAD(masterKey)
	if err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, []byte(plaintext), fieldAAD(recordID, fieldName))
	return EnvelopeVersion + "." + b64.EncodeToString(nonce) + "." + b64.EncodeToString(ct), nil
}

// OpenField decrypts an HRBC2 envelope back to plaintext under the master key,
// using AAD bound to (recordID, fieldName). It returns ErrNotEnvelope for a
// non-envelope string and ErrDecrypt when authentication fails (wrong key/AAD or
// corrupt data).
func OpenField(masterKey []byte, recordID, fieldName, envelope string) (string, error) {
	iv, ct, err := splitEnvelope(envelope)
	if err != nil {
		return "", err
	}
	aead, err := newAEAD(masterKey)
	if err != nil {
		return "", err
	}
	pt, err := aead.Open(nil, iv, ct, fieldAAD(recordID, fieldName))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(pt), nil
}

// IsEnvelope reports whether s is a structurally well-formed HRBC2 envelope,
// mirroring the server's looksLikeEnvelope check (3 dot parts, "HRBC2" tag,
// base64url-decodable, 12-byte IV, ≥16-byte ciphertext). It never decrypts.
func IsEnvelope(s string) bool {
	_, _, err := splitEnvelope(s)
	return err == nil
}

// splitEnvelope parses and validates an HRBC2 envelope's structure, returning the
// decoded IV and ciphertext(‖tag). It enforces the same shape rules as the
// server so anything the CLI considers an envelope will also pass server-side
// structural validation.
func splitEnvelope(s string) (iv, ct []byte, err error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != EnvelopeVersion {
		return nil, nil, ErrNotEnvelope
	}
	iv, e1 := b64.DecodeString(parts[1])
	ct, e2 := b64.DecodeString(parts[2])
	if e1 != nil || e2 != nil || len(iv) != nonceLen || len(ct) < tagLen {
		return nil, nil, ErrNotEnvelope
	}
	return iv, ct, nil
}

// newAEAD builds an AES-256-GCM AEAD from a 32-byte key, rejecting a wrong-length
// key so a bug can never silently weaken the cipher.
func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != kekLen {
		return nil, fmt.Errorf("invalid key length %d (want %d)", len(key), kekLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// gcmSeal encrypts plaintext with AES-256-GCM under key and a fresh random nonce,
// returning iv(12)‖ciphertext‖tag(16). Used to wrap the master key.
func gcmSeal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

// gcmOpen reverses gcmSeal: it splits iv(12)‖ciphertext‖tag and authenticates +
// decrypts. Used to unwrap the master key.
func gcmOpen(key, blob, aad []byte) ([]byte, error) {
	if len(blob) < nonceLen+tagLen {
		return nil, ErrDecrypt
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce, ct := blob[:nonceLen], blob[nonceLen:]
	return aead.Open(nil, nonce, ct, aad)
}

// NewUUIDv4 returns a random RFC 4122 version-4 UUID. Encrypted notes must be
// created with a client-supplied id because the field AAD binds to that id, so
// the id has to exist before the title/content are sealed.
func NewUUIDv4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

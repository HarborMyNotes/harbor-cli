// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// fastParams is a cheap Argon2id profile so the keystore tests stay quick while
// still exercising the real KDF.
var fastParams = Argon2Params{MemKiB: 8192, Iterations: 1, Parallelism: 1}

// sealRefAAD independently builds an HRBC2 envelope from the documented recipe —
// a fixed nonce, AES-256-GCM under the master key, an explicitly supplied AAD,
// and UNPADDED base64url. Taking the AAD as a parameter (rather than deriving it
// the way the production code does) is deliberate: this helper must be able to
// build a DELIBERATELY WRONG envelope for the negative tests, and a helper that
// silently agrees with the code under test is exactly how #86 stayed hidden.
func sealRefAAD(t *testing.T, masterKey []byte, aad, plaintext string, nonce []byte) string {
	t.Helper()
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	ct := g.Seal(nil, nonce, []byte(plaintext), []byte(aad))
	enc := base64.RawURLEncoding
	return "HRBC2." + enc.EncodeToString(nonce) + "." + enc.EncodeToString(ct)
}

// sealRef builds an HRBC2 envelope using the CANONICAL cross-client AAD,
// utf8(id + ":" + field), so the tests prove OpenField conforms to the external
// contract rather than merely round-tripping against our own SealField.
func sealRef(t *testing.T, masterKey []byte, id, field, plaintext string, nonce []byte) string {
	t.Helper()
	return sealRefAAD(t, masterKey, id+":"+field, plaintext, nonce)
}

// TestOpenField_ReferenceVector proves OpenField decrypts an envelope built by
// the documented recipe, locking the format (HRBC2 tag, unpadded base64url,
// AAD = id + ":" + field) against accidental drift.
func TestOpenField_ReferenceVector(t *testing.T) {
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	nonce := []byte("0123456789ab") // 12 bytes, fixed
	id := "9c2e7b10-1a2b-3c4d-5e6f-7a8b9c0d1e2f"
	env := sealRef(t, masterKey, id, "content", "hello, harbor", nonce)

	got, err := OpenField(masterKey, id, "content", env)
	if err != nil {
		t.Fatalf("OpenField: %v", err)
	}
	if got != "hello, harbor" {
		t.Fatalf("plaintext = %q, want %q", got, "hello, harbor")
	}
}

// crossClientVector is the known-answer vector shared by every other Harbor
// client — generated from the web app (the canonical definition) and pinned
// identically in harbor-swift's HarborCryptoTests. Parameters: master key = the
// bytes 0x00..0x1f, nonce = the bytes 0x00..0x0b, record id "note-1", field
// "content", plaintext "Secret note body".
//
// It is a hardcoded LITERAL on purpose. #86 slipped through because the test
// helper recomputed the expected envelope with the same wrong AAD the code used,
// so the suite agreed with the bug. A literal cannot do that.
//
// legacyVector is the same parameters sealed with the pre-#86 separatorless AAD
// ("note-1content"). Pinned as a literal too, so the negative test asserts
// against the real broken output rather than something merely different. The
// ciphertext is identical to crossClientVector; only the GCM tag differs, which
// is why the bug surfaced as a clean auth failure rather than corruption.
const (
	crossClientVector    = "HRBC2.AAECAwQFBgcICQoL.FGe1aaCR4nXiNfKr04YcFISiyIPEgI0uXE_6tfgGWQw"
	legacyVector         = "HRBC2.AAECAwQFBgcICQoL.FGe1aaCR4nXiNfKr04YcFE51m3k-lGztdNYH6AGwEZU"
	crossClientID        = "note-1"
	crossClientField     = "content"
	crossClientPlaintext = "Secret note body"
)

// crossClientKey returns the vector's master key: the bytes 0x00 through 0x1f.
func crossClientKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

// crossClientNonce returns the vector's nonce: the bytes 0x00 through 0x0b.
func crossClientNonce() []byte {
	nonce := make([]byte, 12)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	return nonce
}

// TestOpenField_CrossClientVector proves this CLI can READ an envelope produced
// by web, macOS/iOS, Android or Windows, by decrypting their shared pinned
// vector. If this fails, notes encrypted on every other Harbor client have
// become unreadable here.
func TestOpenField_CrossClientVector(t *testing.T) {
	got, err := OpenField(crossClientKey(), crossClientID, crossClientField, crossClientVector)
	if err != nil {
		t.Fatalf("OpenField on the shared cross-client vector: %v (the field AAD no longer matches the other clients)", err)
	}
	if got != crossClientPlaintext {
		t.Fatalf("plaintext = %q, want %q", got, crossClientPlaintext)
	}
}

// TestSealField_CrossClientVector proves this CLI can be READ BY the other
// clients, in the strongest form available: sealing the vector's parameters with
// the vector's nonce must reproduce the shared envelope byte for byte. Anything
// less — a different AAD, tag order, or base64 flavour — changes the string.
func TestSealField_CrossClientVector(t *testing.T) {
	got, err := sealFieldWithNonce(crossClientKey(), crossClientID, crossClientField, crossClientPlaintext, crossClientNonce())
	if err != nil {
		t.Fatalf("sealFieldWithNonce: %v", err)
	}
	if got != crossClientVector {
		t.Fatalf("envelope does not match the shared cross-client vector — other clients cannot read what we seal\n got: %s\nwant: %s", got, crossClientVector)
	}
}

// TestSealField_ReadableByCanonicalReader proves the same property for the real,
// random-nonce SealField path that production actually uses: an independent
// reader supplying the canonical AAD must be able to open it.
func TestSealField_ReadableByCanonicalReader(t *testing.T) {
	key := crossClientKey()
	env, err := SealField(key, crossClientID, crossClientField, crossClientPlaintext)
	if err != nil {
		t.Fatalf("SealField: %v", err)
	}
	if env == crossClientVector {
		t.Fatal("SealField reused the vector's nonce — it must generate a fresh one")
	}
	got, err := openRefAAD(t, key, crossClientID+":"+crossClientField, env)
	if err != nil {
		t.Fatalf("a canonical-AAD reader could not open our envelope: %v (other clients would see 'could not decrypt')", err)
	}
	if got != crossClientPlaintext {
		t.Fatalf("plaintext = %q, want %q", got, crossClientPlaintext)
	}
}

// TestFieldAAD_RejectsLegacySeparatorless proves the pre-#86 AAD — the record id
// with the field name jammed on, no colon — no longer decrypts. This is the
// regression guard: the bug was a missing separator, so a test that only checks
// the happy path would pass again the moment someone "simplified" fieldAAD.
func TestFieldAAD_RejectsLegacySeparatorless(t *testing.T) {
	key := crossClientKey()

	// Built with the vector's own nonce, so this is the exact envelope the CLI
	// used to produce — and it must equal the pinned legacyVector literal.
	legacy := sealRefAAD(t, key, crossClientID+crossClientField, crossClientPlaintext, crossClientNonce())
	if legacy != legacyVector {
		t.Fatalf("legacy reference envelope drifted\n got: %s\nwant: %s", legacy, legacyVector)
	}
	if legacy == crossClientVector {
		t.Fatal("the separatorless AAD produced the canonical vector — the vector or the helper is wrong")
	}
	if _, err := OpenField(key, crossClientID, crossClientField, legacyVector); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("OpenField on a legacy separatorless envelope: err = %v, want ErrDecrypt", err)
	}

	// And the mirror: our own envelope must NOT open under the legacy AAD.
	env, err := SealField(key, crossClientID, crossClientField, crossClientPlaintext)
	if err != nil {
		t.Fatalf("SealField: %v", err)
	}
	if _, err := openRefAAD(t, key, crossClientID+crossClientField, env); err == nil {
		t.Fatal("our envelope opened under the legacy separatorless AAD — fieldAAD lost its separator")
	}
}

// openRefAAD independently decrypts an HRBC2 envelope under an explicitly
// supplied AAD, standing in for another Harbor client reading our output. It
// deliberately does not call fieldAAD, so it cannot inherit a mistake from the
// code under test.
func openRefAAD(t *testing.T, masterKey []byte, aad, envelope string) (string, error) {
	t.Helper()
	parts := strings.Split(envelope, ".")
	if len(parts) != 3 || parts[0] != EnvelopeVersion {
		t.Fatalf("not an HRBC2 envelope: %q", envelope)
	}
	enc := base64.RawURLEncoding
	iv, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("iv: %v", err)
	}
	ct, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("ciphertext: %v", err)
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	pt, err := g.Open(nil, iv, ct, []byte(aad))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// TestSealOpenRoundTrip proves SealField/OpenField round-trip for both fields and
// that a fresh nonce makes two seals of the same plaintext differ.
func TestSealOpenRoundTrip(t *testing.T) {
	masterKey := make([]byte, 32)
	id := "11111111-2222-3333-4444-555555555555"
	for _, field := range []string{"title", "content"} {
		env, err := SealField(masterKey, id, field, "secret "+field)
		if err != nil {
			t.Fatalf("SealField(%s): %v", field, err)
		}
		if !IsEnvelope(env) {
			t.Fatalf("SealField(%s) produced a non-envelope: %q", field, env)
		}
		got, err := OpenField(masterKey, id, field, env)
		if err != nil {
			t.Fatalf("OpenField(%s): %v", field, err)
		}
		if got != "secret "+field {
			t.Fatalf("round-trip(%s) = %q", field, got)
		}
	}
	a, _ := SealField(masterKey, id, "content", "same")
	b, _ := SealField(masterKey, id, "content", "same")
	if a == b {
		t.Fatal("two seals of the same plaintext should differ (nonce reuse)")
	}
}

// TestAAD_BindsNoteAndField proves an envelope cannot be opened under a different
// note id or field name — the AAD binding is enforced.
func TestAAD_BindsNoteAndField(t *testing.T) {
	masterKey := make([]byte, 32)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	env, err := SealField(masterKey, id, "content", "bound")
	if err != nil {
		t.Fatalf("SealField: %v", err)
	}
	// Wrong field name.
	if _, err := OpenField(masterKey, id, "title", env); !errors.Is(err, ErrDecrypt) {
		t.Errorf("open with wrong field: err = %v, want ErrDecrypt", err)
	}
	// Wrong note id.
	if _, err := OpenField(masterKey, "ffffffff-ffff-ffff-ffff-ffffffffffff", "content", env); !errors.Is(err, ErrDecrypt) {
		t.Errorf("open with wrong id: err = %v, want ErrDecrypt", err)
	}
	// Wrong key.
	other := make([]byte, 32)
	other[0] = 1
	if _, err := OpenField(other, id, "content", env); !errors.Is(err, ErrDecrypt) {
		t.Errorf("open with wrong key: err = %v, want ErrDecrypt", err)
	}
}

// TestIsEnvelope locks the structural rules and proves padded base64 and the
// wrong version token are rejected (matching the server validator).
func TestIsEnvelope(t *testing.T) {
	good := "HRBC2.AAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAA" // iv=12, ct=16
	if !IsEnvelope(good) {
		t.Error("well-formed envelope should validate")
	}
	bad := []string{
		"",
		"plaintext",
		"SPNC2.AAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAA",   // wrong version
		"HRBC2.AAAA.AAAAAAAAAAAAAAAAAAAAAA",               // iv too short
		"HRBC2.AAAAAAAAAAAAAAAA.AAAA",                     // ct too short
		"HRBC2.@@@.AAAAAAAAAAAAAAAAAAAAAA",                // bad base64
		"HRBC2.AAAAAAAAAAAAAAAA",                          // two parts
		"HRBC2.AAAAAAAAAAAAAAAA==.AAAAAAAAAAAAAAAAAAAAAA", // padded base64 → invalid for RawURLEncoding
	}
	for _, s := range bad {
		if IsEnvelope(s) {
			t.Errorf("expected %q to be rejected", s)
		}
	}
}

// TestKeystoreRoundTrip proves setup→parse→unwrap recovers the master key and a
// wrong passphrase fails cleanly as ErrBadPassphrase.
func TestKeystoreRoundTrip(t *testing.T) {
	blob, masterKey, err := NewKeystore("correct horse battery staple", fastParams)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	k, err := ParseKeystore(blob)
	if err != nil {
		t.Fatalf("ParseKeystore: %v", err)
	}
	got, err := UnwrapMasterKey(k, "correct horse battery staple")
	if err != nil {
		t.Fatalf("UnwrapMasterKey: %v", err)
	}
	if string(got) != string(masterKey) {
		t.Fatal("unwrapped master key does not match the generated one")
	}
	if _, err := UnwrapMasterKey(k, "wrong passphrase"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("wrong passphrase: err = %v, want ErrBadPassphrase", err)
	}
}

// TestRewrapMasterKey proves a passphrase rotation keeps the same master key (so
// existing ciphertext still decrypts) and that the old passphrase no longer works.
func TestRewrapMasterKey(t *testing.T) {
	blob, masterKey, err := NewKeystore("old-pass", fastParams)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	k, _ := ParseKeystore(blob)
	newBlob, err := RewrapMasterKey(k, "old-pass", "new-pass", fastParams)
	if err != nil {
		t.Fatalf("RewrapMasterKey: %v", err)
	}
	nk, _ := ParseKeystore(newBlob)
	got, err := UnwrapMasterKey(nk, "new-pass")
	if err != nil {
		t.Fatalf("unwrap after rotation: %v", err)
	}
	if string(got) != string(masterKey) {
		t.Fatal("master key changed across rotation — existing notes would break")
	}
	if _, err := UnwrapMasterKey(nk, "old-pass"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("old passphrase should fail after rotation: %v", err)
	}
}

// TestParseKeystore_Rejects proves malformed or unsupported keystores are
// rejected rather than silently mis-derived.
func TestParseKeystore_Rejects(t *testing.T) {
	cases := map[string]string{
		"bad json":      "{not json",
		"wrong version": `{"version":"HRBK9","kdf":"argon2id","kdf_mem_kib":8,"kdf_iterations":1,"kdf_parallelism":1,"salt":"AAAA","wrapped_key":"AAAA"}`,
		"wrong kdf":     `{"version":"HRBK1","kdf":"scrypt","kdf_mem_kib":8,"kdf_iterations":1,"kdf_parallelism":1,"salt":"AAAA","wrapped_key":"AAAA"}`,
		"zero param":    `{"version":"HRBK1","kdf":"argon2id","kdf_mem_kib":0,"kdf_iterations":1,"kdf_parallelism":1,"salt":"AAAA","wrapped_key":"AAAA"}`,
		"padded salt":   `{"version":"HRBK1","kdf":"argon2id","kdf_mem_kib":8,"kdf_iterations":1,"kdf_parallelism":1,"salt":"AA==","wrapped_key":"AAAA"}`,
	}
	for name, blob := range cases {
		if _, err := ParseKeystore(blob); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestNewUUIDv4 proves the generated id is a canonical v4 UUID (version nibble 4,
// variant bits 10).
func TestNewUUIDv4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := NewUUIDv4()
		if err != nil {
			t.Fatalf("NewUUIDv4: %v", err)
		}
		if !re.MatchString(id) {
			t.Fatalf("not a v4 UUID: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID: %q", id)
		}
		seen[id] = true
	}
}

// TestSealedFieldMatchesServerShape proves our SealField output satisfies the
// server's structural envelope rules (3 dot parts, HRBC2 tag, 12-byte IV,
// ≥16-byte ciphertext), so an encrypted write will pass server validation.
func TestSealedFieldMatchesServerShape(t *testing.T) {
	masterKey := make([]byte, 32)
	env, err := SealField(masterKey, "id-1", "content", "x")
	if err != nil {
		t.Fatalf("SealField: %v", err)
	}
	parts := strings.Split(env, ".")
	if len(parts) != 3 || parts[0] != "HRBC2" {
		t.Fatalf("bad envelope shape: %q", env)
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(iv) != 12 {
		t.Fatalf("iv not 12 raw-url bytes: %q (%v)", parts[1], err)
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(ct) < 16 {
		t.Fatalf("ct not ≥16 raw-url bytes: %q (%v)", parts[2], err)
	}
}

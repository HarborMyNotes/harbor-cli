// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/HarborMyNotes/harbor-cli/crypto"
)

// testParams is a cheap Argon2id profile so the wiring tests stay fast.
var testParams = crypto.Argon2Params{MemKiB: 8192, Iterations: 1, Parallelism: 1}

// resetSession clears the memoized per-process encryption session so each test
// re-derives from its own fixture.
func resetSession() {
	sessionUnlockd = false
	sessionKey = nil
	sessionErr = nil
	decryptWarned = false
}

// setupEncryption isolates HOME, writes a cached keystore, and sets the
// passphrase env, returning the master key for building ciphertext fixtures. With
// the keystore cached locally, the unlock path needs no network/client.
func setupEncryption(t *testing.T, pass string) []byte {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	resetSession()
	blob, key, err := crypto.NewKeystore(pass, testParams)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	if err := config.SaveKeystoreBlob(blob); err != nil {
		t.Fatalf("SaveKeystoreBlob: %v", err)
	}
	t.Setenv("HARBOR_PASSPHRASE", pass)
	return key
}

// TestDecryptResult_MutationEnvelope proves a {note:{…}} response is decrypted in
// place (the shape get/create/update return).
func TestDecryptResult_MutationEnvelope(t *testing.T) {
	key := setupEncryption(t, "pw")
	id := "11111111-2222-3333-4444-555555555555"
	encTitle, _ := crypto.SealField(key, id, "title", "Secret Title")
	encBody, _ := crypto.SealField(key, id, "content", "Secret Body")
	in, _ := json.Marshal(map[string]any{
		"note": map[string]any{"id": id, "is_encrypted": true, "title": encTitle, "content": encBody},
		"usn":  5,
	})
	out := string(decryptResult(nil, &config.Credentials{}, in))
	if !strings.Contains(out, "Secret Title") || !strings.Contains(out, "Secret Body") {
		t.Fatalf("expected decrypted fields, got: %s", out)
	}
	if strings.Contains(out, "HRBC2.") {
		t.Fatalf("ciphertext still present: %s", out)
	}
}

// TestDecryptResult_Collection proves a {data:[…]} list is walked and each
// encrypted note decrypted, while a plaintext note is left alone.
func TestDecryptResult_Collection(t *testing.T) {
	key := setupEncryption(t, "pw")
	id := "22222222-3333-4444-5555-666666666666"
	encTitle, _ := crypto.SealField(key, id, "title", "Encrypted One")
	in, _ := json.Marshal(map[string]any{
		"data": []any{
			map[string]any{"id": id, "is_encrypted": true, "title": encTitle},
			map[string]any{"id": "plain", "is_encrypted": false, "title": "Plain Note"},
		},
		"paging": map[string]any{"total": 2},
	})
	out := string(decryptResult(nil, &config.Credentials{}, in))
	if !strings.Contains(out, "Encrypted One") || !strings.Contains(out, "Plain Note") {
		t.Fatalf("unexpected output: %s", out)
	}
}

// TestDecryptResult_NoPassphrasePassthrough proves that without HARBOR_PASSPHRASE
// the data is returned untouched (ciphertext shown).
func TestDecryptResult_NoPassphrasePassthrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetSession()
	in := []byte(`{"note":{"id":"x","is_encrypted":true,"title":"HRBC2.AAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAA"}}`)
	out := decryptResult(nil, &config.Credentials{}, in)
	if string(out) != string(in) {
		t.Fatalf("expected passthrough, got %s", out)
	}
}

// TestDecryptResult_WrongPassphraseFallsBack proves a wrong passphrase warns and
// shows ciphertext rather than failing the command.
func TestDecryptResult_WrongPassphraseFallsBack(t *testing.T) {
	key := setupEncryption(t, "right")
	id := "33333333-4444-5555-6666-777777777777"
	enc, _ := crypto.SealField(key, id, "content", "body")
	in, _ := json.Marshal(map[string]any{"id": id, "is_encrypted": true, "content": enc})

	t.Setenv("HARBOR_PASSPHRASE", "wrong")
	resetSession()
	out := string(decryptResult(nil, &config.Credentials{}, in))
	if !strings.Contains(out, "HRBC2.") {
		t.Fatalf("expected ciphertext fallback, got %s", out)
	}
}

// TestEncryptCreateBody proves create encryption seals both fields under a
// generated id, marks the note encrypted, drops content_format, and round-trips.
func TestEncryptCreateBody(t *testing.T) {
	key := setupEncryption(t, "pw")
	body := map[string]any{"title": "Hello", "content": "World", "content_format": "markdown"}
	if err := encryptCreateBody(nil, &config.Credentials{}, body); err != nil {
		t.Fatalf("encryptCreateBody: %v", err)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatal("expected a generated note id")
	}
	if enc, _ := body["is_encrypted"].(bool); !enc {
		t.Fatal("expected is_encrypted true")
	}
	if _, ok := body["content_format"]; ok {
		t.Fatal("content_format should be removed for an encrypted note")
	}
	title, _ := body["title"].(string)
	content, _ := body["content"].(string)
	if !crypto.IsEnvelope(title) || !crypto.IsEnvelope(content) {
		t.Fatalf("fields not sealed: title=%q content=%q", title, content)
	}
	if got, err := crypto.OpenField(key, id, "title", title); err != nil || got != "Hello" {
		t.Fatalf("title decrypt: %v %q", err, got)
	}
	if got, err := crypto.OpenField(key, id, "content", content); err != nil || got != "World" {
		t.Fatalf("content decrypt: %v %q", err, got)
	}
}

// TestEncryptCreateBody_EmptyTitleStaysEmpty proves an empty title is left empty
// (an encrypted note may have a non-envelope empty title) rather than sealed.
func TestEncryptCreateBody_EmptyTitleStaysEmpty(t *testing.T) {
	setupEncryption(t, "pw")
	body := map[string]any{"content": "body only"}
	if err := encryptCreateBody(nil, &config.Credentials{}, body); err != nil {
		t.Fatalf("encryptCreateBody: %v", err)
	}
	if _, ok := body["title"]; ok {
		t.Fatal("empty title should not be added/sealed")
	}
	if !crypto.IsEnvelope(body["content"].(string)) {
		t.Fatal("content should be sealed")
	}
}

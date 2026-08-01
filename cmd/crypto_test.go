// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
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
	defaultNotebookWarned = false
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

// ===========================================================================
// The default notebook can be on any page (issue #67's sibling)
// ===========================================================================
//
// GET /notebooks is paged and carries no "just the default" filter, so which page
// the default notebook lands on is decided by its NAME. Reading only the first
// page answers "the default does not want encryption" for any account with more
// notebooks than fit in it — and the caller then writes the note in the clear.
// This is the same one-page assumption as the task guard, with a quieter failure:
// nothing refuses, nothing warns, the note is just not encrypted.

// notebookPageMock serves a notebooks list where the default notebook — the only
// one with default_encrypt set — sits on the SECOND page.
func notebookPageMock(t *testing.T) *apiMock {
	t.Helper()
	rows := make([]string, 0, collectionPageSize)
	for i := 0; i < collectionPageSize; i++ {
		rows = append(rows, fmt.Sprintf(`{"id":"nb%d","name":"Notebook %d","is_default":false,"default_encrypt":false}`, i, i))
	}
	first := fmt.Sprintf(`{"data":[%s],"paging":{"limit":500,"offset":0,"total":%d,"has_more":true}}`,
		strings.Join(rows, ","), collectionPageSize+1)
	second := fmt.Sprintf(`{"data":[{"id":"nbdefault","name":"Zed","is_default":true,"default_encrypt":true}],`+
		`"paging":{"limit":500,"offset":500,"total":%d,"has_more":false}}`, collectionPageSize+1)

	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "500" {
			_, _ = w.Write([]byte(second))
			return
		}
		_, _ = w.Write([]byte(first))
	}
	return m
}

// TestNotebookWantsEncryptionFindsADefaultPastTheFirstPage proves the lookup walks
// the pages. Against a single-page read this returns false and the note is written
// unencrypted, against the user's own stated default.
func TestNotebookWantsEncryptionFindsADefaultPastTheFirstPage(t *testing.T) {
	m := notebookPageMock(t)

	if !notebookWantsEncryption(client.NewClient(m.baseURL(), "tok"), "") {
		t.Error("the default notebook asks for encryption and the lookup said no — it only read page 1")
	}
}

// TestNotebookWantsEncryptionStopsAtTheDefault proves the walk is not a tax on the
// ordinary account: it stops as soon as the default is in hand, so a default on the
// first page costs the one request it always cost.
func TestNotebookWantsEncryptionStopsAtTheDefault(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[{"id":"nb1","is_default":true,"default_encrypt":true}],` +
			`"paging":{"limit":500,"offset":0,"total":900,"has_more":true}}`},
	})

	if !notebookWantsEncryption(client.NewClient(m.baseURL(), "tok"), "") {
		t.Fatal("the default notebook on page 1 was not found")
	}
	if len(m.calls()) != 1 {
		t.Errorf("kept paging after finding the default: %v", m.calls())
	}
}

// TestNotebookWantsEncryptionSaysSoWhenItCannotTell proves the fail-open is
// AUDIBLE. A walk that cannot finish still answers "no" — encrypting on a guess is
// the worse failure, since a note sealed under the wrong passphrase is
// unrecoverable where an unencrypted one can be re-saved — but the user is told
// which question went unanswered instead of the answer being fabricated silently.
func TestNotebookWantsEncryptionSaysSoWhenItCannotTell(t *testing.T) {
	resetSession()
	// A list that claims more and hands back nothing: the walk cannot advance, so
	// the default notebook is never reached and its setting stays unknown.
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[],"paging":{"limit":500,"offset":0,"total":900,"has_more":true}}`},
	})

	var got bool
	warned := captureStderr(t, func() {
		got = notebookWantsEncryption(client.NewClient(m.baseURL(), "tok"), "")
	})

	if got {
		t.Error("an unreadable notebook list encrypted the note on a guess")
	}
	if !strings.Contains(warned, "unencrypted") {
		t.Errorf("the note was written in the clear on an unanswered question, silently:\n%s", warned)
	}
}

// TestNotebookWantsEncryptionIsQuietWhenItCanTell proves the warning is not noise
// on the ordinary path: a list that reads whole says nothing, whatever the answer.
func TestNotebookWantsEncryptionIsQuietWhenItCanTell(t *testing.T) {
	resetSession()
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[{"id":"nb1","is_default":true,"default_encrypt":false}],` +
			`"paging":{"limit":500,"offset":0,"total":1,"has_more":false}}`},
	})

	warned := captureStderr(t, func() {
		notebookWantsEncryption(client.NewClient(m.baseURL(), "tok"), "")
	})

	if warned != "" {
		t.Errorf("a clean lookup warned anyway:\n%s", warned)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/spf13/cobra"
)

// ===========================================================================
// A stub server that actually holds a note
// ===========================================================================
//
// The other command tests answer from a fixed route table, which is enough when
// the question is "what did the CLI send?". It is not enough here. The claim this
// file has to support is that a note SURVIVES a conversion — that everything the
// CLI did not mean to touch is still there afterwards — and that is a claim about
// state, not about one request. So the stub keeps the note, applies each PATCH to
// it the way the real server does (only the fields present in the body, plus the
// base_usn precondition and the encrypted-envelope validation), and hands it back
// on the next GET. The test then diffs the note against the one it started with.

// convertMock is a stub Harbor API holding a single note and its task list.
type convertMock struct {
	m     *apiMock
	note  map[string]any
	tasks []map[string]any

	// patchStatus, when non-zero, makes the next PATCH fail with that status and
	// leave the stored note untouched — a write that did not land.
	patchStatus int
	patchBody   string
}

// newConvertMock starts the stub around a note and the tasks linked to it.
func newConvertMock(t *testing.T, note map[string]any, tasks ...map[string]any) *convertMock {
	t.Helper()
	cm := &convertMock{note: note, tasks: tasks}
	cm.m = newAPIMock(t, map[string]mockReply{})
	cm.m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tasks"):
			cm.writeTasks(w)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/notes/"):
			writeJSON(w, 200, cm.note)
		case r.Method == http.MethodGet:
			cm.writeList(w)
		case r.Method == http.MethodPatch:
			cm.applyPatch(t, w, r)
		default:
			t.Errorf("convertMock: unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return cm
}

// writeTasks answers GET /notes/:id/tasks with the note's task list.
func (cm *convertMock) writeTasks(w http.ResponseWriter) {
	items := make([]any, 0, len(cm.tasks))
	for _, task := range cm.tasks {
		items = append(items, task)
	}
	writeJSON(w, 200, map[string]any{
		"data":   items,
		"paging": map[string]any{"limit": 500, "offset": 0, "total": len(items), "has_more": false},
	})
}

// writeList answers a notes listing with the one note it holds.
func (cm *convertMock) writeList(w http.ResponseWriter) {
	writeJSON(w, 200, map[string]any{
		"data":   []any{cm.note},
		"paging": map[string]any{"limit": 500, "offset": 0, "total": 1, "has_more": false},
	})
}

// applyPatch behaves like the server's note update: check the base_usn
// precondition, validate the envelopes when the note is encrypted, apply ONLY the
// fields the body carried, bump the usn, and return {note, usn}. Applying only
// what was sent is the part that matters — it is what lets a test tell "the CLI
// preserved the note" apart from "the stub preserved it".
func (cm *convertMock) applyPatch(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if cm.patchStatus != 0 {
		w.WriteHeader(cm.patchStatus)
		_, _ = w.Write([]byte(cm.patchBody))
		return
	}
	// The body is taken from what apiMock already recorded: its outer handler has
	// consumed r.Body before this runs, so reading the request again yields EOF.
	var body map[string]any
	raw := cm.m.requests[len(cm.m.requests)-1].Body
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("convertMock: undecodable PATCH body %q: %v", raw, err)
	}
	if base, ok := body["base_usn"]; ok {
		if int64(base.(float64)) != int64(num(cm.note, "usn")) {
			writeJSON(w, 409, map[string]any{"error": map[string]any{
				"code": "note_usn_stale", "message": "the note has changed",
			}})
			return
		}
	}
	next := map[string]any{}
	for k, v := range cm.note {
		next[k] = v
	}
	for k, v := range body {
		if k == "base_usn" || k == "content_format" {
			continue
		}
		next[k] = v
	}
	// The server's structural check on an encrypted write (validateEncryptedFields):
	// content must be an envelope, and a non-empty title must be one too.
	if enc, _ := next["is_encrypted"].(bool); enc {
		content, _ := next["content"].(string)
		title, _ := next["title"].(string)
		if !crypto.IsEnvelope(content) || (title != "" && !crypto.IsEnvelope(title)) {
			writeJSON(w, 422, map[string]any{"error": map[string]any{
				"code": "validation_failed", "message": "encrypted note fields must be HRBC2 envelopes",
			}})
			return
		}
	}
	next["usn"] = num(cm.note, "usn") + 1
	cm.note = next
	writeJSON(w, 200, map[string]any{"note": cm.note, "usn": cm.note["usn"]})
}

// writeJSON is the stub's one way of answering, so every route agrees on shape.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ===========================================================================
// Fixtures
// ===========================================================================

// convertNoteID is the fixture note's id. It is a canonical UUID because the
// field AAD binds to it and the task-block scanner only recognizes that spelling.
const convertNoteID = "9c2e1f3a-4b5c-6d7e-8f90-a1b2c3d4e5f6"

// convertTaskID is the task the fixture body references.
const convertTaskID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// convertBody is a note body carrying the three things a whole-body rewrite can
// destroy: a task block, an inline attachment reference, and a note→note link.
const convertBody = `<h1>Quarterly plan</h1>
<p>Ship it. See <a href="harbor://note/7d4a1111-2222-3333-4444-555566667777">the other note</a>.</p>
<harbor-embed hash="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"></harbor-embed>
<harbor-task id="` + convertTaskID + `"></harbor-task>`

// plaintextFixture is a plaintext note with everything a conversion must not
// disturb: notebook, tags, attribution, reminder, counters and both timestamps.
func plaintextFixture() map[string]any {
	return map[string]any{
		"id":             convertNoteID,
		"title":          "Quarterly plan",
		"content":        convertBody,
		"is_encrypted":   false,
		"notebook_id":    "5b1f2c9a-1111-2222-3333-444455556666",
		"tags":           []any{map[string]any{"id": "a1b2", "name": "not-reviewed"}},
		"author":         "you@example.com",
		"source_url":     "https://example.com/plan",
		"reminder_time":  float64(1750000000000),
		"word_count":     float64(9),
		"content_length": float64(len(convertBody)),
		"usn":            float64(88),
		"created_at":     float64(1749000000000),
		"updated_at":     float64(1750000000000),
		"deleted":        false,
	}
}

// linkedTask is the task row the fixture body references.
func linkedTask() map[string]any {
	return map[string]any{"id": convertTaskID, "title": "Send the deck", "note_id": convertNoteID}
}

// unlockedSession pre-seeds the memoized encryption session, which is what a
// process that has already unlocked looks like. It bypasses the keystore fetch
// (covered by the crypto tests) so these tests exercise the conversion itself.
func unlockedSession(t *testing.T, key []byte) {
	t.Helper()
	resetSession()
	sessionUnlockd, sessionKey, sessionErr = true, key, nil
	t.Setenv("HARBOR_PASSPHRASE", "pw")
	t.Cleanup(resetSession)
}

// newMasterKey mints a master key without touching HOME or the network.
func newMasterKey(t *testing.T) []byte {
	t.Helper()
	_, key, err := crypto.NewKeystore("pw", testParams)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	return key
}

// ===========================================================================
// The round trip
// ===========================================================================

// TestConversionRoundTripLosesNothing is the acceptance test for this whole
// feature, and it is written as a DIFF rather than as a list of assertions about
// the fields somebody remembered to check.
//
// A note goes in plaintext, is encrypted through the real command, and is
// decrypted back through the real command. The title and body must come out
// character-for-character as they went in, and every OTHER field — notebook,
// tags, author, source url, reminder, both timestamps, the attachment reference
// and the task block inside the body — must be untouched at every step. The usn
// is the one exception: it moves on every write, which is what it is for.
//
// Diffing the whole object is deliberate. The data-loss bugs this feature exists
// to repair (harbor-cli#62, #66, #67) were all of the same shape — a write that
// carried something it should not have, or dropped something nobody thought to
// assert about — and a test that checks the fields it thought of would have
// passed for every one of them.
func TestConversionRoundTripLosesNothing(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	original := plaintextFixture()
	cm := newConvertMock(t, plaintextFixture(), linkedTask())

	// ---- plaintext → encrypted ----------------------------------------------
	if _, err := runCLI(t, cm.m, "notes", "encrypt", "--yes", convertNoteID); err != nil {
		t.Fatalf("notes encrypt: %v", err)
	}
	sealed := cm.note
	if enc, _ := sealed["is_encrypted"].(bool); !enc {
		t.Fatal("the note was not marked encrypted")
	}
	sealedTitle, _ := sealed["title"].(string)
	sealedContent, _ := sealed["content"].(string)
	if !crypto.IsEnvelope(sealedTitle) || !crypto.IsEnvelope(sealedContent) {
		t.Fatalf("fields are not envelopes: title=%q content=%q", sealedTitle, sealedContent)
	}
	// The server holds no readable trace of the note.
	if strings.Contains(sealedContent, "Quarterly") || strings.Contains(sealedTitle, "Quarterly") {
		t.Error("plaintext survived into the stored note")
	}
	// What was sealed opens back to exactly what went in.
	if got, err := crypto.OpenField(key, convertNoteID, "title", sealedTitle); err != nil || got != original["title"] {
		t.Errorf("title does not open to the original: %v %q", err, got)
	}
	if got, err := crypto.OpenField(key, convertNoteID, "content", sealedContent); err != nil || got != convertBody {
		t.Errorf("content does not open to the original: %v\n got %q\nwant %q", err, got, convertBody)
	}
	assertOnlyChanged(t, original, sealed, "title", "content", "is_encrypted", "usn")

	// ---- encrypted → plaintext ----------------------------------------------
	if _, err := runCLI(t, cm.m, "notes", "decrypt", convertNoteID, "--yes"); err != nil {
		t.Fatalf("notes decrypt: %v", err)
	}
	back := cm.note
	if enc, _ := back["is_encrypted"].(bool); enc {
		t.Fatal("the note is still marked encrypted")
	}
	if back["title"] != original["title"] {
		t.Errorf("title = %q, want %q", back["title"], original["title"])
	}
	if back["content"] != original["content"] {
		t.Errorf("content did not survive the round trip:\n got %q\nwant %q", back["content"], original["content"])
	}
	assertOnlyChanged(t, original, back, "usn")
}

// assertOnlyChanged fails for every field that differs between before and after
// except the ones named. It also fails on a field that APPEARED or VANISHED, so a
// conversion that quietly adds or drops something is caught too.
func assertOnlyChanged(t *testing.T, before, after map[string]any, allowed ...string) {
	t.Helper()
	skip := map[string]bool{}
	for _, k := range allowed {
		skip[k] = true
	}
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if skip[k] {
			continue
		}
		b, hadBefore := before[k]
		a, hasAfter := after[k]
		switch {
		case hadBefore && !hasAfter:
			t.Errorf("field %q was dropped by the conversion (was %v)", k, b)
		case !hadBefore && hasAfter:
			t.Errorf("the conversion added a field %q = %v that the note did not have", k, a)
		case !reflect.DeepEqual(b, a):
			t.Errorf("field %q changed: %v → %v", k, b, a)
		}
	}
}

// TestEncryptSendsOnlyWhatItMeansTo pins the wire body itself, because the diff
// above can only catch a stray field the stub happens to store. A PATCH carrying
// `notebook_id: ""` or an empty tag list would be a silent wipe against the real
// server no matter what the stub does with it.
func TestEncryptSendsOnlyWhatItMeansTo(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	cm := newConvertMock(t, plaintextFixture(), linkedTask())

	if _, err := runCLI(t, cm.m, "notes", "encrypt", "--yes", convertNoteID); err != nil {
		t.Fatalf("notes encrypt: %v", err)
	}

	body := cm.m.bodyOf(t, "PATCH /api/v1/notes/"+convertNoteID)
	want := map[string]bool{"is_encrypted": true, "title": true, "content": true, "base_usn": true}
	for k := range body {
		if !want[k] {
			t.Errorf("the encrypt PATCH carries %q = %v, which it has no business changing", k, body[k])
		}
	}
	if _, ok := body["base_usn"]; !ok {
		t.Error("no base_usn: a note edited between the read and the write would be silently overwritten")
	}
	if _, ok := body["content_format"]; ok {
		t.Error("content_format was sent for an encrypted body the server stores verbatim")
	}
}

// TestDecryptSendsTheFormatItPromised proves the decrypted body is handed back
// with a content_format, since the server has to interpret it — and that --format
// is what chooses it.
func TestDecryptSendsTheFormatItPromised(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, "# Heading\n\n- one"), linkedTask())
	cm.tasks = nil

	if _, err := runCLI(t, cm.m, "notes", "decrypt", convertNoteID, "--yes", "--format", "markdown"); err != nil {
		t.Fatalf("notes decrypt: %v", err)
	}

	body := cm.m.bodyOf(t, "PATCH /api/v1/notes/"+convertNoteID)
	if body["content_format"] != "markdown" {
		t.Errorf("content_format = %v, want markdown", body["content_format"])
	}
	if body["content"] != "# Heading\n\n- one" {
		t.Errorf("content = %v, want the decrypted source", body["content"])
	}
}

// encryptedFixture builds an encrypted note fixture whose body is content.
func encryptedFixture(t *testing.T, key []byte, content string) map[string]any {
	t.Helper()
	note := plaintextFixture()
	title, err := crypto.SealField(key, convertNoteID, "title", "Quarterly plan")
	if err != nil {
		t.Fatalf("SealField(title): %v", err)
	}
	sealed, err := crypto.SealField(key, convertNoteID, "content", content)
	if err != nil {
		t.Fatalf("SealField(content): %v", err)
	}
	note["is_encrypted"] = true
	note["title"] = title
	note["content"] = sealed
	return note
}

// ===========================================================================
// A failed conversion leaves the note as it was
// ===========================================================================

// TestAFailedWriteLeavesTheNoteExactlyAsItWas is the other half of the safety
// claim. Everything the CLI does before the PATCH happens in memory — read, seal,
// verify — so there is no state to unwind: if the write does not land, the note on
// the server is untouched, down to its usn.
func TestAFailedWriteLeavesTheNoteExactlyAsItWas(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	original := plaintextFixture()
	cm := newConvertMock(t, plaintextFixture(), linkedTask())
	cm.patchStatus, cm.patchBody = 500, apiErrorBody("internal", "the write did not land")

	_, err := runCLI(t, cm.m, "notes", "encrypt", "--yes", convertNoteID)

	if err == nil {
		t.Fatal("a failed write reported success")
	}
	assertOnlyChanged(t, original, cm.note)
}

// TestAStaleNoteIsRefusedRatherThanClobbered proves the base_usn precondition is
// live: a note that moved between the read and the write is refused by the server
// and the CLI says what to do about it, rather than overwriting an edit it never
// saw. This is the read-then-write race a conversion cannot avoid having.
func TestAStaleNoteIsRefusedRatherThanClobbered(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	cm := newConvertMock(t, plaintextFixture(), linkedTask())
	// Somebody else's edit lands between the CLI's GET and its PATCH.
	cm.m.handler = withPreWriteEdit(cm)

	_, err := runCLI(t, cm.m, "notes", "encrypt", "--yes", convertNoteID)

	if err == nil {
		t.Fatal("a stale write was accepted")
	}
	if !strings.Contains(err.Error(), "changed after this command read it") {
		t.Errorf("err = %q, want the note_usn_stale explanation", err.Error())
	}
	// The concurrent edit is still there; the conversion did not roll over it.
	if cm.note["title"] != "Edited elsewhere" {
		t.Errorf("title = %v, want the concurrent edit to have survived", cm.note["title"])
	}
	if enc, _ := cm.note["is_encrypted"].(bool); enc {
		t.Error("the note was encrypted anyway, over an edit the CLI never read")
	}
}

// withPreWriteEdit wraps the stub so that the first PATCH arrives after somebody
// else has already saved the note — the exact interleaving base_usn exists for.
func withPreWriteEdit(cm *convertMock) http.HandlerFunc {
	inner := cm.m.handler
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			cm.note["title"] = "Edited elsewhere"
			cm.note["usn"] = num(cm.note, "usn") + 1
		}
		inner(w, r)
	}
}

// ===========================================================================
// No key, no conversion
// ===========================================================================

// TestConversionRefusesWithoutThePassphrase is the requirement this whole issue
// turns on: a locked vault must stop the command, not degrade it. The assertion
// that no request was made at all is the important half — the failure being
// repaired here is a client that wrote a note the user believed was encrypted.
func TestConversionRefusesWithoutThePassphrase(t *testing.T) {
	for _, args := range [][]string{
		{"notes", "encrypt", "--yes", convertNoteID},
		{"notes", "decrypt", convertNoteID, "--yes"},
	} {
		t.Run(args[1], func(t *testing.T) {
			t.Setenv("HARBOR_PASSPHRASE", "")
			resetSession()
			t.Cleanup(resetSession)
			cm := newConvertMock(t, plaintextFixture())

			_, err := runCLI(t, cm.m, args...)

			if err == nil {
				t.Fatal("the command proceeded with no key")
			}
			if !strings.Contains(err.Error(), passphraseEnv) {
				t.Errorf("err = %q, want it to name %s", err.Error(), passphraseEnv)
			}
			if !strings.Contains(err.Error(), "nothing was written") {
				t.Errorf("err = %q, want it to say plainly that nothing was written", err.Error())
			}
			if len(cm.m.calls()) != 0 {
				t.Errorf("the API was called anyway: %v", cm.m.calls())
			}
		})
	}
}

// TestConversionKeyExplainsAMissingKeystore proves the OTHER locked-vault case —
// a passphrase is set but this account has never set encryption up — names the
// command that fixes it instead of surfacing the internal sentinel.
func TestConversionKeyExplainsAMissingKeystore(t *testing.T) {
	resetSession()
	t.Cleanup(resetSession)
	sessionUnlockd, sessionErr = true, errNoKeystore

	_, err := conversionKey(nil, nil)

	if err == nil {
		t.Fatal("a missing keystore was not refused")
	}
	if !strings.Contains(err.Error(), "crypto setup") {
		t.Errorf("err = %q, want it to name 'harbor crypto setup'", err.Error())
	}
}

// TestPlanDecryptRefusesAWrongKey proves a note sealed under another passphrase is
// refused rather than written back as whatever OpenField managed to produce.
func TestPlanDecryptRefusesAWrongKey(t *testing.T) {
	sealedWith := newMasterKey(t)
	cm := newConvertMock(t, encryptedFixture(t, sealedWith, convertBody))

	_, err := planDecrypt(client.NewClient(cm.m.baseURL(), "tok"), newMasterKey(t), convertNoteID, "html", false)

	if err == nil {
		t.Fatal("a note that could not be opened was still converted")
	}
	if !strings.Contains(err.Error(), "nothing was written") {
		t.Errorf("err = %q, want it to say nothing was written", err.Error())
	}
}

// TestPlanDecryptRefusesANonEnvelopeBody covers the note that CLAIMS to be
// encrypted and is not — the local dev API's SPNC2 placeholder rows are exactly
// this. Writing such a body back as plaintext would publish it on a guess.
func TestPlanDecryptRefusesANonEnvelopeBody(t *testing.T) {
	note := plaintextFixture()
	note["is_encrypted"] = true
	note["content"] = "SPNC2-opaque-placeholder"
	cm := newConvertMock(t, note)

	_, err := planDecrypt(client.NewClient(cm.m.baseURL(), "tok"), newMasterKey(t), convertNoteID, "html", false)

	if err == nil {
		t.Fatal("a non-envelope body was accepted for decryption")
	}
	if !strings.Contains(err.Error(), "HRBC2") {
		t.Errorf("err = %q, want it to say the body is not an HRBC2 envelope", err.Error())
	}
}

// TestSealAndVerifyRefusesWhatItCannotReadBack proves the post-seal check is real:
// a key that cannot produce a readable envelope is a refusal, not a write. Without
// this check the plaintext would be replaced by ciphertext nobody can ever open.
func TestSealAndVerifyRefusesWhatItCannotReadBack(t *testing.T) {
	_, err := sealAndVerify([]byte("too short for AES-256"), convertNoteID, "content", "hello")

	if err == nil {
		t.Fatal("a seal that cannot be performed was reported as fine")
	}
	if !strings.Contains(err.Error(), "nothing was written") {
		t.Errorf("err = %q, want it to say nothing was written", err.Error())
	}
}

// ===========================================================================
// Decrypting is a downgrade the user has to mean
// ===========================================================================

// TestDecryptAsksBeforeItPublishes proves the gate is wired to the real command:
// a person who answers anything but "yes" gets no write at all.
func TestDecryptAsksBeforeItPublishes(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, convertBody), linkedTask())
	asked := answerPrompt(t, "no")

	_, err := runCLI(t, cm.m, "notes", "decrypt", convertNoteID)

	if err == nil {
		t.Fatal("the note was decrypted without consent")
	}
	if err.Error() != notesDecryptConfirmation.Aborted {
		t.Errorf("err = %q, want %q", err.Error(), notesDecryptConfirmation.Aborted)
	}
	if len(*asked) != 1 {
		t.Errorf("prompts = %v, want exactly one", *asked)
	}
	for _, call := range cm.m.calls() {
		if strings.HasPrefix(call, "PATCH") {
			t.Errorf("a write happened after the user declined: %v", cm.m.calls())
		}
	}
}

// TestDecryptRefusesUnattendedWithoutYes proves a script or an agent cannot
// publish a note by accident: with nobody at the keyboard the command refuses and
// names the flag that would have worked.
func TestDecryptRefusesUnattendedWithoutYes(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, convertBody), linkedTask())
	pipedStdin(t)

	_, err := runCLI(t, cm.m, "notes", "decrypt", convertNoteID)

	if err == nil {
		t.Fatal("an unattended run decrypted the note")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %q, want it to name --yes", err.Error())
	}
}

// TestDecryptCountsWhatItIsAboutToPublish proves the question is about the run in
// front of the user. Over a notebook sweep "decrypt these notes?" and "decrypt
// these 240 notes?" are different questions.
func TestDecryptCountsWhatItIsAboutToPublish(t *testing.T) {
	answerPrompt(t, "yes")
	out := captureStdout(t, func() {
		if err := notesConfirmDecrypt(240, false); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	})
	if !strings.Contains(out, "240 notes") {
		t.Errorf("the prompt does not say how many notes are about to be published:\n%s", out)
	}
}

// TestEncryptAsksBeforeDestroyingHistory pins the gate. Encrypting reads like the
// SAFE direction — it is the one that adds protection — which is exactly why it
// shipped without a prompt while decrypt had one. It is not safe: the write hard
// -deletes every earlier version of the note.
//
// The count assertion is "exactly one", not "at least one". Asking per note over
// a --notebook sweep is how a person learns to type "yes" without reading, so
// asking once for the whole run is the design, not an incidental.
func TestEncryptAsksBeforeDestroyingHistory(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	cm := newConvertMock(t, plaintextFixture(), linkedTask())
	asked := answerPrompt(t, "no")

	_, err := runCLI(t, cm.m, "notes", "encrypt", convertNoteID)
	if err == nil {
		t.Fatal("encrypting went ahead without consent — it deletes the note's history")
	}
	if !strings.Contains(err.Error(), "nothing was encrypted") {
		t.Errorf("a declined confirmation should say nothing was encrypted: %v", err)
	}
	if len(*asked) != 1 {
		t.Errorf("asked %d times, want exactly 1 for the whole run: %v", len(*asked), *asked)
	}
	if enc, _ := cm.note["is_encrypted"].(bool); enc {
		t.Error("the note was encrypted despite the user declining")
	}
}

// TestEncryptAsksOncePerRunNotPerNote drives the multi-note form, which is the
// case the single-note test cannot distinguish: a confirmation moved inside the
// per-note loop still asks exactly once when there is exactly one note.
func TestEncryptAsksOncePerRunNotPerNote(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	cm := newConvertMock(t, plaintextFixture(), linkedTask())
	asked := answerPrompt(t, "no")

	if _, err := runCLI(t, cm.m, "notes", "encrypt", convertNoteID, convertNoteID, convertNoteID); err == nil {
		t.Fatal("expected the declined confirmation to stop the run")
	}
	if len(*asked) != 1 {
		t.Errorf("asked %d times for a 3-note run, want exactly 1: %v", len(*asked), *asked)
	}
}

// TestEncryptRefusesUnattendedWithoutYes pins the scripted path: with nobody at a
// keyboard the command must refuse rather than silently destroying history, and
// must name the flag that would have worked.
func TestEncryptRefusesUnattendedWithoutYes(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	// Declared, not inherited: without this the test only passes because `go test`
	// happens to hand the binary a non-TTY stdin, and it fails (or blocks) under a
	// pty. Its decrypt twin has always done this.
	pipedStdin(t)
	cm := newConvertMock(t, plaintextFixture(), linkedTask())

	_, err := runCLI(t, cm.m, "notes", "encrypt", convertNoteID)
	if err == nil {
		t.Fatal("expected a refusal with no terminal to prompt on")
	}
	for _, want := range []string{"version history", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
	if enc, _ := cm.note["is_encrypted"].(bool); enc {
		t.Error("the note was encrypted without confirmation")
	}
}

// TestEncryptConfirmationCountsTheNotes proves a --notebook sweep says how many
// notes' histories it is about to delete, so the answer is informed rather than
// reflexive — the same reason the decrypt confirmation carries a count.
func TestEncryptConfirmationCountsTheNotes(t *testing.T) {
	answerPrompt(t, "yes")
	out := captureStdout(t, func() {
		if err := notesConfirmEncrypt(7, false); err != nil {
			t.Fatalf("notesConfirmEncrypt: %v", err)
		}
	})
	for _, want := range []string{"7 notes", "deleting the version history"} {
		if !strings.Contains(out, want) {
			t.Errorf("the question does not say %q:\n%s", want, out)
		}
	}

	// Singular reads as a sentence too, not "1 notes".
	answerPrompt(t, "yes")
	one := captureStdout(t, func() {
		if err := notesConfirmEncrypt(1, false); err != nil {
			t.Fatalf("notesConfirmEncrypt: %v", err)
		}
	})
	if !strings.Contains(one, "1 note,") {
		t.Errorf("singular wording is wrong:\n%s", one)
	}

	if notesEncryptConfirmation.Affirmative != "yes" {
		t.Errorf("affirmative = %q, want \"yes\"", notesEncryptConfirmation.Affirmative)
	}
	if !strings.Contains(notesEncryptConfirmation.Warning, "DELETES every earlier version") {
		t.Errorf("the confirmation does not lead with the deletion:\n%s", notesEncryptConfirmation.Warning)
	}
}

// ===========================================================================
// The one thing decrypting can delete
// ===========================================================================

// TestDecryptRefusesToDeleteAStrandedTask is the data-loss guard. A task linked to
// the note whose block the decrypted body does not carry is TOMBSTONED by the
// server the moment the body becomes readable — so the conversion is refused with
// nothing written, and the task is named so the user can tell which one it is.
func TestDecryptRefusesToDeleteAStrandedTask(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	// The sealed body carries no <harbor-task> block, but a task still points here.
	cm := newConvertMock(t, encryptedFixture(t, key, "<p>no blocks in here</p>"), linkedTask())

	_, err := runCLI(t, cm.m, "notes", "decrypt", convertNoteID, "--yes")

	if err == nil {
		t.Fatal("a decrypt that deletes a task was allowed through")
	}
	if !strings.Contains(err.Error(), convertTaskID) || !strings.Contains(err.Error(), "Send the deck") {
		t.Errorf("err = %q, want it to name the task that would be deleted", err.Error())
	}
	if !strings.Contains(err.Error(), "--allow-task-loss") {
		t.Errorf("err = %q, want it to name the way through", err.Error())
	}
	for _, call := range cm.m.calls() {
		if strings.HasPrefix(call, "PATCH") {
			t.Errorf("the refusal still wrote: %v", cm.m.calls())
		}
	}
}

// TestDecryptProceedsWithAllowTaskLoss proves the escape hatch works and says what
// it is doing rather than doing it quietly.
func TestDecryptProceedsWithAllowTaskLoss(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, "<p>no blocks in here</p>"), linkedTask())

	var err error
	warned := captureStderr(t, func() {
		_, err = runCLI(t, cm.m, "notes", "decrypt", convertNoteID, "--yes", "--allow-task-loss")
	})

	if err != nil {
		t.Fatalf("notes decrypt --allow-task-loss: %v", err)
	}
	if enc, _ := cm.note["is_encrypted"].(bool); enc {
		t.Error("the note was not decrypted")
	}
	if !strings.Contains(warned, "1 task") {
		t.Errorf("the deletion was not announced:\n%q", warned)
	}
}

// TestDecryptIsQuietWhenTheBodyCarriesItsTasks proves the guard is not a tax on
// the ordinary case: the body that comes out of the envelope is the body that went
// in, blocks and all, so nothing is at risk and nothing is said.
func TestDecryptIsQuietWhenTheBodyCarriesItsTasks(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, convertBody), linkedTask())

	if _, err := runCLI(t, cm.m, "notes", "decrypt", convertNoteID, "--yes"); err != nil {
		t.Fatalf("notes decrypt: %v", err)
	}
	if cm.note["content"] != convertBody {
		t.Errorf("content = %v, want the sealed body back", cm.note["content"])
	}
}

// ===========================================================================
// Choosing what to convert
// ===========================================================================

// TestConversionTargetsRejectsAmbiguousInput pins both ways of asking for nothing
// useful. Unioning ids with --notebook would turn a leftover flag into a hundred
// writes; taking neither would convert nothing while exiting 0.
func TestConversionTargetsRejectsAmbiguousInput(t *testing.T) {
	cases := map[string]struct {
		args     []string
		notebook string
		want     string
	}{
		"both":    {[]string{convertNoteID}, "nb1", "not both"},
		"neither": {nil, "", "at least one note id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := conversionTargetCmd(tc.notebook)
			_, _, err := conversionTargets(cmd, nil, tc.args, true)
			if err == nil {
				t.Fatal("ambiguous input was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// conversionTargetCmd builds the flag set conversionTargets reads.
func conversionTargetCmd(notebook string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("notebook", "", "")
	if notebook != "" {
		_ = cmd.Flags().Set("notebook", notebook)
	}
	return cmd
}

// TestSweepReadsEveryPageAndSkipsWhatIsDone proves the notebook sweep is a WHOLE
// read (issue #67's lesson) and that it only names the notes that need work. A
// sweep that stopped at the first page would report a notebook fixed while leaving
// the notes past it in the clear — the exact failure this command repairs.
func TestSweepReadsEveryPageAndSkipsWhatIsDone(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "500" {
			_, _ = w.Write([]byte(`{"data":[{"id":"past-the-first-page","is_encrypted":false}],` +
				`"paging":{"limit":500,"offset":500,"total":501,"has_more":false}}`))
			return
		}
		rows := make([]string, 0, collectionPageSize)
		for i := 0; i < collectionPageSize; i++ {
			// Every note on page one is already encrypted: nothing to do, but the
			// walk must not stop because of it.
			rows = append(rows, fmt.Sprintf(`{"id":"done%d","is_encrypted":true}`, i))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[%s],"paging":{"limit":500,"offset":0,"total":501,"has_more":true}}`,
			strings.Join(rows, ","))))
	}

	ids, complete, err := sweepNotebook(client.NewClient(m.baseURL(), "tok"), "nb1", true)

	if err != nil {
		t.Fatalf("sweepNotebook: %v", err)
	}
	if !complete {
		t.Error("the walk reported a short read of a notebook it read whole")
	}
	if len(ids) != 1 || ids[0] != "past-the-first-page" {
		t.Errorf("ids = %v, want just the one unencrypted note, which is on page two", ids)
	}
}

// TestSweepSaysSoWhenItCouldNotReadTheWholeNotebook proves a short read is never
// reported as a finished notebook. Silence here would read as "done".
func TestSweepSaysSoWhenItCouldNotReadTheWholeNotebook(t *testing.T) {
	report := conversionReport{Converted: []string{"n1"}, Complete: false}

	out := captureStdout(t, func() { printConversionSummary(report, "encrypted") })

	if !strings.Contains(out, "could not be listed in full") {
		t.Errorf("a partial sweep looked complete:\n%s", out)
	}
	if !strings.Contains(out, "Re-run") {
		t.Errorf("the summary does not say what to do about it:\n%s", out)
	}
}

// TestConversionIsIdempotent proves re-running over an already-converted note is a
// skip, not a failure and not a second write. That is what makes "re-run to catch
// the rest" a real answer for a notebook that could not be read whole.
func TestConversionIsIdempotent(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, convertBody), linkedTask())

	out, err := runCLI(t, cm.m, "notes", "encrypt", "--yes", convertNoteID)

	if err != nil {
		t.Fatalf("re-encrypting an encrypted note failed: %v", err)
	}
	if !strings.Contains(out, "already encrypted") {
		t.Errorf("output does not say the note was skipped:\n%s", out)
	}
	for _, call := range cm.m.calls() {
		if strings.HasPrefix(call, "PATCH") {
			t.Errorf("an already-encrypted note was written again: %v", cm.m.calls())
		}
	}
}

// ===========================================================================
// Reporting
// ===========================================================================

// TestVerifyConversionLandedCatchesAServerThatIgnoredTheFlag pins the check that a
// 200 is not taken for a conversion. A server that dropped is_encrypted would
// otherwise earn a green tick over a note still sitting in the clear — which is
// precisely the bug class this command exists to clean up.
func TestVerifyConversionLandedCatchesAServerThatIgnoredTheFlag(t *testing.T) {
	answered, _ := json.Marshal(map[string]any{
		"note": map[string]any{"id": convertNoteID, "is_encrypted": false}, "usn": 9,
	})

	err := verifyConversionLanded(answered, true, convertNoteID)

	if err == nil {
		t.Fatal("a server that ignored is_encrypted was reported as a success")
	}
	if !strings.Contains(err.Error(), "before trusting it") {
		t.Errorf("err = %q, want it to tell the user to check the note", err.Error())
	}
}

// TestBulkConversionFinishesTheRunAndStillFails proves a bad note does not strand
// the good ones — the remedy case is a whole notebook — while the command still
// exits non-zero so a script cannot read a partial run as a clean one.
func TestBulkConversionFinishesTheRunAndStillFails(t *testing.T) {
	attempted := []string{}
	err := runConversion([]string{"a", "b", "c"}, true, "encrypted", func(id string) (bool, error) {
		attempted = append(attempted, id)
		if id == "b" {
			return false, fmt.Errorf("note %s could not be read", id)
		}
		return true, nil
	})

	if err == nil {
		t.Fatal("a run with a failed note reported success")
	}
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Errorf("err = %q, want it to count the failures", err.Error())
	}
	if !reflect.DeepEqual(attempted, []string{"a", "b", "c"}) {
		t.Errorf("attempted = %v, want the run to continue past the failure", attempted)
	}
}

// TestSingleNoteFailureIsReportedInFull proves the common case keeps its full
// error. The task-loss refusal is several lines of explanation, and flattening it
// into a summary line would cost the user the part that says what to do.
func TestSingleNoteFailureIsReportedInFull(t *testing.T) {
	want := "line one\nline two\nline three"

	err := runConversion([]string{"only"}, true, "decrypted", func(string) (bool, error) {
		return false, fmt.Errorf("%s", want)
	})

	if err == nil {
		t.Fatal("the failure was swallowed")
	}
	if err.Error() != want {
		t.Errorf("err = %q, want the original multi-line message %q", err.Error(), want)
	}
}

// TestConversionJSONReportListsEveryOutcome proves --json is machine-usable: a
// script fixing a notebook can retry exactly the notes that did not make it.
func TestConversionJSONReportListsEveryOutcome(t *testing.T) {
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out := captureStdout(t, func() {
		_ = runConversion([]string{"ok", "skip", "bad"}, false, "encrypted", func(id string) (bool, error) {
			switch id {
			case "skip":
				return false, nil
			case "bad":
				return false, fmt.Errorf("nope")
			}
			return true, nil
		})
	})

	var report conversionReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("--json output is not valid JSON (%v):\n%s", err, out)
	}
	if !reflect.DeepEqual(report.Converted, []string{"ok"}) {
		t.Errorf("converted = %v", report.Converted)
	}
	if !reflect.DeepEqual(report.Skipped, []string{"skip"}) {
		t.Errorf("skipped = %v", report.Skipped)
	}
	if len(report.Failed) != 1 || report.Failed[0].ID != "bad" {
		t.Errorf("failed = %v, want the id and its reason", report.Failed)
	}
	if report.Complete {
		t.Error("complete = true for a run that could not read the whole notebook")
	}
}

// TestASweepWithNothingToDoSaysSo proves the answer a remedy run wants to hear —
// "this notebook is clean" — is said in words. "Encrypted 0 notes." is true and
// reads like a failure.
func TestASweepWithNothingToDoSaysSo(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runConversion(nil, true, "encrypted", func(string) (bool, error) {
			t.Error("nothing should have been converted")
			return false, nil
		}); err != nil {
			t.Fatalf("runConversion: %v", err)
		}
	})

	if !strings.Contains(out, "Nothing to do") {
		t.Errorf("a clean notebook did not say so:\n%s", out)
	}
	if strings.Contains(out, "0 notes") {
		t.Errorf("the summary counts at the user instead of answering them:\n%s", out)
	}
}

// TestAClearSweepStillWarnsAboutAShortRead pins the one thing that must survive
// the "nothing to do" shortcut: a notebook that could not be read whole cannot be
// reported as clean, even when every note it DID see was already converted.
func TestAClearSweepStillWarnsAboutAShortRead(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runConversion(nil, false, "encrypted", func(string) (bool, error) { return false, nil })
	})

	if !strings.Contains(out, "could not be listed in full") {
		t.Errorf("a short read was reported as a clean notebook:\n%s", out)
	}
}

// TestEncryptSaysNothingAboutHistoryWhenItSealedNothing keeps the history caveat
// tied to the notes this run actually sealed. On a re-run that converted nothing
// it would be a warning about somebody else's writes.
func TestEncryptSaysNothingAboutHistoryWhenItSealedNothing(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	cm := newConvertMock(t, encryptedFixture(t, key, convertBody), linkedTask())

	warned := captureStderr(t, func() {
		if _, err := runCLI(t, cm.m, "notes", "encrypt", "--yes", convertNoteID); err != nil {
			t.Fatalf("notes encrypt: %v", err)
		}
	})

	if strings.Contains(warned, "history") {
		t.Errorf("a run that sealed nothing warned about history anyway:\n%q", warned)
	}
}

// TestTaskLossRefusalOnlyNamesCommandsThatExist is a small rule with a specific
// history: the first draft of this refusal offered `harbor tasks attach`, which
// is a server endpoint and not a command in this CLI. A refusal that sends the
// user to a command that does not exist is worse than one that says nothing,
// because they follow it.
func TestTaskLossRefusalOnlyNamesCommandsThatExist(t *testing.T) {
	prepareCommandTree()
	paths := map[string]bool{}
	forEachCommand(t, func(c *cobra.Command) { paths[c.CommandPath()] = true })

	msg := conversionTaskLossRefusal(convertNoteID, []noteTask{{ID: convertTaskID, Title: "Send the deck"}})

	named := regexp.MustCompile(`harbor(?: [a-z-]+)+`)
	for _, cmd := range named.FindAllString(msg, -1) {
		if !paths[cmd] {
			t.Errorf("the refusal tells the user to run %q, which is not a command in this CLI", cmd)
		}
	}
	// And it must still tell them the way out, or the rule above is satisfied by
	// saying nothing at all.
	if !strings.Contains(msg, "--allow-task-loss") || !strings.Contains(msg, "harbor notes update") {
		t.Errorf("the refusal does not offer both ways forward:\n%s", msg)
	}
}

// ===========================================================================
// A run that failed part way still sealed the notes it sealed
// ===========================================================================

// TestEncryptWarnsAboutHistoryEvenWhenAnotherNoteFailed pins the ordering the
// first version got wrong. runConversion returns an error whenever ANY note
// failed, and the history caveat used to sit behind that return — so a sweep
// where one write failed left the other notes genuinely encrypted on the server
// and said nothing at all about the plaintext still sitting in their history.
//
// A partial failure is exactly when the caveat matters most: it is the notebook
// sweep this command exists for, and the user is walking away believing the run
// did not really happen.
func TestEncryptWarnsAboutHistoryEvenWhenAnotherNoteFailed(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	const good = "11111111-1111-1111-1111-111111111111"
	const bad = "22222222-2222-2222-2222-222222222222"

	sealed := map[string]bool{}
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch {
		case r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"id": id, "title": "T", "content": "<p>body</p>",
				"is_encrypted": false, "usn": float64(5),
			})
		case id == bad:
			writeJSON(w, 500, map[string]any{"error": map[string]any{
				"code": "internal", "message": "the write did not land",
			}})
		default:
			sealed[id] = true
			writeJSON(w, 200, map[string]any{
				"note": map[string]any{"id": id, "is_encrypted": true}, "usn": 6,
			})
		}
	}

	var out string
	var err error
	warned := captureStderr(t, func() { out, err = runCLI(t, m, "notes", "encrypt", "--yes", good, bad) })

	// The run still fails: a script must not read a partial sweep as a clean one.
	if err == nil {
		t.Fatal("a run with a failed note reported success")
	}
	// ...but the good note really was encrypted...
	if !sealed[good] {
		t.Fatal("the note that could be encrypted was not — the test proves nothing")
	}
	if !strings.Contains(out, good) || !strings.Contains(out, bad) {
		t.Errorf("both outcomes should be reported per note:\n%s", out)
	}
	// ...so the caveat about its history must have been said.
	if !strings.Contains(warned, "history") {
		t.Errorf("a note was sealed and the history caveat was never printed:\n%q", warned)
	}
}

// TestHistoryCaveatIsTrueInBothDirections guards the wording against drifting
// back to either flat version, both of which are wrong.
//
// "Earlier versions stay as plaintext" is wrong because the server COALESCES
// snapshots: a save from the same client, inside the configured idle window,
// folds into the previous history row rather than starting a new one — so
// encrypting a note edited moments ago overwrites that row's plaintext and the
// version really does disappear. "Encrypting cleans up your history" is wrong
// because that depends on timing, on which client saved last, and on a
// server-side window this CLI cannot see, and every older row is untouched
// regardless. The message has to carry both halves and land on the part that
// holds either way.
func TestHistoryCaveatSaysTheHistoryIsDeleted(t *testing.T) {
	out := captureStderr(t, printHistoryCaveat)

	for _, want := range []string{
		"DELETES the note's version history",
		"Every earlier version is discarded",
		"not recoverable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the caveat no longer says %q:\n%s", want, out)
		}
	}
}

// TestNothingClaimsHistorySurvivesEncryption is the regression guard the issue
// asked for: nothing the CLI can print may claim a note's earlier versions
// survive an encryption change.
//
// It WALKS THE COMMAND TREE rather than checking a hand-listed set of strings.
// The first version of this test listed six fields by name, and a false claim
// added to a Short, an Example, or any other command's help would have sailed
// past it — which is the same shape of gap that let the original sentence live
// here for months. The repo already rejects hand-maintained registries for
// exactly this reason (see irreversibleClientCalls).
//
// The banned phrases are checked only on commands that actually talk about
// encryption, because a couple of them ("is untouched") are ordinary English
// elsewhere — 'harbor account' uses it about export formats.
func TestNothingClaimsHistorySurvivesEncryption(t *testing.T) {
	prepareCommandTree()

	// Each phrase asserted the opposite of what the server does.
	banned := []string{
		"stay readable",
		"stays readable",
		"are untouched",
		"is untouched",
		"folded into this write",
		"stored in the clear",
		"snapshotted as plaintext",
	}

	checked := 0
	forEachCommand(t, func(c *cobra.Command) {
		text := c.Long + "\n" + c.Short + "\n" + c.Example
		if !strings.Contains(strings.ToLower(text), "encrypt") {
			return
		}
		checked++
		for _, bad := range banned {
			if strings.Contains(text, bad) {
				t.Errorf("%q still claims history survives — contains %q", c.CommandPath(), bad)
			}
		}
	})
	if checked < 4 {
		t.Fatalf("only %d commands mention encryption — the walk is not reaching the tree", checked)
	}

	// The confirmations are not part of the command tree, so they are named.
	for name, text := range map[string]string{
		"encrypt confirmation":   notesEncryptConfirmation.Warning,
		"decrypt confirmation":   notesDecryptConfirmation.Warning,
		"printed history caveat": captureStderr(t, printHistoryCaveat),
	} {
		for _, bad := range banned {
			if strings.Contains(text, bad) {
				t.Errorf("%s still claims history survives — contains %q", name, bad)
			}
		}
	}

	// And the truth is stated everywhere a user could be about to lose history.
	for name, text := range map[string]string{
		"notes encrypt --help":   notesEncryptCmd.Long,
		"notes decrypt --help":   notesDecryptCmd.Long,
		"notes update --help":    notesUpdateCmd.Long,
		"printed history caveat": captureStderr(t, printHistoryCaveat),
	} {
		if !strings.Contains(text, "DELETES the note's version history") {
			t.Errorf("%s does not say the history is deleted", name)
		}
	}
	// 'harbor history' must not read as though history were permanent.
	if !strings.Contains(historyCmd.Long, "discards every") {
		t.Error("harbor history --help does not mention that encryption discards versions")
	}
}

// TestEncryptDoesNotClaimAnythingAboutAttachmentBytes keeps the other half of the
// same honesty pair in the help. A note whose body is sealed still has readable
// attachments, and a user relying on "the note is encrypted" needs to know that
// before they lean on it, not after.
func TestEncryptDoesNotClaimAnythingAboutAttachmentBytes(t *testing.T) {
	if !strings.Contains(notesEncryptCmd.Long, "does not encrypt the BYTES of attached files") {
		t.Error("the help no longer says attachment bytes are left in the clear")
	}
	if !strings.Contains(notesEncryptCmd.Long, "downloaded and read in full") {
		t.Error("the help states the limitation without saying what it means in practice")
	}
}

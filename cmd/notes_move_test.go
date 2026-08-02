// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-02

package cmd

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/crypto"
)

// ===========================================================================
// A stub server that holds notes AND notebooks
// ===========================================================================
//
// The claim this file has to support is about the relationship between two
// objects — what a note becomes when the notebook it is moved into says its
// notes are encrypted — so the stub has to hold both, and it has to be able to
// CHANGE its answer between two reads of the same note. That last part is not a
// convenience: the whole reason the seal re-reads the note is that another
// device may have sealed it since the caller last looked, and a stub that
// answers identically forever cannot express that at all.

// moveMock is a stub Harbor API holding notes and notebooks.
type moveMock struct {
	m         *apiMock
	notes     map[string]map[string]any
	notebooks map[string]map[string]any

	// notebookStatus, when non-zero, fails every notebook read with that status —
	// the "could not establish what this notebook wants" case.
	notebookStatus int

	// patchStatus/patchBody, when set, fail every PATCH with that status and leave
	// the stored note untouched: a write that did not land.
	patchStatus int
	patchBody   string

	// onNoteRead runs before each GET /notes/:id is answered, told which note and
	// how many times it has been read so far (1 on the first). It is how a test
	// makes the server's answer change BETWEEN the caller's read and the seal's.
	onNoteRead func(noteID string, reads int)
	reads      map[string]int
}

// newMoveMock starts the stub around a set of notebooks and notes.
func newMoveMock(t *testing.T, notebooks []map[string]any, notes ...map[string]any) *moveMock {
	t.Helper()
	mm := &moveMock{
		notes:     map[string]map[string]any{},
		notebooks: map[string]map[string]any{},
		reads:     map[string]int{},
	}
	for _, nb := range notebooks {
		mm.notebooks[nb["id"].(string)] = nb
	}
	for _, n := range notes {
		mm.notes[n["id"].(string)] = n
	}
	mm.m = newAPIMock(t, map[string]mockReply{})
	mm.m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
		switch {
		case r.Method == http.MethodGet && p == "notebooks":
			mm.writeNotebookList(w)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "notebooks/"):
			mm.writeNotebook(w, strings.TrimPrefix(p, "notebooks/"))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/tasks"):
			writeJSON(w, 200, map[string]any{
				"data":   []any{},
				"paging": map[string]any{"limit": 500, "offset": 0, "total": 0, "has_more": false},
			})
		case r.Method == http.MethodGet && p == "notes":
			mm.writeNoteList(w, r.URL.Query().Get("notebook_id"))
		case r.Method == http.MethodGet && strings.HasPrefix(p, "notes/"):
			mm.writeNote(w, strings.TrimPrefix(p, "notes/"))
		case r.Method == http.MethodPatch && strings.HasPrefix(p, "notes/"):
			mm.applyPatch(t, w, strings.TrimPrefix(p, "notes/"))
		default:
			t.Errorf("moveMock: unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return mm
}

// writeNotebook answers GET /notebooks/:id.
func (mm *moveMock) writeNotebook(w http.ResponseWriter, id string) {
	if mm.notebookStatus != 0 {
		writeJSON(w, mm.notebookStatus, map[string]any{"error": map[string]any{
			"code": "internal", "message": "boom",
		}})
		return
	}
	nb, ok := mm.notebooks[id]
	if !ok {
		writeJSON(w, 404, map[string]any{"error": map[string]any{
			"code": "not_found", "message": "no such notebook",
		}})
		return
	}
	writeJSON(w, 200, nb)
}

// writeNotebookList answers GET /notebooks, which is how an empty --notebook is
// resolved to the account's default notebook.
func (mm *moveMock) writeNotebookList(w http.ResponseWriter) {
	if mm.notebookStatus != 0 {
		writeJSON(w, mm.notebookStatus, map[string]any{"error": map[string]any{
			"code": "internal", "message": "boom",
		}})
		return
	}
	items := make([]any, 0, len(mm.notebooks))
	for _, id := range mm.notebookIDs() {
		items = append(items, mm.notebooks[id])
	}
	writeJSON(w, 200, map[string]any{
		"data":   items,
		"paging": map[string]any{"limit": 500, "offset": 0, "total": len(items), "has_more": false},
	})
}

// writeNote answers GET /notes/:id, giving onNoteRead its chance to change what
// the server says first.
func (mm *moveMock) writeNote(w http.ResponseWriter, id string) {
	mm.reads[id]++
	if mm.onNoteRead != nil {
		mm.onNoteRead(id, mm.reads[id])
	}
	n, ok := mm.notes[id]
	if !ok {
		writeJSON(w, 404, map[string]any{"error": map[string]any{
			"code": "not_found", "message": "no such note",
		}})
		return
	}
	writeJSON(w, 200, n)
}

// writeNoteList answers GET /notes filtered by notebook_id, which is what a
// conversion sweep reads.
func (mm *moveMock) writeNoteList(w http.ResponseWriter, notebookID string) {
	items := []any{}
	for _, id := range mm.noteIDs() {
		if n := mm.notes[id]; notebookID == "" || str(n, "notebook_id") == notebookID {
			items = append(items, n)
		}
	}
	writeJSON(w, 200, map[string]any{
		"data":   items,
		"paging": map[string]any{"limit": 500, "offset": 0, "total": len(items), "has_more": false},
	})
}

// applyPatch behaves like the server's note update: apply only the fields the
// body carried, honour base_usn, validate the encrypted envelopes, bump the usn.
func (mm *moveMock) applyPatch(t *testing.T, w http.ResponseWriter, id string) {
	if mm.patchStatus != 0 {
		w.WriteHeader(mm.patchStatus)
		_, _ = w.Write([]byte(mm.patchBody))
		return
	}
	var body map[string]any
	raw := mm.m.requests[len(mm.m.requests)-1].Body
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("moveMock: undecodable PATCH body %q: %v", raw, err)
	}
	note, ok := mm.notes[id]
	if !ok {
		writeJSON(w, 404, map[string]any{"error": map[string]any{
			"code": "not_found", "message": "no such note",
		}})
		return
	}
	if base, ok := body["base_usn"]; ok {
		if int64(base.(float64)) != int64(num(note, "usn")) {
			writeJSON(w, 409, map[string]any{"error": map[string]any{
				"code": "note_usn_stale", "message": "the note has changed",
			}})
			return
		}
	}
	next := map[string]any{}
	for k, v := range note {
		next[k] = v
	}
	for k, v := range body {
		if k == "base_usn" || k == "content_format" {
			continue
		}
		next[k] = v
	}
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
	next["usn"] = num(note, "usn") + 1
	mm.notes[id] = next
	writeJSON(w, 200, map[string]any{"note": next, "usn": next["usn"]})
}

// notebookIDs and noteIDs give the stub a stable listing order, so a test that
// pins traffic does not depend on Go's map iteration.
func (mm *moveMock) notebookIDs() []string { return sortedKeys(mm.notebooks) }
func (mm *moveMock) noteIDs() []string     { return sortedKeys(mm.notes) }

// sortedKeys returns a map's keys in a fixed order.
func sortedKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ===========================================================================
// Fixtures
// ===========================================================================

const (
	// nbPlain is an ordinary notebook, nbLocked one marked "encrypt new notes by
	// default", and nbDefault the account's default notebook — which is what an
	// empty --notebook resolves to and is therefore ALSO marked encrypting, so
	// that a test can tell "resolved the default" apart from "did nothing".
	nbPlain   = "5b1f2c9a-1111-2222-3333-444455556666"
	nbLocked  = "3f43f48d-aaaa-bbbb-cccc-ddddeeeeffff"
	nbDefault = "39705cb9-9999-8888-7777-666655554444"

	moveNoteID = "9c2e1f3a-4b5c-6d7e-8f90-a1b2c3d4e5f6"
)

// moveNotebooks is the notebook set every test in this file runs against. Two of
// the three encrypt by default and one does not, on purpose: a fixture where the
// flag is the same everywhere makes every assertion about it vacuous.
func moveNotebooks() []map[string]any {
	return []map[string]any{
		{"id": nbPlain, "name": "Inbox", "is_default": false, "default_encrypt": false},
		{"id": nbLocked, "name": "Encrypted Testing", "is_default": false, "default_encrypt": true},
		{"id": nbDefault, "name": "My Notes", "is_default": true, "default_encrypt": true},
	}
}

// movePlaintextNote is a readable note sitting in the ordinary notebook, carrying
// the fields a move must not disturb.
func movePlaintextNote() map[string]any {
	return map[string]any{
		"id":            moveNoteID,
		"title":         "Quarterly plan",
		"content":       "<h1>Quarterly plan</h1><p>Ship it.</p>",
		"is_encrypted":  false,
		"notebook_id":   nbPlain,
		"author":        "you@example.com",
		"source_url":    "https://example.com/plan",
		"reminder_time": float64(1750000000000),
		"word_count":    float64(9),
		"usn":           float64(88),
		"created_at":    float64(1749000000000),
		"updated_at":    float64(1750000000000),
		"deleted":       false,
	}
}

// moveEncryptedNote is the same note already sealed, living in the encrypting
// notebook. The envelopes are built with the test's own key so they can be opened
// back and compared.
func moveEncryptedNote(t *testing.T, key []byte, notebookID string) map[string]any {
	t.Helper()
	n := movePlaintextNote()
	n["is_encrypted"] = true
	n["notebook_id"] = notebookID
	n["title"] = sealFixture(t, key, moveNoteID, "title", "Quarterly plan")
	n["content"] = sealFixture(t, key, moveNoteID, "content", "<h1>Quarterly plan</h1><p>Ship it.</p>")
	return n
}

// sealFixture builds one HRBC2 envelope for a fixture.
func sealFixture(t *testing.T, key []byte, noteID, field, plaintext string) string {
	t.Helper()
	sealed, err := crypto.SealField(key, noteID, field, plaintext)
	if err != nil {
		t.Fatalf("SealField(%s): %v", field, err)
	}
	return sealed
}

// lockedSession is unlockedSession's opposite: a run with no passphrase at all,
// which is the state the fail-closed cases are about.
func lockedSession(t *testing.T) {
	t.Helper()
	resetSession()
	t.Setenv("HARBOR_PASSPHRASE", "")
	t.Cleanup(resetSession)
}

// patchesTo counts the PATCHes a run sent to one note, which is how "one write"
// is asserted as a number rather than as a hope.
func patchesTo(mm *moveMock, noteID string) int {
	n := 0
	for _, r := range mm.m.requests {
		if r.Method == http.MethodPatch && strings.HasSuffix(r.Path, "/notes/"+noteID) {
			n++
		}
	}
	return n
}

// ===========================================================================
// R1 — moving a plaintext note into an encrypting notebook seals it
// ===========================================================================

// TestMoveIntoEncryptingNotebookSealsInOneWrite is the acceptance test for the
// whole issue. A readable note is moved into a notebook that encrypts by default
// and must arrive there as ciphertext, in a SINGLE write — because two writes
// mean a window in which the note is sitting in that notebook readable, which is
// the exact state the notebook's owner believes cannot exist.
func TestMoveIntoEncryptingNotebookSealsInOneWrite(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	original := movePlaintextNote()
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked); err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}

	if got := patchesTo(mm, moveNoteID); got != 1 {
		t.Fatalf("the move sent %d writes, not one — a second write is a window where the note is readable in an encrypting notebook", got)
	}
	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if body["notebook_id"] != nbLocked {
		t.Errorf("the one write did not carry the move: notebook_id = %v", body["notebook_id"])
	}
	if enc, _ := body["is_encrypted"].(bool); !enc {
		t.Errorf("the one write did not carry is_encrypted: %v", body["is_encrypted"])
	}
	if got, _ := body["base_usn"].(float64); got != 88 {
		t.Errorf("base_usn = %v, want the usn the read saw (88) — without it an edit that lands in between is overwritten", got)
	}
	if _, sent := body["content_format"]; sent {
		t.Error("the write claims a content_format for a body the server stores as opaque ciphertext")
	}

	stored := mm.notes[moveNoteID]
	title, _ := stored["title"].(string)
	content, _ := stored["content"].(string)
	if !crypto.IsEnvelope(title) || !crypto.IsEnvelope(content) {
		t.Fatalf("the note landed readable: title=%q content=%q", title, content)
	}
	if strings.Contains(content, "Quarterly") || strings.Contains(title, "Quarterly") {
		t.Error("plaintext survived into the stored note")
	}
	if got, err := crypto.OpenField(key, moveNoteID, "title", title); err != nil || got != original["title"] {
		t.Errorf("the sealed title does not open to the original: %v %q", err, got)
	}
	if got, err := crypto.OpenField(key, moveNoteID, "content", content); err != nil || got != original["content"] {
		t.Errorf("the sealed content does not open to the original: %v %q", err, got)
	}
	// Everything the user did not ask to change is still exactly as it was.
	assertOnlyChanged(t, original, stored, "title", "content", "is_encrypted", "notebook_id", "usn")
}

// TestMoveIntoEncryptingNotebookWithoutPassphraseWritesNothing is the fail-closed
// case, and it is the one that distinguishes this fix from the bug. Failing OPEN
// here means the note lands in the encrypting notebook in the clear — which is
// precisely what the CLI did before, silently.
func TestMoveIntoEncryptingNotebookWithoutPassphraseWritesNothing(t *testing.T) {
	lockedSession(t)
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())

	_, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked)

	if err == nil {
		t.Fatal("a plaintext note was moved into an encrypting notebook with no key to seal it")
	}
	if !strings.Contains(err.Error(), passphraseEnv) {
		t.Errorf("the refusal never names the thing that is missing:\n%s", err)
	}
	if got := patchesTo(mm, moveNoteID); got != 0 {
		t.Errorf("the refusal still wrote %d times", got)
	}
	if got := str(mm.notes[moveNoteID], "notebook_id"); got != nbPlain {
		t.Errorf("the note moved anyway: notebook_id = %q", got)
	}
	if boolean(mm.notes[moveNoteID], "is_encrypted") {
		t.Error("the note was marked encrypted by a run that wrote nothing")
	}
}

// TestMoveWithUnreadableDestinationWritesNothing covers the other fail-closed
// door. Creating a note through an unanswerable notebook lookup warns and
// proceeds, deliberately — nothing is lost and re-running fixes it. A MOVE cannot
// borrow that reasoning: the note already exists somewhere safe, and moving it
// into a notebook that may well encrypt and leaving it readable there is not a
// state re-running gets you out of.
func TestMoveWithUnreadableDestinationWritesNothing(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())
	mm.notebookStatus = 500

	_, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked)

	if err == nil {
		t.Fatal("a destination that could not be asked whether it encrypts was treated as one that said no")
	}
	if got := patchesTo(mm, moveNoteID); got != 0 {
		t.Errorf("the refusal still wrote %d times", got)
	}
	if got := str(mm.notes[moveNoteID], "notebook_id"); got != nbPlain {
		t.Errorf("the note moved anyway: notebook_id = %q", got)
	}
}

// TestMoveIntoOrdinaryNotebookIsAPlainMove is the common case, and it has to stay
// cheap and unencumbered: no key, no ciphertext, one PATCH carrying nothing but
// the move.
func TestMoveIntoOrdinaryNotebookIsAPlainMove(t *testing.T) {
	lockedSession(t)
	note := movePlaintextNote()
	note["notebook_id"] = nbLocked
	mm := newMoveMock(t, moveNotebooks(), note)

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbPlain); err != nil {
		t.Fatalf("a move into an ordinary notebook must not need a passphrase: %v", err)
	}

	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if len(body) != 1 || body["notebook_id"] != nbPlain {
		t.Errorf("a plain move sent more than the move: %v", body)
	}
	if boolean(mm.notes[moveNoteID], "is_encrypted") {
		t.Error("an ordinary destination encrypted the note")
	}
}

// ===========================================================================
// The no-op echo, and the resolved destination
// ===========================================================================

// TestReSavingANoteAlreadyInAnEncryptingNotebookIsNotAMove pins the scoping the
// server uses (nbID != note.NotebookID). Re-sending a note's own notebook_id is
// not a move, so it neither seals nor refuses — a CLI that treated it as one
// would refuse a write the API accepts, which is worse than the gap it closes
// because it breaks a command that works today.
func TestReSavingANoteAlreadyInAnEncryptingNotebookIsNotAMove(t *testing.T) {
	lockedSession(t)
	note := movePlaintextNote()
	note["notebook_id"] = nbLocked
	mm := newMoveMock(t, moveNotebooks(), note)

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked); err != nil {
		t.Fatalf("echoing a note's own notebook_id was refused: %v", err)
	}

	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if _, sealed := body["is_encrypted"]; sealed {
		t.Errorf("a no-op echo sealed the note: %v", body)
	}
	if boolean(mm.notes[moveNoteID], "is_encrypted") {
		t.Error("the stored note came back encrypted from a write that was not a move")
	}
}

// TestUnreadableNotebookStillAllowsANoOpEcho is where the two rules above pull
// against each other, and it is the case a mutation run found nothing asserting.
//
// The notebook read fails, so nothing is established about what it wants — and
// the fail-closed rule says a move into an unanswerable destination is refused.
// But this is not a move: the id is the one the note is already in, and that is
// knowable without the read ever succeeding, because a named notebook resolves to
// itself. Refusing here would break re-saving a note whenever a metadata GET
// hiccuped, to protect it from going somewhere it was never going.
func TestUnreadableNotebookStillAllowsANoOpEcho(t *testing.T) {
	lockedSession(t)
	note := movePlaintextNote()
	note["notebook_id"] = nbLocked
	mm := newMoveMock(t, moveNotebooks(), note)
	mm.notebookStatus = 500

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked, "--author", "you@example.com"); err != nil {
		t.Fatalf("a note's own notebook_id was refused because the notebook could not be read: %v", err)
	}
	if got := patchesTo(mm, moveNoteID); got != 1 {
		t.Errorf("the update wrote %d times, want the one PATCH it asked for", got)
	}
	if _, sealed := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)["is_encrypted"]; sealed {
		t.Error("a no-op echo sealed the note")
	}
}

// TestEmptyNotebookFlagResolvesToTheDefaultNotebook is the other half of the same
// scoping rule. `--notebook ""` means "my default notebook", the server resolves
// it before comparing, and if THAT notebook encrypts then the seal is required.
// A CLI that read "" as "no destination" would move a note into an encrypting
// default notebook in the clear and never look at the flag at all.
func TestEmptyNotebookFlagResolvesToTheDefaultNotebook(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", ""); err != nil {
		t.Fatalf("notes update --notebook '': %v", err)
	}

	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if enc, _ := body["is_encrypted"].(bool); !enc {
		t.Fatalf("the default notebook encrypts by default and the move did not seal the note: %v", body)
	}
	// The server does the resolving, so the CLI still sends what the user typed.
	if body["notebook_id"] != "" {
		t.Errorf("notebook_id = %v, want the empty string the user asked for", body["notebook_id"])
	}
	if content, _ := mm.notes[moveNoteID]["content"].(string); !crypto.IsEnvelope(content) {
		t.Errorf("the note landed in the default notebook readable: %q", content)
	}
}

// TestEmptyNotebookFlagOnANoteAlreadyThereIsNotAMove is the resolution rule and
// the no-op rule meeting. The note is already in the default notebook, so "" is
// an echo of where it already is, and nothing should be sealed.
func TestEmptyNotebookFlagOnANoteAlreadyThereIsNotAMove(t *testing.T) {
	lockedSession(t)
	note := movePlaintextNote()
	note["notebook_id"] = nbDefault
	mm := newMoveMock(t, moveNotebooks(), note)

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", ""); err != nil {
		t.Fatalf("echoing the default notebook was refused: %v", err)
	}
	if _, sealed := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)["is_encrypted"]; sealed {
		t.Error("a no-op echo of the default notebook sealed the note")
	}
}

// ===========================================================================
// R3 — the SERVER's is_encrypted wins
// ===========================================================================

// TestMoveDoesNotSealANoteTheServerSaysIsAlreadyEncrypted is the correction the
// web reference paid for with a live bug (app.harbor.my#1267, commit 117a377).
//
// The stub answers this note's FIRST read as plaintext and every read after it as
// encrypted — another device sealing it in between, which is exactly what the
// caller's copy cannot know. If the seal trusts that first answer it wraps
// ciphertext in a second envelope: a note that decrypts once into more
// ciphertext, and reads as corrupt to every client including this one.
func TestMoveDoesNotSealANoteTheServerSaysIsAlreadyEncrypted(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	sealed := moveEncryptedNote(t, key, nbPlain)
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())
	mm.onNoteRead = func(noteID string, reads int) {
		if reads > 1 {
			mm.notes[noteID] = sealed
		}
	}

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked); err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}

	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if _, resealed := body["content"]; resealed {
		t.Errorf("the note was sealed a second time — the write carries content: %v", body)
	}
	if body["notebook_id"] != nbLocked {
		t.Errorf("the move itself was dropped: %v", body)
	}
	stored, _ := mm.notes[moveNoteID]["content"].(string)
	if stored != sealed["content"] {
		t.Error("the stored ciphertext changed — it was re-encrypted rather than left alone")
	}
	// The decisive check: one open must yield readable text, not another envelope.
	opened, err := crypto.OpenField(key, moveNoteID, "content", stored)
	if err != nil {
		t.Fatalf("the stored note no longer opens: %v", err)
	}
	if crypto.IsEnvelope(opened) {
		t.Error("the note decrypts into more ciphertext — it was double-encrypted")
	}
}

// TestMoveOfAnAlreadyEncryptedNoteIsAPlainMove is R3's ordinary case: the note
// was encrypted before the command started and stays byte-identical through it.
// No re-encryption and no re-key — the field AAD binds to the note id, not to the
// notebook, so there is nothing about the move for the envelope to care about.
func TestMoveOfAnAlreadyEncryptedNoteIsAPlainMove(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	before := moveEncryptedNote(t, key, nbPlain)
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbPlain))
	mm.notes[moveNoteID]["title"] = before["title"]
	mm.notes[moveNoteID]["content"] = before["content"]

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked); err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}

	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if len(body) != 1 || body["notebook_id"] != nbLocked {
		t.Errorf("an already-encrypted note was re-written rather than moved: %v", body)
	}
	after := mm.notes[moveNoteID]
	if after["content"] != before["content"] || after["title"] != before["title"] {
		t.Error("the ciphertext was rewritten by a move that had nothing to encrypt")
	}
	assertOnlyChanged(t, before, after, "notebook_id", "usn")
}

// ===========================================================================
// R2 — moving OUT changes nothing, and says so
// ===========================================================================

// TestMovingAnEncryptedNoteOutLeavesItEncrypted pins the no-op AND the notice.
// Both matter: the behaviour is right and surprising, and a user who moved a note
// into an ordinary notebook expecting that to un-encrypt it needs to be told
// otherwise rather than discovering it later.
func TestMovingAnEncryptedNoteOutLeavesItEncrypted(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	before := moveEncryptedNote(t, key, nbLocked)
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbLocked))
	mm.notes[moveNoteID]["title"] = before["title"]
	mm.notes[moveNoteID]["content"] = before["content"]

	var err error
	said := captureStderr(t, func() {
		_, err = runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbPlain)
	})
	if err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}

	after := mm.notes[moveNoteID]
	if !boolean(after, "is_encrypted") {
		t.Fatal("moving an encrypted note into an ordinary notebook decrypted it")
	}
	if after["content"] != before["content"] {
		t.Error("the ciphertext changed on the way out")
	}
	assertOnlyChanged(t, before, after, "notebook_id", "usn")

	want := "note stays encrypted — encryption is per-note, moving it out does not remove it"
	if !strings.Contains(said, want) {
		t.Errorf("the move-out said nothing about the note still being encrypted:\n%s", said)
	}
}

// TestMovingAnEncryptedNoteBetweenOrdinaryNotebooksIsQuiet is the other side of
// that notice, and it is what keeps it worth reading. The message is about
// LEAVING a notebook that encrypts; firing it on every move of every encrypted
// note would make it noise, and noise is not a warning.
func TestMovingAnEncryptedNoteBetweenOrdinaryNotebooksIsQuiet(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	notebooks := append(moveNotebooks(), map[string]any{
		"id": "0000aaaa-1111-2222-3333-444455556666", "name": "Archive",
		"is_default": false, "default_encrypt": false,
	})
	mm := newMoveMock(t, notebooks, moveEncryptedNote(t, key, nbPlain))

	var err error
	said := captureStderr(t, func() {
		_, err = runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", "0000aaaa-1111-2222-3333-444455556666")
	})
	if err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}
	if strings.Contains(said, "moving it out does not remove it") {
		t.Errorf("the move-out notice fired for a move that left no encrypting notebook:\n%s", said)
	}
}

// ===========================================================================
// One pipeline, not two
// ===========================================================================

// TestSealedMoveAndNotesEncryptSendTheSameWrite is the behavioural form of "there
// is exactly one plaintext-to-ciphertext pipeline", and it is the assertion that
// actually holds the line.
//
// The same note is sealed twice — once by `notes encrypt`, once by a move into an
// encrypting notebook — and the two PATCHes must be the same write apart from the
// move itself. A forked second pipeline is not caught by asking whether it seals;
// it is caught by the ways the two copies drift while both still "work": one
// forgets the compare-and-set, one starts sending content_format, one seals the
// title and the other does not. Every one of those shows up here as a difference.
func TestSealedMoveAndNotesEncryptSendTheSameWrite(t *testing.T) {
	key := newMasterKey(t)

	unlockedSession(t, key)
	viaEncrypt := newMoveMock(t, moveNotebooks(), movePlaintextNote())
	if _, err := runCLI(t, viaEncrypt.m, "notes", "encrypt", moveNoteID); err != nil {
		t.Fatalf("notes encrypt: %v", err)
	}
	encryptBody := viaEncrypt.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)

	unlockedSession(t, key)
	viaMove := newMoveMock(t, moveNotebooks(), movePlaintextNote())
	if _, err := runCLI(t, viaMove.m, "notes", "update", moveNoteID, "--notebook", nbLocked); err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}
	moveBody := viaMove.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)

	// The move carries the destination and nothing else beyond the seal.
	if moveBody["notebook_id"] != nbLocked {
		t.Errorf("the sealed move did not carry the move: %v", moveBody["notebook_id"])
	}
	delete(moveBody, "notebook_id")

	if len(moveBody) != len(encryptBody) {
		t.Fatalf("the two seals send different field sets:\n  notes encrypt: %v\n  sealed move:   %v",
			sortedFieldNames(encryptBody), sortedFieldNames(moveBody))
	}
	for k, want := range encryptBody {
		got, present := moveBody[k]
		if !present {
			t.Errorf("the sealed move omits %q, which notes encrypt sends", k)
			continue
		}
		// The envelopes differ byte-for-byte on every seal (a fresh nonce), so they
		// are compared by what they OPEN to — which is the thing that has to match.
		ws, isEnvelope := want.(string)
		if isEnvelope && crypto.IsEnvelope(ws) {
			gs, _ := got.(string)
			if !crypto.IsEnvelope(gs) {
				t.Errorf("the sealed move sent %q in the clear where notes encrypt sealed it", k)
				continue
			}
			a, aerr := crypto.OpenField(key, moveNoteID, k, ws)
			b, berr := crypto.OpenField(key, moveNoteID, k, gs)
			if aerr != nil || berr != nil || a != b {
				t.Errorf("the two pipelines sealed different %s: %q vs %q (%v %v)", k, a, b, aerr, berr)
			}
			continue
		}
		if got != want {
			t.Errorf("the two pipelines disagree about %q: notes encrypt sent %v, the sealed move sent %v", k, want, got)
		}
	}
}

// sortedFieldNames names a wire body's fields for a failure message.
func sortedFieldNames(body map[string]any) []string {
	out := make([]string, 0, len(body))
	for k := range body {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestOnlyTheKnownFunctionsSeal is the structural tripwire the as-built record
// asked for: a test that fails when a SECOND sealing path appears.
//
// It is deliberately the weaker of the two guards — it proves a name exists, not
// that the code does the right thing, and the test above is what proves the
// behaviour. What it adds is the case that one cannot cover: a new function that
// seals correctly today. That is exactly how the reference's fork started, and it
// stayed correct for about a day.
func TestOnlyTheKnownFunctionsSeal(t *testing.T) {
	// encryptCreateBody seals a note being born, encryptUpdateBody re-seals one
	// that is already ciphertext, and sealAndVerify is the single primitive the
	// in-place conversion (planEncrypt) — and therefore the sealed move — goes
	// through. A fourth name here means a fourth idea about how to encrypt a note.
	allowed := map[string]bool{
		"encryptCreateBody": true,
		"encryptUpdateBody": true,
		"sealAndVerify":     true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd package: %v", err)
	}
	fset := token.NewFileSet()
	found := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, isFunc := n.(*ast.FuncDecl)
			if !isFunc {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, isCall := inner.(*ast.CallExpr)
				if !isCall {
					return true
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel || sel.Sel.Name != "SealField" {
					return true
				}
				if pkgName, isIdent := sel.X.(*ast.Ident); isIdent && pkgName.Name == "crypto" {
					found[fn.Name.Name] = true
				}
				return true
			})
			return false
		})
	}

	if len(found) == 0 {
		t.Fatal("no crypto.SealField call sites were found at all — this test is asserting nothing")
	}
	for name := range found {
		if !allowed[name] {
			t.Errorf("%s() turns plaintext into ciphertext, and it is not one of the pipelines this CLI is supposed to have. "+
				"Extend the existing one instead — the web reference forked its seal and the two copies disagreed inside a single PR", name)
		}
	}
	for name := range allowed {
		if !found[name] {
			t.Errorf("%s() no longer seals anything — this allowlist has gone stale and is no longer guarding what it claims to", name)
		}
	}
}

// ===========================================================================
// Editing and moving in the same command
// ===========================================================================

// TestSealedMoveSealsTheNewTitleNotTheOldOne covers the command that both edits
// and moves. The seal reads the note from the SERVER, so the naive version seals
// the title that is stored and then sends the user's new title beside it in the
// clear — which is neither an update nor an encryption, and which the server
// would reject as a plaintext field on an encrypted note.
func TestSealedMoveSealsTheNewTitleNotTheOldOne(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID,
		"--notebook", nbLocked, "--title", "Quarterly plan (final)"); err != nil {
		t.Fatalf("notes update --title --notebook: %v", err)
	}

	title, _ := mm.notes[moveNoteID]["title"].(string)
	if !crypto.IsEnvelope(title) {
		t.Fatalf("the new title was stored in the clear: %q", title)
	}
	got, err := crypto.OpenField(key, moveNoteID, "title", title)
	if err != nil {
		t.Fatalf("the sealed title does not open: %v", err)
	}
	if got != "Quarterly plan (final)" {
		t.Errorf("the sealed title is %q — the command sealed the note's old title and threw the edit away", got)
	}
}

// TestSealedMoveSealsTheNewBodyAndClaimsNoFormat is the same case for the body,
// and it is the only one that can catch the format claim: a content_format only
// exists on the wire when a content flag put it there, so an assertion about it
// on a move that carries no body is asserting about a field that was never going
// to be present.
//
// The server stores an encrypted body verbatim and never renders it, so a format
// alongside ciphertext is a claim about bytes nothing will look at — and on the
// way back out it is what a decrypt would use to interpret them.
func TestSealedMoveSealsTheNewBodyAndClaimsNoFormat(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())

	if _, err := runCLI(t, mm.m, "notes", "update", moveNoteID,
		"--notebook", nbLocked, "--format", "html", "--content", "<p>Rewritten</p>"); err != nil {
		t.Fatalf("notes update --content --notebook: %v", err)
	}

	body := mm.m.bodyOf(t, "PATCH /api/v1/notes/"+moveNoteID)
	if _, sent := body["content_format"]; sent {
		t.Errorf("the write claims a content_format for ciphertext: %v", body)
	}
	content, _ := mm.notes[moveNoteID]["content"].(string)
	if !crypto.IsEnvelope(content) {
		t.Fatalf("the new body was stored in the clear: %q", content)
	}
	got, err := crypto.OpenField(key, moveNoteID, "content", content)
	if err != nil {
		t.Fatalf("the sealed body does not open: %v", err)
	}
	if got != "<p>Rewritten</p>" {
		t.Errorf("the sealed body is %q — the command sealed the note's old body and threw the edit away", got)
	}
}

// ===========================================================================
// The caveats a sealed move inherits
// ===========================================================================

// TestSealedMovePrintsTheAttachmentCaveat pins that the move says what it does
// NOT cover, and says it in the words `notes encrypt` documents. Nobody reads
// `notes update --help` to find out what encryption covers, so the sealed move
// has to volunteer it.
func TestSealedMovePrintsTheAttachmentCaveat(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())

	var err error
	said := captureStderr(t, func() {
		_, err = runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked)
	})
	if err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}

	for _, line := range strings.Split(attachmentCaveat, "\n") {
		if !strings.Contains(said, line) {
			t.Errorf("the sealed move never said %q — the user is left believing their attachments were sealed too:\n%s", line, said)
		}
	}
	if !strings.Contains(said, "Note moved and encrypted") {
		t.Errorf("the sealed move never said it encrypted anything:\n%s", said)
	}
	// It is a limit, not an apology: the sentence must still be the one the
	// encrypt command's help text carries.
	if !strings.Contains(notesEncryptCmd.Long, "It does not encrypt the BYTES of attached files.") {
		t.Error("the encrypt help text no longer carries the attachment caveat this move copies")
	}
}

// TestPlainMoveIsQuiet keeps the caveats meaningful. A move that encrypted
// nothing has nothing to caveat, and a command that prints the same warning
// whatever it did has stopped saying anything.
func TestPlainMoveIsQuiet(t *testing.T) {
	lockedSession(t)
	note := movePlaintextNote()
	note["notebook_id"] = nbLocked
	mm := newMoveMock(t, moveNotebooks(), note)

	var err error
	said := captureStderr(t, func() {
		_, err = runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbPlain)
	})
	if err != nil {
		t.Fatalf("notes update --notebook: %v", err)
	}
	if strings.Contains(said, "attached files") || strings.Contains(said, "Note moved and encrypted") {
		t.Errorf("a move that encrypted nothing talked about encryption:\n%s", said)
	}
}

// ===========================================================================
// R7 — the server's own backstop
// ===========================================================================

// TestServerRefusalOfAPlaintextMoveReadsAsEnglish covers the case where the local
// check was bypassed — an older binary, some other path — and the server refuses
// instead. Nothing is written and no usn is spent, so the message's job is to say
// what to do next rather than to translate an error code.
func TestServerRefusalOfAPlaintextMoveReadsAsEnglish(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())
	mm.patchStatus = 422
	mm.patchBody = apiErrorBody("cannot_move_plaintext_into_encrypted",
		"That notebook keeps its notes encrypted; encrypt the note and move it in the same request.")

	_, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked)

	if err == nil {
		t.Fatal("the server refused the move and the CLI reported success")
	}
	if strings.Contains(err.Error(), "cannot_move_plaintext_into_encrypted") {
		t.Errorf("the refusal is a raw error code:\n%s", err)
	}
	if !strings.Contains(err.Error(), passphraseEnv) {
		t.Errorf("the refusal never says what to do about it:\n%s", err)
	}
}

// TestSealedMoveVerifiesTheServersAnswer covers a server that took the write and
// did not take the flag — an older build, a proxy that dropped a field. HTTP 200
// is not the same as "the note is encrypted", and reporting success here would
// leave the user believing a readable note is sealed.
func TestSealedMoveVerifiesTheServersAnswer(t *testing.T) {
	unlockedSession(t, newMasterKey(t))
	mm := newMoveMock(t, moveNotebooks(), movePlaintextNote())
	mm.patchStatus = 200
	mm.patchBody = `{"note":{"id":"` + moveNoteID + `","is_encrypted":false,"notebook_id":"` + nbLocked + `"},"usn":89}`

	_, err := runCLI(t, mm.m, "notes", "update", moveNoteID, "--notebook", nbLocked)

	if err == nil {
		t.Fatal("the server reported the note as still plaintext and the CLI called it encrypted")
	}
	if !strings.Contains(err.Error(), "is_encrypted") {
		t.Errorf("the failure does not say what came back wrong:\n%s", err)
	}
}

// ===========================================================================
// R5 — decrypt is gated by the notebook
// ===========================================================================

// TestDecryptInsideAnEncryptingNotebookIsRefused is the CLI's form of the dialog
// the other clients show. The note stays sealed and nothing is written; the way
// through is to move it out, which is allowed and which the message names.
func TestDecryptInsideAnEncryptingNotebookIsRefused(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbLocked))

	_, err := runCLI(t, mm.m, "notes", "decrypt", moveNoteID, "--yes")

	if err == nil {
		t.Fatal("a note inside an always-encrypted notebook was decrypted in place")
	}
	if !strings.Contains(err.Error(), `notes in "Encrypted Testing" are always encrypted`) {
		t.Errorf("the refusal does not name the notebook:\n%s", err)
	}
	if !strings.Contains(err.Error(), "harbor notes update "+moveNoteID+" --notebook <notebook-id>") {
		t.Errorf("the refusal does not say how to get through it:\n%s", err)
	}
	if got := patchesTo(mm, moveNoteID); got != 0 {
		t.Errorf("the refusal still wrote %d times", got)
	}
	if !boolean(mm.notes[moveNoteID], "is_encrypted") {
		t.Error("the note was decrypted by a run that refused")
	}
}

// TestDecryptGateIsNotAConfirmation is the point of R5's last line. --yes says
// "I understand decrypting publishes this note", which is a different question
// from "does this notebook hold readable notes at all". The gate is a rule, and a
// rule that a flag turns off is a suggestion.
func TestDecryptGateIsNotAConfirmation(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbLocked))

	if _, err := runCLI(t, mm.m, "notes", "decrypt", moveNoteID, "--yes"); err == nil {
		t.Fatal("--yes decrypted a note inside an always-encrypted notebook")
	}
	if got := patchesTo(mm, moveNoteID); got != 0 {
		t.Errorf("--yes wrote %d times past the gate", got)
	}
}

// TestDecryptOutsideAnEncryptingNotebookStillWorks is the control. The gate has
// to be about the notebook and nothing else — a gate that refuses everything
// would pass every assertion above while breaking the command.
func TestDecryptOutsideAnEncryptingNotebookStillWorks(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbPlain))

	if _, err := runCLI(t, mm.m, "notes", "decrypt", moveNoteID, "--yes"); err != nil {
		t.Fatalf("a note in an ordinary notebook could not be decrypted: %v", err)
	}
	if boolean(mm.notes[moveNoteID], "is_encrypted") {
		t.Error("the note was not decrypted")
	}
	if got := str(mm.notes[moveNoteID], "title"); got != "Quarterly plan" {
		t.Errorf("title = %q, want the plaintext back", got)
	}
}

// TestDecryptWhenTheNotebookCannotBeReadFallsThrough is the deliberate asymmetry
// with the move path, and it is worth stating: this gate is a courtesy on a note
// the user owns and has already unlocked, and the invariant that actually
// protects the notebook is enforced on the way IN. Refusing to decrypt because a
// metadata GET timed out would be the wrong trade.
func TestDecryptWhenTheNotebookCannotBeReadFallsThrough(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbLocked))
	mm.notebookStatus = 500

	if _, err := runCLI(t, mm.m, "notes", "decrypt", moveNoteID, "--yes"); err != nil {
		t.Fatalf("an unreadable notebook blocked a decrypt: %v", err)
	}
	if boolean(mm.notes[moveNoteID], "is_encrypted") {
		t.Error("the note was not decrypted")
	}
}

// TestDecryptSweepSkipsGatedNotesWithoutAborting covers the notebook sweep. One
// note that cannot be converted is no reason to abandon the rest, so each is
// reported with its own reason and the run still exits non-zero — a script must
// not be able to read a partial run as a clean one.
func TestDecryptSweepSkipsGatedNotesWithoutAborting(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	second := moveEncryptedNote(t, key, nbLocked)
	second["id"] = "11112222-3333-4444-5555-666677778888"
	second["title"] = sealFixture(t, key, "11112222-3333-4444-5555-666677778888", "title", "Second")
	second["content"] = sealFixture(t, key, "11112222-3333-4444-5555-666677778888", "content", "<p>Second</p>")
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbLocked), second)

	out, err := runCLI(t, mm.m, "notes", "decrypt", "--notebook", nbLocked, "--yes")

	if err == nil {
		t.Fatal("a sweep that decrypted nothing exited zero")
	}
	if got := patchesTo(mm, moveNoteID); got != 0 {
		t.Errorf("the sweep wrote %d times to a gated note", got)
	}
	for _, id := range []string{moveNoteID, second["id"].(string)} {
		if !strings.Contains(out, id) {
			t.Errorf("the sweep never reported note %s at all:\n%s", id, out)
		}
		if !boolean(mm.notes[id], "is_encrypted") {
			t.Errorf("note %s was decrypted inside an always-encrypted notebook", id)
		}
	}
	if !strings.Contains(out, "are always encrypted") {
		t.Errorf("the sweep reported failures without saying why:\n%s", out)
	}
}

// TestDecryptSweepAsksAboutTheNotebookOnce keeps the gate from turning a sweep
// into one extra round trip per note. The answer cannot change inside a single
// command, so asking twice is asking twice for nothing.
func TestDecryptSweepAsksAboutTheNotebookOnce(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	second := moveEncryptedNote(t, key, nbLocked)
	second["id"] = "11112222-3333-4444-5555-666677778888"
	second["title"] = sealFixture(t, key, "11112222-3333-4444-5555-666677778888", "title", "Second")
	second["content"] = sealFixture(t, key, "11112222-3333-4444-5555-666677778888", "content", "<p>Second</p>")
	mm := newMoveMock(t, moveNotebooks(), moveEncryptedNote(t, key, nbLocked), second)

	_, _ = runCLI(t, mm.m, "notes", "decrypt", "--notebook", nbLocked, "--yes")

	reads := 0
	for _, r := range mm.m.requests {
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/notebooks/"+nbLocked) {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("the sweep read the same notebook %d times; two notes should cost one lookup", reads)
	}
}

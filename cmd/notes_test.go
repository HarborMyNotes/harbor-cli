// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
)

func TestExtractNote(t *testing.T) {
	// Mutation envelope.
	n, usn := extractNote([]byte(`{"note":{"id":"n1","title":"T"},"usn":88}`))
	if str(n, "id") != "n1" || usn != "88" {
		t.Errorf("mutation extract: id=%q usn=%q", str(n, "id"), usn)
	}
	// Bare note.
	n2, usn2 := extractNote([]byte(`{"id":"n2","title":"T2"}`))
	if str(n2, "id") != "n2" || usn2 != "" {
		t.Errorf("bare extract: id=%q usn=%q", str(n2, "id"), usn2)
	}
}

func TestDisplayNoteRendersBodyAndUSN(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","title":"Plan","notebook_id":"nb1","is_encrypted":false,"word_count":3,"usn":88,"content":"<p>Hello <strong>world</strong></p>","updated_at":1750000000000},"usn":88}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "Plan") {
		t.Errorf("title missing:\n%s", out)
	}
	if !strings.Contains(out, "New USN") {
		t.Errorf("new USN missing:\n%s", out)
	}
	// HTML body should be stripped to readable text.
	if !strings.Contains(out, "Hello world") {
		t.Errorf("body not rendered:\n%s", out)
	}
}

func TestDisplayNoteSourceAndAuthor(t *testing.T) {
	// A clipped note carries source_url and author: both should print.
	clipped := []byte(`{"id":"n1","title":"Clip","source_url":"https://example.com/clip","author":"Jane Doe","content":"body"}`)
	out := captureStdout(t, func() { displayNote(clipped) })
	if !strings.Contains(out, "Source") || !strings.Contains(out, "https://example.com/clip") {
		t.Errorf("source line missing:\n%s", out)
	}
	if !strings.Contains(out, "Author") || !strings.Contains(out, "Jane Doe") {
		t.Errorf("author line missing:\n%s", out)
	}
	// A plain note has neither field: nothing extra should appear.
	plain := []byte(`{"id":"n2","title":"Plain","content":"body"}`)
	out = captureStdout(t, func() { displayNote(plain) })
	if strings.Contains(out, "Source") || strings.Contains(out, "Author") {
		t.Errorf("unclipped note should not print Source/Author:\n%s", out)
	}
}

func TestDisplayNoteEncrypted(t *testing.T) {
	data := []byte(`{"id":"n1","title":"sealed","is_encrypted":true,"content":"AAAA"}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "[encrypted]") {
		t.Errorf("encrypted body should be hidden:\n%s", out)
	}
	if strings.Contains(out, "AAAA") {
		t.Errorf("ciphertext should not be printed:\n%s", out)
	}
}

func TestDisplayNotesTable(t *testing.T) {
	data := []byte(`{"data":[{"id":"n1","title":"Plan","notebook_id":"nbxxxxxxxx","is_encrypted":true,"word_count":3,"usn":88,"updated_at":1750000000000}],"paging":{"offset":0,"total":1}}`)
	out := captureStdout(t, func() { displayNotes(data) })
	if !strings.Contains(out, "Plan") || !strings.Contains(out, "🔒") {
		t.Errorf("notes table missing fields:\n%s", out)
	}
}

func TestMapNoteError(t *testing.T) {
	cases := map[string]string{
		"note_title_too_long":            "title is too long",
		"note_too_large":                 "too large",
		"append_not_supported_encrypted": "encrypted",
		// The base_usn precondition the task guard sends. The message has to say
		// "nothing was written" and "merge" — a user told only "stale" retries the
		// same body, which is exactly the clobber the server just refused.
		"note_usn_stale": "nothing was written",
	}
	for code, sub := range cases {
		if got := mapNoteError(apiErr(code)); !strings.Contains(got.Error(), sub) {
			t.Errorf("mapNoteError(%s) = %q", code, got.Error())
		}
	}
	if got := mapNoteError(apiErr("note_usn_stale")); !strings.Contains(got.Error(), "merge") {
		t.Errorf("the stale-usn message never says to merge: %q", got)
	}
}

// ===========================================================================
// `notes delete --permanent` — the second route to a permanent expunge
// ===========================================================================
//
// `notes delete` is the safe, everyday command; the one flag that makes it
// irreversible is easy to miss when skimming a script. It reaches the same
// expunge `trash expunge` does, so it asks the same question — and, like every
// other gate, the wrong-answer branch is pinned at the call site rather than
// only in the helper.

// TestNotesDeletePermanentRunEStopsOnAWrongAnswer proves nothing reaches the
// server when the user declines.
func TestNotesDeletePermanentRunEStopsOnAWrongAnswer(t *testing.T) {
	for _, answer := range []string{"no", "n", "", "y", "YES"} {
		t.Run(answer, func(t *testing.T) {
			answerPrompt(t, answer)
			m := newAPIMock(t, map[string]mockReply{})

			out, err := runCLI(t, m, "notes", "delete", "n1", "--permanent")
			if err == nil {
				t.Fatalf("answering %q permanently deleted the note", answer)
			}
			if len(m.calls()) != 0 {
				t.Fatalf("the note was deleted anyway: %v", m.calls())
			}
			if strings.Contains(out, "permanently deleted") {
				t.Errorf("stdout claimed success after an abort:\n%s", out)
			}
		})
	}
}

// TestNotesDeletePermanentRunERefusesUnattended covers scripts and agents: no
// terminal to ask at means refuse, not proceed.
func TestNotesDeletePermanentRunERefusesUnattended(t *testing.T) {
	pipedStdin(t)
	m := newAPIMock(t, map[string]mockReply{})

	_, err := runCLI(t, m, "notes", "delete", "n1", "--permanent")
	if err == nil {
		t.Fatal("an unattended --permanent delete without --yes must refuse")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %q, want it to name the flag that would have worked", err.Error())
	}
	if len(m.calls()) != 0 {
		t.Errorf("nothing should have been sent, got %v", m.calls())
	}
}

// TestNotesDeletePermanentRunEProceedsWhenConfirmed keeps both confirmed routes
// working, and pins that --permanent really reaches the wire as permanent — a
// gate that quietly downgraded the delete to a trash would also "pass" the
// aborts above.
func TestNotesDeletePermanentRunEProceedsWhenConfirmed(t *testing.T) {
	const route = "DELETE /api/v1/notes/n1"

	answerPrompt(t, "yes")
	m := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})
	out, err := runCLI(t, m, "notes", "delete", "n1", "--permanent")
	if err != nil {
		t.Fatalf("typed yes: %v", err)
	}
	if got := m.queryOf(t, route).Get("permanent"); got != "true" {
		t.Errorf("permanent=%q on the wire, want \"true\"", got)
	}
	if !strings.Contains(out, "permanently deleted") {
		t.Errorf("output = %q", out)
	}

	pipedStdin(t) // --yes must not prompt
	m2 := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})
	if _, err := runCLI(t, m2, "notes", "delete", "n1", "--permanent", "--yes"); err != nil {
		t.Fatalf("--yes: %v", err)
	}
	if len(m2.calls()) != 1 {
		t.Errorf("calls = %v", m2.calls())
	}
}

// TestNotesDeleteWithoutPermanentNeverAsks is the other half of the rule. The
// ordinary delete is recoverable, and making it prompt would train people to
// type "yes" without reading — which is how the prompt that matters stops being
// read at all.
func TestNotesDeleteWithoutPermanentNeverAsks(t *testing.T) {
	const route = "DELETE /api/v1/notes/n1"
	pipedStdin(t) // any prompt fails the test
	m := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})

	out, err := runCLI(t, m, "notes", "delete", "n1")
	if err != nil {
		t.Fatalf("trashing a note must not need confirmation: %v", err)
	}
	if !strings.Contains(out, "moved to trash") {
		t.Errorf("output = %q", out)
	}
	if got := m.queryOf(t, route).Get("permanent"); got == "true" {
		t.Errorf("a plain delete must not send permanent=true")
	}
}

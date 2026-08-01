// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
)

// TestDisplayTrash checks the trash list renders titles and the paging footer.
func TestDisplayTrash(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"n1","title":"Quarterly plan","notebook_id":"nb1aaaaaaa","is_encrypted":false,"trashed_at":1750000000000,"usn":90,"updated_at":1750000000000},
		{"id":"n2","title":"Old draft","notebook_id":"nb2","is_encrypted":true,"trashed_at":1750000000000,"usn":91,"updated_at":1750000000000}
	],"paging":{"limit":100,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayTrash(data) })
	if !strings.Contains(out, "Quarterly plan") || !strings.Contains(out, "Old draft") {
		t.Errorf("missing trashed note titles:\n%s", out)
	}
	if !strings.Contains(out, "showing 1–2 of 2") {
		t.Errorf("paging footer missing:\n%s", out)
	}
}

// TestDisplayTrashEmpty checks an empty trash renders the friendly no-results
// line rather than a bare table.
func TestDisplayTrashEmpty(t *testing.T) {
	data := []byte(`{"data":[],"paging":{"limit":100,"offset":0,"total":0,"has_more":false}}`)
	out := captureStdout(t, func() { displayTrash(data) })
	if !strings.Contains(out, "No results.") {
		t.Errorf("expected No results., got:\n%s", out)
	}
}

// TestDisplayRestoredNote checks a restore confirms and shows the note detail.
func TestDisplayRestoredNote(t *testing.T) {
	data := []byte(`{"id":"n1","title":"Quarterly plan","notebook_id":"nb1","in_trash":false,"is_encrypted":false,"usn":92,"updated_at":1750000060000}`)
	out := captureStdout(t, func() { displayRestoredNote(data) })
	if !strings.Contains(out, "restored") {
		t.Errorf("missing restored confirmation:\n%s", out)
	}
	if !strings.Contains(out, "Quarterly plan") || !strings.Contains(out, "n1") {
		t.Errorf("missing restored note fields:\n%s", out)
	}
}

// TestDisplayEmptyTrash checks the expunged-count message for plural and
// singular counts.
func TestDisplayEmptyTrash(t *testing.T) {
	out := captureStdout(t, func() { displayEmptyTrash([]byte(`{"expunged":3}`)) })
	if !strings.Contains(out, "3 notes") {
		t.Errorf("expected plural count, got:\n%s", out)
	}
	one := captureStdout(t, func() { displayEmptyTrash([]byte(`{"expunged":1}`)) })
	if !strings.Contains(one, "1 note permanently deleted") {
		t.Errorf("expected singular count, got:\n%s", one)
	}
}

// TestMapTrashError checks the trash-specific friendly error messages.
func TestMapTrashError(t *testing.T) {
	cases := map[string]string{
		"not_in_trash":      "not in the trash",
		"validation_failed": "invalid sort field",
	}
	for code, sub := range cases {
		got := mapTrashError(apiErr(code))
		if !strings.Contains(got.Error(), sub) {
			t.Errorf("mapTrashError(%s) = %q, want substring %q", code, got.Error(), sub)
		}
	}
}

// ===========================================================================
// The destructive gate
// ===========================================================================
//
// `trash empty` permanently deletes every note in the bin, so the gate in front
// of it is the last thing between a user and losing notes. The rules themselves
// are pinned once in confirm_test.go; what follows pins THIS command — that it
// consults the gate at all, and that a refusal really stops the request rather
// than printing a line and deleting anyway.

// TestTrashConfirmEmptyYes verifies --yes bypasses the prompt entirely.
func TestTrashConfirmEmptyYes(t *testing.T) {
	if err := trashConfirmEmpty(true); err != nil {
		t.Errorf("with --yes, want nil, got %v", err)
	}
}

// TestTrashConfirmEmptyJSONRequiresYes verifies that in --json mode (where we
// must never prompt) confirmation without --yes is refused.
func TestTrashConfirmEmptyJSONRequiresYes(t *testing.T) {
	jsonOutput = true
	defer func() { jsonOutput = false }()
	err := trashConfirmEmpty(false)
	if err == nil {
		t.Fatal("expected refusal in --json mode without --yes")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %q, want it to mention --yes", err.Error())
	}
}

// TestTrashConfirmEmptyAbortsOnAnyAnswerButYes drives the branch that only a
// person at a keyboard could reach before: the user was asked, and said
// something other than "yes".
func TestTrashConfirmEmptyAbortsOnAnyAnswerButYes(t *testing.T) {
	for _, answer := range []string{"no", "n", "", "y", "YES", "nope"} {
		t.Run(answer, func(t *testing.T) {
			asked := answerPrompt(t, answer)
			var err error
			captureStdout(t, func() { err = trashConfirmEmpty(false) })
			if err == nil {
				t.Fatalf("answering %q emptied the trash", answer)
			}
			if !strings.Contains(err.Error(), "the trash was not emptied") {
				t.Errorf("err = %q, want it to say the trash was not emptied", err.Error())
			}
			if len(*asked) != 1 {
				t.Errorf("the user should have been asked exactly once, got %v", *asked)
			}
		})
	}
}

// TestTrashConfirmEmptyProceedsOnTypedYes is the other side of the same seam —
// the gate must not be so strict that the documented answer fails.
func TestTrashConfirmEmptyProceedsOnTypedYes(t *testing.T) {
	answerPrompt(t, "yes")
	var err error
	out := captureStdout(t, func() { err = trashConfirmEmpty(false) })
	if err != nil {
		t.Fatalf("typing yes must proceed, got %v", err)
	}
	if !strings.Contains(out, "cannot be undone") {
		t.Errorf("the user must be told this is irreversible before typing:\n%s", out)
	}
}

// ===========================================================================
// RunE wiring (the real command against a stub API)
// ===========================================================================
//
// The assertion that matters in every abort case below is m.calls(): a guard
// that returns an error the command ignores is not a guard. Nothing may reach
// the server.

// emptyTrashRoute is the one destructive call `trash empty` makes.
const emptyTrashRoute = "DELETE /api/v1/trash"

// TestTrashEmptyRunEStopsOnAnyAnswerButYes is the regression this whole issue is
// about: a typed "no" must abort, exit non-zero, and send nothing at all.
func TestTrashEmptyRunEStopsOnAnyAnswerButYes(t *testing.T) {
	for _, answer := range []string{"no", "n", "", "y", "YES", "yes please"} {
		t.Run(answer, func(t *testing.T) {
			answerPrompt(t, answer)
			// No routes: any request would be flagged by the mock as well.
			m := newAPIMock(t, map[string]mockReply{})

			out, err := runCLI(t, m, "trash", "empty")
			if err == nil {
				t.Fatal("answering something other than yes must fail, not print a notice and exit 0")
			}
			if got := exitCodeFor(err); got == exitOK {
				t.Errorf("exit code = %d, want non-zero so a script can tell it did not run", got)
			}
			if len(m.calls()) != 0 {
				t.Fatalf("the trash was emptied anyway: %v", m.calls())
			}
			if strings.Contains(out, "Emptied the trash") {
				t.Errorf("stdout claimed success after an abort:\n%s", out)
			}
		})
	}
}

// TestTrashEmptyRunERefusesUnattended covers scripts, CI and agents: stdin is a
// pipe, so there is nobody to ask and the command must refuse rather than read
// whatever byte is next as consent.
func TestTrashEmptyRunERefusesUnattended(t *testing.T) {
	pipedStdin(t)
	m := newAPIMock(t, map[string]mockReply{})

	_, err := runCLI(t, m, "trash", "empty")
	if err == nil {
		t.Fatal("an unattended run without --yes must refuse")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %q, want it to name the flag that would have worked", err.Error())
	}
	if len(m.calls()) != 0 {
		t.Errorf("nothing should have been sent, got %v", m.calls())
	}
}

// TestTrashEmptyRunERefusesJSONWithoutYes pins the --json rule specifically at a
// terminal, where the prompt WOULD otherwise be possible: machine-readable
// output means a machine is reading, and a prompt it never sees is not consent.
func TestTrashEmptyRunERefusesJSONWithoutYes(t *testing.T) {
	asked := answerPrompt(t, "yes") // would proceed if the gate ever asked
	m := newAPIMock(t, map[string]mockReply{})

	_, err := runCLI(t, m, "trash", "empty", "--json")
	if err == nil {
		t.Fatal("--json without --yes must refuse")
	}
	if len(*asked) != 0 {
		t.Errorf("--json must never prompt, but it asked %v", *asked)
	}
	if len(m.calls()) != 0 {
		t.Errorf("nothing should have been sent, got %v", m.calls())
	}
}

// TestTrashEmptyRunEProceedsOnTypedYes proves the gate is passable — a
// confirmation that never lets anyone through is its own kind of broken.
func TestTrashEmptyRunEProceedsOnTypedYes(t *testing.T) {
	answerPrompt(t, "yes")
	m := newAPIMock(t, map[string]mockReply{
		emptyTrashRoute: {Status: 200, Body: `{"expunged":2}`},
	})

	out, err := runCLI(t, m, "trash", "empty")
	if err != nil {
		t.Fatalf("trash empty after typing yes: %v", err)
	}
	if len(m.calls()) != 1 || m.calls()[0] != emptyTrashRoute {
		t.Fatalf("calls = %v, want exactly [%s]", m.calls(), emptyTrashRoute)
	}
	if !strings.Contains(out, "2 notes permanently deleted") {
		t.Errorf("the user should be told how much was destroyed:\n%s", out)
	}
}

// TestTrashEmptyRunEProceedsWithYesFlag covers the scripted path: --yes is the
// documented way to empty the trash unattended, and it must not prompt.
func TestTrashEmptyRunEProceedsWithYesFlag(t *testing.T) {
	pipedStdin(t) // any prompt at all fails the test
	m := newAPIMock(t, map[string]mockReply{
		emptyTrashRoute: {Status: 200, Body: `{"expunged":1}`},
	})

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err != nil {
		t.Fatalf("trash empty --yes: %v", err)
	}
	if len(m.calls()) != 1 || m.calls()[0] != emptyTrashRoute {
		t.Fatalf("calls = %v, want exactly [%s]", m.calls(), emptyTrashRoute)
	}
	if !strings.Contains(out, "1 note permanently deleted") {
		t.Errorf("output = %q", out)
	}
}

// TestTrashEmptyRunEReportsAPIFailureAsAnError guards the other half of the
// stdout/exit-code distinction: when the server refuses, the command must fail
// rather than print and exit 0.
func TestTrashEmptyRunEReportsAPIFailureAsAnError(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		emptyTrashRoute: {Status: 500, Body: apiErrorBody("internal", "boom")},
	})

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err == nil {
		t.Fatal("a failed empty must be an error, not a printed line")
	}
	if strings.Contains(out, "Emptied the trash") {
		t.Errorf("stdout claimed success after a 500:\n%s", out)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestDisplayEmptyTrashUsesThousandsSeparators checks a big count reads the way
// the progress line does.
func TestDisplayEmptyTrashUsesThousandsSeparators(t *testing.T) {
	out := captureStdout(t, func() { displayEmptyTrash([]byte(`{"expunged":15843}`)) })
	if out != "Emptied the trash — 15,843 notes permanently deleted.\n" {
		t.Errorf("output = %q", out)
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

// TestTrashExpungeRunEStopsOnAWrongAnswer covers the single-note expunge, which
// destroys just as permanently as emptying the bin — it simply destroys less.
func TestTrashExpungeRunEStopsOnAWrongAnswer(t *testing.T) {
	for _, answer := range []string{"no", "", "y", "YES"} {
		t.Run(answer, func(t *testing.T) {
			answerPrompt(t, answer)
			m := newAPIMock(t, map[string]mockReply{})

			_, err := runCLI(t, m, "trash", "expunge", "n1")
			if err == nil {
				t.Fatalf("answering %q expunged the note", answer)
			}
			if len(m.calls()) != 0 {
				t.Fatalf("the note was expunged anyway: %v", m.calls())
			}
		})
	}
}

// TestTrashExpungeRunEProceedsWhenConfirmed keeps the gate passable, both by
// typing and by the scripted --yes.
func TestTrashExpungeRunEProceedsWhenConfirmed(t *testing.T) {
	const route = "POST /api/v1/notes/n1/expunge"

	answerPrompt(t, "yes")
	m := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})
	if _, err := runCLI(t, m, "trash", "expunge", "n1"); err != nil {
		t.Fatalf("typed yes: %v", err)
	}
	if len(m.calls()) != 1 || m.calls()[0] != route {
		t.Errorf("calls = %v, want [%s]", m.calls(), route)
	}

	pipedStdin(t) // --yes must not prompt
	m2 := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})
	if _, err := runCLI(t, m2, "trash", "expunge", "n1", "--yes"); err != nil {
		t.Fatalf("--yes: %v", err)
	}
	if len(m2.calls()) != 1 {
		t.Errorf("calls = %v", m2.calls())
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

// ===========================================================================
// A big trash: waiting for the background job
// ===========================================================================
//
// More than 200 notes come back as 202 with a job. The notes are still there
// at that point, so the success line must wait for the job to complete.

// trashJobRoute is the poll the wait makes for job j1.
const trashJobRoute = "GET /api/v1/trash/empty-jobs/j1"

// bigTrashAccepted is the 202 answer for a trash of 15,843 notes.
const bigTrashAccepted = `{"expunged":15843,"job":{"id":"j1","status":"queued","workspace":"","total_notes":15843,"processed_notes":0,"error":"","created_at":1750000000000,"updated_at":1750000000000}}`

// jobReply is one poll answer: the job at a status and progress.
func jobReply(status string, processed int) mockReply {
	return mockReply{Status: 200, Body: fmt.Sprintf(
		`{"data":{"id":"j1","status":%q,"workspace":"","total_notes":15843,"processed_notes":%d,"error":""}}`,
		status, processed)}
}

// failedJobReply is a poll answer for a job that failed with the given sentence.
func failedJobReply(sentence string) mockReply {
	return mockReply{Status: 200, Body: fmt.Sprintf(
		`{"data":{"id":"j1","status":"failed","workspace":"","total_notes":15843,"processed_notes":4000,"error":%q}}`,
		sentence)}
}

// bigTrashMock answers the Empty Trash with bigTrashAccepted and each poll with
// the next reply in order, repeating the last one.
func bigTrashMock(t *testing.T, replies ...mockReply) *apiMock {
	t.Helper()
	m := newAPIMock(t, nil)
	n := 0
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case emptyTrashRoute:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(bigTrashAccepted))
		case trashJobRoute:
			reply := replies[min(n, len(replies)-1)]
			n++
			w.WriteHeader(reply.Status)
			_, _ = w.Write([]byte(reply.Body))
		default:
			t.Errorf("unrouted request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return m
}

// fastTrashPoll makes the wait poll every millisecond for this test.
func fastTrashPoll(t *testing.T) {
	t.Helper()
	orig := trashEmptyPollInterval
	t.Cleanup(func() { trashEmptyPollInterval = orig })
	trashEmptyPollInterval = time.Millisecond
}

// shortRetryWindow shortens how long failed checks are retried for this test.
func shortRetryWindow(t *testing.T, d time.Duration) {
	t.Helper()
	orig := trashEmptyRetryWindow
	t.Cleanup(func() { trashEmptyRetryWindow = orig })
	trashEmptyRetryWindow = d
}

// stderrTerminal makes stderr read as a terminal (or not) for this test.
func stderrTerminal(t *testing.T, on bool) {
	t.Helper()
	orig := stderrIsInteractive
	t.Cleanup(func() { stderrIsInteractive = orig })
	stderrIsInteractive = func() bool { return on }
}

// countCalls counts the recorded requests matching "METHOD /path".
func countCalls(m *apiMock, route string) int {
	n := 0
	for _, c := range m.calls() {
		if c == route {
			n++
		}
	}
	return n
}

// TestTrashEmptyRunESmallTrashIsUnchanged pins the 200 path: the same line as
// before, and no request after the delete.
func TestTrashEmptyRunESmallTrashIsUnchanged(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		emptyTrashRoute: {Status: 200, Body: `{"expunged":3}`},
	})

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	if out != "Emptied the trash — 3 notes permanently deleted.\n" {
		t.Errorf("output = %q", out)
	}
	if calls := m.calls(); len(calls) != 1 || calls[0] != emptyTrashRoute {
		t.Errorf("calls = %v, want exactly [%s]", calls, emptyTrashRoute)
	}
}

// TestTrashEmptyRunEWaitsForABigTrash checks a 202 is polled until the job
// completes, and only then prints the success line.
func TestTrashEmptyRunEWaitsForABigTrash(t *testing.T) {
	fastTrashPoll(t)
	m := bigTrashMock(t, jobReply("queued", 0), jobReply("running", 1200), jobReply("completed", 15843))

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	if got := countCalls(m, trashJobRoute); got != 3 {
		t.Errorf("polled %d times, want 3 (until completed, then stop): %v", got, m.calls())
	}
	if out != "Emptied the trash — 15,843 notes permanently deleted.\n" {
		t.Errorf("output = %q", out)
	}
}

// TestTrashEmptyRunENoPollWhenTheJobIsAlreadyDone covers a 202 whose job has
// already finished: there is nothing to wait for.
func TestTrashEmptyRunENoPollWhenTheJobIsAlreadyDone(t *testing.T) {
	done := strings.Replace(bigTrashAccepted, `"status":"queued"`, `"status":"completed"`, 1)
	m := newAPIMock(t, map[string]mockReply{emptyTrashRoute: {Status: 202, Body: done}})

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	if calls := m.calls(); len(calls) != 1 {
		t.Errorf("calls = %v, want only the delete", calls)
	}
	if !strings.Contains(out, "15,843 notes permanently deleted") {
		t.Errorf("output = %q", out)
	}
}

// TestTrashEmptyRunEJobAlreadyFailed covers a 202 whose job failed before the
// answer was sent (the server could not start it): no poll, and a non-zero exit.
func TestTrashEmptyRunEJobAlreadyFailed(t *testing.T) {
	const sentence = "We couldn’t start emptying the Trash. Please try again."
	failed := strings.Replace(bigTrashAccepted, `"status":"queued"`, `"status":"failed"`, 1)
	failed = strings.Replace(failed, `"error":""`, `"error":"`+sentence+`"`, 1)
	m := newAPIMock(t, map[string]mockReply{emptyTrashRoute: {Status: 202, Body: failed}})

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err == nil || !strings.Contains(err.Error(), sentence) {
		t.Errorf("err = %v, want the job's sentence", err)
	}
	if calls := m.calls(); len(calls) != 1 {
		t.Errorf("calls = %v, want only the delete", calls)
	}
	if out != "" {
		t.Errorf("stdout should be empty, got %q", out)
	}
}

// TestTrashEmptyRunEReportsAFailedJob checks a failed job prints the server's
// sentence, exits non-zero, and never claims the trash is empty.
func TestTrashEmptyRunEReportsAFailedJob(t *testing.T) {
	fastTrashPoll(t)
	const sentence = "Emptying the Trash stopped before it finished. Please try again."
	m := bigTrashMock(t, jobReply("running", 2000), failedJobReply(sentence))

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err == nil {
		t.Fatal("a failed job exited zero")
	}
	if err.Error() != "trash_empty_failed: "+sentence {
		t.Errorf("err = %q, want the job's own sentence", err.Error())
	}
	if exitCodeFor(err) == exitOK {
		t.Error("a failed job must exit non-zero")
	}
	if out != "" {
		t.Errorf("stdout should be empty on failure, got %q", out)
	}
}

// TestTrashEmptyRunEFailedJobWithoutASentence falls back to a sentence of our
// own rather than an empty error.
func TestTrashEmptyRunEFailedJobWithoutASentence(t *testing.T) {
	fastTrashPoll(t)
	m := bigTrashMock(t, failedJobReply(""))

	_, err := runCLI(t, m, "trash", "empty", "--yes")
	if err == nil || !strings.Contains(err.Error(), trashEmptyFailedFallback) {
		t.Errorf("err = %v, want the fallback sentence", err)
	}
}

// TestTrashEmptyRunEJSONPrintsTheFinalJob checks --json waits the same way,
// prints no progress even on a terminal, and prints the finished job beside
// the count scripts already read.
func TestTrashEmptyRunEJSONPrintsTheFinalJob(t *testing.T) {
	fastTrashPoll(t)
	stderrTerminal(t, true)
	m := bigTrashMock(t, jobReply("running", 1200), jobReply("completed", 15843))

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = runCLI(t, m, "trash", "empty", "--yes", "--json") })
	if err != nil {
		t.Fatalf("trash empty --json: %v", err)
	}
	if stderr != "" {
		t.Errorf("--json must print no progress, stderr = %q", stderr)
	}
	var doc struct {
		Expunged int `json:"expunged"`
		Job      struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			ProcessedNotes int    `json:"processed_notes"`
		} `json:"job"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", jerr, out)
	}
	if doc.Expunged != 15843 || doc.Job.ID != "j1" || doc.Job.Status != "completed" || doc.Job.ProcessedNotes != 15843 {
		t.Errorf("want the finished job beside expunged, got %+v", doc)
	}
}

// TestTrashEmptyRunEJSONFailedJobStillPrintsTheJob checks a script gets the
// failed job on stdout as well as a non-zero exit.
func TestTrashEmptyRunEJSONFailedJobStillPrintsTheJob(t *testing.T) {
	fastTrashPoll(t)
	m := bigTrashMock(t, failedJobReply("The Trash could not be emptied completely. Please try again."))

	out, err := runCLI(t, m, "trash", "empty", "--yes", "--json")
	if err == nil {
		t.Fatal("a failed job exited zero")
	}
	if !strings.Contains(out, `"status": "failed"`) {
		t.Errorf("stdout should carry the failed job:\n%s", out)
	}
}

// TestTrashEmptyRunEShowsProgressOnATerminal checks the progress line is drawn
// in place on stderr, notes a retried check, and is erased before the success
// line.
func TestTrashEmptyRunEShowsProgressOnATerminal(t *testing.T) {
	fastTrashPoll(t)
	stderrTerminal(t, true)
	m := bigTrashMock(t,
		jobReply("running", 1200),
		mockReply{Status: 503, Body: apiErrorBody("unavailable", "restarting")},
		jobReply("completed", 15843),
	)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = runCLI(t, m, "trash", "empty", "--yes") })
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	for _, want := range []string{
		"\r\033[KEmptying the trash… 0 of 15,843 notes",
		"\r\033[KEmptying the trash… 1,200 of 15,843 notes",
		"\r\033[KEmptying the trash… 1,200 of 15,843 notes (retrying…)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%q", want, stderr)
		}
	}
	if !strings.HasSuffix(stderr, "\r\033[K") {
		t.Errorf("the progress line should be erased at the end, stderr = %q", stderr)
	}
	if !strings.Contains(out, "15,843 notes permanently deleted") {
		t.Errorf("output = %q", out)
	}
}

// TestTrashEmptyRunENoProgressOffATerminal keeps a pipe or a log free of
// carriage returns.
func TestTrashEmptyRunENoProgressOffATerminal(t *testing.T) {
	fastTrashPoll(t)
	stderrTerminal(t, false)
	m := bigTrashMock(t, jobReply("running", 1200), jobReply("completed", 15843))

	var err error
	stderr := captureStderr(t, func() { _, err = runCLI(t, m, "trash", "empty", "--yes") })
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty off a terminal, got %q", stderr)
	}
}

// TestTrashEmptyRunERetriesAFailedCheck checks a server error during the wait is
// waited out, since the job keeps running either way.
func TestTrashEmptyRunERetriesAFailedCheck(t *testing.T) {
	fastTrashPoll(t)
	m := bigTrashMock(t,
		mockReply{Status: 502, Body: apiErrorBody("bad_gateway", "upstream down")},
		mockReply{Status: 429, Body: apiErrorBody("rate_limited", "Too many requests.")},
		jobReply("completed", 15843),
	)

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	if got := countCalls(m, trashJobRoute); got != 3 {
		t.Errorf("polled %d times, want 3", got)
	}
	if !strings.Contains(out, "15,843 notes permanently deleted") {
		t.Errorf("output = %q", out)
	}
}

// TestTrashEmptyRunEStopsOnAMissingJob checks an answer that will not change on
// a retry ends the wait at once, says the trash may still be emptying, and
// never claims success.
func TestTrashEmptyRunEStopsOnAMissingJob(t *testing.T) {
	fastTrashPoll(t)
	m := bigTrashMock(t, mockReply{Status: 404, Body: apiErrorBody("not_found", "Not found.")})

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err == nil {
		t.Fatal("a wait that lost its job exited zero")
	}
	if !strings.Contains(err.Error(), "may still be emptying") || !strings.Contains(err.Error(), "not_found") {
		t.Errorf("err = %q, want the cause and that the trash may still be emptying", err.Error())
	}
	if exitCodeFor(err) != exitError {
		t.Errorf("exit code = %d, want %d", exitCodeFor(err), exitError)
	}
	if got := countCalls(m, trashJobRoute); got != 1 {
		t.Errorf("polled %d times, want 1 (a 404 is not retried)", got)
	}
	if out != "" {
		t.Errorf("stdout should be empty, got %q", out)
	}
}

// TestTrashEmptyRunEGivesUpAfterRepeatedFailures checks retries stop once
// checks have kept failing for the retry window.
func TestTrashEmptyRunEGivesUpAfterRepeatedFailures(t *testing.T) {
	fastTrashPoll(t)
	shortRetryWindow(t, 30*time.Millisecond)
	m := bigTrashMock(t, mockReply{Status: 500, Body: apiErrorBody("internal", "boom")})

	_, err := runCLI(t, m, "trash", "empty", "--yes")
	if err == nil {
		t.Fatal("a wait that could never check the job exited zero")
	}
	if !strings.Contains(err.Error(), "may still be emptying") {
		t.Errorf("err = %q", err.Error())
	}
	if got := countCalls(m, trashJobRoute); got < 2 {
		t.Errorf("polled %d times, want it to retry before giving up", got)
	}
}

// TestTrashEmptyRunEGoodCheckResetsTheRetryWindow checks a successful check
// starts the window over, so failures spread across a long job do not add up.
// The run of good checks lasts at least 60ms (one 1ms pause each), longer than
// the 30ms window, so a window that never resets gives up at the second 500.
func TestTrashEmptyRunEGoodCheckResetsTheRetryWindow(t *testing.T) {
	fastTrashPoll(t)
	shortRetryWindow(t, 30*time.Millisecond)
	boom := mockReply{Status: 500, Body: apiErrorBody("internal", "boom")}
	replies := []mockReply{boom}
	for i := 0; i < 60; i++ {
		replies = append(replies, jobReply("running", i*100))
	}
	replies = append(replies, boom, jobReply("completed", 15843))
	m := bigTrashMock(t, replies...)

	out, err := runCLI(t, m, "trash", "empty", "--yes")
	if err != nil {
		t.Fatalf("trash empty: %v", err)
	}
	if !strings.Contains(out, "15,843 notes permanently deleted") {
		t.Errorf("output = %q", out)
	}
}

// TestTrashEmptyRunECtrlCStopsOnlyTheWait sends a real SIGINT during a check,
// so the signal wiring is tested along with the wait. The person must be told
// the trash is still emptying, see no success line, and get a non-zero exit.
func TestTrashEmptyRunECtrlCStopsOnlyTheWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be delivered to a process on Windows")
	}
	fastTrashPoll(t)
	stderrTerminal(t, true)
	m := newAPIMock(t, nil)
	polls := 0
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method+" "+r.URL.Path == emptyTrashRoute {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(bigTrashAccepted))
			return
		}
		polls++
		if polls == 2 {
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(os.Interrupt)
			}
			// Hold this check open so the signal, not the answer, ends the wait.
			time.Sleep(150 * time.Millisecond)
		}
		reply := jobReply("running", polls*200)
		w.WriteHeader(reply.Status)
		_, _ = w.Write([]byte(reply.Body))
	}

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = runCLI(t, m, "trash", "empty", "--yes") })
	if !errors.Is(err, errTrashStoppedWaiting) {
		t.Fatalf("err = %v, want errTrashStoppedWaiting", err)
	}
	if err.Error() != "Stopped waiting. The trash is still being emptied on the server." {
		t.Errorf("err = %q", err.Error())
	}
	if exitCodeFor(err) == exitOK {
		t.Error("Ctrl-C must exit non-zero")
	}
	if out != "" {
		t.Errorf("stdout should be empty, got %q", out)
	}
	if !strings.HasSuffix(stderr, "\r\033[K") {
		t.Errorf("the progress line should be erased before the error, stderr = %q", stderr)
	}
}

// TestTrashWaitForEmptyStopsOnCtrlC checks Ctrl-C ends the wait at once, even
// in the middle of a check that never answers, and says the job keeps going.
func TestTrashWaitForEmptyStopsOnCtrlC(t *testing.T) {
	fastTrashPoll(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	fetch := func(string) ([]byte, error) {
		cancel() // Ctrl-C lands while this check is in flight
		<-hang
		return nil, errors.New("unreachable")
	}

	done := make(chan error, 1)
	go func() {
		_, err := trashWaitForEmpty(ctx, fetch, []byte(bigTrashAccepted), &trashEmptyProgress{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "Stopped waiting. The trash is still being emptied on the server." {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not end the wait while a check hung")
	}
}

// TestTrashWaitForEmptyCtrlCBeforeTheFirstCheck checks no request is sent once
// Ctrl-C has landed.
func TestTrashWaitForEmptyCtrlCBeforeTheFirstCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fetch := func(string) ([]byte, error) {
		t.Error("a check was sent after Ctrl-C")
		return nil, nil
	}
	data, err := trashWaitForEmpty(ctx, fetch, []byte(bigTrashAccepted), &trashEmptyProgress{})
	if !errors.Is(err, errTrashStoppedWaiting) {
		t.Errorf("err = %v, want errTrashStoppedWaiting", err)
	}
	if string(data) != bigTrashAccepted {
		t.Errorf("data should be the last answer seen, got %s", data)
	}
}

// TestTrashWaitForEmptyNeedsAJobID checks a running job with no id fails
// instead of polling a path that cannot exist.
func TestTrashWaitForEmptyNeedsAJobID(t *testing.T) {
	noID := `{"expunged":450,"job":{"status":"queued","total_notes":450,"processed_notes":0}}`
	fetch := func(string) ([]byte, error) {
		t.Error("polled a job with no id")
		return nil, nil
	}
	_, err := trashWaitForEmpty(context.Background(), fetch, []byte(noID), &trashEmptyProgress{})
	if err == nil || !strings.Contains(err.Error(), "no job to follow") {
		t.Errorf("err = %v", err)
	}
}

// TestTrashWaitForEmptyUnreadableJob checks a 200 that is not a job counts as a
// failed check rather than a job that never finishes.
func TestTrashWaitForEmptyUnreadableJob(t *testing.T) {
	fastTrashPoll(t)
	calls := 0
	fetch := func(string) ([]byte, error) {
		calls++
		return []byte("<html>oops</html>"), nil
	}
	_, err := trashWaitForEmpty(context.Background(), fetch, []byte(bigTrashAccepted), &trashEmptyProgress{})
	if err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("checked %d times, want 1", calls)
	}
}

// TestTrashPollFailedKeepsTheNetworkExitCode checks a wait lost to the network
// still exits 3, so a script knows a retry may work.
func TestTrashPollFailedKeepsTheNetworkExitCode(t *testing.T) {
	cause := &url.Error{Op: "Get", URL: "https://example.com", Err: errors.New("connection refused")}
	err := trashPollFailed(cause)
	if got := exitCodeFor(err); got != exitNetwork {
		t.Errorf("exit code = %d, want %d", got, exitNetwork)
	}
	if !trashPollRetryable(cause) {
		t.Error("a network failure should be retried")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %q, want the cause in it", err.Error())
	}
}

// TestNewTrashEmptyProgressIsOffUnderJSON checks --json silences progress even
// on a terminal.
func TestNewTrashEmptyProgressIsOffUnderJSON(t *testing.T) {
	stderrTerminal(t, true)
	defer func() { jsonOutput = false }()

	jsonOutput = true
	if newTrashEmptyProgress().on {
		t.Error("progress is on under --json")
	}
	jsonOutput = false
	if !newTrashEmptyProgress().on {
		t.Error("progress is off on a terminal without --json")
	}
}

// TestTrashEmptyProgressRedrawsOnlyOnChange checks an idle poll writes nothing
// and clear erases the line once.
func TestTrashEmptyProgressRedrawsOnlyOnChange(t *testing.T) {
	noColorFlag, colorReady = true, false
	defer func() { noColorFlag, colorReady = false, false }()

	var buf bytes.Buffer
	p := &trashEmptyProgress{w: &buf, on: true}
	job := map[string]any{"total_notes": float64(15843), "processed_notes": float64(1200)}
	p.show(job, false)
	p.show(job, false)
	if buf.String() != "\r\033[KEmptying the trash… 1,200 of 15,843 notes" {
		t.Errorf("after two identical shows: %q", buf.String())
	}
	buf.Reset()
	p.clear()
	p.clear()
	if buf.String() != "\r\033[K" {
		t.Errorf("clear should erase once, wrote %q", buf.String())
	}
}

// TestTrashEmptyProgressLine checks the progress wording.
func TestTrashEmptyProgressLine(t *testing.T) {
	big := map[string]any{"total_notes": float64(15843), "processed_notes": float64(1200)}
	cases := []struct {
		job      map[string]any
		retrying bool
		want     string
	}{
		{big, false, "Emptying the trash… 1,200 of 15,843 notes"},
		{big, true, "Emptying the trash… 1,200 of 15,843 notes (retrying…)"},
		{map[string]any{"total_notes": float64(1), "processed_notes": float64(0)}, false, "Emptying the trash… 0 of 1 note"},
		{map[string]any{}, false, "Emptying the trash…"},
	}
	for _, c := range cases {
		if got := trashEmptyProgressLine(c.job, c.retrying); got != c.want {
			t.Errorf("trashEmptyProgressLine(%v, %v) = %q, want %q", c.job, c.retrying, got, c.want)
		}
	}
}

// TestTrashEmptyWithJobKeepsExpunged checks swapping in a newer job keeps the
// count beside it.
func TestTrashEmptyWithJobKeepsExpunged(t *testing.T) {
	out := trashEmptyWithJob([]byte(bigTrashAccepted), []byte(`{"id":"j1","status":"completed"}`))
	root := parseJSON(out)
	if num(root, "expunged") != 15843 {
		t.Errorf("expunged lost: %s", out)
	}
	if str(trashEmptyJobOf(out), "status") != "completed" {
		t.Errorf("job not replaced: %s", out)
	}
}

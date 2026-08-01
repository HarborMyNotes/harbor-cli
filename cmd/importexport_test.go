// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// TestDisplayImportJobInline verifies the inline (201) summary shows the
// counters and does NOT print the "poll it" hint.
func TestDisplayImportJobInline(t *testing.T) {
	importEnexAsync = false
	data := []byte(`{"data":{"import_job_id":"job-123","status":"completed","total_notes":12,"imported_notes":11,"skipped_notes":0,"failed_notes":1}}`)
	out := captureStdout(t, func() { displayImportJob(data) })
	if !containsSub(out, "job-123", "completed", "12", "11") {
		t.Errorf("import job summary missing fields:\n%s", out)
	}
	if strings.Contains(out, "import status") {
		t.Errorf("inline import should not print the poll hint:\n%s", out)
	}
}

// TestDisplayImportJobAsync verifies the enqueued (202) summary prints the
// follow-up poll command with the job id.
func TestDisplayImportJobAsync(t *testing.T) {
	importEnexAsync = true
	defer func() { importEnexAsync = false }()
	data := []byte(`{"data":{"import_job_id":"job-async","status":"queued","total_notes":0}}`)
	out := captureStdout(t, func() { displayImportJob(data) })
	if !strings.Contains(out, "harbor import status job-async") {
		t.Errorf("async import should print the poll hint:\n%s", out)
	}
}

// TestDisplayImportStatusWithErrors verifies the poll view renders counters and
// the per-note error table, mapping a job-level index (-1) to "job".
func TestDisplayImportStatusWithErrors(t *testing.T) {
	data := []byte(`{"data":{"id":"job-9","status":"partial","total_notes":12,"imported_notes":11,"skipped_notes":0,"failed_notes":1,"updated_at":1750000000000,"errors":[{"note_index":7,"title":"Broken note","reason":"resource 0: invalid base64 data"}]}}`)
	out := captureStdout(t, func() { displayImportStatus(data) })
	if !containsSub(out, "job-9", "partial", "Broken note", "invalid base64") {
		t.Errorf("import status missing fields:\n%s", out)
	}
	if !strings.Contains(out, "7") {
		t.Errorf("per-note index missing:\n%s", out)
	}
}

// TestDisplayImportStatusJobLevelError verifies a note_index of -1 renders as a
// job-level error rather than a literal "-1".
func TestDisplayImportStatusJobLevelError(t *testing.T) {
	data := []byte(`{"data":{"id":"job-x","status":"failed","errors":[{"note_index":-1,"title":"","reason":"import aborted"}]}}`)
	out := captureStdout(t, func() { displayImportStatus(data) })
	if !strings.Contains(out, "job") || !strings.Contains(out, "import aborted") {
		t.Errorf("job-level error not rendered:\n%s", out)
	}
}

// TestDisplayImportStatusNoErrors verifies the error table is omitted when the
// errors list is empty.
func TestDisplayImportStatusNoErrors(t *testing.T) {
	data := []byte(`{"data":{"id":"job-ok","status":"completed","total_notes":3,"imported_notes":3,"errors":[]}}`)
	out := captureStdout(t, func() { displayImportStatus(data) })
	if strings.Contains(out, "Errors:") {
		t.Errorf("no error table expected when errors is empty:\n%s", out)
	}
}

// TestMapImportExportError verifies the domain codes map to friendly messages
// and other codes pass through unchanged.
func TestMapImportExportError(t *testing.T) {
	cases := map[string]string{
		"invalid_enex":                 "well-formed",
		"enex_too_large":               "maximum import size",
		"cannot_import_into_encrypted": "encrypted notebook",
	}
	for code, sub := range cases {
		if got := mapImportExportError(apiErr(code)); !strings.Contains(got.Error(), sub) {
			t.Errorf("mapImportExportError(%s) = %q", code, got.Error())
		}
	}
	// An unrelated code is returned untouched.
	other := apiErr("not_found")
	if got := mapImportExportError(other); got != other {
		t.Errorf("unrelated error should pass through, got %v", got)
	}
}

// TestExportEnexDeprecationNotice pins which mode of the synchronous ENEX export
// is deprecated. --notebook has a successor on the job path and must point at it;
// --notes deliberately does NOT (per-note scoping is out of scope for the export
// work), so warning about it would send people to a command that cannot do what
// they asked. The notice must also flag that the successor is not a drop-in: it
// includes trashed notes, which this endpoint leaves out.
func TestExportEnexDeprecationNotice(t *testing.T) {
	notice := exportEnexDeprecationNotice("true", "nb1")
	for _, want := range []string{"deprecated", "harbor account export --format enex --notebook nb1", "trash"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notebook notice missing %q:\n%s", want, notice)
		}
	}

	// A note selection is fully supported — silence, with or without a header.
	if got := exportEnexDeprecationNotice("", ""); got != "" {
		t.Errorf("a note selection should not warn, got %q", got)
	}

	// The flag alone is enough: a server that has not shipped the header yet must
	// still produce the notice.
	if got := exportEnexDeprecationNotice("", "nb1"); got == "" {
		t.Error("a notebook export should warn even without the response header")
	}

	// And if the server ever deprecates a mode this CLI does not know about, relay
	// it rather than swallow it.
	if got := exportEnexDeprecationNotice("true", ""); !strings.Contains(got, "deprecated") {
		t.Errorf("an unexpected Deprecation header should be surfaced, got %q", got)
	}
}

// TestExportEnexEmitsDeprecationNoticeOnStderr covers the WIRING the test above
// cannot: that the command actually calls the notice, sends it to stderr so a
// '--output -' pipe stays a valid ENEX, and stays silent for --notes. Deleting
// the call site leaves the notice function fully tested and the user unwarned.
func TestExportEnexEmitsDeprecationNoticeOnStderr(t *testing.T) {
	newMock := func(t *testing.T) *apiMock {
		m := newAPIMock(t, map[string]mockReply{})
		m.handler = func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "notebook_id") {
				w.Header().Set("Deprecation", "true")
				w.Header().Set("Link", `</api/v1/account/export>; rel="successor-version"`)
			}
			_, _ = w.Write([]byte("<?xml version='1.0'?><en-export></en-export>"))
		}
		return m
	}

	var out string
	var err error
	errOut := captureStderr(t, func() {
		out, err = runCLI(t, newMock(t), "export", "enex", "--notebook", "nb1", "--output", "-")
	})
	if err != nil {
		t.Fatalf("export enex --notebook: %v", err)
	}
	if !strings.Contains(errOut, "deprecated") || !strings.Contains(errOut, "account export") {
		t.Errorf("the deprecation notice never reached stderr:\n%s", errOut)
	}
	// The pipe must stay a usable ENEX document.
	if strings.Contains(out, "deprecated") {
		t.Errorf("the notice leaked into the exported document:\n%s", out)
	}
	if !strings.HasPrefix(out, "<?xml") {
		t.Errorf("stdout should be the ENEX verbatim:\n%s", out)
	}

	// --notes has no successor, so warning about it would send people to a
	// command that cannot do what they asked.
	errOut2 := captureStderr(t, func() {
		_, err = runCLI(t, newMock(t), "export", "enex", "--notes", "n1,n2", "--output", "-")
	})
	if err != nil {
		t.Fatalf("export enex --notes: %v", err)
	}
	if strings.Contains(errOut2, "deprecated") {
		t.Errorf("a note selection must not be warned about:\n%s", errOut2)
	}
}

// TestImportExportSkipCount verifies header parsing tolerates missing/garbage
// values.
func TestImportExportSkipCount(t *testing.T) {
	cases := map[string]int{"": 0, "0": 0, "3": 3, "garbage": 0, "-1": 0}
	for in, want := range cases {
		if got := importExportSkipCount(in); got != want {
			t.Errorf("importExportSkipCount(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestFilepathBase verifies the local base-name helper across separators.
func TestFilepathBase(t *testing.T) {
	cases := map[string]string{
		"/a/b/c.enex":        "c.enex",
		"c.enex":             "c.enex",
		`C:\dir\export.enex`: "export.enex",
		"/trailing/":         "",
	}
	for in, want := range cases {
		if got := filepathBase(in); got != want {
			t.Errorf("filepathBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// containsSub reports whether s contains every substring in subs. A small local
// helper mirroring the client package's containsAll for cmd display assertions.
func containsSub(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// ===========================================================================
// An import that lost notes (#69 sweep)
// ===========================================================================
//
// import enex answers 201/202 whether it imported every note or none of them —
// the outcome is in the body's counters — so before this an import that dropped
// every note exited 0 and a script moved on believing the data was in Harbor.

// TestImportJobFailureFlagsLostNotes covers the three shapes that mean notes
// did not make it: the API's own `partial` and `failed` statuses, and a
// non-zero failed_notes count under any status.
func TestImportJobFailureFlagsLostNotes(t *testing.T) {
	cases := map[string]string{
		"partial":              `{"data":{"import_job_id":"job-1","status":"partial","total_notes":12,"imported_notes":11,"failed_notes":1}}`,
		"failed":               `{"data":{"import_job_id":"job-2","status":"failed","total_notes":12,"imported_notes":0,"failed_notes":12}}`,
		"failures while green": `{"data":{"import_job_id":"job-3","status":"completed","total_notes":12,"imported_notes":11,"failed_notes":1}}`,
	}
	for name, body := range cases {
		err := importJobFailure([]byte(body))
		if err == nil {
			t.Errorf("%s: an import that lost notes must not be a silent success", name)
			continue
		}
		if got := exitCodeFor(err); got != exitError {
			t.Errorf("%s: exit code = %d, want %d", name, got, exitError)
		}
		if !strings.Contains(err.Error(), "of 12 notes imported") {
			t.Errorf("%s: message should say how many landed: %q", name, err.Error())
		}
	}

	// The per-note reasons live behind the poller, so the error has to say so —
	// the counters alone cannot tell the user what went wrong.
	err := importJobFailure([]byte(`{"data":{"import_job_id":"job-1","status":"partial","total_notes":2,"imported_notes":1,"failed_notes":1}}`))
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want an *client.APIError, got %T", err)
	}
	if got := apiErr.Details["per-note reasons"]; got != "harbor import status job-1" {
		t.Errorf("details should point at the poller, got %v", apiErr.Details)
	}
}

// TestImportJobFailureStaysQuietWhenNothingWasLost keeps it from crying wolf. A
// queued import in particular has not failed at anything — it has not run yet.
func TestImportJobFailureStaysQuietWhenNothingWasLost(t *testing.T) {
	cases := map[string]string{
		"clean inline import": `{"data":{"import_job_id":"job-1","status":"completed","total_notes":12,"imported_notes":12,"failed_notes":0}}`,
		"enqueued import":     `{"data":{"import_job_id":"job-2","status":"queued","total_notes":0,"imported_notes":0,"failed_notes":0}}`,
		"awaiting upload":     `{"data":{"import_job_id":"job-3","status":"awaiting_upload"}}`,
		"unparseable body":    `<html>nope</html>`,
	}
	for name, body := range cases {
		if err := importJobFailure([]byte(body)); err != nil {
			t.Errorf("%s: want no error, got %v", name, err)
		}
	}
}

// TestImportEnexCommandExitsNonZeroAndStillPrintsCounters is the end-to-end
// regression: the counters stay on stdout — they are the answer the user came
// for — while the exit code says the import did not fully land.
func TestImportEnexCommandExitsNonZeroAndStillPrintsCounters(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/import/enex": {Status: 201, Body: `{"data":{"import_job_id":"job-1","status":"partial","total_notes":12,"imported_notes":11,"skipped_notes":0,"failed_notes":1}}`},
	})
	file := t.TempDir() + "/notes.enex"
	if err := os.WriteFile(file, []byte(`<en-export></en-export>`), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, m, "import", "enex", file)
	if err == nil {
		t.Fatal("an import that lost a note must not exit 0")
	}
	if got := exitCodeFor(err); got != exitError {
		t.Errorf("exit code = %d, want %d", got, exitError)
	}
	if !strings.Contains(out, "job-1") || !strings.Contains(out, "11") {
		t.Errorf("the counters must still be printed:\n%s", out)
	}
}

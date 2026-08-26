// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// TestDisplayImportJobWaited verifies the summary shows the counters and does
// NOT print the "poll it" hint when the command already waited for the result.
func TestDisplayImportJobWaited(t *testing.T) {
	importEnexAsync = false
	data := []byte(`{"data":{"import_job_id":"job-123","status":"completed","total_notes":12,"imported_notes":11,"skipped_notes":0,"failed_notes":1}}`)
	out := captureStdout(t, func() { displayImportJob(data) })
	if !containsSub(out, "job-123", "completed", "12", "11") {
		t.Errorf("import job summary missing fields:\n%s", out)
	}
	if strings.Contains(out, "import status") {
		t.Errorf("a waited-for import should not print the poll hint:\n%s", out)
	}
}

// TestDisplayImportJobNoWait verifies a --no-wait import prints the follow-up
// poll command with the job id — the only route back to the outcome.
func TestDisplayImportJobNoWait(t *testing.T) {
	importEnexAsync = true
	defer func() { importEnexAsync = false }()
	data := []byte(`{"data":{"import_job_id":"job-async","status":"queued","total_notes":0}}`)
	out := captureStdout(t, func() { displayImportJob(data) })
	if !strings.Contains(out, "harbor import status job-async") {
		t.Errorf("a --no-wait import should print the poll hint:\n%s", out)
	}
}

// TestDisplayImportStatusWithErrors verifies the poll view renders counters and
// the per-note error table, mapping a job-level index (-1) to "job".
func TestDisplayImportStatusWithErrors(t *testing.T) {
	data := []byte(`{"data":{"id":"job-9","status":"partial","total_notes":12,"imported_notes":11,"skipped_notes":0,"failed_notes":1,"updated_at":1750000000000,"errors":[{"note_index":7,"title":"Broken note","reason":"attachment_unreadable"}]}}`)
	out := captureStdout(t, func() { displayImportStatus(data) })
	if !containsSub(out, "job-9", "partial", "Broken note", "corrupted") {
		t.Errorf("import status missing fields:\n%s", out)
	}
	if !strings.Contains(out, "7") {
		t.Errorf("per-note index missing:\n%s", out)
	}
	// The wire value is a code from a closed set the API says never to render.
	if strings.Contains(out, "attachment_unreadable") {
		t.Errorf("the raw reason code leaked to the user:\n%s", out)
	}
}

// TestDisplayImportStatusJobLevelError verifies a note_index of -1 renders as a
// job-level error rather than a literal "-1".
func TestDisplayImportStatusJobLevelError(t *testing.T) {
	data := []byte(`{"data":{"id":"job-x","status":"failed","failure_reason":"not_enex","errors":[{"note_index":-1,"title":"","reason":"not_enex"}]}}`)
	out := captureStdout(t, func() { displayImportStatus(data) })
	// The job's own reason is the half that says whether re-running can help, so
	// it has to appear under the counters rather than only in the error table.
	if !strings.Contains(out, "Why:") || !strings.Contains(out, "isn't an Evernote export") {
		t.Errorf("job-level failure reason not rendered:\n%s", out)
	}
	// And the errors list restating it is the same sentence twice.
	if strings.Contains(out, "Errors:") {
		t.Errorf("the job-level error only restates the reason above:\n%s", out)
	}

	// A job-level error that says something the reason did not still shows.
	data = []byte(`{"data":{"id":"job-y","status":"failed","failure_reason":"","errors":[{"note_index":-1,"title":"","reason":"storage_unavailable"}]}}`)
	out = captureStdout(t, func() { displayImportStatus(data) })
	if !containsSub(out, "Errors:", "job", "temporarily unavailable") {
		t.Errorf("job-level error not rendered:\n%s", out)
	}
}

// TestImportReasonSentence pins the contract that a failure CODE is never shown
// to the user, including a code this build has never heard of — an unknown value
// must fall back to the generic sentence rather than be printed raw.
func TestImportReasonSentence(t *testing.T) {
	if got := importReasonSentence(""); got != "" {
		t.Errorf("no code should render nothing, got %q", got)
	}
	if got := importReasonSentence("note_too_large"); !strings.Contains(got, "per-note limit") {
		t.Errorf("note_too_large = %q", got)
	}
	if got := importReasonSentence("a_code_from_the_future"); got != importReasonSentences["unknown"] {
		t.Errorf("an unrecognised code must fall back to the generic sentence, got %q", got)
	}

	// file_truncated means two different things on a note, and only the job's own
	// reason tells them apart: a cut-off upload is worth retrying, an Evernote
	// export that ends mid-note is not — it has to be re-exported.
	cutOff := importNoteReasonSentence("file_truncated", "")
	shortExport := importNoteReasonSentence("file_truncated", "truncated_source")
	if cutOff == shortExport {
		t.Error("a cut-off upload and a short Evernote export must not read the same")
	}
	if !strings.Contains(shortExport, "re-export") {
		t.Errorf("a truncated source must point at re-exporting, got %q", shortExport)
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
		"enex_too_large":               "maximum import size",
		"import_upload_incomplete":     "did not finish",
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
// The direct-to-storage upload (#101)
// ===========================================================================

// importStub is a stub Harbor API plus a stub object store, wired together the
// way the real pair are: the API hands out presigned URLs that point at the
// store, and the store is the only thing that ever sees the file's bytes.
//
// It exists because the interesting assertions about this command are about
// TRAFFIC — which calls were made, in what order, and above all that no request
// to the API carried the .enex — and none of those survive testing the helpers
// in isolation.
type importStub struct {
	api      *apiMock
	storage  *httptest.Server
	partSize int64

	// puts holds the bytes of each part, keyed by part number, so a test can
	// reassemble what the store received and compare it with the file on disk.
	puts map[int]string
	// failPart, when non-zero, makes that part's PUT fail — the trigger for the
	// abort path.
	failPart int
	// failAbort makes the abort call itself fail, which is the case the CLI has
	// to tell the user about: the job keeps its staged bytes and only the job id
	// gets them back to it.
	failAbort bool
	// interruptOnPart, when non-zero, raises a real SIGINT from inside that
	// part's PUT. It is the only way to exercise the Ctrl-C branch: a rejected
	// chunk fails the upload without ever cancelling the context, so the two
	// paths diverge exactly where the message does.
	interruptOnPart int
	// completedParts records the part/ETag list the complete call received.
	completedParts []map[string]any
	// statuses is the sequence the poller reports; the last entry repeats.
	statuses []string
	polls    int
}

// newImportStub starts the API/storage pair. partSize is the chunk size the
// stub's plan advertises, so a test can force several parts out of a small file.
func newImportStub(t *testing.T, partSize int64, statuses ...string) *importStub {
	t.Helper()
	st := &importStub{partSize: partSize, puts: map[int]string{}, statuses: statuses}

	st.storage = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/part/"))
		if n == st.interruptOnPart {
			if p, perr := os.FindProcess(os.Getpid()); perr == nil {
				_ = p.Signal(os.Interrupt)
			}
			// Give the signal time to reach the handler before this PUT's
			// failure races it, so the run is cancelled rather than merely
			// broken.
			time.Sleep(150 * time.Millisecond)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
			return
		}
		if n == st.failPart {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		st.puts[n] = string(body)
		w.Header().Set("ETag", fmt.Sprintf("%q", "etag-"+strconv.Itoa(n)))
	}))
	t.Cleanup(st.storage.Close)

	st.api = newAPIMock(t, map[string]mockReply{})
	st.api.handler = st.serve
	return st
}

// serve answers the five API calls the import flow makes.
func (st *importStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/api/v1/import/enex/uploads":
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		total := int64(req["total_bytes"].(float64))
		count := (total + st.partSize - 1) / st.partSize
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"data":{"import_job_id":"job-1","status":"awaiting_upload","part_size":%d,"part_count":%d}}`,
			st.partSize, count)

	case strings.HasSuffix(r.URL.Path, "/parts"):
		var req struct {
			PartNumbers []int `json:"part_numbers"`
		}
		_ = json.Unmarshal(body, &req)
		parts := make([]string, 0, len(req.PartNumbers))
		for _, n := range req.PartNumbers {
			parts = append(parts, fmt.Sprintf(`{"part_number":%d,"url":%q}`, n,
				st.storage.URL+"/part/"+strconv.Itoa(n)))
		}
		fmt.Fprintf(w, `{"data":{"parts":[%s],"expires_in_seconds":21600}}`, strings.Join(parts, ","))

	case strings.HasSuffix(r.URL.Path, "/complete"):
		var req struct {
			Parts []map[string]any `json:"parts"`
		}
		_ = json.Unmarshal(body, &req)
		st.completedParts = req.Parts
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"data":{"import_job_id":"job-1","status":"queued"}}`)

	case strings.HasSuffix(r.URL.Path, "/abort"):
		if st.failAbort {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"code":"internal_error","message":"abort failed"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"id":"job-1","status":"aborted"}}`)

	case strings.HasPrefix(r.URL.Path, "/api/v1/import/enex/"):
		i := min(st.polls, len(st.statuses)-1)
		st.polls++
		fmt.Fprint(w, st.statuses[i])

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// assembled returns the parts the store received, joined back in order.
func (st *importStub) assembled() string {
	var sb strings.Builder
	for n := 1; n <= len(st.puts); n++ {
		sb.WriteString(st.puts[n])
	}
	return sb.String()
}

// writeENEX writes a fixture file of the given size and returns its path.
func writeENEX(t *testing.T, size int) string {
	t.Helper()
	path := t.TempDir() + "/notes.enex"
	body := strings.Repeat("A", size)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestImportEnexUsesDirectUpload is the regression for the bug this command had:
// it posted the .enex to POST /import/enex, a route the server deleted, so every
// import 404'd. It must now run the four-call upload, and — the part that made
// the old route unshippable — not one byte of the file may travel through the
// API.
func TestImportEnexUsesDirectUpload(t *testing.T) {
	st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"completed","total_notes":3,"imported_notes":3,"errors":[]}}`)
	file := writeENEX(t, 10)

	out, err := runCLI(t, st.api, "import", "enex", file, "--poll-interval", "1ms")
	if err != nil {
		t.Fatalf("import enex: %v\n%s", err, out)
	}

	want := []string{
		"POST /api/v1/import/enex/uploads",
		"POST /api/v1/import/enex/uploads/job-1/parts",
		"POST /api/v1/import/enex/uploads/job-1/complete",
		"GET /api/v1/import/enex/job-1",
	}
	if got := st.api.calls(); !slices.Equal(got, want) {
		t.Errorf("call sequence = %v, want %v", got, want)
	}
	// A 10-byte file in 4-byte chunks is three parts, the last a remainder, and
	// they must reassemble byte-for-byte.
	if len(st.puts) != 3 {
		t.Errorf("parts uploaded = %d, want 3", len(st.puts))
	}
	if got := st.assembled(); got != strings.Repeat("A", 10) {
		t.Errorf("the store received %q", got)
	}
	// The whole point of the new path: the bytes never reach the API.
	for _, req := range st.api.requests {
		if strings.Contains(req.Body, "AAAA") {
			t.Errorf("%s %s carried the file's bytes: %s", req.Method, req.Path, req.Body)
		}
	}
	if !containsSub(out, "job-1", "completed") {
		t.Errorf("the final counters should be printed:\n%s", out)
	}
}

// TestImportEnexSendsOptionsOnCreate verifies --notebook and --filename survive
// the move: both now ride the create-upload body, and --filename still defaults
// to the file's base name (it names the auto-created notebook).
func TestImportEnexSendsOptionsOnCreate(t *testing.T) {
	done := `{"data":{"id":"job-1","status":"completed","total_notes":1,"imported_notes":1,"errors":[]}}`

	st := newImportStub(t, 64, done)
	file := writeENEX(t, 8)
	if _, err := runCLI(t, st.api, "import", "enex", file,
		"--notebook", "nb1", "--filename", "My Export.enex", "--poll-interval", "1ms"); err != nil {
		t.Fatalf("import enex: %v", err)
	}
	body := st.api.bodyOf(t, "POST /api/v1/import/enex/uploads")
	if body["target_notebook_id"] != "nb1" {
		t.Errorf("target_notebook_id = %v", body["target_notebook_id"])
	}
	if body["filename"] != "My Export.enex" {
		t.Errorf("filename = %v", body["filename"])
	}
	if body["total_bytes"] != float64(8) {
		t.Errorf("total_bytes = %v, want the file's real size", body["total_bytes"])
	}

	// Without --filename the file's base name is what names the notebook.
	st2 := newImportStub(t, 64, done)
	if _, err := runCLI(t, st2.api, "import", "enex", file, "--poll-interval", "1ms"); err != nil {
		t.Fatalf("import enex: %v", err)
	}
	if got := st2.api.bodyOf(t, "POST /api/v1/import/enex/uploads")["filename"]; got != "notes.enex" {
		t.Errorf("filename = %v, want the file's base name", got)
	}
}

// TestImportEnexNoWaitStopsAtComplete pins the documented --no-wait contract:
// the command returns as soon as the upload is accepted, never polls, and hands
// back the job id plus the command that reads its outcome.
func TestImportEnexNoWaitStopsAtComplete(t *testing.T) {
	st := newImportStub(t, 64, `{"data":{"id":"job-1","status":"running"}}`)
	file := writeENEX(t, 8)

	out, err := runCLI(t, st.api, "import", "enex", file, "--no-wait")
	if err != nil {
		t.Fatalf("import enex --no-wait: %v", err)
	}
	if st.polls != 0 {
		t.Errorf("--no-wait must not poll (polled %d times)", st.polls)
	}
	if !strings.Contains(out, "harbor import status job-1") {
		t.Errorf("--no-wait should hand back the poll command:\n%s", out)
	}
}

// TestImportEnexAbortsAFailedUpload covers the acceptance criterion behind
// Ctrl-C: an upload that does not finish must be cancelled server-side. Leaving
// it means the job sits in awaiting_upload with a half-written multipart object
// behind it. A rejected chunk is the same shape of failure as an interrupt and
// is what a test can actually provoke.
func TestImportEnexAbortsAFailedUpload(t *testing.T) {
	st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"completed"}}`)
	st.failPart = 2
	file := writeENEX(t, 10)

	out, err := runCLI(t, st.api, "import", "enex", file, "--poll-interval", "1ms")
	if err == nil {
		t.Fatalf("a failed upload must not exit 0:\n%s", out)
	}
	if !slices.Contains(st.api.calls(), "POST /api/v1/import/enex/uploads/job-1/abort") {
		t.Errorf("the upload was never aborted: %v", st.api.calls())
	}
	// The upload's own failure is what went wrong; a clean abort must not
	// rewrite the error into a cancellation the user never asked for.
	if strings.Contains(err.Error(), "canceled") {
		t.Errorf("a rejected chunk is not a cancellation: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "storage") {
		t.Errorf("the error should still name the upload failure, got %q", err.Error())
	}
	// Nothing was staged, so nothing may be completed or polled either.
	for _, call := range st.api.calls() {
		if strings.HasSuffix(call, "/complete") {
			t.Errorf("a failed upload must not be completed: %v", st.api.calls())
		}
	}
}

// TestImportEnexRejectsAnEmptyFile catches a zero-byte file locally. The size is
// declared up front and is what the server chunks by, so an empty file can only
// come back as a validation error on a request that could never have worked.
func TestImportEnexRejectsAnEmptyFile(t *testing.T) {
	st := newImportStub(t, 64)
	file := writeENEX(t, 0)

	if _, err := runCLI(t, st.api, "import", "enex", file); err == nil {
		t.Fatal("an empty file must not be uploaded")
	}
	if len(st.api.calls()) != 0 {
		t.Errorf("nothing should have been sent: %v", st.api.calls())
	}
}

// TestImportEnexExitsNonZeroAndStillPrintsCounters is the end-to-end regression
// for #69: the counters stay on stdout — they are the answer the user came for —
// while the exit code says the import did not fully land.
func TestImportEnexExitsNonZeroAndStillPrintsCounters(t *testing.T) {
	st := newImportStub(t, 64,
		`{"data":{"id":"job-1","status":"partial","total_notes":12,"imported_notes":11,"skipped_notes":0,"failed_notes":1,"errors":[]}}`)
	file := writeENEX(t, 8)

	out, err := runCLI(t, st.api, "import", "enex", file, "--poll-interval", "1ms")
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

// TestImportEnexWaitsThroughQueuedAndRunning proves the default really does wait
// rather than returning on the first answer, and that a job still in flight is
// not mistaken for one that failed.
func TestImportEnexWaitsThroughQueuedAndRunning(t *testing.T) {
	st := newImportStub(t, 64,
		`{"data":{"id":"job-1","status":"queued","total_notes":0}}`,
		`{"data":{"id":"job-1","status":"running","total_notes":9,"imported_notes":4}}`,
		`{"data":{"id":"job-1","status":"completed","total_notes":9,"imported_notes":9,"errors":[]}}`)
	file := writeENEX(t, 8)

	out, err := runCLI(t, st.api, "import", "enex", file, "--poll-interval", "1ms")
	if err != nil {
		t.Fatalf("import enex: %v\n%s", err, out)
	}
	if st.polls != 3 {
		t.Errorf("polls = %d, want 3 (queued, running, completed)", st.polls)
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("the final status should be printed:\n%s", out)
	}
}

// TestImportEnexTimeoutReportsWhereItGotTo covers giving up on the wait. The
// import keeps running on the server, so the error must say so and name the
// command that reads its outcome — and the last status seen still gets printed,
// because that is what the user waited for.
func TestImportEnexTimeoutReportsWhereItGotTo(t *testing.T) {
	st := newImportStub(t, 64, `{"data":{"id":"job-1","status":"running","total_notes":9,"imported_notes":4}}`)
	file := writeENEX(t, 8)

	out, err := runCLI(t, st.api, "import", "enex", file, "--poll-interval", "1ms", "--timeout", "1ms")
	if err == nil {
		t.Fatal("a wait that timed out must not exit 0")
	}
	if !containsSub(err.Error(), "harbor import status job-1") {
		t.Errorf("the timeout should name the follow-up command: %v", err)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("the last status seen should still be printed:\n%s", out)
	}
}

// TestImportProgressLine covers the stderr progress copy: a queued job says it is
// waiting, a running one counts notes, and a terminal one says nothing (the
// result card that follows is the answer).
func TestImportProgressLine(t *testing.T) {
	queued := importProgressLine(map[string]any{"status": "queued"})
	if !strings.Contains(queued, "Queued") {
		t.Errorf("queued = %q", queued)
	}
	running := importProgressLine(map[string]any{
		"status": "running", "total_notes": 120.0, "imported_notes": 38.0, "failed_notes": 2.0,
	})
	if !containsSub(running, "40", "120") {
		t.Errorf("running should count every note it has finished with: %q", running)
	}
	if got := importProgressLine(map[string]any{"status": "completed"}); got != "" {
		t.Errorf("a terminal job needs no progress line, got %q", got)
	}
}

// TestImportPartURLs verifies the presign response is indexed by part number, so
// the upload loop pairs chunks with URLs regardless of the order they came back.
func TestImportPartURLs(t *testing.T) {
	urls := importPartURLs([]byte(`{"data":{"parts":[{"part_number":2,"url":"u2"},{"part_number":1,"url":"u1"}]}}`))
	if urls[1] != "u1" || urls[2] != "u2" {
		t.Errorf("urls = %v", urls)
	}
}

// TestImportIsTerminal pins which statuses end a poll. aborted belongs with the
// failures: a cancelled upload imported nothing and polling it forever would
// hang the command.
func TestImportIsTerminal(t *testing.T) {
	for _, s := range []string{"completed", "partial", "failed", "aborted"} {
		if !importIsTerminal(s) {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []string{"awaiting_upload", "queued", "running", ""} {
		if importIsTerminal(s) {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

// ===========================================================================
// An import that lost notes (#69 sweep)
// ===========================================================================
//
// An import is accepted whether it goes on to import every note or none of them
// — the outcome is in the body's counters — so before this an import that
// dropped every note exited 0 and a script moved on believing the data was in
// Harbor.

// TestImportJobFailureFlagsLostNotes covers the shapes that mean notes did not
// make it: the API's `partial`, `failed` and `aborted` statuses, and a non-zero
// failed_notes count under any status.
func TestImportJobFailureFlagsLostNotes(t *testing.T) {
	cases := map[string]string{
		"partial":              `{"data":{"import_job_id":"job-1","status":"partial","total_notes":12,"imported_notes":11,"failed_notes":1}}`,
		"failed":               `{"data":{"import_job_id":"job-2","status":"failed","total_notes":12,"imported_notes":0,"failed_notes":12}}`,
		"aborted":              `{"data":{"id":"job-3","status":"aborted","total_notes":12,"imported_notes":0}}`,
		"failures while green": `{"data":{"import_job_id":"job-4","status":"completed","total_notes":12,"imported_notes":11,"failed_notes":1}}`,
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
	// the counters alone cannot tell the user what went wrong. The poller names
	// the job `id` where the complete call names it `import_job_id`; both have to
	// produce a usable follow-up command.
	for _, body := range []string{
		`{"data":{"import_job_id":"job-1","status":"partial","total_notes":2,"imported_notes":1,"failed_notes":1}}`,
		`{"data":{"id":"job-1","status":"partial","total_notes":2,"imported_notes":1,"failed_notes":1}}`,
	} {
		err := importJobFailure([]byte(body))
		var apiError *client.APIError
		if !errors.As(err, &apiError) {
			t.Fatalf("want an *client.APIError, got %T", err)
		}
		if got := apiError.Details["per-note reasons"]; got != "harbor import status job-1" {
			t.Errorf("details should point at the poller, got %v", apiError.Details)
		}
	}

	// A job-level failure explains itself, and that explanation is what says
	// whether re-running the same file can possibly help.
	err := importJobFailure([]byte(`{"data":{"id":"job-9","status":"failed","failure_reason":"truncated_source","total_notes":2,"imported_notes":0}}`))
	var apiError *client.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("want an *client.APIError, got %T", err)
	}
	if why, _ := apiError.Details["reason"].(string); !strings.Contains(why, "re-export") {
		t.Errorf("a truncated source should point at re-exporting, got %v", apiError.Details["reason"])
	}
}

// TestImportJobFailureStaysQuietWhenNothingWasLost keeps it from crying wolf. A
// queued import in particular has not failed at anything — it has not run yet.
func TestImportJobFailureStaysQuietWhenNothingWasLost(t *testing.T) {
	cases := map[string]string{
		"clean import":     `{"data":{"import_job_id":"job-1","status":"completed","total_notes":12,"imported_notes":12,"failed_notes":0}}`,
		"enqueued import":  `{"data":{"import_job_id":"job-2","status":"queued","total_notes":0,"imported_notes":0,"failed_notes":0}}`,
		"awaiting upload":  `{"data":{"import_job_id":"job-3","status":"awaiting_upload"}}`,
		"unparseable body": `<html>nope</html>`,
	}
	for name, body := range cases {
		if err := importJobFailure([]byte(body)); err != nil {
			t.Errorf("%s: want no error, got %v", name, err)
		}
	}
}

// TestImportEnexReportsAnAbortThatFailed covers the half of the abort criterion
// the happy path cannot: when the abort ITSELF fails the job keeps its staged
// bytes, and the job id is the only handle the user has left on it.
//
// It must not depend on --verbose. A silent failure here leaves an upload open
// with nothing on screen to say so.
func TestImportEnexReportsAnAbortThatFailed(t *testing.T) {
	st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"completed"}}`)
	st.failPart = 2
	st.failAbort = true

	stderr := captureStderr(t, func() {
		if _, err := runCLI(t, st.api, "import", "enex", writeENEX(t, 10), "--poll-interval", "1ms"); err == nil {
			t.Error("a failed upload must not exit 0")
		}
	})

	if !strings.Contains(stderr, "could not abort") {
		t.Errorf("a failed abort must be reported without --verbose:\n%s", stderr)
	}
	if !strings.Contains(stderr, "harbor import abort job-1") {
		t.Errorf("the user needs the job id and a way to use it:\n%s", stderr)
	}
}

// TestImportAbortCommand verifies the command the failure message points at
// actually reaches the abort endpoint — a message naming a command that does
// not work would be worse than saying nothing.
func TestImportAbortCommand(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/import/enex/uploads/job-1/abort": {Status: 200, Body: `{"data":{"id":"job-1","status":"aborted"}}`},
	})
	out, err := runCLI(t, m, "import", "abort", "job-1")
	if err != nil {
		t.Fatalf("import abort: %v", err)
	}
	if !slices.Contains(m.calls(), "POST /api/v1/import/enex/uploads/job-1/abort") {
		t.Errorf("abort was not sent: %v", m.calls())
	}
	if !strings.Contains(out, "aborted") {
		t.Errorf("no confirmation printed:\n%s", out)
	}

	// --json is a machine surface: the server's job record, not a sentence.
	m2 := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/import/enex/uploads/job-1/abort": {Status: 200, Body: `{"data":{"id":"job-1","status":"aborted"}}`},
	})
	out, err = runCLI(t, m2, "import", "abort", "job-1", "--json")
	if err != nil {
		t.Fatalf("import abort --json: %v", err)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("--json did not emit JSON: %v\n%s", jerr, out)
	}
	if data, ok := got["data"].(map[string]any); !ok || data["status"] != "aborted" {
		t.Errorf("--json lost the job record: %s", out)
	}
}

// TestImportEnexNotifyEmail pins the one field that must never be omitted — the
// server reads an ABSENT notify_email as true — and the rule that decides it.
//
// The default follows --no-wait rather than being a fixed false: waiting prints
// the outcome to the terminal you are sitting at, while --no-wait is the case
// where the email is the only completion signal you get.
func TestImportEnexNotifyEmail(t *testing.T) {
	done := `{"data":{"id":"job-1","status":"completed","total_notes":1,"imported_notes":1,"errors":[]}}`

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"waiting sends none", []string{"--poll-interval", "1ms"}, `"notify_email":false`},
		{"--no-wait asks for one", []string{"--no-wait"}, `"notify_email":true`},
		{"explicit on while waiting", []string{"--notify-email", "--poll-interval", "1ms"}, `"notify_email":true`},
		{"explicit off beats --no-wait", []string{"--no-wait", "--notify-email=false"}, `"notify_email":false`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newImportStub(t, 64, done)
			args := append([]string{"import", "enex", writeENEX(t, 8)}, tc.args...)
			if _, err := runCLI(t, st.api, args...); err != nil {
				t.Fatalf("import enex %v: %v", tc.args, err)
			}
			if raw := st.api.rawBodyOf(t, "POST /api/v1/import/enex/uploads"); !strings.Contains(raw, tc.want) {
				t.Errorf("want %s in %s", tc.want, raw)
			}
		})
	}
}

// TestImportEnexInterruptTellsTheTruthAboutTheAbort raises a real SIGINT
// mid-upload — the only way to reach the Ctrl-C branch, since a rejected chunk
// fails without ever cancelling the context.
//
// The two outcomes must not share a sentence. Saying "the upload was aborted and
// nothing was imported" when the abort is what failed sends the user away
// believing a job was cleaned up that is still holding its bytes.
func TestImportEnexInterruptTellsTheTruthAboutTheAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be delivered to a process on Windows")
	}
	t.Run("abort succeeds", func(t *testing.T) {
		st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"aborted"}}`)
		st.interruptOnPart = 2

		_, err := runCLI(t, st.api, "import", "enex", writeENEX(t, 10), "--poll-interval", "1ms")
		if err == nil {
			t.Fatal("an interrupted import must not exit 0")
		}
		if !strings.Contains(err.Error(), "was aborted and nothing was imported") {
			t.Errorf("error = %q, want the clean-cancel wording", err.Error())
		}
	})

	t.Run("abort fails", func(t *testing.T) {
		st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"aborted"}}`)
		st.interruptOnPart = 2
		st.failAbort = true

		_, err := runCLI(t, st.api, "import", "enex", writeENEX(t, 10), "--poll-interval", "1ms")
		if err == nil {
			t.Fatal("an interrupted import must not exit 0")
		}
		if strings.Contains(err.Error(), "was aborted and nothing was imported") {
			t.Errorf("a failed abort must not claim the upload was aborted: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "could NOT be aborted") {
			t.Errorf("error = %q, want the failed-abort wording", err.Error())
		}
	})
}

// TestImportEnexAbortFailureIsMachineReadable pins the --json contract: stderr
// carries one parseable error envelope and no prose, and the job id — the only
// handle left on an upload still holding its bytes — is a field rather than
// something a script must regex out of a sentence.
func TestImportEnexAbortFailureIsMachineReadable(t *testing.T) {
	st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"completed"}}`)
	st.failPart = 2
	st.failAbort = true

	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLI(t, st.api, "import", "enex", writeENEX(t, 10), "--json", "--poll-interval", "1ms")
		renderError(err)
	})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("--json must not put prose on stderr:\n%s", stderr)
	}

	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &env); jerr != nil {
		t.Fatalf("stderr is not one JSON envelope: %v\n%s", jerr, stderr)
	}
	if env.Error.Code != "import_abort_failed" {
		t.Errorf("code = %q, want import_abort_failed", env.Error.Code)
	}
	if env.Error.Details["job_id"] != "job-1" {
		t.Errorf("job_id = %v, want job-1", env.Error.Details["job_id"])
	}
	if env.Error.Details["recover"] != "harbor import abort job-1" {
		t.Errorf("recover = %v", env.Error.Details["recover"])
	}
}

// TestImportEnexHandsBackEveryETag pins the half of the ETag criterion the
// client test cannot reach: object storage refuses to assemble a multipart
// upload without them, so dropping them between the PUT and the complete call
// fails only against a real store.
func TestImportEnexHandsBackEveryETag(t *testing.T) {
	st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"completed","total_notes":1,"imported_notes":1,"errors":[]}}`)
	if _, err := runCLI(t, st.api, "import", "enex", writeENEX(t, 10), "--poll-interval", "1ms"); err != nil {
		t.Fatalf("import enex: %v", err)
	}
	if len(st.completedParts) != 3 {
		t.Fatalf("complete carried %d parts, want 3", len(st.completedParts))
	}
	for i, p := range st.completedParts {
		want := fmt.Sprintf("%q", "etag-"+strconv.Itoa(i+1))
		if p["etag"] != want {
			t.Errorf("part %d etag = %v, want %s", i+1, p["etag"], want)
		}
		if p["part_number"] != float64(i+1) {
			t.Errorf("part_number = %v, want %d", p["part_number"], i+1)
		}
	}
}

// TestImportPresignBatchRespectsTheServerCap pins the batch size below the
// server's documented 1000-per-call maximum. Exceeding it fails the presign
// request outright, which no unit test would otherwise notice.
func TestImportPresignBatchRespectsTheServerCap(t *testing.T) {
	const serverMax = 1000
	if importPresignBatch > serverMax {
		t.Errorf("importPresignBatch = %d, over the server's %d-per-call cap", importPresignBatch, serverMax)
	}
	if importPresignBatch < 1 {
		t.Errorf("importPresignBatch = %d, must request at least one part", importPresignBatch)
	}
}

// TestImportPresignsInMultipleBatches exercises the batching loop, which a
// small fixture otherwise leaves entirely unrun — every import so far fits in
// one request.
func TestImportPresignsInMultipleBatches(t *testing.T) {
	st := newImportStub(t, 1, `{"data":{"id":"job-1","status":"completed","total_notes":1,"imported_notes":1,"errors":[]}}`)
	if _, err := runCLI(t, st.api, "import", "enex", writeENEX(t, importPresignBatch+3), "--poll-interval", "1ms"); err != nil {
		t.Fatalf("import enex: %v", err)
	}
	var presigns int
	for _, call := range st.api.calls() {
		if strings.HasSuffix(call, "/parts") {
			presigns++
		}
	}
	if presigns < 2 {
		t.Errorf("a %d-part upload should presign in more than one batch, got %d call(s)",
			importPresignBatch+3, presigns)
	}
	if len(st.completedParts) != importPresignBatch+3 {
		t.Errorf("complete carried %d parts, want %d", len(st.completedParts), importPresignBatch+3)
	}
}

// TestImportProgressIsSuppressedUnderJSON keeps the machine-readable surface
// machine-readable: progress is a human affordance, and a percentage printed
// mid-stream would sit in front of whatever a script is parsing.
func TestImportProgressIsSuppressedUnderJSON(t *testing.T) {
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	quiet := &importProgress{total: 100, lastPct: -1}
	if out := captureStderr(t, func() { quiet.advance(50) }); out != "" {
		t.Errorf("--json must print no progress, got %q", out)
	}

	jsonOutput = false
	human := &importProgress{total: 100, lastPct: -1}
	if out := captureStderr(t, func() { human.advance(50) }); !strings.Contains(out, "50%") {
		t.Errorf("progress should still print for a human, got %q", out)
	}
}

// TestImportEnexKeepsTheUploadErrorWhenTheAbortAlsoFails covers the case that
// makes a failed abort common in the first place: the network drops, so the
// chunk PUT fails AND the abort POST goes over the same dead connection.
//
// Reporting only "could not be aborted" would leave the user knowing their
// import broke but not why — and would cost a wrapper script the exit code it
// branches on, since a transport failure is retryable and a bare CLI error is
// not.
func TestImportEnexKeepsTheUploadErrorWhenTheAbortAlsoFails(t *testing.T) {
	st := newImportStub(t, 4, `{"data":{"id":"job-1","status":"completed"}}`)
	st.failPart = 2
	st.failAbort = true

	_, err := runCLI(t, st.api, "import", "enex", writeENEX(t, 10), "--poll-interval", "1ms")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "storage") {
		t.Errorf("the upload's own failure was discarded: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "could not be aborted") {
		t.Errorf("the failed abort was not reported: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "job-1") {
		t.Errorf("the job id is the only handle left and is missing: %q", err.Error())
	}
}

// TestImportEnexNetworkFailureKeepsExitThree pins the documented exit-code
// contract through the failed-abort path: 3 means "the API could not be reached,
// try again", and a wrapper that retries on it must not be told 1 — a permanent
// failure — for a dropped connection.
func TestImportEnexNetworkFailureKeepsExitThree(t *testing.T) {
	cause := &url.Error{Op: "Put", URL: "https://store.example/part/2", Err: errors.New("connection refused")}

	if got := exitCodeFor(abortFailedError(cause, "job-1", false)); got != exitNetwork {
		t.Errorf("exit code = %d, want %d (retryable) — the wrapped transport error was lost", got, exitNetwork)
	}
	// A deliberate Ctrl-C is not a retryable network condition.
	if got := exitCodeFor(abortFailedError(cause, "job-1", true)); got != exitError {
		t.Errorf("interrupted exit code = %d, want %d", got, exitError)
	}
}

// TestAbortFailedErrorKeepsAServerCode verifies a typed cause survives with its
// own code and details, gaining the recovery keys rather than being replaced by
// them — so --json still reports what the server actually said.
func TestAbortFailedErrorKeepsAServerCode(t *testing.T) {
	cause := &client.APIError{
		Code:    "storage_rejected",
		Message: "the store refused the chunk",
		Status:  403,
		Details: map[string]any{"part": "2"},
	}

	var got *client.APIError
	if !errors.As(abortFailedError(cause, "job-1", false), &got) {
		t.Fatal("a typed cause must stay typed")
	}
	if got.Code != "storage_rejected" || got.Status != 403 {
		t.Errorf("server code lost: code=%q status=%d", got.Code, got.Status)
	}
	if got.Details["part"] != "2" {
		t.Errorf("the server's own details were dropped: %v", got.Details)
	}
	if got.Details["job_id"] != "job-1" || got.Details["recover"] != "harbor import abort job-1" {
		t.Errorf("recovery details missing: %v", got.Details)
	}
	if cause.Details["job_id"] != nil {
		t.Error("the caller's details map was mutated")
	}

	// The code belongs on its own line, not spliced into the wording. Building
	// the headline from Error() rather than Message doubles it — and for a plan
	// limit it corrupts the sentence every Harbor client shares, since
	// planLimitHeadline prints Message verbatim.
	if strings.HasPrefix(got.Message, "storage_rejected:") {
		t.Errorf("the code was baked into the message: %q", got.Message)
	}
	if !strings.HasPrefix(got.Message, "the store refused the chunk — ") {
		t.Errorf("message = %q, want the server's own wording first", got.Message)
	}

	plan := &client.APIError{Code: planLimitCode, Message: "You have reached your plan's limit.", Status: 403}
	var planGot *client.APIError
	if !errors.As(abortFailedError(plan, "job-1", false), &planGot) {
		t.Fatal("a plan limit must stay typed")
	}
	if !strings.HasPrefix(planLimitHeadline(planGot), "You have reached your plan's limit.") {
		t.Errorf("the shared plan-limit wording was corrupted: %q", planLimitHeadline(planGot))
	}
	if got := exitCodeFor(abortFailedError(plan, "job-1", false)); got != exitPlanLimit {
		t.Errorf("plan-limit exit code = %d, want %d", got, exitPlanLimit)
	}
}

// TestImportUploadsOneChunkAtATime is the memory criterion made checkable: a
// file many times the part size must reach the store as part-sized pieces, so
// nothing the size of the file is ever held at once.
func TestImportUploadsOneChunkAtATime(t *testing.T) {
	const partSize, fileSize = 8, 100
	st := newImportStub(t, partSize, `{"data":{"id":"job-1","status":"completed","total_notes":1,"imported_notes":1,"errors":[]}}`)

	if _, err := runCLI(t, st.api, "import", "enex", writeENEX(t, fileSize), "--poll-interval", "1ms"); err != nil {
		t.Fatalf("import enex: %v", err)
	}
	if len(st.puts) != (fileSize+partSize-1)/partSize {
		t.Fatalf("store received %d parts, want %d", len(st.puts), (fileSize+partSize-1)/partSize)
	}
	for n, body := range st.puts {
		if len(body) > partSize {
			t.Errorf("part %d carried %d bytes, over the %d-byte plan — the file was not sliced",
				n, len(body), partSize)
		}
	}
	if got := st.assembled(); len(got) != fileSize {
		t.Errorf("reassembled %d bytes, want %d", len(got), fileSize)
	}
}

// TestImportProgressClosesItsLine pins the newline that ends an in-place
// progress line. On a terminal advance() redraws with a carriage return and no
// newline, so without this the next stderr write lands on the same line — which
// is what a failed upload's warning does.
//
// Whether the call is DEFERRED cannot be observed here: the reporter is created
// inside uploadImportParts, and tests capture to a pipe where tty is false. This
// guards the helper; the defer is a one-line reading of the code above it.
func TestImportProgressClosesItsLine(t *testing.T) {
	drawn := &importProgress{total: 100, tty: true, lastPct: 40}
	if out := captureStderr(t, drawn.done); out != "\n" {
		t.Errorf("a drawn progress line must be closed with a newline, got %q", out)
	}

	// Nothing was ever drawn, so there is no line to close.
	untouched := &importProgress{total: 100, tty: true, lastPct: -1}
	if out := captureStderr(t, untouched.done); out != "" {
		t.Errorf("nothing was drawn, so nothing should be closed, got %q", out)
	}

	// Off a terminal every line already ended itself.
	piped := &importProgress{total: 100, tty: false, lastPct: 40}
	if out := captureStderr(t, piped.done); out != "" {
		t.Errorf("off a terminal there is no line to close, got %q", out)
	}
}

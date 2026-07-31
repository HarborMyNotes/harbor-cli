// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// TestAccountDeleteGuard exercises the non-interactive / confirmation-phrase
// guard, which is the security-critical bit of the destructive delete flow.
func TestAccountDeleteGuard(t *testing.T) {
	const phrase = accountDeleteConfirmPhrase

	cases := []struct {
		name        string
		jsonMode    bool
		interactive bool
		confirm     string
		yes         bool
		wantPhrase  string // expected returned phrase ("" = defer to prompt)
		wantErr     string // substring expected in the error ("" = no error)
	}{
		// Non-interactive: both --confirm (verbatim) and --yes required.
		{"noninteractive ok", false, false, phrase, true, phrase, ""},
		{"json ok", true, true, phrase, true, phrase, ""},
		{"noninteractive missing yes", false, false, phrase, false, "", "--yes"},
		{"noninteractive missing confirm", false, false, "", true, "", "--confirm"},
		{"noninteractive wrong confirm", false, false, "delete my account", true, "", "--confirm"},
		{"json without yes", true, true, phrase, false, "", "--yes"},

		// Interactive: empty confirm defers to the prompt; a wrong one fails fast;
		// a correct one is accepted.
		{"interactive defer", false, true, "", false, "", ""},
		{"interactive presupplied ok", false, true, phrase, false, phrase, ""},
		{"interactive wrong confirm", false, true, "nope", false, "", "did not match"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := accountDeleteGuard(tc.jsonMode, tc.interactive, tc.confirm, tc.yes)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantPhrase {
				t.Errorf("phrase = %q, want %q", got, tc.wantPhrase)
			}
		})
	}
}

// TestAccountDeleteGuardRejectsCaseFold confirms the phrase is matched verbatim,
// not case-folded or trimmed.
func TestAccountDeleteGuardRejectsCaseFold(t *testing.T) {
	for _, bad := range []string{"delete my account", " DELETE MY ACCOUNT", "DELETE MY ACCOUNT "} {
		if _, err := accountDeleteGuard(true, true, bad, true); err == nil {
			t.Errorf("guard accepted non-verbatim phrase %q", bad)
		}
	}
}

// TestMapAccountError maps the domain codes to friendly messages.
func TestMapAccountError(t *testing.T) {
	cases := map[string]string{
		"confirmation_mismatch": "did not match",
		"already_scheduled":     "already pending",
		"not_scheduled":         "no account deletion is pending",
		"grace_expired":         "window has passed",
		"reauth_required":       "incorrect current password",
		"export_in_progress":    "still being built",
		"not_found":             "no such export job",
	}
	for code, sub := range cases {
		got := mapAccountError(apiErr(code))
		if !strings.Contains(got.Error(), sub) {
			t.Errorf("mapAccountError(%s) = %q, want substring %q", code, got.Error(), sub)
		}
	}
}

// ===========================================================================
// The 409 refusal
// ===========================================================================

// TestAccountExportExistsMessage is the acceptance criterion that a refused
// export reads as an instruction rather than a raw conflict: it must name the
// format, what the blocking export covers, when it expires on its own, and both
// ways out. There is no list endpoint on this path, so these details are the
// ONLY thing standing between the user and a mystery.
func TestAccountExportExistsMessage(t *testing.T) {
	details := map[string]any{
		"export_job_id":     "e1",
		"format":            "enex",
		"scope":             "notebook",
		"notebook_id":       "nb1",
		"notebook_name":     "Recipes",
		"result_expires_at": "1750003600000",
	}
	got := mapAccountError(&client.APIError{Code: "export_exists", Message: "conflict", Details: details, Status: 409}).Error()

	for _, want := range []string{"ENEX", "Recipes", "export-delete e1", "export-status e1"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, epochMS(1750003600000)) {
		t.Errorf("refusal did not render the expiry date:\n%s", got)
	}
}

// TestAccountExportExistsMessageAccountScope words a whole-account refusal
// without inventing a notebook.
func TestAccountExportExistsMessageAccountScope(t *testing.T) {
	got := accountExportExistsMessage(map[string]any{
		"export_job_id": "e1", "format": "html", "scope": "account",
	})
	if !strings.Contains(got, "whole account") || !strings.Contains(got, "HTML") {
		t.Errorf("account-scope refusal wrong:\n%s", got)
	}
	if strings.Contains(got, "notebook") {
		t.Errorf("account-scope refusal must not mention a notebook:\n%s", got)
	}
}

// TestAccountExportExistsMessageUnnamedNotebook covers a scoped export written
// before the name column existed: notebook_id arrives without notebook_name, and
// the id is still better than saying nothing.
func TestAccountExportExistsMessageUnnamedNotebook(t *testing.T) {
	got := accountExportExistsMessage(map[string]any{
		"export_job_id": "e1", "format": "enex", "scope": "notebook", "notebook_id": "nb1",
	})
	if !strings.Contains(got, "nb1") {
		t.Errorf("unnamed notebook refusal should fall back to the id:\n%s", got)
	}
}

// TestAccountDetailEpoch accepts both spellings of a timestamp. Every value in
// the error envelope's details is serialized as a JSON string while the same
// field is a number in a success body — reading only one would silently drop the
// expiry from the refusal message.
func TestAccountDetailEpoch(t *testing.T) {
	const want = float64(1750003600000)
	if got := accountDetailEpoch(map[string]any{"result_expires_at": "1750003600000"}, "result_expires_at"); got != want {
		t.Errorf("string form = %v, want %v", got, want)
	}
	if got := accountDetailEpoch(map[string]any{"result_expires_at": want}, "result_expires_at"); got != want {
		t.Errorf("number form = %v, want %v", got, want)
	}
	if got := accountDetailEpoch(map[string]any{"result_expires_at": "later"}, "result_expires_at"); got != 0 {
		t.Errorf("unparseable form = %v, want 0", got)
	}
	if got := accountDetailEpoch(nil, "result_expires_at"); got != 0 {
		t.Errorf("missing form = %v, want 0", got)
	}
}

// TestAccountArticle keeps the refusal message readable: both format labels are
// acronyms whose first letter is NAMED with a vowel sound, so both take "an" —
// "a HTML export" is the kind of wrong that makes a message look machine-made.
func TestAccountArticle(t *testing.T) {
	cases := map[string]string{"HTML": "an", "ENEX": "an", "PDF": "a", "zip": "a", "": "a"}
	for label, want := range cases {
		if got := accountArticle(label); got != want {
			t.Errorf("accountArticle(%q) = %q, want %q", label, got, want)
		}
	}
}

// TestDisplayExportJobKeepsURLOutOfTheTable pins the presigned URL below the
// detail view. Several hundred characters in a table cell stretches every other
// row to the width of the signature and makes the whole status unreadable.
func TestDisplayExportJobKeepsURLOutOfTheTable(t *testing.T) {
	url := "https://s3.example/exports/x?X-Amz-Signature=" + strings.Repeat("a", 300)
	body := fmt.Sprintf(`{"data":{"id":"e1","format":"enex","status":"completed","total_units":4,"done_units":4,"download_url":%q}}`, url)
	out := captureStdout(t, func() { displayExportJob([]byte(body)) })

	if !strings.Contains(out, url) {
		t.Fatalf("the URL should still be printed:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "│") && len(line) > 200 {
			t.Errorf("a table row was stretched by the URL (%d chars): %s", len(line), line)
		}
	}
}

// TestMapAccountExportStartError keeps the two meanings of a 404 apart: on the
// start path with a --notebook it is the NOTEBOOK that does not exist, and
// telling the user "no such export job" would send them looking in the wrong
// place entirely.
func TestMapAccountExportStartError(t *testing.T) {
	scoped := mapAccountExportStartError(apiErr("not_found"), "nb1").Error()
	if !strings.Contains(scoped, "notebook") || !strings.Contains(scoped, "nb1") {
		t.Errorf("scoped 404 = %q, want it to name the notebook", scoped)
	}
	unscoped := mapAccountExportStartError(apiErr("not_found"), "").Error()
	if !strings.Contains(unscoped, "export job") {
		t.Errorf("unscoped 404 = %q, want the export-job wording", unscoped)
	}
	// Everything else still flows through the shared mapper.
	if got := mapAccountExportStartError(apiErr("export_in_progress"), "nb1").Error(); !strings.Contains(got, "still being built") {
		t.Errorf("other codes should fall through: %q", got)
	}
}

// ===========================================================================
// The states
// ===========================================================================

// TestDisplayExportJobStates walks every state the card can be in and pins what
// each must say. These are the acceptance criteria for the status output: a
// queued export is waiting its turn (not 0%), a running one counts NOTES, a
// ready one shows the completed date and the expiry, a failed one gives the
// reason, and expired/deleted explain where the download went.
func TestDisplayExportJobStates(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
		deny []string
	}{
		{
			name: "queued shows its place in line, not a progress bar",
			body: `{"data":{"id":"e1","format":"enex","status":"queued","queue_position":3,"total_units":0,"done_units":0}}`,
			want: []string{"queued", "3rd in line", "one export at a time"},
			deny: []string{"0%"},
		},
		{
			name: "running counts notes",
			body: `{"data":{"id":"e1","format":"html","status":"running","total_units":36500,"done_units":4120}}`,
			want: []string{"running", "4,120 / 36,500 notes", "11%"},
			deny: []string{"notebooks"},
		},
		{
			name: "completed shows the completed date and the expiry",
			body: `{"data":{"id":"e1","format":"enex","status":"completed","total_units":42,"done_units":42,` +
				`"filename":"harbor-export-2026-07-31.zip","download_url":"https://s3/x?sig=1",` +
				`"completed_at":1749744400000,"result_expires_at":1750003600000}}`,
			want: []string{"completed", "42 / 42 notes", "harbor-export-2026-07-31.zip", "https://s3/x",
				epochMS(1749744400000), epochMS(1750003600000), "export-delete e1"},
		},
		{
			name: "failed gives the reason",
			body: `{"data":{"id":"e1","format":"enex","status":"failed","error_text":"ran out of disk"}}`,
			want: []string{"failed", "ran out of disk", "try again"},
		},
		{
			name: "expired explains where the download went",
			body: `{"data":{"id":"e1","format":"enex","status":"expired"}}`,
			want: []string{"expired", "retention window", "harbor account export"},
		},
		{
			name: "deleted is worded as the user's own action",
			body: `{"data":{"id":"e1","format":"html","status":"deleted"}}`,
			want: []string{"deleted", "You deleted this export"},
		},
		{
			name: "a notebook-scoped export names the notebook",
			body: `{"data":{"id":"e1","format":"enex","status":"running","scope":"notebook","notebook_id":"nb1","notebook_name":"Recipes","total_units":20,"done_units":5}}`,
			want: []string{"notebook Recipes"},
		},
		{
			name: "a freshly started job renders from export_job_id",
			body: `{"data":{"export_job_id":"e1","status":"queued","format":"html"}}`,
			want: []string{"e1", "queued", "HTML", "whole account"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() { displayExportJob([]byte(tc.body)) })
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(out, deny) {
					t.Errorf("should not contain %q:\n%s", deny, out)
				}
			}
		})
	}
}

// TestDisplayExportJobCompletedWithoutURL covers the belt-and-braces case: a
// completed job whose archive is gone should say so rather than offer a download
// that is not there.
func TestDisplayExportJobCompletedWithoutURL(t *testing.T) {
	out := captureStdout(t, func() {
		displayExportJob([]byte(`{"data":{"id":"e1","format":"enex","status":"completed","total_units":4,"done_units":4}}`))
	})
	if !strings.Contains(out, "no longer available") {
		t.Errorf("missing the gone-archive note:\n%s", out)
	}
}

// TestAccountExportProgress pins the unit and the arithmetic. The counters used
// to be notebooks, which reported a 36,500-note account as "1/79"; getting this
// wrong again is the specific regression worth a test.
func TestAccountExportProgress(t *testing.T) {
	cases := []struct {
		job  map[string]any
		want string
	}{
		{map[string]any{"total_units": float64(36500), "done_units": float64(4120)}, "4,120 / 36,500 notes (11%)"},
		{map[string]any{"total_units": float64(0), "done_units": float64(0)}, "0 / 0 notes"},
		{map[string]any{}, ""},
	}
	for _, tc := range cases {
		if got := accountExportProgress(tc.job); got != tc.want {
			t.Errorf("accountExportProgress(%v) = %q, want %q", tc.job, got, tc.want)
		}
	}
}

// TestAccountExportProgressLine covers the one-liner --wait prints, including the
// queued wording, which must read as a position in a line rather than a stall.
func TestAccountExportProgressLine(t *testing.T) {
	queued := accountExportProgressLine(map[string]any{"status": "queued", "queue_position": float64(2)})
	if !strings.Contains(queued, "2nd in line") || !strings.Contains(queued, "one export at a time") {
		t.Errorf("queued line = %q", queued)
	}
	noPos := accountExportProgressLine(map[string]any{"status": "queued"})
	if !strings.Contains(noPos, "Waiting to start") || strings.Contains(noPos, "in line") {
		t.Errorf("queued line without a position = %q", noPos)
	}
	running := accountExportProgressLine(map[string]any{"status": "running", "total_units": float64(10), "done_units": float64(5)})
	if !strings.Contains(running, "5 / 10 notes") {
		t.Errorf("running line = %q", running)
	}
	if got := accountExportProgressLine(map[string]any{"status": "completed"}); got != "" {
		t.Errorf("terminal states have no progress line, got %q", got)
	}
}

// TestAccountExportIsTerminal pins which states stop a poll. Treating `expired`
// or `deleted` as non-terminal would spin forever on an export that is gone.
func TestAccountExportIsTerminal(t *testing.T) {
	for _, s := range []string{"completed", "failed", "expired", "deleted"} {
		if !accountExportIsTerminal(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []string{"queued", "running", ""} {
		if accountExportIsTerminal(s) {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

// ===========================================================================
// The slot listing
// ===========================================================================

// TestDisplayExportList renders both slots, each with its own state.
func TestDisplayExportList(t *testing.T) {
	data := []byte(`{"data":[` +
		`{"id":"e1","format":"enex","status":"completed","scope":"notebook","notebook_name":"Recipes","completed_at":1749744400000,"result_expires_at":1750003600000},` +
		`{"id":"e2","format":"html","status":"running","total_units":36500,"done_units":4120}]}`)
	out := captureStdout(t, func() { displayExportList(data) })
	for _, want := range []string{"ENEX", "HTML", "Recipes", "whole account", "completed", "running", "4,120 / 36,500 notes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// TestDisplayExportListEmpty prints a sentence and the way forward rather than an
// empty table.
func TestDisplayExportListEmpty(t *testing.T) {
	out := captureStdout(t, func() { displayExportList([]byte(`{"data":[]}`)) })
	if !strings.Contains(out, "No exports") || !strings.Contains(out, "harbor account export") {
		t.Errorf("empty listing should explain what to do:\n%s", out)
	}
}

// TestDisplayExportDeleted words the confirmation by what the server reports.
// The endpoint is idempotent, so an export that had already run out its own
// retention answers `expired` — telling that user "you deleted this" would be a
// small lie about something they did not do.
func TestDisplayExportDeleted(t *testing.T) {
	out := captureStdout(t, func() { displayExportDeleted([]byte(`{"data":{"id":"e1","status":"deleted"}}`)) })
	if !strings.Contains(out, "Export deleted") {
		t.Errorf("delete confirmation missing:\n%s", out)
	}
	out = captureStdout(t, func() { displayExportDeleted([]byte(`{"data":{"id":"e1","status":"expired"}}`)) })
	if !strings.Contains(out, "already expired") {
		t.Errorf("expired delete should say so:\n%s", out)
	}
}

// ===========================================================================
// Flags, paths and guards
// ===========================================================================

// TestAccountExportFormatFlag validates --format locally. An unset flag must come
// back empty so the request omits it and the SERVER's default applies — pinning
// "enex" in the client is how a default silently drifts.
func TestAccountExportFormatFlag(t *testing.T) {
	cmd := accountExportCmd
	defer resetFlags(cmd)

	for _, ok := range []string{"enex", "html", "HTML", " enex "} {
		_ = cmd.Flags().Set("format", ok)
		got, err := accountExportFormat(cmd)
		if err != nil {
			t.Errorf("--format %q rejected: %v", ok, err)
		}
		if got != strings.ToLower(strings.TrimSpace(ok)) {
			t.Errorf("--format %q normalized to %q", ok, got)
		}
	}

	_ = cmd.Flags().Set("format", "")
	if got, err := accountExportFormat(cmd); err != nil || got != "" {
		t.Errorf("unset --format = (%q, %v), want the server's default", got, err)
	}

	_ = cmd.Flags().Set("format", "pdf")
	if _, err := accountExportFormat(cmd); err == nil {
		t.Error("--format pdf should be rejected before a request is made")
	}
}

// TestAccountExportOutputPath resolves where an archive lands: stdout, a
// directory (where the server's self-describing filename is used so a download
// is not saved under a content hash), or a literal path.
func TestAccountExportOutputPath(t *testing.T) {
	dir := t.TempDir()
	if got := accountExportOutputPath(dir, "harbor-export-recipes.zip"); got != filepath.Join(dir, "harbor-export-recipes.zip") {
		t.Errorf("directory target = %q", got)
	}
	if got := accountExportOutputPath("-", "harbor-export.zip"); got != "-" {
		t.Errorf("stdout target = %q", got)
	}
	explicit := filepath.Join(dir, "mine.zip")
	if got := accountExportOutputPath(explicit, "harbor-export.zip"); got != explicit {
		t.Errorf("explicit target = %q", got)
	}
	if got := accountExportOutputPath(dir, ""); got != dir {
		t.Errorf("no server filename should leave the path alone, got %q", got)
	}
}

// TestAccountExportNotReadyReason explains each non-downloadable state in terms
// of what to do next, rather than echoing a status word.
func TestAccountExportNotReadyReason(t *testing.T) {
	cases := map[string]string{
		"queued":  "waiting its turn",
		"running": "still being built",
		"failed":  "start a new one",
		"expired": "retention window",
		"deleted": "was deleted",
	}
	for status, want := range cases {
		if got := accountExportNotReadyReason(status, "e1"); !strings.Contains(got, want) {
			t.Errorf("reason(%s) = %q, want substring %q", status, got, want)
		}
	}
}

// TestAccountConfirmExportDelete pins the destructive guard: --yes passes, and a
// non-interactive run without it refuses rather than prompting into the void.
func TestAccountConfirmExportDelete(t *testing.T) {
	if err := accountConfirmExportDelete(true); err != nil {
		t.Errorf("--yes should pass the guard: %v", err)
	}
	// Tests never run on a TTY, so this exercises the non-interactive branch.
	if err := accountConfirmExportDelete(false); err == nil {
		t.Error("a non-interactive delete without --yes must refuse")
	}
}

// ===========================================================================
// Wiring (the real command tree against a stub API)
// ===========================================================================

// TestAccountExportSendsFormatAndNotebook proves the flags reach the wire under
// the names the API expects — a display test cannot catch a flag that is read
// but never sent.
func TestAccountExportSendsFormatAndNotebook(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/account/export": {Status: 202, Body: `{"data":{"export_job_id":"e1","status":"queued","format":"html","notebook_id":"nb1","notebook_name":"Recipes"}}`},
	})
	out, err := runCLI(t, m, "account", "export", "--format", "html", "--notebook", "nb1")
	if err != nil {
		t.Fatalf("account export: %v", err)
	}
	body := m.bodyOf(t, "POST /api/v1/account/export")
	if body["format"] != "html" || body["notebook_id"] != "nb1" {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(out, "Recipes") {
		t.Errorf("output should name the scope it started:\n%s", out)
	}
}

// TestAccountExportRejectsBadFormatWithoutCallingAPI proves the validation is
// local: a typo must not cost a round-trip.
func TestAccountExportRejectsBadFormatWithoutCallingAPI(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	if _, err := runCLI(t, m, "account", "export", "--format", "pdf"); err == nil {
		t.Fatal("expected --format pdf to fail")
	}
	if len(m.calls()) != 0 {
		t.Errorf("no request should have been made, got %v", m.calls())
	}
}

// TestAccountExportRefusalIsActionable runs the real 409 through the command and
// checks the user is told what is in the way and how to clear it.
func TestAccountExportRefusalIsActionable(t *testing.T) {
	body := `{"error":{"code":"export_exists","message":"You already have an export.","details":{` +
		`"export_job_id":"e9","format":"enex","scope":"notebook","notebook_id":"nb1","notebook_name":"Recipes",` +
		`"result_expires_at":"1750003600000"}}}`
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/account/export": {Status: 409, Body: body},
	})
	_, err := runCLI(t, m, "account", "export", "--notebook", "nb1")
	if err == nil {
		t.Fatal("a 409 must surface as an error")
	}
	for _, want := range []string{"Recipes", "export-delete e9"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%s", want, err.Error())
		}
	}
}

// TestAccountExportsListsSlots pins the listing command onto the list endpoint —
// the only way a shell that did not start the export can find it.
func TestAccountExportsListsSlots(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/account/export": {Status: 200, Body: `{"data":[{"id":"e1","format":"enex","status":"completed","completed_at":1749744400000,"result_expires_at":1750003600000}]}`},
	})
	out, err := runCLI(t, m, "account", "exports")
	if err != nil {
		t.Fatalf("account exports: %v", err)
	}
	if !strings.Contains(out, "ENEX") || !strings.Contains(out, "completed") {
		t.Errorf("listing missing rows:\n%s", out)
	}
}

// TestAccountExportDeleteRequiresConfirmation is the safety criterion: without
// --yes, a non-interactive run must fail having sent NOTHING. Asserting on the
// recorded traffic is the only way to prove the archive survived.
func TestAccountExportDeleteRequiresConfirmation(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"DELETE /api/v1/account/export/e1": {Status: 200, Body: `{"data":{"id":"e1","status":"deleted"}}`},
	})
	if _, err := runCLI(t, m, "account", "export-delete", "e1"); err == nil {
		t.Fatal("expected a refusal without --yes")
	}
	if len(m.calls()) != 0 {
		t.Errorf("nothing should have been sent, got %v", m.calls())
	}
}

// TestAccountExportDelete deletes with --yes and confirms it hit the right verb
// and path.
func TestAccountExportDelete(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"DELETE /api/v1/account/export/e1": {Status: 200, Body: `{"data":{"id":"e1","status":"deleted"}}`},
	})
	out, err := runCLI(t, m, "account", "export-delete", "e1", "--yes")
	if err != nil {
		t.Fatalf("account export-delete: %v", err)
	}
	if got := m.calls(); len(got) != 1 || got[0] != "DELETE /api/v1/account/export/e1" {
		t.Errorf("calls = %v", got)
	}
	if !strings.Contains(out, "Export deleted") {
		t.Errorf("confirmation missing:\n%s", out)
	}
}

// TestAccountExportDeleteInProgress turns the 409 into the plain explanation
// that an export being built cannot be cancelled, only waited out.
func TestAccountExportDeleteInProgress(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"DELETE /api/v1/account/export/e1": {Status: 409, Body: apiErrorBody("export_in_progress", "still building")},
	})
	_, err := runCLI(t, m, "account", "export-delete", "e1", "--yes")
	if err == nil || !strings.Contains(err.Error(), "still being built") {
		t.Errorf("err = %v, want the in-progress explanation", err)
	}
}

// TestAccountExportWaitAndDownload is the end-to-end scripting path: start an
// export, poll it, and land the ZIP on disk from the signed URL with no browser
// involved.
func TestAccountExportWaitAndDownload(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/account/export": {Status: 202, Body: `{"data":{"export_job_id":"e1","status":"queued","format":"enex"}}`},
	})
	// The presigned URL has to point somewhere real, which is only knowable once
	// the stub is listening — so both routes are registered after it starts.
	m.routes["GET /dl/export.zip"] = mockReply{Status: 200, Body: "PK-zip-bytes"}
	m.routes["GET /api/v1/account/export/e1"] = mockReply{Status: 200, Body: fmt.Sprintf(
		`{"data":{"id":"e1","format":"enex","status":"completed","total_units":4,"done_units":4,`+
			`"filename":"harbor-export-2026-07-31.zip","download_url":%q,"completed_at":1749744400000,`+
			`"result_expires_at":1750003600000}}`, m.srv.URL+"/dl/export.zip")}

	dir := t.TempDir()
	out, err := runCLI(t, m, "account", "export", "--wait", "--download", dir, "--poll-interval", "1ms")
	if err != nil {
		t.Fatalf("account export --wait --download: %v", err)
	}

	// The directory target must have picked up the server's own filename.
	saved := filepath.Join(dir, "harbor-export-2026-07-31.zip")
	got, rerr := os.ReadFile(saved)
	if rerr != nil {
		t.Fatalf("archive not written to %s: %v", saved, rerr)
	}
	if string(got) != "PK-zip-bytes" {
		t.Errorf("archive contents = %q", got)
	}
	if !strings.Contains(out, "Wrote") {
		t.Errorf("download report missing:\n%s", out)
	}
}

// ===========================================================================
// Polling and download, against a stub that changes its answer
// ===========================================================================

// exportStub starts a server whose handler the test controls, returning a client
// pointed at it. The route-table mock always answers the same way, which cannot
// express "queued, then running, then completed" — or "the download 403s but the
// status endpoint has moved on".
func exportStub(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return client.NewClient(srv.URL+"/api/v1", "at_test_token")
}

// TestAccountPollExportWaitsForATerminalState proves --wait actually loops: it
// must keep polling through queued and running and stop only when the job is
// final.
func TestAccountPollExportWaitsForATerminalState(t *testing.T) {
	replies := []string{
		`{"data":{"id":"e1","status":"queued","queue_position":2}}`,
		`{"data":{"id":"e1","status":"running","total_units":10,"done_units":3}}`,
		`{"data":{"id":"e1","status":"completed","total_units":10,"done_units":10}}`,
	}
	calls := 0
	c := exportStub(t, func(w http.ResponseWriter, r *http.Request) {
		i := calls
		if i >= len(replies) {
			i = len(replies) - 1
		}
		calls++
		_, _ = w.Write([]byte(replies[i]))
	})

	data, err := accountPollExport(c, "e1", time.Millisecond, 0, true)
	if err != nil {
		t.Fatalf("accountPollExport: %v", err)
	}
	if calls != len(replies) {
		t.Errorf("polled %d times, want %d", calls, len(replies))
	}
	if status := str(parseJSON(client.UnwrapData(data)), "status"); status != "completed" {
		t.Errorf("final status = %q", status)
	}
}

// TestAccountPollExportWithoutWaitFetchesOnce keeps a plain status poll a single
// request even when the job is still running.
func TestAccountPollExportWithoutWaitFetchesOnce(t *testing.T) {
	calls := 0
	c := exportStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":{"id":"e1","status":"running","total_units":10,"done_units":1}}`))
	})
	if _, err := accountPollExport(c, "e1", time.Millisecond, 0, false); err != nil {
		t.Fatalf("accountPollExport: %v", err)
	}
	if calls != 1 {
		t.Errorf("polled %d times, want 1", calls)
	}
}

// TestAccountPollExportTimeout gives up rather than waiting forever, and says how
// to pick the export back up.
func TestAccountPollExportTimeout(t *testing.T) {
	c := exportStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"e1","status":"running","total_units":10,"done_units":1}}`))
	})
	_, err := accountPollExport(c, "e1", time.Millisecond, time.Millisecond, true)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "export-status e1") {
		t.Errorf("timeout should tell the user how to resume: %v", err)
	}
}

// TestAccountDownloadExportRechecksOnGoneURL is the contract's explicit
// instruction: a 403/404 from the presigned URL means the ARCHIVE may be gone,
// not that the download broke. Reporting "HTTP 403" would send someone hunting a
// network problem when another device simply deleted the export.
func TestAccountDownloadExportRechecksOnGoneURL(t *testing.T) {
	var base string
	c := exportStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl/export.zip" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
			return
		}
		// The status endpoint has moved on: the export was deleted elsewhere.
		_, _ = w.Write([]byte(`{"data":{"id":"e1","status":"deleted"}}`))
	})
	base = c.Origin()

	stale := fmt.Sprintf(`{"data":{"id":"e1","status":"completed","download_url":%q,"filename":"x.zip"}}`, base+"/dl/export.zip")
	err := accountDownloadExport(c, "e1", filepath.Join(t.TempDir(), "x.zip"), []byte(stale))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "was deleted") {
		t.Errorf("err = %v, want the re-checked status, not an HTTP code", err)
	}
	if strings.Contains(err.Error(), "403") {
		t.Errorf("a raw HTTP status must not reach the user: %v", err)
	}
}

// TestAccountDownloadExportNotReady refuses to download a job that is not
// completed, and says which state it is in.
func TestAccountDownloadExportNotReady(t *testing.T) {
	c := exportStub(t, func(w http.ResponseWriter, r *http.Request) {})
	err := accountDownloadExport(c, "e1", "out.zip", []byte(`{"data":{"id":"e1","status":"running"}}`))
	if err == nil || !strings.Contains(err.Error(), "still being built") {
		t.Errorf("err = %v", err)
	}
	if _, serr := os.Stat("out.zip"); serr == nil {
		t.Error("nothing should have been written")
	}
}

// TestDisplayDeletionScheduled surfaces the purge window and the cancel hint.
func TestDisplayDeletionScheduled(t *testing.T) {
	data := []byte(`{"data":{"status":"scheduled","purge_after":1752592000000,"grace_days":30,"can_cancel_until":1752592000000}}`)
	out := captureStdout(t, func() { displayDeletionScheduled(data) })
	if !strings.Contains(out, "scheduled") || !strings.Contains(out, "30") {
		t.Errorf("scheduled view missing fields:\n%s", out)
	}
	if !strings.Contains(out, "cancel-delete") {
		t.Errorf("cancel hint missing:\n%s", out)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// TestDisplayTemplates verifies the template list table renders names, the
// system marker, and the paging footer.
func TestDisplayTemplates(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"tpl1","name":"Meeting notes","is_system":false,"usn":12,"updated_at":1750000000000},
		{"id":"tpl2","name":"Daily standup","is_system":true,"usn":3,"updated_at":1750000000000}
	],"paging":{"limit":100,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayTemplates(data) })
	if !strings.Contains(out, "Meeting notes") || !strings.Contains(out, "Daily standup") {
		t.Errorf("missing template names:\n%s", out)
	}
	if !strings.Contains(out, "showing 1–2 of 2") {
		t.Errorf("paging footer missing:\n%s", out)
	}
}

// TestDisplayTemplateDetail verifies the detail view renders the id, name, and
// the stripped body content.
func TestDisplayTemplateDetail(t *testing.T) {
	data := []byte(`{"id":"tpl1","name":"Meeting notes","is_system":false,"usn":12,"updated_at":1750000000000,"created_at":1749000000000,"content":"<h1>Meeting</h1><p>Attendees:</p>"}`)
	out := captureStdout(t, func() { displayTemplate(data) })
	if !strings.Contains(out, "tpl1") || !strings.Contains(out, "Meeting notes") {
		t.Errorf("detail view missing fields:\n%s", out)
	}
	// HTML content should be stripped to plain text in the table view.
	if strings.Contains(out, "<h1>") {
		t.Errorf("expected HTML to be stripped:\n%s", out)
	}
	if !strings.Contains(out, "Attendees:") {
		t.Errorf("expected body text:\n%s", out)
	}
}

// TestDisplayTemplateApplyNote verifies that apply's {note, usn} envelope is
// rendered through the shared note display.
func TestDisplayTemplateApplyNote(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","title":"Standup","notebook_id":"nb1","is_encrypted":false,"word_count":2,"usn":88,"updated_at":1750000000000,"content":"<p>hi</p>"},"usn":88}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "n1") || !strings.Contains(out, "Standup") {
		t.Errorf("apply note display missing fields:\n%s", out)
	}
	if !strings.Contains(out, "New USN") || !strings.Contains(out, "88") {
		t.Errorf("expected new USN in apply output:\n%s", out)
	}
}

// TestMapTemplateError verifies friendly messages for template-specific codes.
func TestMapTemplateError(t *testing.T) {
	got := mapTemplateError(apiErr("system_template_readonly"))
	if !strings.Contains(got.Error(), "built-in") {
		t.Errorf("system_template_readonly = %q", got.Error())
	}

	// A bare validation_failed (no notebook_id detail) passes through unchanged.
	passthrough := apiErr("validation_failed")
	if got := mapTemplateError(passthrough); got != passthrough {
		t.Errorf("plain validation_failed should pass through, got %q", got.Error())
	}

	// A validation_failed carrying a notebook_id detail (encrypt-by-default
	// rejection) is surfaced with that explanation.
	encErr := &client.APIError{
		Code:    "validation_failed",
		Message: "validation failed",
		Status:  422,
		Details: map[string]any{"notebook_id": "is encrypted by default"},
	}
	if got := mapTemplateError(encErr); !strings.Contains(got.Error(), "encrypted by default") {
		t.Errorf("encrypt-by-default error = %q", got.Error())
	}
}

// ===========================================================================
// Default notebook and tags
// ===========================================================================

// The body an apply returns, as the server would send it: {{date}} already
// expanded, and a token the server does NOT know left exactly as typed. The CLI
// must reproduce both byte for byte — it does no expansion of its own, and a
// client-side expander would be a second expansion the contract forbids.
const (
	appliedBodyHTML   = "<h1>Standup Aug 25, 2026</h1><p>{{ Unknown_Token }} stays</p>"
	appliedTitleToken = "Aug 25, 2026"
	appliedNotice     = "That notebook is no longer available, so this note went to your default notebook."
)

// templatesMock stubs the create/update/apply routes so a test can drive the
// real command tree and read back the exact body it sent.
func templatesMock(t *testing.T) *apiMock {
	t.Helper()
	tpl := `{"id":"tpl1","name":"Meeting notes","notebook_id":"nb1","tag_ids":["t1"],"usn":13}`
	return newAPIMock(t, map[string]mockReply{
		"POST /api/v1/templates":       {Status: 201, Body: tpl},
		"PATCH /api/v1/templates/tpl1": {Status: 200, Body: tpl},
		"POST /api/v1/templates/tpl1/apply": {Status: 201, Body: `{"note":{"id":"n1","title":"Standup ` +
			appliedTitleToken + `","content":"` + appliedBodyHTML + `"},"usn":88,"notice":"` + appliedNotice + `"}`},
	})
}

// A create carries both new fields straight through to the API.
func TestTemplatesCreateSendsNotebookAndTags(t *testing.T) {
	m := templatesMock(t)
	if _, err := runCLI(t, m, "templates", "create", "--name", "Meeting notes",
		"--notebook", "nb1", "--tags", "t1,t2"); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	body := m.bodyOf(t, "POST /api/v1/templates")
	if body["notebook_id"] != "nb1" {
		t.Errorf("notebook_id = %v, want nb1", body["notebook_id"])
	}
	if got := toStringSlice(body["tag_ids"]); len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Errorf("tag_ids = %v, want [t1 t2]", body["tag_ids"])
	}
}

// The partial-update distinction is the whole point of these two flags: a user
// saving only a rename must not silently wipe the template's filing, and there
// must still be a way to empty it deliberately.
func TestTemplatesUpdateOmittedPreservesEmptyClears(t *testing.T) {
	t.Run("omitted preserves", func(t *testing.T) {
		m := templatesMock(t)
		if _, err := runCLI(t, m, "templates", "update", "tpl1", "--name", "Renamed"); err != nil {
			t.Fatalf("update failed: %v", err)
		}
		raw := m.rawBodyOf(t, "PATCH /api/v1/templates/tpl1")
		if strings.Contains(raw, "notebook_id") || strings.Contains(raw, "tag_ids") {
			t.Errorf("a rename must not mention the filing fields at all, got %s", raw)
		}
	})

	t.Run("empty clears", func(t *testing.T) {
		m := templatesMock(t)
		if _, err := runCLI(t, m, "templates", "update", "tpl1", "--notebook", "", "--tags", ""); err != nil {
			t.Fatalf("update failed: %v", err)
		}
		// Checked on the raw bytes: a decoded map cannot tell an explicit
		// empty list from a null, and the server reads those differently.
		raw := m.rawBodyOf(t, "PATCH /api/v1/templates/tpl1")
		if !strings.Contains(raw, `"notebook_id":""`) {
			t.Errorf(`want "notebook_id":"" in %s`, raw)
		}
		if !strings.Contains(raw, `"tag_ids":[]`) {
			t.Errorf(`want "tag_ids":[] (never null) in %s`, raw)
		}
	})

	t.Run("set to values", func(t *testing.T) {
		m := templatesMock(t)
		if _, err := runCLI(t, m, "templates", "update", "tpl1", "--notebook", "nb9", "--tags", "t7,t8"); err != nil {
			t.Fatalf("update failed: %v", err)
		}
		body := m.bodyOf(t, "PATCH /api/v1/templates/tpl1")
		if body["notebook_id"] != "nb9" {
			t.Errorf("notebook_id = %v", body["notebook_id"])
		}
		if got := toStringSlice(body["tag_ids"]); len(got) != 2 {
			t.Errorf("tag_ids = %v", body["tag_ids"])
		}
	})
}

// Apply follows "sent wins, omitted inherits": an omitted --tags must leave the
// key off entirely so the server hands the note the template's tags, while an
// empty one is an explicit "no tags".
func TestTemplatesApplyTagsSentWinsOmittedInherits(t *testing.T) {
	t.Run("omitted inherits", func(t *testing.T) {
		m := templatesMock(t)
		out, err := runCLI(t, m, "templates", "apply", "tpl1")
		if err != nil {
			t.Fatalf("apply failed: %v", err)
		}
		if raw := m.rawBodyOf(t, "POST /api/v1/templates/tpl1/apply"); strings.Contains(raw, "tags") {
			t.Errorf("omitted --tags must not send the key, got %s", raw)
		}
		// Driving the real command proves apply is still WIRED to the display
		// that prints the notice; testing the display function alone does not.
		if !strings.Contains(out, appliedNotice) {
			t.Errorf("apply did not print the server's notice:\n%s", out)
		}
		// The HUMAN path needs its own passthrough check: --json short-circuits
		// before the display function, so a display that rewrote tokens would
		// never be exercised by the --json test.
		if !strings.Contains(out, "{{ Unknown_Token }}") {
			t.Errorf("the rendered body must not touch an unexpanded token:\n%s", out)
		}
	})

	t.Run("notebook override is forwarded", func(t *testing.T) {
		m := templatesMock(t)
		if _, err := runCLI(t, m, "templates", "apply", "tpl1", "--notebook", "nb9"); err != nil {
			t.Fatalf("apply failed: %v", err)
		}
		if body := m.bodyOf(t, "POST /api/v1/templates/tpl1/apply"); body["notebook_id"] != "nb9" {
			t.Errorf("notebook_id = %v, want nb9", body["notebook_id"])
		}
	})

	t.Run("empty means none", func(t *testing.T) {
		m := templatesMock(t)
		if _, err := runCLI(t, m, "templates", "apply", "tpl1", "--tags", ""); err != nil {
			t.Fatalf("apply failed: %v", err)
		}
		if raw := m.rawBodyOf(t, "POST /api/v1/templates/tpl1/apply"); !strings.Contains(raw, `"tags":[]`) {
			t.Errorf(`want "tags":[] in %s`, raw)
		}
	})

	t.Run("a list is authoritative", func(t *testing.T) {
		m := templatesMock(t)
		if _, err := runCLI(t, m, "templates", "apply", "tpl1", "--tags", "t7,t8"); err != nil {
			t.Fatalf("apply failed: %v", err)
		}
		body := m.bodyOf(t, "POST /api/v1/templates/tpl1/apply")
		if got := toStringSlice(body["tags"]); len(got) != 2 || got[0] != "t7" {
			t.Errorf("tags = %v, want [t7 t8]", body["tags"])
		}
	})
}

// TestDisplayTemplatesShowsTagCount verifies the list table carries a tag COUNT
// — ids are too wide for a column and belong in the detail view.
//
// The count is read out of its own CELL, not searched for in the row. Every
// other cell can contain the digit by accident: the USNs are numbers, and the
// UPDATED column renders in the machine's LOCAL zone, so a timestamp that reads
// "2025-06-16 23:06" east of UTC satisfies a whole-row search for "3" while the
// count itself is wrong.
func TestDisplayTemplatesShowsTagCount(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"tpl1","name":"Meeting notes","is_system":false,"tag_ids":["t1","t2","t3"],"usn":1200,"updated_at":1750000000000},
		{"id":"tpl2","name":"Bare","is_system":false,"tag_ids":[],"usn":3400,"updated_at":1750000000000}
	],"paging":{"limit":100,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayTemplates(data) })
	if !strings.Contains(out, "TAGS") {
		t.Errorf("no TAGS column:\n%s", out)
	}
	if got := tableCell(t, out, "Meeting notes", 3); got != "3" {
		t.Errorf("TAGS cell = %q, want %q:\n%s", got, "3", out)
	}
	if got := tableCell(t, out, "Bare", 3); got != "—" {
		t.Errorf("an empty tag list should read as an em dash, got %q:\n%s", got, out)
	}
}

// tableCell returns the trimmed contents of the nth zero-indexed column on the
// rendered row containing marker.
//
// Assertions that search a whole row pass on any cell that happens to contain
// the value, which for a table carrying ids, counts, USNs and a local-time
// timestamp is most of them.
func tableCell(t *testing.T, out, marker string, n int) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		cells := strings.Split(strings.Trim(line, "│ "), "│")
		if n >= len(cells) {
			t.Fatalf("row %q has %d cells, wanted #%d", marker, len(cells), n)
		}
		return strings.TrimSpace(cells[n])
	}
	t.Fatalf("no row containing %q in:\n%s", marker, out)
	return ""
}

// The detail view shows the filing as ids, and says plainly when there is none.
func TestDisplayTemplateShowsNotebookAndTags(t *testing.T) {
	set := []byte(`{"id":"tpl1","name":"Meeting notes","notebook_id":"nb1","tag_ids":["t1","t2"],"usn":12,"updated_at":1750000000000,"created_at":1749000000000,"content":"hi"}`)
	out := captureStdout(t, func() { displayTemplate(set) })
	if !strings.Contains(out, "Notebook") || !strings.Contains(out, "nb1") {
		t.Errorf("notebook missing:\n%s", out)
	}
	if !strings.Contains(out, "t1, t2") {
		t.Errorf("tag ids missing:\n%s", out)
	}

	none := []byte(`{"id":"tpl1","name":"Bare","notebook_id":"","tag_ids":[],"usn":12,"updated_at":1750000000000,"created_at":1749000000000}`)
	out = captureStdout(t, func() { displayTemplate(none) })
	if strings.Count(out, "—") < 2 {
		t.Errorf("an unset notebook and empty tags should both read as em dashes:\n%s", out)
	}
}

// Apply's notice explains a silent substitution — the template's notebook was
// gone or encrypted, so the note was filed elsewhere. It is printed verbatim,
// and an empty one prints nothing at all.
func TestDisplayAppliedNotePrintsTheNotice(t *testing.T) {
	notice := "That template's notebook is no longer available, so this note went to your default notebook."
	data := []byte(`{"note":{"id":"n1","title":"Standup","usn":88,"updated_at":1750000000000},"usn":88,"notice":` +
		`"That template's notebook is no longer available, so this note went to your default notebook."}`)
	out := captureStdout(t, func() { displayAppliedNote(data) })
	if !strings.Contains(out, notice) {
		t.Errorf("notice not printed verbatim:\n%s", out)
	}

	// An empty notice must add NOTHING — compared against the plain note render
	// rather than probed for a word, so any spurious advisory line fails.
	quiet := []byte(`{"note":{"id":"n1","title":"Standup","usn":88,"updated_at":1750000000000},"usn":88,"notice":""}`)
	withEmpty := captureStdout(t, func() { displayAppliedNote(quiet) })
	bare := captureStdout(t, func() { displayNote(quiet) })
	if withEmpty != bare {
		t.Errorf("an empty notice changed the output:\n got  %q\n want %q", withEmpty, bare)
	}
}

// A rejected tag list gets the same treatment as a rejected notebook, and the
// wording stays neutral because the same codes arrive from create, update and
// apply alike.
func TestMapTemplateErrorSurfacesTagDetails(t *testing.T) {
	// Both spellings must land: create and update validate the template's own
	// list and report "tag_ids", while apply validates the list sent for the new
	// note and reports "tags". Missing the apply spelling would leave the one
	// path where a stale tag id actually bites with the least helpful message.
	for _, key := range []string{"tag_ids", "tags"} {
		tagErr := &client.APIError{
			Code:    "validation_failed",
			Message: "validation failed",
			Status:  422,
			Details: map[string]any{key: "tag t9 does not exist"},
		}
		got := mapTemplateError(tagErr)
		if !strings.Contains(got.Error(), "t9 does not exist") {
			t.Errorf("details.%s not surfaced: %q", key, got.Error())
		}
		// The friendlier prose must not cost the diagnostics: the code line,
		// the detail bullets and --verbose's http/request_id all come off the
		// typed value, and --json reports its code rather than cli_error.
		var typed *client.APIError
		if !errors.As(got, &typed) {
			t.Fatalf("details.%s mapping destroyed the *client.APIError (%T)", key, got)
		}
		if typed.Code != "validation_failed" || typed.Status != 422 {
			t.Errorf("diagnostics lost: code=%q status=%d", typed.Code, typed.Status)
		}
		if len(typed.DetailLines()) != 1 {
			t.Errorf("detail bullets lost: %v", typed.DetailLines())
		}
		if tagErr.Message != "validation failed" {
			t.Errorf("the caller's error was mutated: %q", tagErr.Message)
		}
	}

	nbErr := &client.APIError{
		Code:    "validation_failed",
		Status:  422,
		Details: map[string]any{"notebook_id": "is encrypted by default"},
	}
	// "apply" would be wrong here: the same error arrives from create and
	// update, which are not applying anything.
	if got := mapTemplateError(nbErr); strings.Contains(got.Error(), "apply") {
		t.Errorf("wording must not assume the operation: %q", got.Error())
	}
}

// The server expands a template's {{…}} variables at apply time, so the CLI's
// only job is to not touch what comes back. This pins that: an expanded date
// survives, and a token the server left alone survives EXACTLY as typed —
// casing, inner spacing and braces intact.
//
// Without it, a client-side expander could be added and nothing would fail,
// which is the double expansion the cross-client contract forbids.
func TestTemplatesApplyPassesTheExpandedBodyThrough(t *testing.T) {
	m := templatesMock(t)
	out, err := runCLI(t, m, "templates", "apply", "tpl1", "--json")
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	var got struct {
		Note struct {
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply --json is not valid JSON: %v\n%s", err, out)
	}
	if got.Note.Content != appliedBodyHTML {
		t.Errorf("body was altered in flight:\n got  %q\n want %q", got.Note.Content, appliedBodyHTML)
	}
	if !strings.Contains(got.Note.Title, appliedTitleToken) {
		t.Errorf("expanded title lost: %q", got.Note.Title)
	}
	if !strings.Contains(got.Note.Content, "{{ Unknown_Token }}") {
		t.Errorf("an unexpanded token must survive verbatim, got %q", got.Note.Content)
	}

	// The request carries no expansion hint of any kind — expansion is the
	// server's, and the CLI never asks for it or does it.
	if raw := m.rawBodyOf(t, "POST /api/v1/templates/tpl1/apply"); strings.Contains(raw, "{{") {
		t.Errorf("the CLI must not send template tokens back: %s", raw)
	}
}

// TestDisplayAppliedNoteReadsTheNoticeThroughTheEnvelope verifies the notice is
// read through the same unwrapping the note display uses.
//
// Apply answers bare today, so this is defensive: the two halves of the same
// render must not disagree about the envelope, or a server that starts wrapping
// would keep printing the note while silently dropping the line that explains
// where it went.
func TestDisplayAppliedNoteReadsTheNoticeThroughTheEnvelope(t *testing.T) {
	for _, shape := range []string{
		`{"note":{"id":"n1","title":"S","usn":88,"updated_at":1750000000000},"usn":88,"notice":%q}`,
		`{"data":{"note":{"id":"n1","title":"S","usn":88,"updated_at":1750000000000},"usn":88,"notice":%q}}`,
	} {
		data := []byte(strings.Replace(shape, "%q", `"`+appliedNotice+`"`, 1))
		out := captureStdout(t, func() { displayAppliedNote(data) })
		if !strings.Contains(out, appliedNotice) {
			t.Errorf("notice lost for shape %s:\n%s", shape, out)
		}
	}
}

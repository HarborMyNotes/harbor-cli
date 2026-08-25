// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
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

// templatesMock stubs the create/update/apply routes so a test can drive the
// real command tree and read back the exact body it sent.
func templatesMock(t *testing.T) *apiMock {
	t.Helper()
	tpl := `{"id":"tpl1","name":"Meeting notes","notebook_id":"nb1","tag_ids":["t1"],"usn":13}`
	return newAPIMock(t, map[string]mockReply{
		"POST /api/v1/templates":            {Status: 201, Body: tpl},
		"PATCH /api/v1/templates/tpl1":      {Status: 200, Body: tpl},
		"POST /api/v1/templates/tpl1/apply": {Status: 201, Body: `{"note":{"id":"n1","title":"Standup"},"usn":88,"notice":""}`},
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
		if _, err := runCLI(t, m, "templates", "apply", "tpl1"); err != nil {
			t.Fatalf("apply failed: %v", err)
		}
		if raw := m.rawBodyOf(t, "POST /api/v1/templates/tpl1/apply"); strings.Contains(raw, "tags") {
			t.Errorf("omitted --tags must not send the key, got %s", raw)
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

// The list table carries a tag COUNT; ids are too wide for a column and belong
// in the detail view.
func TestDisplayTemplatesShowsTagCount(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"tpl1","name":"Meeting notes","is_system":false,"tag_ids":["t1","t2","t3"],"usn":12,"updated_at":1750000000000},
		{"id":"tpl2","name":"Bare","is_system":false,"tag_ids":[],"usn":3,"updated_at":1750000000000}
	],"paging":{"limit":100,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayTemplates(data) })
	if !strings.Contains(out, "TAGS") {
		t.Errorf("no TAGS column:\n%s", out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("tag count missing:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("an empty tag list should read as an em dash, not a blank:\n%s", out)
	}
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

	quiet := []byte(`{"note":{"id":"n1","title":"Standup","usn":88,"updated_at":1750000000000},"usn":88,"notice":""}`)
	out = captureStdout(t, func() { displayAppliedNote(quiet) })
	if strings.Contains(out, "notebook") {
		t.Errorf("an empty notice should print nothing:\n%s", out)
	}
}

// A rejected tag list gets the same treatment as a rejected notebook, and the
// wording stays neutral because the same codes arrive from create, update and
// apply alike.
func TestMapTemplateErrorSurfacesTagDetails(t *testing.T) {
	tagErr := &client.APIError{
		Code:    "validation_failed",
		Message: "validation failed",
		Status:  422,
		Details: map[string]any{"tag_ids": "tag t9 does not exist"},
	}
	got := mapTemplateError(tagErr)
	if !strings.Contains(got.Error(), "t9 does not exist") {
		t.Errorf("tag detail not surfaced: %q", got.Error())
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

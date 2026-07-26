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

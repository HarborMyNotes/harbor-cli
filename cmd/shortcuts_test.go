// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// TestDisplayShortcuts checks the list table shows labels, the saved query for a
// search shortcut, the target id for a record shortcut, and the paging footer.
func TestDisplayShortcuts(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"s1","type":"note","target_id":"9c2e7b10abcd","saved_query":"","label":"Quarterly plan","position":100,"usn":5,"updated_at":1750000000000},
		{"id":"s2","type":"search","target_id":"","saved_query":"tag:receipts year:2026","label":"Receipts","position":200,"usn":6,"updated_at":1750000000000}
	],"paging":{"limit":100,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayShortcuts(data) })
	if !strings.Contains(out, "Quarterly plan") || !strings.Contains(out, "Receipts") {
		t.Errorf("missing labels:\n%s", out)
	}
	if !strings.Contains(out, "tag:receipts") {
		t.Errorf("search query not shown:\n%s", out)
	}
	if !strings.Contains(out, "9c2e7b10") {
		t.Errorf("record target id not shown:\n%s", out)
	}
	if !strings.Contains(out, "showing 1–2 of 2") {
		t.Errorf("paging footer missing:\n%s", out)
	}
}

// TestDisplayShortcutDetail checks the bare single-object detail view.
func TestDisplayShortcutDetail(t *testing.T) {
	data := []byte(`{"id":"s1","type":"note","target_id":"9c2e","saved_query":"","label":"Plan","position":100,"usn":5,"updated_at":1750000000000,"created_at":1749000000000}`)
	out := captureStdout(t, func() { displayShortcut(data) })
	if !strings.Contains(out, "s1") || !strings.Contains(out, "Plan") || !strings.Contains(out, "note") {
		t.Errorf("detail view missing fields:\n%s", out)
	}
}

// TestShortcutValidateTypeFields exercises the client-side type↔field rules for
// both valid and invalid combinations.
func TestShortcutValidateTypeFields(t *testing.T) {
	// Valid combinations should return no error.
	valid := []struct {
		typ, targetID, query string
	}{
		{"note", "n1", ""},
		{"notebook", "nb1", ""},
		{"tag", "t1", ""},
		{"search", "", "tag:receipts"},
	}
	for _, v := range valid {
		if err := shortcutValidateTypeFields(v.typ, v.targetID, v.query); err != nil {
			t.Errorf("shortcutValidateTypeFields(%q,%q,%q) unexpected error: %v", v.typ, v.targetID, v.query, err)
		}
	}

	// Invalid combinations should each return an error mentioning the offending flag.
	invalid := []struct {
		name                 string
		typ, targetID, query string
		wantSub              string
	}{
		{"record missing target", "note", "", "", "--target-id is required"},
		{"record with query", "note", "n1", "q", "--query is not valid"},
		{"search missing query", "search", "", "", "--query is required"},
		{"search with target", "search", "t1", "q", "--target-id is not valid"},
		{"empty type", "", "", "", "--type is required"},
		{"bad type", "bogus", "x", "", "invalid --type"},
	}
	for _, c := range invalid {
		err := shortcutValidateTypeFields(c.typ, c.targetID, c.query)
		if err == nil {
			t.Errorf("%s: expected an error, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("%s: error = %q, want substring %q", c.name, err.Error(), c.wantSub)
		}
	}
}

// TestMapShortcutError checks the conflict mapping and the validation detail
// passthroughs (reorder order errors and bad target ids).
func TestMapShortcutError(t *testing.T) {
	if got := mapShortcutError(apiErr("conflict")); !strings.Contains(got.Error(), "already exists") {
		t.Errorf("conflict mapping = %q", got.Error())
	}

	orderErr := &client.APIError{Code: "validation_failed", Message: "validation_failed", Status: 422, Details: map[string]any{"order": "duplicate id s1"}}
	if got := mapShortcutError(orderErr); !strings.Contains(got.Error(), "reorder rejected") || !strings.Contains(got.Error(), "duplicate id s1") {
		t.Errorf("order detail mapping = %q", got.Error())
	}

	targetErr := &client.APIError{Code: "validation_failed", Message: "validation_failed", Status: 422, Details: map[string]any{"target_id": "is not a live note"}}
	if got := mapShortcutError(targetErr); !strings.Contains(got.Error(), "invalid target") || !strings.Contains(got.Error(), "live note") {
		t.Errorf("target detail mapping = %q", got.Error())
	}

	// A validation_failed with no recognized detail keys falls through unchanged.
	plain := apiErr("validation_failed")
	if got := mapShortcutError(plain); got != plain {
		t.Errorf("plain validation_failed should pass through, got %q", got.Error())
	}
}

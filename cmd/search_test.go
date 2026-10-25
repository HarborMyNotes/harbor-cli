// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
)

func TestDisplaySearchMixedHits(t *testing.T) {
	data := []byte(`{"data":[
		{"type":"note","note_id":"9c2e","title":"Quarterly plan","snippet":"the <em>budget</em> for Q3","score":8.42},
		{"type":"attachment","resource_id":"0f9c","note_id":"9c2e","filename":"invoice.pdf","snippet":"total <em>budget</em>","score":3.07,"has_coordinates":true}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displaySearch(data) })
	if !strings.Contains(out, "Quarterly plan") || !strings.Contains(out, "invoice.pdf") {
		t.Errorf("hits missing:\n%s", out)
	}
	// Snippet <em> tags should be stripped for the table.
	if strings.Contains(out, "<em>") {
		t.Errorf("snippet HTML not stripped:\n%s", out)
	}
	if !strings.Contains(out, "budget") {
		t.Errorf("snippet text missing:\n%s", out)
	}
}

// searchRowCells returns the table cells of the first row that contains want,
// with the border and padding trimmed, so a test can check one column.
func searchRowCells(t *testing.T, out, want string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, want) {
			continue
		}
		var cells []string
		for _, c := range strings.Split(strings.Trim(strings.TrimSpace(line), "│|"), "│") {
			cells = append(cells, strings.TrimSpace(c))
		}
		if len(cells) != 6 {
			t.Fatalf("row for %q should have 6 cells, got %d: %q", want, len(cells), cells)
		}
		return cells
	}
	t.Fatalf("no row contains %q:\n%s", want, out)
	return nil
}

// TestDisplaySearchNameHit checks a file found by its name prints cleanly: a
// dash for its empty snippet, score 0, and no highlight mark even when the file
// has OCR word boxes from text that did not match the query.
func TestDisplaySearchNameHit(t *testing.T) {
	data := []byte(`{"data":[
		{"type":"attachment","resource_id":"r1","note_id":"n1","note_title":"Billing","filename":"Hours.xlsx","mime":"application/vnd.ms-excel","snippet":"","highlights":[],"score":0,"has_coordinates":true,"match":"name"},
		{"type":"attachment","resource_id":"r2","note_id":"n1","note_title":"Billing","filename":"scan.png","mime":"image/png","snippet":"Hours for <em>xlsx</em>","highlights":["xlsx"],"score":2.5,"has_coordinates":true,"match":"text"}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displaySearch(data) })

	name := searchRowCells(t, out, "Hours.xlsx")
	if name[0] != "attachment" || name[1] != "r1" {
		t.Errorf("name hit type/id = %q/%q", name[0], name[1])
	}
	if name[3] != "—" {
		t.Errorf("empty snippet should show a dash, got %q", name[3])
	}
	if name[4] != "0.00" {
		t.Errorf("name hit score = %q, want 0.00", name[4])
	}
	if name[5] != "—" {
		t.Errorf("name hit should not offer highlights, COORD = %q", name[5])
	}

	text := searchRowCells(t, out, "scan.png")
	if text[3] != "Hours for xlsx" {
		t.Errorf("text hit snippet = %q", text[3])
	}
	if text[5] != "✓" {
		t.Errorf("text hit with OCR boxes should show ✓, got %q", text[5])
	}
}

// TestDisplaySearchHitWithoutMatchField checks an attachment hit from an older
// server, which sends no match field, is shown as a text hit.
func TestDisplaySearchHitWithoutMatchField(t *testing.T) {
	data := []byte(`{"data":[
		{"type":"attachment","resource_id":"r1","filename":"invoice.pdf","snippet":"total","score":1.5,"has_coordinates":true}
	],"paging":{"offset":0,"total":1}}`)
	out := captureStdout(t, func() { displaySearch(data) })
	if row := searchRowCells(t, out, "invoice.pdf"); row[5] != "✓" {
		t.Errorf("hit with no match field should keep ✓, got %q", row[5])
	}
}

// TestIsNameMatch checks only match "name" counts as a file-name hit.
func TestIsNameMatch(t *testing.T) {
	cases := []struct {
		hit  map[string]any
		want bool
	}{
		{map[string]any{"match": "name"}, true},
		{map[string]any{"match": "text"}, false},
		{map[string]any{}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isNameMatch(c.hit); got != c.want {
			t.Errorf("isNameMatch(%v) = %v, want %v", c.hit, got, c.want)
		}
	}
}

// TestDisplaySearchKeepsExtension checks a long file name keeps its extension
// in the table, since that is often what a name search matched.
func TestDisplaySearchKeepsExtension(t *testing.T) {
	data := []byte(`{"data":[
		{"type":"attachment","resource_id":"r1","filename":"Contractor Hours September 2026.xlsx","snippet":"","score":0,"has_coordinates":false,"match":"name"}
	],"paging":{"offset":0,"total":1}}`)
	out := captureStdout(t, func() { displaySearch(data) })
	if row := searchRowCells(t, out, "Contractor"); row[2] != "Contractor Hours Septe….xlsx" {
		t.Errorf("long file name = %q, want the extension kept", row[2])
	}
}

// TestTruncateFilename checks the extension survives a cut, and that names it
// cannot help fall back to a plain cut.
func TestTruncateFilename(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want string
	}{
		{"short.pdf", 28, "short.pdf"},
		{"Contractor Hours September 2026.xlsx", 28, "Contractor Hours Septe….xlsx"},
		{"Übersicht der Rechnungen für das Jahr.pdf", 20, "Übersicht der R….pdf"},
		{"a name with no extension at all here", 20, "a name with no exte…"},
		{"Dr. Smith and the very long visit notes", 20, "Dr. Smith and the v…"},
		{"x.abcdefghijklmnopqrstuvwxyz0123456789", 20, "x.abcdefghijklmnopq…"},
		{"line\nbreak in a long file name.txt", 20, "line break in a….txt"},
	}
	for _, c := range cases {
		got := truncateFilename(c.name, c.n)
		if got != c.want {
			t.Errorf("truncateFilename(%q, %d) = %q, want %q", c.name, c.n, got, c.want)
		}
		if l := len([]rune(got)); l > c.n {
			t.Errorf("truncateFilename(%q, %d) is %d characters long", c.name, c.n, l)
		}
	}
}

// TestSearchHelpMentionsFileNames checks the help says search matches
// attachment file names.
func TestSearchHelpMentionsFileNames(t *testing.T) {
	if !strings.Contains(searchCmd.Short, "file names") {
		t.Errorf("Short should say search matches file names: %q", searchCmd.Short)
	}
	if !strings.Contains(searchCmd.Long, "attachment file names") || !strings.Contains(searchCmd.Long, "harbor search xlsx") {
		t.Errorf("Long should explain file-name matching:\n%s", searchCmd.Long)
	}
}

func TestDisplayCoordinates(t *testing.T) {
	data := []byte(`{"data":{"resource_id":"0f9c","mime":"application/pdf","page_count":3,"terms":["budget"],"truncated":false,"pages":[
		{"page":0,"page_width":1700,"page_height":2200,"matches":[{"term":"budget","word":"Budget","word_index":42,"box":{"x":510,"y":880,"w":120,"h":28},"confidence":0.98}]}
	]}}`)
	out := captureStdout(t, func() { displayCoordinates(data) })
	if !strings.Contains(out, "Budget") || !strings.Contains(out, "0f9c") {
		t.Errorf("coordinates missing fields:\n%s", out)
	}
}

func TestMapSearchError(t *testing.T) {
	if got := mapSearchError(apiErr("encrypted_not_searchable")); !strings.Contains(got.Error(), "never searchable") {
		t.Errorf("encrypted_not_searchable = %q", got.Error())
	}
	if got := mapSearchError(apiErr("ocr_not_ready")); !strings.Contains(got.Error(), "OCR") {
		t.Errorf("ocr_not_ready = %q", got.Error())
	}
}

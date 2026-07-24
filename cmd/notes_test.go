// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
)

func TestExtractNote(t *testing.T) {
	// Mutation envelope.
	n, usn := extractNote([]byte(`{"note":{"id":"n1","title":"T"},"usn":88}`))
	if str(n, "id") != "n1" || usn != "88" {
		t.Errorf("mutation extract: id=%q usn=%q", str(n, "id"), usn)
	}
	// Bare note.
	n2, usn2 := extractNote([]byte(`{"id":"n2","title":"T2"}`))
	if str(n2, "id") != "n2" || usn2 != "" {
		t.Errorf("bare extract: id=%q usn=%q", str(n2, "id"), usn2)
	}
}

func TestDisplayNoteRendersBodyAndUSN(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","title":"Plan","notebook_id":"nb1","is_encrypted":false,"word_count":3,"usn":88,"content":"<p>Hello <strong>world</strong></p>","updated_at":1750000000000},"usn":88}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "Plan") {
		t.Errorf("title missing:\n%s", out)
	}
	if !strings.Contains(out, "New USN") {
		t.Errorf("new USN missing:\n%s", out)
	}
	// HTML body should be stripped to readable text.
	if !strings.Contains(out, "Hello world") {
		t.Errorf("body not rendered:\n%s", out)
	}
}

func TestDisplayNoteSourceAndAuthor(t *testing.T) {
	// A clipped note carries source_url and author: both should print.
	clipped := []byte(`{"id":"n1","title":"Clip","source_url":"https://example.com/clip","author":"Jane Doe","content":"body"}`)
	out := captureStdout(t, func() { displayNote(clipped) })
	if !strings.Contains(out, "Source") || !strings.Contains(out, "https://example.com/clip") {
		t.Errorf("source line missing:\n%s", out)
	}
	if !strings.Contains(out, "Author") || !strings.Contains(out, "Jane Doe") {
		t.Errorf("author line missing:\n%s", out)
	}
	// A plain note has neither field: nothing extra should appear.
	plain := []byte(`{"id":"n2","title":"Plain","content":"body"}`)
	out = captureStdout(t, func() { displayNote(plain) })
	if strings.Contains(out, "Source") || strings.Contains(out, "Author") {
		t.Errorf("unclipped note should not print Source/Author:\n%s", out)
	}
}

func TestDisplayNoteEncrypted(t *testing.T) {
	data := []byte(`{"id":"n1","title":"sealed","is_encrypted":true,"content":"AAAA"}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "[encrypted]") {
		t.Errorf("encrypted body should be hidden:\n%s", out)
	}
	if strings.Contains(out, "AAAA") {
		t.Errorf("ciphertext should not be printed:\n%s", out)
	}
}

func TestDisplayNotesTable(t *testing.T) {
	data := []byte(`{"data":[{"id":"n1","title":"Plan","notebook_id":"nbxxxxxxxx","is_encrypted":true,"word_count":3,"usn":88,"updated_at":1750000000000}],"paging":{"offset":0,"total":1}}`)
	out := captureStdout(t, func() { displayNotes(data) })
	if !strings.Contains(out, "Plan") || !strings.Contains(out, "🔒") {
		t.Errorf("notes table missing fields:\n%s", out)
	}
}

func TestMapNoteError(t *testing.T) {
	cases := map[string]string{
		"note_title_too_long":            "title is too long",
		"note_too_large":                 "too large",
		"append_not_supported_encrypted": "encrypted",
	}
	for code, sub := range cases {
		if got := mapNoteError(apiErr(code)); !strings.Contains(got.Error(), sub) {
			t.Errorf("mapNoteError(%s) = %q", code, got.Error())
		}
	}
}

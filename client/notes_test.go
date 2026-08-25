// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListNotes(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).ListNotes(map[string]string{"notebook_id": "nb1", "fields": "meta"})
	if err != nil {
		t.Fatalf("ListNotes error: %v", err)
	}
	if rec.Path != "/notes" {
		t.Errorf("path = %s", rec.Path)
	}
	if !containsAll(rec.Query, "notebook_id=nb1", "fields=meta") {
		t.Errorf("query = %q", rec.Query)
	}
}

func TestGetNoteWithFormat(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"n1"}`)
	defer srv.Close()
	_, err := testClient(srv.URL).GetNote("n1", map[string]string{"format": "markdown", "deleted": "true"})
	if err != nil {
		t.Fatalf("GetNote error: %v", err)
	}
	if rec.Path != "/notes/n1" {
		t.Errorf("path = %s", rec.Path)
	}
	if !containsAll(rec.Query, "format=markdown", "deleted=true") {
		t.Errorf("query = %q", rec.Query)
	}
}

func TestCreateNoteSendsContentFormat(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"note":{"id":"n1"},"usn":5}`)
	defer srv.Close()
	_, err := testClient(srv.URL).CreateNote(map[string]any{"title": "T", "content": "# Hi", "content_format": "markdown"})
	if err != nil {
		t.Fatalf("CreateNote error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/notes" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["content_format"] != "markdown" {
		t.Errorf("content_format = %v", body["content_format"])
	}
}

func TestAppendNote(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"note":{"id":"n1"},"usn":6}`)
	defer srv.Close()
	_, err := testClient(srv.URL).AppendNote("n1", map[string]any{"content": "x"})
	if err != nil {
		t.Fatalf("AppendNote error: %v", err)
	}
	if rec.Path != "/notes/n1/append" {
		t.Errorf("path = %s", rec.Path)
	}
}

func TestDeleteNotePermanent(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 204, ``)
	defer srv.Close()
	_, err := testClient(srv.URL).DeleteNote("n1", true)
	if err != nil {
		t.Fatalf("DeleteNote error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Query != "permanent=true" {
		t.Errorf("%s query=%s", rec.Method, rec.Query)
	}
}

// TestConvertNoteToEncrypted pins the wire shape of the encrypt conversion: a
// PATCH to the note carrying the envelopes and the marker, plus the base_usn
// precondition that keeps a concurrent edit from being overwritten.
func TestConvertNoteToEncrypted(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"note":{"id":"n1","is_encrypted":true},"usn":9}`)
	defer srv.Close()
	_, err := testClient(srv.URL).ConvertNoteToEncrypted("n1", map[string]any{
		"is_encrypted": true,
		"title":        "HRBC2.aaa.bbb",
		"content":      "HRBC2.ccc.ddd",
		"base_usn":     8,
	})
	if err != nil {
		t.Fatalf("ConvertNoteToEncrypted error: %v", err)
	}
	if rec.Method != "PATCH" || rec.Path != "/notes/n1" {
		t.Errorf("%s %s, want PATCH /notes/n1", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["is_encrypted"] != true {
		t.Errorf("is_encrypted = %v, want true", body["is_encrypted"])
	}
	if body["content"] != "HRBC2.ccc.ddd" || body["title"] != "HRBC2.aaa.bbb" {
		t.Errorf("envelopes not sent verbatim: %v / %v", body["title"], body["content"])
	}
	if body["base_usn"] != float64(8) {
		t.Errorf("base_usn = %v, want 8 — without it the write can clobber a concurrent edit", body["base_usn"])
	}
}

// TestConvertNoteToPlaintext pins the other direction, including the
// content_format the server needs to interpret the body it is handed back.
func TestConvertNoteToPlaintext(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"note":{"id":"n1","is_encrypted":false},"usn":9}`)
	defer srv.Close()
	_, err := testClient(srv.URL).ConvertNoteToPlaintext("n1", map[string]any{
		"is_encrypted":   false,
		"title":          "Quarterly plan",
		"content":        "<p>hello</p>",
		"content_format": "html",
	})
	if err != nil {
		t.Fatalf("ConvertNoteToPlaintext error: %v", err)
	}
	if rec.Method != "PATCH" || rec.Path != "/notes/n1" {
		t.Errorf("%s %s, want PATCH /notes/n1", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["is_encrypted"] != false {
		t.Errorf("is_encrypted = %v, want false", body["is_encrypted"])
	}
	if body["content_format"] != "html" {
		t.Errorf("content_format = %v, want html", body["content_format"])
	}
}

// TestExportNoteMarkdown pins the request the per-note export makes: the
// endpoint carries the format in its own path, and zip is a query flag rather
// than a second endpoint.
func TestExportNoteMarkdown(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, "---\ntitle: \"Plan\"\n---\n\n# Plan\n")
	defer srv.Close()

	resp, err := testClient(srv.URL).ExportNoteMarkdown("n1", false)
	if err != nil {
		t.Fatalf("ExportNoteMarkdown error: %v", err)
	}
	defer resp.Body.Close()

	if rec.Method != "GET" || rec.Path != "/notes/n1/export.md" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if rec.Query != "" {
		t.Errorf("query = %q, want none — zip must not be sent unless asked for", rec.Query)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "# Plan") {
		t.Errorf("raw body = %q", raw)
	}
}

// TestExportNoteMarkdownZip pins the archive form, and that the caller can read
// the headers that say which of the two shapes came back.
func TestExportNoteMarkdownZip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("zip"); got != "1" {
			t.Errorf("zip = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="Plan.zip"`)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("PK\x03\x04"))
	}))
	defer srv.Close()

	resp, err := testClient(srv.URL).ExportNoteMarkdown("n1", true)
	if err != nil {
		t.Fatalf("ExportNoteMarkdown error: %v", err)
	}
	defer resp.Body.Close()

	// The header has to be readable BEFORE the body is drained — it is the only
	// thing that says whether this is a .md or a .zip.
	if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename="Plan.zip"` {
		t.Errorf("Content-Disposition = %q", got)
	}
}

// TestExportNoteMarkdownEncrypted keeps the refusal a typed API error rather
// than a body the caller has to sniff.
func TestExportNoteMarkdownEncrypted(t *testing.T) {
	srv := newTestServer(t, nil, 422, `{"error":{"code":"encrypted_not_exportable","message":"nope"}}`)
	defer srv.Close()

	resp, err := testClient(srv.URL).ExportNoteMarkdown("n1", false)
	if err == nil {
		resp.Body.Close()
		t.Fatal("an encrypted note exported without complaint")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "encrypted_not_exportable" {
		t.Errorf("err = %v, want an APIError with code encrypted_not_exportable", err)
	}
}

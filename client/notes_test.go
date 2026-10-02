// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestExportNotePDFAndHTML pins the two newer per-note exports: each is a plain
// GET on its own path with no query, and the headers the caller reads come back
// intact on the live response.
func TestExportNotePDFAndHTML(t *testing.T) {
	cases := []struct {
		name string
		call func(c *Client) (*http.Response, error)
		path string
	}{
		{"pdf", func(c *Client) (*http.Response, error) { return c.ExportNotePDF("n1") }, "/notes/n1/export.pdf"},
		{"html", func(c *Client) (*http.Response, error) { return c.ExportNoteHTML("n1") }, "/notes/n1/export.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recordedRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.Method, rec.Path, rec.Query = r.Method, r.URL.Path, r.URL.RawQuery
				w.Header().Set("Content-Disposition", `attachment; filename="Plan"`)
				w.Header().Set("X-Skipped-Attachments", "2")
				w.WriteHeader(200)
				_, _ = w.Write([]byte("bytes"))
			}))
			defer srv.Close()

			resp, err := tc.call(testClient(srv.URL))
			if err != nil {
				t.Fatalf("export error: %v", err)
			}
			defer resp.Body.Close()

			if rec.Method != "GET" || rec.Path != tc.path || rec.Query != "" {
				t.Errorf("request = %s %s?%s, want GET %s", rec.Method, rec.Path, rec.Query, tc.path)
			}
			if got := resp.Header.Get("X-Skipped-Attachments"); got != "2" {
				t.Errorf("X-Skipped-Attachments = %q, want it readable off the response", got)
			}
			raw, _ := io.ReadAll(resp.Body)
			if string(raw) != "bytes" {
				t.Errorf("body = %q", raw)
			}
		})
	}
}

// TestExportNotePDFAndHTMLEncrypted keeps the encrypted refusal a typed API
// error for both, so the command can turn it into one sentence.
func TestExportNotePDFAndHTMLEncrypted(t *testing.T) {
	srv := newTestServer(t, nil, 422, `{"error":{"code":"encrypted_not_exportable","message":"nope"}}`)
	defer srv.Close()
	c := testClient(srv.URL)

	for name, call := range map[string]func() (*http.Response, error){
		"pdf":  func() (*http.Response, error) { return c.ExportNotePDF("n1") },
		"html": func() (*http.Response, error) { return c.ExportNoteHTML("n1") },
	} {
		resp, err := call()
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: an encrypted note exported without complaint", name)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "encrypted_not_exportable" {
			t.Errorf("%s: err = %v, want an APIError with code encrypted_not_exportable", name, err)
		}
	}
}

// TestListNotesQuery covers the caller-built query form, which carries a
// repeated meta_has and a meta.KEY filter to the notes route.
func TestListNotesQuery(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"total":0}}`)
	defer srv.Close()
	q := url.Values{"meta.gallery": {"true"}, "meta_has": {"crm_id", "owner"}}
	if _, err := testClient(srv.URL).ListNotesQuery(q); err != nil {
		t.Fatalf("ListNotesQuery: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/notes" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "meta.gallery=true", "meta_has=crm_id", "meta_has=owner") {
		t.Errorf("query = %q", rec.Query)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMetadataPaths pins where each record type's metadata route lives.
func TestMetadataPaths(t *testing.T) {
	cases := map[string]string{
		NoteMetadataPath("n1"):        "/notes/n1/metadata",
		NotebookMetadataPath("nb1"):   "/notebooks/nb1/metadata",
		StackMetadataPath("Projects"): "/stacks/Projects/metadata",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}

// TestStackMetadataPathEscapesTheName proves a stack name reaches the server as
// one path segment: a space, `?` or `/` in a name must not change the route or
// start a query string.
func TestStackMetadataPathEscapesTheName(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
		_, _ = w.Write([]byte(`{"metadata":{}}`))
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).GetMetadata(StackMetadataPath("Client Work?/2026")); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if gotPath != "/stacks/Client%20Work%3F%2F2026/metadata" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want none", gotQuery)
	}
}

// TestMetadataVerbs pins the verb and body each metadata call sends. The body
// is the bare object — never wrapped — because that is the route's contract.
func TestMetadataVerbs(t *testing.T) {
	cases := []struct {
		name     string
		call     func(c *Client) ([]byte, error)
		method   string
		wantBody string
	}{
		{"get", func(c *Client) ([]byte, error) { return c.GetMetadata("/notes/n1/metadata") }, "GET", ""},
		{"replace", func(c *Client) ([]byte, error) {
			return c.ReplaceMetadata("/notes/n1/metadata", map[string]any{"a": 1})
		}, "PUT", `{"a":1}`},
		{"merge", func(c *Client) ([]byte, error) {
			return c.MergeMetadata("/notes/n1/metadata", map[string]any{"a": nil})
		}, "PATCH", `{"a":null}`},
		{"clear", func(c *Client) ([]byte, error) { return c.ClearMetadata("/notes/n1/metadata") }, "DELETE", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recordedRequest
			srv := newTestServer(t, &rec, 200, `{"metadata":{}}`)
			defer srv.Close()
			if _, err := tc.call(testClient(srv.URL)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if rec.Method != tc.method || rec.Path != "/notes/n1/metadata" {
				t.Errorf("%s %s", rec.Method, rec.Path)
			}
			if string(rec.Body) != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body, tc.wantBody)
			}
		})
	}
}

// TestClearMetadataAcceptsNoContent covers the 204 a clear answers with.
func TestClearMetadataAcceptsNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).ClearMetadata("/stacks/Projects/metadata"); err != nil {
		t.Fatalf("ClearMetadata: %v", err)
	}
}

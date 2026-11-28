// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestListStacks pins the route and that a repeated or empty-valued filter
// survives into the query string.
func TestListStacks(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"total":0}}`)
	defer srv.Close()
	q := url.Values{"meta_has": {"a", "b"}, "meta.note": {""}}
	if _, err := testClient(srv.URL).ListStacks(q); err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/stacks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "meta_has=a", "meta_has=b", "meta.note=") {
		t.Errorf("query = %q", rec.Query)
	}
}

// TestUpdateStack pins the verb and body, and that the name reaches the server
// as one escaped path segment.
func TestUpdateStack(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody = r.Method, r.URL.EscapedPath(), string(b)
		_, _ = w.Write([]byte(`{"name":"Client Work?","notebook_count":0,"color":"#ef4444","metadata":{}}`))
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).UpdateStack("Client Work?", map[string]any{"color": "#ef4444"}); err != nil {
		t.Fatalf("UpdateStack: %v", err)
	}
	if gotMethod != "PATCH" || gotPath != "/stacks/Client%20Work%3F" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotBody != `{"color":"#ef4444"}` {
		t.Errorf("body = %s", gotBody)
	}
}

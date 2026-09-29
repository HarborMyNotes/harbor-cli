// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestListNotebooks(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"total":0}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ListNotebooks(map[string]string{"stack": "Projects", "order": "-usn"}); err != nil {
		t.Fatalf("ListNotebooks error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/notebooks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if rec.Query == "" || !containsAll(rec.Query, "stack=Projects", "order=-usn") {
		t.Errorf("query = %q", rec.Query)
	}
}

func TestGetNotebookIncludeDeleted(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"nb1"}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetNotebook("nb1", true); err != nil {
		t.Fatalf("GetNotebook error: %v", err)
	}
	if rec.Path != "/notebooks/nb1" {
		t.Errorf("path = %s", rec.Path)
	}
	if rec.Query != "include_deleted=true" {
		t.Errorf("query = %q", rec.Query)
	}
}

func TestCreateNotebook(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"id":"nb1","name":"Work"}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).CreateNotebook(map[string]any{"name": "Work", "stack": "Projects"}); err != nil {
		t.Fatalf("CreateNotebook error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/notebooks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["name"] != "Work" || body["stack"] != "Projects" {
		t.Errorf("body = %v", body)
	}
}

func TestUpdateNotebook(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"nb1","is_default":true}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).UpdateNotebook("nb1", map[string]any{"is_default": true}); err != nil {
		t.Fatalf("UpdateNotebook error: %v", err)
	}
	if rec.Method != "PATCH" || rec.Path != "/notebooks/nb1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["is_default"] != true {
		t.Errorf("body = %v", body)
	}
}

func TestDeleteNotebook(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 204, ``)
	defer srv.Close()
	if _, err := testClient(srv.URL).DeleteNotebook("nb1", "trash"); err != nil {
		t.Fatalf("DeleteNotebook error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/notebooks/nb1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if rec.Query != "notes=trash" {
		t.Errorf("query = %q", rec.Query)
	}
}

// containsAll reports whether s contains every substring.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestListNotebooksQuery covers the caller-built query form used by the
// metadata filters.
func TestListNotebooksQuery(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"total":0}}`)
	defer srv.Close()
	q := url.Values{"meta.gallery": {"true"}, "stack": {"Projects"}}
	if _, err := testClient(srv.URL).ListNotebooksQuery(q); err != nil {
		t.Fatalf("ListNotebooksQuery: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/notebooks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "meta.gallery=true", "stack=Projects") {
		t.Errorf("query = %q", rec.Query)
	}
}

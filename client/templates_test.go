// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestListTemplates verifies the list method's verb, path, and query encoding.
func TestListTemplates(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"total":0}}`)
	defer srv.Close()
	params := map[string]string{"order": "-usn", "include_system": "false", "include_deleted": "true"}
	if _, err := testClient(srv.URL).ListTemplates(params); err != nil {
		t.Fatalf("ListTemplates error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/templates" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "order=-usn", "include_system=false", "include_deleted=true") {
		t.Errorf("query = %q", rec.Query)
	}
}

// TestGetTemplateIncludeDeleted verifies the get path and the include_deleted
// query param.
func TestGetTemplateIncludeDeleted(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"tpl1","name":"Meeting"}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetTemplate("tpl1", true); err != nil {
		t.Fatalf("GetTemplate error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/templates/tpl1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if rec.Query != "include_deleted=true" {
		t.Errorf("query = %q", rec.Query)
	}
}

// TestGetTemplateNoQuery verifies that omitting include_deleted sends no query.
func TestGetTemplateNoQuery(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"tpl1"}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetTemplate("tpl1", false); err != nil {
		t.Fatalf("GetTemplate error: %v", err)
	}
	if rec.Query != "" {
		t.Errorf("query = %q, want empty", rec.Query)
	}
}

// TestCreateTemplate verifies the create verb, path, and JSON body.
func TestCreateTemplate(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"id":"tpl1","name":"Meeting"}`)
	defer srv.Close()
	body := map[string]any{"name": "Meeting", "content": "# Hi", "content_format": "markdown",
		"notebook_id": "nb1", "tag_ids": []string{"t1", "t2"}}
	if _, err := testClient(srv.URL).CreateTemplate(body); err != nil {
		t.Fatalf("CreateTemplate error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/templates" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body, &got)
	if got["name"] != "Meeting" || got["content"] != "# Hi" || got["content_format"] != "markdown" {
		t.Errorf("body = %v", got)
	}
	if got["notebook_id"] != "nb1" {
		t.Errorf("notebook_id = %v, want nb1", got["notebook_id"])
	}
	if tags, ok := got["tag_ids"].([]any); !ok || len(tags) != 2 {
		t.Errorf("tag_ids = %v, want a 2-element array", got["tag_ids"])
	}
}

// TestUpdateTemplate verifies the partial-update verb, path, and body.
func TestUpdateTemplate(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"tpl1","name":"Renamed"}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).UpdateTemplate("tpl1", map[string]any{"name": "Renamed"}); err != nil {
		t.Fatalf("UpdateTemplate error: %v", err)
	}
	if rec.Method != "PATCH" || rec.Path != "/templates/tpl1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body, &got)
	if got["name"] != "Renamed" {
		t.Errorf("body = %v", got)
	}
	// A field the caller left out must not appear at all: the server reads an
	// absent notebook_id as "preserve" and an empty one as "clear".
	if _, ok := got["notebook_id"]; ok {
		t.Errorf("a rename must not carry notebook_id: %v", got)
	}
}

// TestUpdateTemplateClearsFiling verifies an explicit clear reaches the wire as
// an empty string and an empty ARRAY — never as null, which the server does not
// treat as a clear.
func TestUpdateTemplateClearsFiling(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"tpl1","notebook_id":"","tag_ids":[]}`)
	defer srv.Close()
	body := map[string]any{"notebook_id": "", "tag_ids": []string{}}
	if _, err := testClient(srv.URL).UpdateTemplate("tpl1", body); err != nil {
		t.Fatalf("UpdateTemplate error: %v", err)
	}
	if raw := string(rec.Body); !strings.Contains(raw, `"notebook_id":""`) || !strings.Contains(raw, `"tag_ids":[]`) {
		t.Errorf("body = %s, want an empty string and an empty array", raw)
	}
}

// TestDeleteTemplate verifies the delete verb and path.
func TestDeleteTemplate(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 204, ``)
	defer srv.Close()
	if _, err := testClient(srv.URL).DeleteTemplate("tpl1"); err != nil {
		t.Fatalf("DeleteTemplate error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/templates/tpl1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestApplyTemplate verifies the apply verb, path, and override body, and that
// the {note, usn} envelope is returned to the caller.
func TestApplyTemplate(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"note":{"id":"n1","title":"Standup"},"usn":88}`)
	defer srv.Close()
	body := map[string]any{"notebook_id": "nb1", "title": "Standup", "tags": []string{"t1", "t2"}}
	data, err := testClient(srv.URL).ApplyTemplate("tpl1", body)
	if err != nil {
		t.Fatalf("ApplyTemplate error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/templates/tpl1/apply" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body, &got)
	if got["notebook_id"] != "nb1" || got["title"] != "Standup" {
		t.Errorf("body = %v", got)
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 2 || tags[0] != "t1" {
		t.Errorf("tags = %v", got["tags"])
	}
	if !containsAll(string(data), `"note"`, `"usn":88`) {
		t.Errorf("response = %s", data)
	}
}

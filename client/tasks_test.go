// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-28

package client

import (
	"encoding/json"
	"testing"
)

// TestListTasks verifies the GET path and that the filter params are forwarded.
func TestListTasks(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).ListTasks(map[string]string{
		"status": "today", "due_before": "1750100000000", "order": "-priority", "limit": "25",
	})
	if err != nil {
		t.Fatalf("ListTasks error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/tasks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "status=today", "due_before=1750100000000", "order=-priority", "limit=25") {
		t.Errorf("query = %q", rec.Query)
	}
}

// TestListNoteTasks verifies the per-note read hangs off the note's own path.
func TestListNoteTasks(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).ListNoteTasks("n1", map[string]string{"order": "position"})
	if err != nil {
		t.Fatalf("ListNoteTasks error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/notes/n1/tasks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "order=position") {
		t.Errorf("query = %q", rec.Query)
	}
}

// TestGetTask verifies a plain GET of one task.
func TestGetTask(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"task":{"id":"t1"},"usn":12}`)
	defer srv.Close()
	_, err := testClient(srv.URL).GetTask("t1")
	if err != nil {
		t.Fatalf("GetTask error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/tasks/t1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestCreateTask verifies the POST path and that the body reaches the server
// verbatim, including an explicit due_has_time=false for a date-only due.
func TestCreateTask(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"task":{"id":"t1"},"usn":13}`)
	defer srv.Close()
	_, err := testClient(srv.URL).CreateTask(map[string]any{
		"title": "Pay the invoice", "due_at": int64(1785542400000), "due_has_time": false, "priority": "high",
	})
	if err != nil {
		t.Fatalf("CreateTask error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/tasks" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if body["title"] != "Pay the invoice" || body["priority"] != "high" {
		t.Errorf("body = %v", body)
	}
	// JSON numbers decode to float64.
	if body["due_at"] != float64(1785542400000) {
		t.Errorf("due_at = %v", body["due_at"])
	}
	if body["due_has_time"] != false {
		t.Errorf("due_has_time = %v, want false", body["due_has_time"])
	}
}

// TestCreateTaskNeverSendsNoteID guards the rule that a task is never linked to
// a note through this path: a note link without the note's <harbor-task> block
// is deleted by the note's next save (app.harbor.my#1094).
func TestCreateTaskNeverSendsNoteID(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"task":{"id":"t1"},"usn":13}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).CreateTask(map[string]any{"title": "Standalone"}); err != nil {
		t.Fatalf("CreateTask error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if _, ok := body["note_id"]; ok {
		t.Errorf("note_id must never be sent, got %v", body["note_id"])
	}
}

// TestUpdateTask verifies a PATCH carrying only the changed fields, including
// the clear_* sentinels that null a column.
func TestUpdateTask(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"task":{"id":"t1"},"usn":14}`)
	defer srv.Close()
	_, err := testClient(srv.URL).UpdateTask("t1", map[string]any{"title": "Renamed", "clear_due_at": true})
	if err != nil {
		t.Fatalf("UpdateTask error: %v", err)
	}
	if rec.Method != "PATCH" || rec.Path != "/tasks/t1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if body["title"] != "Renamed" || body["clear_due_at"] != true {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["priority"]; ok {
		t.Errorf("untouched fields must be absent from a PATCH, got %v", body)
	}
}

// TestCompleteTaskWithDoneTime verifies the done path and its optional body.
func TestCompleteTaskWithDoneTime(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"task":{"id":"t1"},"usn":15}`)
	defer srv.Close()
	_, err := testClient(srv.URL).CompleteTask("t1", map[string]any{"done_time": int64(1750090000000)})
	if err != nil {
		t.Fatalf("CompleteTask error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/tasks/t1/done" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if body["done_time"] != float64(1750090000000) {
		t.Errorf("done_time = %v", body["done_time"])
	}
}

// TestCompleteTaskNilBody verifies a nil body still posts a valid JSON object
// (the server then defaults done_time to now).
func TestCompleteTaskNilBody(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"task":{"id":"t1"},"usn":15}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).CompleteTask("t1", nil); err != nil {
		t.Fatalf("CompleteTask error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("nil body should marshal to {}, got %q (%v)", rec.Body, err)
	}
	if len(body) != 0 {
		t.Errorf("expected empty body, got %v", body)
	}
}

// TestUncompleteTask verifies reopening is a DELETE of the done sub-resource.
func TestUncompleteTask(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"task":{"id":"t1"},"usn":16}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).UncompleteTask("t1"); err != nil {
		t.Fatalf("UncompleteTask error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/tasks/t1/done" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestDeleteTask verifies a DELETE of the task itself, with the empty 204 body
// handled without error.
func TestDeleteTask(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 204, ``)
	defer srv.Close()
	if _, err := testClient(srv.URL).DeleteTask("t1"); err != nil {
		t.Fatalf("DeleteTask error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/tasks/t1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

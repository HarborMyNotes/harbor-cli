// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"testing"
)

// TestListTrash verifies the trash list hits GET /trash and forwards the paging
// params (limit/offset/order) as query string.
func TestListTrash(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"total":0}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ListTrash(map[string]string{"order": "title", "limit": "50"}); err != nil {
		t.Fatalf("ListTrash error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/trash" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if !containsAll(rec.Query, "order=title", "limit=50") {
		t.Errorf("query = %q", rec.Query)
	}
}

// TestRestoreNote verifies restore POSTs to /notes/:id/restore with no body.
func TestRestoreNote(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"id":"n1","title":"Plan","in_trash":false}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).RestoreNote("n1"); err != nil {
		t.Fatalf("RestoreNote error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/notes/n1/restore" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestExpungeNote verifies expunge POSTs to /notes/:id/expunge (204, no body).
func TestExpungeNote(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 204, ``)
	defer srv.Close()
	if _, err := testClient(srv.URL).ExpungeNote("n1"); err != nil {
		t.Fatalf("ExpungeNote error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/notes/n1/expunge" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestEmptyTrash verifies empty issues DELETE /trash and returns the bare
// {expunged} body.
func TestEmptyTrash(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"expunged":3}`)
	defer srv.Close()
	data, err := testClient(srv.URL).EmptyTrash()
	if err != nil {
		t.Fatalf("EmptyTrash error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/trash" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if string(data) != `{"expunged":3}` {
		t.Errorf("body = %s", data)
	}
}

// TestEmptyTrashPassesTheJobThrough verifies the 202 answer for a big trash
// reaches the caller whole, job included, so the command can wait on it.
func TestEmptyTrashPassesTheJobThrough(t *testing.T) {
	body := `{"expunged":450,"job":{"id":"j1","status":"queued","total_notes":450,"processed_notes":0,"error":""}}`
	srv := newTestServer(t, nil, 202, body)
	defer srv.Close()
	data, err := testClient(srv.URL).EmptyTrash()
	if err != nil {
		t.Fatalf("EmptyTrash error: %v", err)
	}
	if string(data) != body {
		t.Errorf("body = %s", data)
	}
}

// TestGetTrashEmptyJob verifies the poll issues GET /trash/empty-jobs/:id and
// returns the data-wrapped job unchanged.
func TestGetTrashEmptyJob(t *testing.T) {
	var rec recordedRequest
	body := `{"data":{"id":"j1","status":"running","total_notes":450,"processed_notes":200,"error":""}}`
	srv := newTestServer(t, &rec, 200, body)
	defer srv.Close()
	data, err := testClient(srv.URL).GetTrashEmptyJob("j1")
	if err != nil {
		t.Fatalf("GetTrashEmptyJob error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/trash/empty-jobs/j1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if string(data) != body {
		t.Errorf("body = %s", data)
	}
}

// TestRestoreNotInTrashReturnsAPIError verifies a 422 not_in_trash surfaces as a
// typed *APIError so the cmd layer can map it.
func TestRestoreNotInTrashReturnsAPIError(t *testing.T) {
	srv := newTestServer(t, nil, 422, `{"error":{"code":"not_in_trash","message":"Not in trash."}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).RestoreNote("n1")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.Code != "not_in_trash" {
		t.Errorf("code = %q", apiErr.Code)
	}
}

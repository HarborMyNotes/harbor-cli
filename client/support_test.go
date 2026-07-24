// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-23

package client

import (
	"encoding/json"
	"testing"
)

// TestSubmitSupportRequest asserts the verb, path, JSON body, and that the
// X-Harbor-Platform header rides along on the request.
func TestSubmitSupportRequest(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"received":true,"reference":"SUP-123"}}`)
	defer srv.Close()

	body := map[string]any{
		"category": "bug",
		"subject":  "Sync stuck",
		"message":  "It has been spinning for an hour.",
		"metadata": map[string]any{"os": "darwin"},
	}
	data, err := testClient(srv.URL).SubmitSupportRequest(body)
	if err != nil {
		t.Fatalf("SubmitSupportRequest error: %v", err)
	}
	if rec.Method != "POST" {
		t.Errorf("method = %s, want POST", rec.Method)
	}
	if rec.Path != "/support" {
		t.Errorf("path = %s, want /support", rec.Path)
	}
	if rec.ContentType != "application/json" {
		t.Errorf("content-type = %q", rec.ContentType)
	}
	if rec.Platform != "cli" {
		t.Errorf("X-Harbor-Platform = %q, want cli", rec.Platform)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body, &got); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if got["category"] != "bug" || got["subject"] != "Sync stuck" {
		t.Errorf("body = %v", got)
	}
	if got["message"] != "It has been spinning for an hour." {
		t.Errorf("message = %v", got["message"])
	}
	if _, ok := got["metadata"].(map[string]any); !ok {
		t.Errorf("metadata missing/not an object: %v", got["metadata"])
	}
	if string(data) == "" {
		t.Error("expected a response body")
	}
}

// TestSupportPlatformHeaderOnEveryRequest verifies the platform header is sent
// on ordinary requests too (it lives in setCommonHeaders), not just support.
func TestSupportPlatformHeaderOnEveryRequest(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[]}`)
	defer srv.Close()

	if _, err := testClient(srv.URL).doGet("/notes", nil); err != nil {
		t.Fatalf("doGet error: %v", err)
	}
	if rec.Platform != "cli" {
		t.Errorf("X-Harbor-Platform = %q, want cli", rec.Platform)
	}
}

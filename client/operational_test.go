// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"strings"
	"testing"
)

// TestHealthHitsOrigin verifies /health is requested at the server ORIGIN (no
// /api/v1 prefix) even though the client's BaseURL ends in /api/v1. The test
// server's URL has no /api/v1, so constructing the client with srv.URL+"/api/v1"
// means c.Origin() must strip it back to srv.URL for the path to record "/health".
func TestHealthHitsOrigin(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"status":"ok"}`)
	defer srv.Close()

	data, err := NewClient(srv.URL+"/api/v1", "").Health()
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if rec.Method != "GET" {
		t.Errorf("method = %s, want GET", rec.Method)
	}
	if rec.Path != "/health" {
		t.Errorf("path = %q, want /health (origin, no /api/v1)", rec.Path)
	}
	if !strings.Contains(string(data), `"ok"`) {
		t.Errorf("body = %s", data)
	}
}

// TestReadyHitsOrigin verifies /ready is requested at the origin and returns the
// readiness body on a healthy 200.
func TestReadyHitsOrigin(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"status":"ready","checks":{"system_db":{"ok":true,"latency_ms":1}}}`)
	defer srv.Close()

	data, err := NewClient(srv.URL+"/api/v1", "").Ready()
	if err != nil {
		t.Fatalf("Ready error: %v", err)
	}
	if rec.Path != "/ready" {
		t.Errorf("path = %q, want /ready (origin, no /api/v1)", rec.Path)
	}
	if !strings.Contains(string(data), "ready") {
		t.Errorf("body = %s", data)
	}
}

// TestReadyNotReadySurfacesAPIError verifies a 503 from /ready surfaces as a
// typed *APIError. Because /ready does not use the standard error envelope, the
// fallback decoder carries the readiness JSON in the error Message, which the
// status command recovers to render the per-check table even when degraded.
func TestReadyNotReadySurfacesAPIError(t *testing.T) {
	body := `{"status":"not_ready","checks":{"s3":{"ok":false,"latency_ms":2003,"error":"timeout"}}}`
	srv := newTestServer(t, nil, 503, body)
	defer srv.Close()

	_, err := NewClient(srv.URL+"/api/v1", "").Ready()
	if err == nil {
		t.Fatal("expected error on 503")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.Status != 503 {
		t.Errorf("status = %d, want 503", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "not_ready") {
		t.Errorf("error message should carry the readiness body, got %q", apiErr.Message)
	}
}

// TestVersionHitsOrigin verifies /version is requested at the origin.
func TestVersionHitsOrigin(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"version":"2.0.0-m2","commit":"29ac12e","build_time":"2026-06-21T06:41:20Z","go_version":"go1.25.0"}`)
	defer srv.Close()

	data, err := NewClient(srv.URL+"/api/v1", "").Version()
	if err != nil {
		t.Fatalf("Version error: %v", err)
	}
	if rec.Path != "/version" {
		t.Errorf("path = %q, want /version (origin, no /api/v1)", rec.Path)
	}
	if !strings.Contains(string(data), "2.0.0-m2") {
		t.Errorf("body = %s", data)
	}
}

// TestOpenAPIIsUnderAPIV1 verifies the OpenAPI spec IS fetched under the
// versioned base URL (path /api/v1/openapi.json), unlike the root health probes.
func TestOpenAPIIsUnderAPIV1(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"openapi":"3.0.3"}`)
	defer srv.Close()

	data, err := NewClient(srv.URL+"/api/v1", "").OpenAPI()
	if err != nil {
		t.Fatalf("OpenAPI error: %v", err)
	}
	if rec.Path != "/api/v1/openapi.json" {
		t.Errorf("path = %q, want /api/v1/openapi.json", rec.Path)
	}
	if !strings.Contains(string(data), "3.0.3") {
		t.Errorf("body = %s", data)
	}
}

// TestOperationalProbesSendNoAuthHeader verifies the public probes do not send an
// Authorization header even when the client carries a token, since these
// endpoints require none.
func TestOperationalProbesSendNoAuthHeader(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"status":"ok"}`)
	defer srv.Close()

	// Build with a token to prove the probes still go out unauthenticated.
	c := NewClient(srv.URL+"/api/v1", "")
	if _, err := c.Health(); err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if rec.Auth != "" {
		t.Errorf("auth header = %q, want empty for public probe", rec.Auth)
	}
}

// TestClientFlagsHitsOrigin verifies /client-flags is requested at the server
// ORIGIN (no /api/v1 prefix) and that the bare document comes back with the
// upload policy intact. Whether the request carries a bearer is a property of
// the caller's client, and the upload probe's own test pins that.
func TestClientFlagsHitsOrigin(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"offline_priming":true,"max_upload_bytes":104857600,"allowed_mime":"*"}`)
	defer srv.Close()

	data, err := NewClient(srv.URL+"/api/v1", "").ClientFlags()
	if err != nil {
		t.Fatalf("ClientFlags error: %v", err)
	}
	if rec.Method != "GET" {
		t.Errorf("method = %s, want GET", rec.Method)
	}
	if rec.Path != "/client-flags" {
		t.Errorf("path = %q, want /client-flags (origin, no /api/v1)", rec.Path)
	}
	if !strings.Contains(string(data), `"max_upload_bytes":104857600`) {
		t.Errorf("body = %s", data)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package client

import "testing"

// TestGetUsage verifies the GET method and relative path.
func TestGetUsage(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"usage":{"notes":{"used":1,"limit":50}}}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetUsage(); err != nil {
		t.Fatalf("GetUsage error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/usage" {
		t.Errorf("%s %s, want GET /usage", rec.Method, rec.Path)
	}
}

// TestGetSubscription verifies the GET method and relative path.
func TestGetSubscription(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"plan":{"code":"starter"}}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetSubscription(); err != nil {
		t.Fatalf("GetSubscription error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/subscription" {
		t.Errorf("%s %s, want GET /subscription", rec.Method, rec.Path)
	}
}

// TestListPlans verifies the GET method, relative path, and that paging params
// reach the query string.
func TestListPlans(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{"limit":50,"offset":0,"total":0,"has_more":false}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ListPlans(map[string]string{"limit": "25"}); err != nil {
		t.Fatalf("ListPlans error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/plans" {
		t.Errorf("%s %s, want GET /plans", rec.Method, rec.Path)
	}
	if rec.Query != "limit=25" {
		t.Errorf("query = %q, want limit=25", rec.Query)
	}
}

// TestGetUsageSurfacesAPIError verifies a non-2xx body decodes into the typed
// APIError rather than being returned as an opaque failure.
func TestGetUsageSurfacesAPIError(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 403, `{"error":{"code":"insufficient_scope","message":"missing profile scope"}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).GetUsage()
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.Code != "insufficient_scope" || apiErr.Status != 403 {
		t.Errorf("got %s/%d, want insufficient_scope/403", apiErr.Code, apiErr.Status)
	}
}

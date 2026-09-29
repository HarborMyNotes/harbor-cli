// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package client

import (
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

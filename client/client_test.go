// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordedRequest captures what a handler received, for post-hoc assertions.
type recordedRequest struct {
	Method      string
	Path        string
	Query       string
	Body        []byte
	Auth        string
	Accept      string
	ContentType string
	RequestID   string
	Platform    string
}

// newTestServer starts an httptest server whose handler records the incoming
// request into rec and then writes status + body. It is the shared mock used
// across the client tests — no real network is ever touched.
func newTestServer(t *testing.T, rec *recordedRequest, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if rec != nil {
			rec.Method = r.Method
			rec.Path = r.URL.Path
			rec.Query = r.URL.RawQuery
			rec.Body = b
			rec.Auth = r.Header.Get("Authorization")
			rec.Accept = r.Header.Get("Accept")
			rec.ContentType = r.Header.Get("Content-Type")
			rec.RequestID = r.Header.Get("X-Request-Id")
			rec.Platform = r.Header.Get("X-Harbor-Platform")
		}
		w.Header().Set("X-Request-Id", "req_server123")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// testClient builds a Client pointed at a test server with a fake bearer token.
func testClient(url string) *Client {
	return NewClient(url, "at_test_token")
}

func TestNewClient(t *testing.T) {
	c := NewClient("https://app.harbor.my/api/v1/", "at_x")
	if c.BaseURL != "https://app.harbor.my/api/v1" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.AccessToken != "at_x" {
		t.Errorf("AccessToken = %q", c.AccessToken)
	}
	if c.HTTPClient == nil {
		t.Fatal("HTTPClient must not be nil")
	}
}

func TestOrigin(t *testing.T) {
	cases := map[string]string{
		"https://app.harbor.my/api/v1":  "https://app.harbor.my",
		"https://app.harbor.my/api/v1/": "https://app.harbor.my",
		"http://localhost:8472/api/v1":  "http://localhost:8472",
	}
	for base, want := range cases {
		c := NewClient(base, "")
		if got := c.Origin(); got != want {
			t.Errorf("Origin(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestDoGetSetsHeadersAndQuery(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[]}`)
	defer srv.Close()

	_, err := testClient(srv.URL).doGet("/notes", map[string]string{"limit": "5", "empty": ""})
	if err != nil {
		t.Fatalf("doGet error: %v", err)
	}
	if rec.Method != "GET" {
		t.Errorf("method = %s", rec.Method)
	}
	if rec.Path != "/notes" {
		t.Errorf("path = %s", rec.Path)
	}
	if rec.Query != "limit=5" {
		t.Errorf("query = %q, want limit=5 (empty values dropped)", rec.Query)
	}
	if rec.Auth != "Bearer at_test_token" {
		t.Errorf("auth = %q", rec.Auth)
	}
	if rec.Accept != "application/json" {
		t.Errorf("accept = %q", rec.Accept)
	}
	if rec.RequestID == "" || !strings.HasPrefix(rec.RequestID, "req_") {
		t.Errorf("X-Request-Id = %q, want req_ prefix", rec.RequestID)
	}
}

func TestDoPostEncodesJSONBody(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{}}`)
	defer srv.Close()

	_, err := testClient(srv.URL).doPost("/notes", map[string]any{"title": "Hi", "n": 2})
	if err != nil {
		t.Fatalf("doPost error: %v", err)
	}
	if rec.Method != "POST" {
		t.Errorf("method = %s", rec.Method)
	}
	if rec.ContentType != "application/json" {
		t.Errorf("content-type = %q", rec.ContentType)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body, &got); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if got["title"] != "Hi" {
		t.Errorf("body title = %v", got["title"])
	}
}

func TestVerbsMethods(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(c *Client) error
		want string
	}{
		{"patch", func(c *Client) error { _, e := c.doPatch("/x", map[string]any{"a": 1}); return e }, "PATCH"},
		{"put", func(c *Client) error { _, e := c.doPut("/x", map[string]any{"a": 1}); return e }, "PUT"},
		{"delete", func(c *Client) error { _, e := c.doDelete("/x", nil); return e }, "DELETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec recordedRequest
			srv := newTestServer(t, &rec, 200, `{}`)
			defer srv.Close()
			if err := tc.call(testClient(srv.URL)); err != nil {
				t.Fatalf("%s error: %v", tc.name, err)
			}
			if rec.Method != tc.want {
				t.Errorf("method = %s, want %s", rec.Method, tc.want)
			}
		})
	}
}

func TestDeleteQueryParams(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).doDelete("/notebooks/abc", map[string]string{"notes": "trash"}); err != nil {
		t.Fatalf("doDelete error: %v", err)
	}
	if rec.Query != "notes=trash" {
		t.Errorf("query = %q", rec.Query)
	}
}

func TestErrorStatusReturnsAPIError(t *testing.T) {
	srv := newTestServer(t, nil, 404, `{"error":{"code":"not_found","message":"Nope."}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).doGet("/notes/x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.Code != "not_found" || apiErr.Status != 404 {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestDoMultipartBuildsFileBody(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{}}`)
	defer srv.Close()

	_, err := testClient(srv.URL).doMultipart("/files/upload",
		map[string]string{"mime": "text/plain"}, "file", "hello.txt", strings.NewReader("hello bytes"))
	if err != nil {
		t.Fatalf("doMultipart error: %v", err)
	}
	if rec.Method != "POST" {
		t.Errorf("method = %s", rec.Method)
	}
	if !strings.HasPrefix(rec.ContentType, "multipart/form-data") {
		t.Errorf("content-type = %q", rec.ContentType)
	}
	if !strings.Contains(string(rec.Body), "hello bytes") {
		t.Error("multipart body missing file content")
	}
	if !strings.Contains(string(rec.Body), "text/plain") {
		t.Error("multipart body missing mime field")
	}
}

func TestDoGetRawStreams(t *testing.T) {
	srv := newTestServer(t, nil, 200, "RAW-BYTES")
	defer srv.Close()
	resp, err := testClient(srv.URL).doGetRaw("/files/abc/raw", nil)
	if err != nil {
		t.Fatalf("doGetRaw error: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "RAW-BYTES" {
		t.Errorf("body = %q", b)
	}
}

// TestTransparentRefreshOn401 verifies the client refreshes once and retries
// the original request after a 401 invalid_token.
func TestTransparentRefreshOn401(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer at_old" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_token","message":"expired"}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "at_old")
	var refreshed int
	c.OnUnauthorized = func() (string, bool) {
		refreshed++
		return "at_new", true
	}
	data, err := c.doGet("/notes", nil)
	if err != nil {
		t.Fatalf("expected success after refresh, got %v", err)
	}
	if refreshed != 1 {
		t.Errorf("refresh count = %d, want 1", refreshed)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2 (original + retry)", calls)
	}
	if !strings.Contains(string(data), "ok") {
		t.Errorf("unexpected body %s", data)
	}
}

// TestRefreshFailureDoesNotLoop verifies a failed refresh surfaces the original
// 401 without retrying forever.
func TestRefreshFailureDoesNotLoop(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_token","message":"expired"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "at_old")
	c.OnUnauthorized = func() (string, bool) { return "", false }
	_, err := c.doGet("/notes", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want 1 (no retry when refresh fails)", calls)
	}
}

func TestPrettyJSON(t *testing.T) {
	out, err := PrettyJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("PrettyJSON error: %v", err)
	}
	if !strings.Contains(out, "\n  ") {
		t.Errorf("expected indented output, got %q", out)
	}
}

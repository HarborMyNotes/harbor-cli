// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
	if rec.Platform != "cli" {
		t.Errorf("X-Harbor-Platform = %q, want cli", rec.Platform)
	}
}

// TestPlatformHeaderOnEveryTransport is the proof behind the claim that EVERY
// Harbor API request carries X-Harbor-Platform: cli. Asserting the header merely
// exists would pass against the bug worth preventing — a value that is right for
// whichever transport someone happened to test and wrong (or absent) on the
// others — so each case asserts the exact string, written out literally rather
// than read from clientPlatform, so a typo in the source cannot make the test
// agree with it.
//
// The three request-building sites in this package are covered: requestWithStatus
// (every doGet/doPost/doPatch/doPut/doDelete/doJSON/doMultipart), rawRequest
// (doGetRaw and the file downloads), and rawPostWithRefresh (the ENEX export
// stream). The fourth site, FetchURL, hits a presigned non-Harbor URL and must
// NOT carry the header — that is pinned by TestFetchURLSkipsHarborHeaders.
func TestPlatformHeaderOnEveryTransport(t *testing.T) {
	// enex is the streaming ENEX export body; it is not a JSON envelope, so it
	// stands in for any raw response the client streams back.
	const enex = `<?xml version="1.0"?><en-export></en-export>`

	cases := []struct {
		name string
		call func(c *Client) error
	}{
		{"doGet", func(c *Client) error { _, e := c.doGet("/notes", nil); return e }},
		{"doGetQuery", func(c *Client) error {
			_, e := c.doGetQuery("/notebooks", url.Values{"parent_id": {""}})
			return e
		}},
		{"doPost", func(c *Client) error { _, e := c.doPost("/notes", map[string]any{"title": "x"}); return e }},
		{"doPatch", func(c *Client) error { _, e := c.doPatch("/notes/n1", map[string]any{"title": "x"}); return e }},
		{"doPut", func(c *Client) error { _, e := c.doPut("/notes/n1/tags/t1", nil); return e }},
		{"doDelete", func(c *Client) error { _, e := c.doDelete("/notes/n1", nil); return e }},
		{"doMultipart", func(c *Client) error {
			_, e := c.doMultipart("/files/upload", map[string]string{"mime": "text/plain"},
				"file", "hello.txt", strings.NewReader("hello bytes"))
			return e
		}},
		{"doGetRaw", func(c *Client) error {
			resp, e := c.doGetRaw("/files/abc/raw", nil)
			if e == nil {
				resp.Body.Close()
			}
			return e
		}},
		{"rawPost (ENEX export)", func(c *Client) error {
			resp, e := c.ExportENEX("nb1", nil, false)
			if e == nil {
				resp.Body.Close()
			}
			return e
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recordedRequest
			srv := newTestServer(t, &rec, 200, enex)
			defer srv.Close()

			if err := tc.call(testClient(srv.URL)); err != nil {
				t.Fatalf("%s error: %v", tc.name, err)
			}
			if rec.Platform != "cli" {
				t.Errorf("X-Harbor-Platform = %q, want cli", rec.Platform)
			}
		})
	}
}

// TestPlatformHeaderWithoutToken proves the header does not ride on the bearer
// token. The public endpoints — login, register, password reset, public share,
// the /health and /version probes — run through an anonymous client, and the
// server's PlatformMiddleware is global, so those requests are exactly as much a
// "CLI request" as an authenticated one.
func TestPlatformHeaderWithoutToken(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{}}`)
	defer srv.Close()

	if _, err := NewClient(srv.URL, "").doPost("/oauth/token", map[string]any{"grant_type": "password"}); err != nil {
		t.Fatalf("anonymous doPost error: %v", err)
	}
	if rec.Auth != "" {
		t.Errorf("Authorization = %q, want empty on an anonymous client", rec.Auth)
	}
	if rec.Platform != "cli" {
		t.Errorf("X-Harbor-Platform = %q, want cli", rec.Platform)
	}
}

// TestPlatformHeaderSurvivesRefreshRetry covers the one request the CLI builds
// that no command calls directly: the retry issued after a transparent token
// refresh. Each of the three transports rebuilds the request from scratch on
// retry, so a header set only on the first attempt would vanish on exactly the
// requests a long-running session makes most.
func TestPlatformHeaderSurvivesRefreshRetry(t *testing.T) {
	cases := []struct {
		name string
		call func(c *Client) error
	}{
		{"requestWithStatus", func(c *Client) error { _, e := c.doGet("/notes", nil); return e }},
		{"rawRequest", func(c *Client) error {
			resp, e := c.doGetRaw("/files/abc/raw", nil)
			if e == nil {
				resp.Body.Close()
			}
			return e
		}},
		{"rawPostWithRefresh", func(c *Client) error {
			resp, e := c.ExportENEX("nb1", nil, false)
			if e == nil {
				resp.Body.Close()
			}
			return e
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// platforms records the header from every attempt, so a header
			// present on the first request and dropped on the retry still fails.
			var platforms []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				platforms = append(platforms, r.Header.Get("X-Harbor-Platform"))
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
			c.OnUnauthorized = func() (string, bool) { return "at_new", true }
			if err := tc.call(c); err != nil {
				t.Fatalf("%s error: %v", tc.name, err)
			}
			if len(platforms) != 2 {
				t.Fatalf("attempts = %d, want 2 (original + retry)", len(platforms))
			}
			for i, got := range platforms {
				if got != "cli" {
					t.Errorf("attempt %d: X-Harbor-Platform = %q, want cli", i+1, got)
				}
			}
		})
	}
}

// TestEveryRequestBuilderSetsCommonHeaders is the structural half of the proof.
// The table tests above cover the transports that exist today; this one covers
// the transport somebody adds next year. It walks the WHOLE repository's source
// and enforces two rules:
//
//   - Inside this package, a function that builds an *http.Request must also call
//     setCommonHeaders, so a new transport cannot quietly ship as untagged traffic.
//   - Outside this package, nothing may build an *http.Request at all. Every
//     Harbor API call belongs behind this client; a request built in cmd/ would
//     bypass setCommonHeaders by construction and no amount of checking in here
//     would see it. (cmd/auth.go runs an http.Server for the OAuth loopback
//     callback — that is an inbound listener, not a request, and does not trip
//     this.)
//
// exemptRequestBuilders is the deliberate, documented exception list — adding to
// it should be a conscious decision made in review, which is the point.
func TestEveryRequestBuilderSetsCommonHeaders(t *testing.T) {
	// FetchURL targets a presigned storage URL whose credentials live in the
	// query string. It is not a Harbor API endpoint, so it must send neither the
	// bearer token nor the platform header.
	exemptRequestBuilders := map[string]bool{"FetchURL": true}

	fset := token.NewFileSet()

	// callsPrefixed reports whether a function body contains a call whose callee
	// renders with the given source prefix. The match is a PREFIX, not equality,
	// so that "http.NewRequest" also catches http.NewRequestWithContext — the
	// spelling a new transport is most likely to reach for, and the one an exact
	// comparison would wave straight through the guard whose whole job is to
	// catch it. Nothing else in the standard library starts with that text.
	callsPrefixed := func(fn *ast.FuncDecl, prefix string) bool {
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var buf bytes.Buffer
			if err := printer.Fprint(&buf, fset, call.Fun); err == nil && strings.HasPrefix(buf.String(), prefix) {
				found = true
			}
			return true
		})
		return found
	}

	// The test runs with the client package as its working directory, so ".."
	// is the repository root.
	var builders int
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "build", "dist", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("failed to parse %s: %w", path, parseErr)
		}
		inClientPkg := file.Name.Name == "client"

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !callsPrefixed(fn, "http.NewRequest") {
				continue
			}
			builders++
			if !inClientPkg {
				t.Errorf("%s: %s builds an *http.Request outside the client package — "+
					"it cannot reach setCommonHeaders, so it would send Harbor API traffic "+
					"without X-Harbor-Platform: cli. Move the call behind a client method.",
					path, fn.Name.Name)
				continue
			}
			if exemptRequestBuilders[fn.Name.Name] {
				continue
			}
			if !callsPrefixed(fn, "c.setCommonHeaders") {
				t.Errorf("%s: %s builds an *http.Request but never calls setCommonHeaders — "+
					"it would send Harbor API traffic without X-Harbor-Platform: cli. "+
					"Route it through setCommonHeaders, or add it to exemptRequestBuilders "+
					"with a comment saying why it is not a Harbor API request.",
					path, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk the repository source: %v", err)
	}

	// A sanity floor: if a refactor moved request building out of this package
	// the scan would find nothing and pass vacuously, which is the one way this
	// guard could rot without anyone noticing.
	if builders < 4 {
		t.Errorf("found %d http.NewRequest sites, want at least 4 — the scan is no longer finding "+
			"the request builders it is meant to guard", builders)
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

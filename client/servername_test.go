// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-11-12

package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// memoryServerStore stands in for the config file: it holds names per base URL
// and records every save, so a test can see what would reach the disk.
type memoryServerStore struct {
	mu    sync.Mutex
	names map[string]string
	saves []string // "baseURL=name", in order
}

// useServerStore installs a fresh in-memory store seeded with names, and puts
// back a store-less, empty cache when the test ends.
func useServerStore(t *testing.T, seed map[string]string) *memoryServerStore {
	t.Helper()
	s := &memoryServerStore{names: map[string]string{}}
	for k, v := range seed {
		s.names[k] = v
	}
	SetServerNameStore(s.load, s.save)
	t.Cleanup(func() { SetServerNameStore(nil, nil) })
	return s
}

// load returns the stored name for baseURL.
func (s *memoryServerStore) load(baseURL string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.names[baseURL], nil
}

// save records name for baseURL; "" removes it.
func (s *memoryServerStore) save(baseURL, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, baseURL+"="+name)
	if name == "" {
		delete(s.names, baseURL)
	} else {
		s.names[baseURL] = name
	}
	return nil
}

// get returns the stored name for baseURL.
func (s *memoryServerStore) get(baseURL string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.names[baseURL]
}

// saveCount returns how many saves reached the store.
func (s *memoryServerStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saves)
}

// serverReply is one answer from serverStub: a status, an optional
// X-Harbor-Server value (omit to leave the header off), and a body.
type serverReply struct {
	status int
	name   *string
	body   string
}

// named returns a pointer to name, for serverReply.name.
func named(name string) *string { return &name }

// serverStub is a stub API that records the X-Harbor-Server header of every
// request and answers from a queue of replies, repeating the last one.
type serverStub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	sent    []string // the X-Harbor-Server each request carried ("" = none)
	replies []serverReply
}

// newServerStub starts a serverStub with the given replies.
func newServerStub(t *testing.T, replies ...serverReply) *serverStub {
	t.Helper()
	s := &serverStub{replies: replies}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.sent = append(s.sent, r.Header.Get("X-Harbor-Server"))
		reply := s.replies[0]
		if len(s.replies) > 1 {
			s.replies = s.replies[1:]
		}
		s.mu.Unlock()
		if reply.name != nil {
			w.Header().Set("X-Harbor-Server", *reply.name)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// headers returns the X-Harbor-Server values the stub has received so far.
func (s *serverStub) headers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// transportCase is one way the client sends a request to the API.
type transportCase struct {
	name string
	call func(c *Client) error
}

// apiTransports lists every way the client reaches the API: each verb through
// requestWithStatus, the streamed download through rawRequest, the ENEX export
// through rawPostWithRefresh, and a root-level probe on the API's origin.
func apiTransports() []transportCase {
	return []transportCase{
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
			_, e := c.doMultipart("/files/upload", nil, "file", "a.txt", strings.NewReader(""))
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
		{"health probe", func(c *Client) error { _, e := c.Health(); return e }},
	}
}

// TestServerNameSentOnEveryTransportOnceCached proves every transport learns
// the name from a response and sends it back on the next request, and that the
// first request, with nothing cached, carries no header at all.
func TestServerNameSentOnEveryTransportOnceCached(t *testing.T) {
	for _, tc := range apiTransports() {
		t.Run(tc.name, func(t *testing.T) {
			store := useServerStore(t, nil)
			stub := newServerStub(t, serverReply{status: 200, name: named("app-harbor-my-02"), body: `{"data":{}}`})
			c := NewClient(stub.srv.URL+"/api/v1", "at_x")

			for i := 0; i < 2; i++ {
				if err := tc.call(c); err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
			}
			got := stub.headers()
			if len(got) != 2 || got[0] != "" || got[1] != "app-harbor-my-02" {
				t.Errorf("X-Harbor-Server per request = %q, want [\"\" \"app-harbor-my-02\"]", got)
			}
			if name := store.get(stub.srv.URL + "/api/v1"); name != "app-harbor-my-02" {
				t.Errorf("stored name = %q, want app-harbor-my-02", name)
			}
		})
	}
}

// TestServerNameLoadedFromStoreOnFirstRequest is the "new command" case: the
// name saved by an earlier process goes out on the very first request, keyed by
// the client's own base URL, so another API's name is never sent.
func TestServerNameLoadedFromStoreOnFirstRequest(t *testing.T) {
	stub := newServerStub(t, serverReply{status: 200, body: `{}`})
	other := newServerStub(t, serverReply{status: 200, body: `{}`})
	useServerStore(t, map[string]string{stub.srv.URL + "/api/v1": "app-harbor-my-01"})

	if _, err := NewClient(stub.srv.URL+"/api/v1/", "at_x").doGet("/notes", nil); err != nil {
		t.Fatalf("doGet: %v", err)
	}
	if _, err := NewClient(other.srv.URL+"/api/v1", "at_x").doGet("/notes", nil); err != nil {
		t.Fatalf("doGet: %v", err)
	}
	if got := stub.headers(); got[0] != "app-harbor-my-01" {
		t.Errorf("first request carried %q, want the saved app-harbor-my-01", got[0])
	}
	if got := other.headers(); got[0] != "" {
		t.Errorf("a different API got %q, want no header", got[0])
	}
}

// TestServerNameSharedAcrossClients covers the short-lived clients (sign-in,
// whoami, the upload-limits probe): a name one client learns, a new client for
// the same API sends.
func TestServerNameSharedAcrossClients(t *testing.T) {
	useServerStore(t, nil)
	stub := newServerStub(t, serverReply{status: 200, name: named("app-1"), body: `{}`})

	if _, err := NewClient(stub.srv.URL, "").doPost("/oauth/token", nil); err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if _, err := NewClient(stub.srv.URL, "at_x").doGet("/profile", nil); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if got := stub.headers(); got[1] != "app-1" {
		t.Errorf("the second client sent %q, want app-1", got[1])
	}
}

// TestServerNameReplacedByNewName proves a different valid name replaces the
// cached one, and that an unchanged name is not written again.
func TestServerNameReplacedByNewName(t *testing.T) {
	store := useServerStore(t, nil)
	stub := newServerStub(t,
		serverReply{status: 200, name: named("app-1"), body: `{}`},
		serverReply{status: 200, name: named("app-1"), body: `{}`},
		serverReply{status: 200, name: named("app-2"), body: `{}`},
	)
	c := NewClient(stub.srv.URL, "at_x")
	for i := 0; i < 4; i++ {
		if _, err := c.doGet("/notes", nil); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	want := []string{"", "app-1", "app-1", "app-2"}
	if got := stub.headers(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("X-Harbor-Server per request = %q, want %q", got, want)
	}
	if n := store.saveCount(); n != 2 {
		t.Errorf("saves = %v, want 2 (app-1 once, then app-2)", store.saves)
	}
}

// TestMissingOrInvalidHeaderKeepsName proves a response without a usable name
// (Caddy's maintenance page, a malformed value) leaves the cached name alone,
// and that such a response of any status is read the same way.
func TestMissingOrInvalidHeaderKeepsName(t *testing.T) {
	cases := []struct {
		name  string
		reply serverReply
	}{
		{"missing", serverReply{status: 200, body: `{}`}},
		{"missing on a 503", serverReply{status: 503, body: `maintenance`}},
		{"has a space", serverReply{status: 200, name: named("app 1"), body: `{}`}},
		{"has a slash", serverReply{status: 200, name: named("app/1"), body: `{}`}},
		{"65 characters", serverReply{status: 200, name: named(strings.Repeat("a", 65)), body: `{}`}},
		{"not ASCII", serverReply{status: 200, name: named("app-é"), body: `{}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := useServerStore(t, nil)
			stub := newServerStub(t, serverReply{status: 200, name: named("app-1"), body: `{}`}, tc.reply)
			c := NewClient(stub.srv.URL, "at_x")
			_, _ = c.doGet("/notes", nil)
			_, _ = c.doGet("/notes", nil)
			_, _ = c.doGet("/notes", nil)

			if got := stub.headers(); got[2] != "app-1" {
				t.Errorf("after a %s header the request carried %q, want app-1", tc.name, got[2])
			}
			if store.get(stub.srv.URL) != "app-1" || store.saveCount() != 1 {
				t.Errorf("saves = %v, want only app-1", store.saves)
			}
		})
	}
}

// TestValidServerName pins the accepted shape: 1 to 64 characters from
// A-Z a-z 0-9 . _ -.
func TestValidServerName(t *testing.T) {
	for name, want := range map[string]bool{
		"app-harbor-my-02":      true,
		"A.b_c-9":               true,
		strings.Repeat("x", 64): true,
		"":                      false,
		strings.Repeat("x", 65): false,
		"app harbor":            false,
		"app:1":                 false,
		"app\n1":                false,
	} {
		if got := validServerName(name); got != want {
			t.Errorf("validServerName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestMisdirectedIsANormalErrorAndClearsTheName proves a 421 on every
// transport is sent once (no retry), comes back as the ordinary *APIError, and
// clears the name in memory and in the store, so the next request goes
// without it. The 421's own header is not learned.
func TestMisdirectedIsANormalErrorAndClearsTheName(t *testing.T) {
	cases := []transportCase{
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
			stub := newServerStub(t,
				serverReply{status: 421, name: named("app-other"),
					body: `{"error":{"code":"wrong_server","message":"This request reached the wrong server."}}`},
				serverReply{status: 200, body: `{}`},
			)
			store := useServerStore(t, map[string]string{stub.srv.URL: "app-1"})
			c := NewClient(stub.srv.URL, "at_x")
			c.OnUnauthorized = func() (string, bool) {
				t.Error("a 421 must not trigger a token refresh")
				return "", false
			}

			err := tc.call(c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 421 || apiErr.Code != "wrong_server" {
				t.Fatalf("err = %v, want the 421 wrong_server APIError", err)
			}
			if got := stub.headers(); len(got) != 1 || got[0] != "app-1" {
				t.Fatalf("requests = %q, want exactly one, carrying app-1 (no retry)", got)
			}
			if name := store.get(stub.srv.URL); name != "" {
				t.Errorf("stored name = %q after a 421, want it cleared", name)
			}

			if err := tc.call(c); err != nil {
				t.Fatalf("next call: %v", err)
			}
			if got := stub.headers(); got[1] != "" {
				t.Errorf("the request after a 421 carried %q, want no header", got[1])
			}
		})
	}
}

// TestServerNameSurvivesRefreshRetry proves the name is read from a 401 like
// any response, and that the retry after a token refresh carries it.
func TestServerNameSurvivesRefreshRetry(t *testing.T) {
	useServerStore(t, nil)
	stub := newServerStub(t,
		serverReply{status: 401, name: named("app-1"), body: `{"error":{"code":"invalid_token","message":"expired"}}`},
		serverReply{status: 200, body: `{}`},
	)
	c := NewClient(stub.srv.URL, "at_old")
	c.OnUnauthorized = func() (string, bool) { return "at_new", true }
	if _, err := c.doGet("/notes", nil); err != nil {
		t.Fatalf("doGet: %v", err)
	}
	if got := stub.headers(); len(got) != 2 || got[1] != "app-1" {
		t.Errorf("X-Harbor-Server per request = %q, want the retry to carry app-1", got)
	}
}

// TestServerNameNeverSentToOtherHosts proves the presigned transfers (a
// download, an import part, a file upload's PUT) never carry the name, even
// with one cached for the API.
func TestServerNameNeverSentToOtherHosts(t *testing.T) {
	storage := newServerStub(t, serverReply{status: 200, body: `blob`})
	api := newServerStub(t, serverReply{status: 200, body: `{}`})
	useServerStore(t, map[string]string{api.srv.URL: "app-1"})
	c := NewClient(api.srv.URL, "at_x")

	resp, err := c.FetchURL(storage.srv.URL + "/obj?sig=1")
	if err != nil {
		t.Fatalf("FetchURL: %v", err)
	}
	resp.Body.Close()
	// The stub sends no ETag, so the part fails after its one PUT; only the
	// request's headers matter here.
	_, _ = c.UploadImportPart(context.Background(), storage.srv.URL+"/part?sig=1", strings.NewReader("x"), 1)
	if n := len(storage.headers()); n != 2 {
		t.Fatalf("storage got %d requests, want 2 (the download and the part)", n)
	}
	for i, got := range storage.headers() {
		if got != "" {
			t.Errorf("storage request %d carried X-Harbor-Server %q, want none", i+1, got)
		}
	}
}

// TestServerNameStrippedOnRedirectToOtherHost proves a redirect that leaves the
// API's host loses the header (Go copies custom headers to any host), that the
// API itself still got it, and that a name the other host sends is not learned.
func TestServerNameStrippedOnRedirectToOtherHost(t *testing.T) {
	elsewhere := newServerStub(t, serverReply{status: 200, name: named("not-harbor"), body: `{}`})
	var apiGot string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiGot = r.Header.Get("X-Harbor-Server")
		http.Redirect(w, r, elsewhere.srv.URL+"/landing", http.StatusFound)
	}))
	defer api.Close()
	store := useServerStore(t, map[string]string{api.URL: "app-1"})

	if _, err := NewClient(api.URL, "at_x").doGet("/go", nil); err != nil {
		t.Fatalf("doGet: %v", err)
	}
	if apiGot != "app-1" {
		t.Errorf("the API got %q, want app-1", apiGot)
	}
	if got := elsewhere.headers(); got[0] != "" {
		t.Errorf("the other host got X-Harbor-Server %q, want none", got[0])
	}
	if name := store.get(api.URL); name != "app-1" {
		t.Errorf("stored name = %q, want app-1 kept (the other host's name ignored)", name)
	}
}

// TestServerNameKeptOnSameHostRedirect proves a redirect within the API keeps
// the header, and that the redirect limit still holds.
func TestServerNameKeptOnSameHostRedirect(t *testing.T) {
	var got []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Harbor-Server"))
		if r.URL.Path == "/loop" {
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
			return
		}
		if r.URL.Path == "/old" {
			http.Redirect(w, r, srv.URL+"/new", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	useServerStore(t, map[string]string{srv.URL: "app-1"})
	c := NewClient(srv.URL, "at_x")

	if _, err := c.doGet("/old", nil); err != nil {
		t.Fatalf("doGet: %v", err)
	}
	if len(got) != 2 || got[1] != "app-1" {
		t.Errorf("X-Harbor-Server per hop = %q, want app-1 on the redirected request", got)
	}
	if _, err := c.doGet("/loop", nil); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("redirect loop err = %v, want the 10-redirect limit", err)
	}
}

// TestServerNameConcurrentRequests runs many requests at once against a server
// whose name changes, so `go test -race` catches unguarded access to the cache.
func TestServerNameConcurrentRequests(t *testing.T) {
	useServerStore(t, nil)
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		w.Header().Set("X-Harbor-Server", []string{"app-1", "app-2"}[n%2])
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := NewClient(srv.URL, "at_x").doGet("/notes", nil); err != nil {
				t.Errorf("doGet: %v", err)
			}
		}()
	}
	wg.Wait()
	if name := NewClient(srv.URL, "").serverName(); name != "app-1" && name != "app-2" {
		t.Errorf("cached name = %q, want one the server sent", name)
	}
}

// TestForgetServerNamesRereadsTheStore proves ForgetServerNames drops only the
// in-memory copy: the next request reads the store, so a name logout removed
// from disk is gone, and one still on disk comes back.
func TestForgetServerNamesRereadsTheStore(t *testing.T) {
	stub := newServerStub(t, serverReply{status: 200, body: `{}`})
	store := useServerStore(t, map[string]string{stub.srv.URL: "app-1"})
	c := NewClient(stub.srv.URL, "at_x")
	_, _ = c.doGet("/notes", nil)

	_ = store.save(stub.srv.URL, "")
	ForgetServerNames()
	_, _ = c.doGet("/notes", nil)

	if got := stub.headers(); got[0] != "app-1" || got[1] != "" {
		t.Errorf("X-Harbor-Server per request = %q, want [app-1 \"\"]", got)
	}
}

// TestSetCommonHeadersSendsNameOnlyToTheAPIOrigin proves the name goes on a
// request for the API's scheme and host, a root-level path included, and on
// nothing else, whatever URL a caller hands the request builders.
func TestSetCommonHeadersSendsNameOnlyToTheAPIOrigin(t *testing.T) {
	useServerStore(t, map[string]string{"https://api.example.com/api/v1": "app-1"})
	c := NewClient("https://api.example.com/api/v1", "at_x")

	for target, want := range map[string]string{
		"https://api.example.com/api/v1/notes": "app-1",
		"https://API.example.com/health":       "app-1",
		"https://storage.example.com/obj":      "",
		"http://api.example.com/api/v1/notes":  "",
		"https://api.example.com:8443/api/v1":  "",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.setCommonHeaders(req)
		if got := req.Header.Get("X-Harbor-Server"); got != want {
			t.Errorf("%s: X-Harbor-Server = %q, want %q", target, got, want)
		}
	}
}

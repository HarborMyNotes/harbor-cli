// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
)

func TestApplyToken(t *testing.T) {
	creds := &config.Credentials{}
	before := time.Now().UnixMilli()
	applyToken(creds, &client.TokenResponse{
		AccessToken:  "at_new",
		RefreshToken: "rt_new",
		TokenType:    "Bearer",
		Scope:        "notes sync",
		ExpiresIn:    3600,
	})
	if creds.AccessToken != "at_new" || creds.RefreshToken != "rt_new" {
		t.Errorf("tokens not applied: %+v", creds)
	}
	if creds.Scope != "notes sync" {
		t.Errorf("scope = %q", creds.Scope)
	}
	// ExpiresAt should be ~1h in the future.
	wantMin := before + 3590*1000
	if creds.ExpiresAt < wantMin {
		t.Errorf("ExpiresAt = %d, want >= %d", creds.ExpiresAt, wantMin)
	}
}

func TestApplyTokenKeepsRefreshWhenAbsent(t *testing.T) {
	creds := &config.Credentials{RefreshToken: "rt_old"}
	applyToken(creds, &client.TokenResponse{AccessToken: "at_2", ExpiresIn: 60})
	if creds.RefreshToken != "rt_old" {
		t.Errorf("refresh token should be preserved when response omits it; got %q", creds.RefreshToken)
	}
}

func TestLoginSummaryJSONOmitsSecrets(t *testing.T) {
	creds := &config.Credentials{Email: "you@example.com", DeviceID: "cli-1", APIURL: "http://x/api/v1", Scope: "notes", AccessToken: "hbp_secret"}

	var m map[string]any
	_ = json.Unmarshal(loginSummaryJSON(creds, false, "hbp_secret"), &m)
	if _, ok := m["access_token"]; ok {
		t.Error("access_token must be omitted unless --show-token")
	}
	if m["email"] != "you@example.com" || m["scope"] != "notes" {
		t.Errorf("summary = %v", m)
	}
	// A stored PAT (no refresh, ExpiresAt=0) reports never_expires.
	if m["never_expires"] != true {
		t.Errorf("never_expires = %v, want true", m["never_expires"])
	}

	var m2 map[string]any
	_ = json.Unmarshal(loginSummaryJSON(creds, true, "hbp_secret"), &m2)
	if m2["access_token"] != "hbp_secret" {
		t.Error("access_token should be present with --show-token")
	}
}

func TestWhoamiJSON(t *testing.T) {
	creds := &config.Credentials{Email: "you@example.com", Scope: "notes", DeviceID: "cli-1", DeviceName: "dev"}
	var m map[string]any
	_ = json.Unmarshal(whoamiJSON(creds, true, false), &m)
	if m["token_valid"] != true {
		t.Errorf("token_valid = %v", m["token_valid"])
	}
	if _, ok := m["access_token"]; ok {
		t.Error("access_token must be omitted unless --show-token")
	}
}

func TestMapLoginError(t *testing.T) {
	cases := map[string]string{
		"invalid_grant":  "invalid or expired",
		"invalid_token":  "invalid or has been revoked",
		"invalid_client": "unknown OAuth client",
	}
	for code, wantSub := range cases {
		got := mapLoginError(&client.APIError{Code: code, Message: "x"})
		if !strings.Contains(got.Error(), wantSub) {
			t.Errorf("mapLoginError(%s) = %q, want substring %q", code, got.Error(), wantSub)
		}
	}
	// A non-API error passes through.
	plain := mapLoginError(errorString("boom"))
	if plain.Error() != "boom" {
		t.Errorf("plain passthrough = %q", plain.Error())
	}
}

func TestMapPATError(t *testing.T) {
	got := mapPATError(&client.APIError{Code: "pat_limit_reached", Message: "x"})
	if !strings.Contains(got.Error(), "maximum number of access tokens") {
		t.Errorf("pat_limit_reached = %q", got.Error())
	}
	// Unknown codes pass through unchanged.
	other := mapPATError(&client.APIError{Code: "server_error", Message: "boom"})
	if !strings.Contains(other.Error(), "boom") {
		t.Errorf("passthrough = %q", other.Error())
	}
}

func TestNewDeviceID(t *testing.T) {
	id := newDeviceID()
	if !strings.HasPrefix(id, "cli-") {
		t.Errorf("device id = %q, want cli- prefix", id)
	}
	if id == newDeviceID() {
		t.Error("device ids should be unique")
	}
}

// errorString is a trivial error type for passthrough testing.
type errorString string

func (e errorString) Error() string { return string(e) }

// ===========================================================================
// Browser-login (PKCE + loopback) tests
// ===========================================================================

func TestPKCEVerifierAndChallenge(t *testing.T) {
	v, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier: %v", err)
	}
	// 32 bytes base64url (no padding) → 43 chars, within RFC 7636's 43–128.
	if len(v) < 43 || len(v) > 128 {
		t.Errorf("verifier length = %d, want 43–128", len(v))
	}
	if strings.ContainsAny(v, "+/=") {
		t.Errorf("verifier %q must be base64url (no +/=)", v)
	}
	// Challenge must equal BASE64URL(SHA256(verifier)) with no padding.
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := codeChallengeS256(v); got != want {
		t.Errorf("challenge = %q, want %q", got, want)
	}
	// Verifiers must be unique across calls.
	if v2, _ := generateCodeVerifier(); v2 == v {
		t.Error("verifiers should be unique")
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	raw := buildAuthorizeURL("https://app.harbor.my", "http://127.0.0.1:5555/callback", "", "st_1", "chal_1")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Targets the web origin's SPA route, NOT the /api/v1 path.
	if u.Scheme != "https" || u.Host != "app.harbor.my" || u.Path != "/oauth/authorize" {
		t.Errorf("authorize URL base = %s://%s%s", u.Scheme, u.Host, u.Path)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             "harbor-cli",
		"redirect_uri":          "http://127.0.0.1:5555/callback",
		"scope":                 cliScopes, // empty scope falls back to full set
		"state":                 "st_1",
		"code_challenge":        "chal_1",
		"code_challenge_method": "S256",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestStartCallbackServerHappyPath(t *testing.T) {
	redirect, resultCh, shutdown, err := startCallbackServer("st_ok")
	if err != nil {
		t.Fatalf("startCallbackServer: %v", err)
	}
	defer shutdown()
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") {
		t.Fatalf("redirect = %q, want http://127.0.0.1:<port>/callback", redirect)
	}
	resp, err := http.Get(redirect + "?code=ac_1&state=st_ok")
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	resp.Body.Close()
	select {
	case res := <-resultCh:
		if res.err != nil || res.code != "ac_1" {
			t.Errorf("result = %+v, want code ac_1", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback result")
	}
}

func TestStartCallbackServerStateMismatch(t *testing.T) {
	redirect, resultCh, shutdown, err := startCallbackServer("st_expected")
	if err != nil {
		t.Fatalf("startCallbackServer: %v", err)
	}
	defer shutdown()
	resp, err := http.Get(redirect + "?code=ac_1&state=st_WRONG")
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	resp.Body.Close()
	select {
	case res := <-resultCh:
		if res.err == nil || !strings.Contains(res.err.Error(), "state mismatch") {
			t.Errorf("result = %+v, want state mismatch error", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestStartCallbackServerOAuthError(t *testing.T) {
	redirect, resultCh, shutdown, err := startCallbackServer("st_ok")
	if err != nil {
		t.Fatalf("startCallbackServer: %v", err)
	}
	defer shutdown()
	resp, err := http.Get(redirect + "?error=access_denied&error_description=user+said+no&state=st_ok")
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	resp.Body.Close()
	select {
	case res := <-resultCh:
		if res.err == nil || !strings.Contains(res.err.Error(), "denied") {
			t.Errorf("result = %+v, want denied error", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSplitScopes(t *testing.T) {
	if got := splitScopes("notes  sync "); len(got) != 2 || got[0] != "notes" || got[1] != "sync" {
		t.Errorf("splitScopes = %v", got)
	}
	// Empty input falls back to the full CLI scope set.
	if got := splitScopes("   "); len(got) != len(strings.Fields(cliScopes)) {
		t.Errorf("splitScopes(empty) = %v, want full set", got)
	}
}

func TestProfileEmailID(t *testing.T) {
	email, id := profileEmailID([]byte(`{"data":{"id":"u1","email":"you@example.com"}}`))
	if email != "you@example.com" || id != "u1" {
		t.Errorf("profileEmailID = %q, %q", email, id)
	}
}

func TestIsNonExpiring(t *testing.T) {
	pat := &config.Credentials{AccessToken: "hbp_x"}
	if !isNonExpiring(pat) {
		t.Error("a PAT (no refresh, ExpiresAt=0) should be non-expiring")
	}
	session := &config.Credentials{AccessToken: "at_x", RefreshToken: "rt_x", ExpiresAt: 123}
	if isNonExpiring(session) {
		t.Error("a rotating session should NOT be non-expiring")
	}
}

// TestLoginViaBrowserEndToEnd drives the whole browser flow with the browser
// step stubbed: it asserts the flow exchanges the code, mints a PAT, and stores
// the PAT (no refresh token, no expiry) as the durable credential.
func TestLoginViaBrowserEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var revoked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		switch r.URL.Path {
		case "/api/v1/oauth/token":
			w.Write([]byte(`{"access_token":"at_short","refresh_token":"rt_short","token_type":"Bearer","expires_in":3600,"scope":"notes profile"}`))
		case "/api/v1/profile":
			w.Write([]byte(`{"data":{"id":"u1","email":"you@example.com"}}`))
		case "/api/v1/tokens":
			w.WriteHeader(201)
			w.Write([]byte(`{"data":{"id":"pat_1","token":"hbp_minted","name":"x","scopes":["notes","profile"],"token_kind":"pat","expires_at":null,"created_at":1}}`))
		case "/api/v1/oauth/revoke":
			revoked = append(revoked, body)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("HARBOR_API_URL", srv.URL+"/api/v1")

	// Stub the browser: parse the authorize URL, then hit the loopback callback.
	orig := openBrowser
	defer func() { openBrowser = orig }()
	openBrowser = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		cb := q.Get("redirect_uri") + "?code=ac_1&state=" + url.QueryEscape(q.Get("state"))
		go func() {
			resp, gerr := http.Get(cb)
			if gerr == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}

	_ = captureStdout(t, func() {
		if err := loginViaBrowser("", false, false); err != nil {
			t.Fatalf("loginViaBrowser: %v", err)
		}
	})

	creds, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if creds.AccessToken != "hbp_minted" {
		t.Errorf("stored token = %q, want the minted PAT", creds.AccessToken)
	}
	if creds.RefreshToken != "" {
		t.Errorf("PAT creds must not store a refresh token; got %q", creds.RefreshToken)
	}
	if creds.ExpiresAt != 0 {
		t.Errorf("PAT creds must have ExpiresAt=0; got %d", creds.ExpiresAt)
	}
	if creds.Email != "you@example.com" || creds.UserID != "u1" {
		t.Errorf("identity = %q / %q", creds.Email, creds.UserID)
	}
	if creds.ClientID != cliClientID {
		t.Errorf("client id = %q, want %q", creds.ClientID, cliClientID)
	}
	// The short-lived refresh token should have been revoked on the way out.
	if len(revoked) == 0 || !strings.Contains(strings.Join(revoked, " "), "rt_short") {
		t.Errorf("expected short-lived refresh token to be revoked; revokes = %v", revoked)
	}
}

// TestLoginWithTokenPersistsPAT verifies the headless --token path validates and
// stores a pasted PAT.
func TestLoginWithTokenPersistsPAT(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/profile" {
			w.Write([]byte(`{"data":{"id":"u9","email":"ci@example.com"}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	t.Setenv("HARBOR_API_URL", srv.URL+"/api/v1")

	_ = captureStdout(t, func() {
		if err := loginWithToken("hbp_pasted", false); err != nil {
			t.Fatalf("loginWithToken: %v", err)
		}
	})
	creds, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if creds.AccessToken != "hbp_pasted" || creds.Email != "ci@example.com" {
		t.Errorf("creds = %+v", creds)
	}
	if !isNonExpiring(creds) {
		t.Error("a pasted PAT should store as a non-expiring credential")
	}
}

// TestWhoamiUnderEnvTokenReportsTheSession is the other half of #88: whoami read
// only the saved session, so a perfectly working HARBOR_TOKEN run was told "not
// logged in — run 'harbor login' first". It is the first command anyone runs when
// scripting the CLI, which made it the most misleading answer in the tool.
func TestWhoamiUnderEnvTokenReportsTheSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hbp_env-token" {
			t.Errorf("profile fetched without the env token: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","email":"you@example.com","name":"Test User"}`))
	}))
	defer srv.Close()

	t.Setenv("HOME", t.TempDir()) // no saved session at all
	t.Setenv("HARBOR_TOKEN", "hbp_env-token")
	t.Setenv("HARBOR_API_URL", srv.URL)
	resetCommandState(t)

	out := captureStdout(t, func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})
	for _, want := range []string{"HARBOR_TOKEN", "you@example.com", srv.URL} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not logged in") {
		t.Errorf("whoami still claims the user is not logged in:\n%s", out)
	}
	if strings.Contains(out, "hbp_env-token") {
		t.Errorf("whoami printed the token without --show-token:\n%s", out)
	}
}

// TestWhoamiUnderEnvTokenReportsARejectedToken proves a bad token is answered,
// not raised. "Is this token any good?" is the question whoami was asked, so a
// 401 is the answer — returning it as a command error would make the one command
// that diagnoses auth the one that fails on bad auth.
func TestWhoamiUnderEnvTokenReportsARejectedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_token","message":"The access token is invalid."}}`))
	}))
	defer srv.Close()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_TOKEN", "hbp_revoked")
	t.Setenv("HARBOR_API_URL", srv.URL)
	resetCommandState(t)

	out := captureStdout(t, func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami should report a bad token, not fail: %v", err)
		}
	})
	if !strings.Contains(out, "HARBOR_TOKEN") {
		t.Errorf("whoami did not name the session source:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "rejected") {
		t.Errorf("whoami did not say the token was rejected:\n%s", out)
	}
}

// TestWhoamiUnderEnvTokenJSON pins the machine-readable shape, including the
// source field that tells a script where the session came from.
func TestWhoamiUnderEnvTokenJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","email":"you@example.com","name":"Test User"}`))
	}))
	defer srv.Close()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_TOKEN", "hbp_env-token")
	t.Setenv("HARBOR_API_URL", srv.URL)
	resetCommandState(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	out := captureStdout(t, func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("whoami --json is not valid JSON: %v\n%s", err, out)
	}
	if got["source"] != "HARBOR_TOKEN" {
		t.Errorf("source = %v, want HARBOR_TOKEN", got["source"])
	}
	if got["email"] != "you@example.com" {
		t.Errorf("email = %v", got["email"])
	}
	if got["token_valid"] != true {
		t.Errorf("token_valid = %v, want true", got["token_valid"])
	}
	if _, leaked := got["access_token"]; leaked {
		t.Error("--json leaked the access token without --show-token")
	}
}

// TestWhoamiWithoutEnvTokenStillReadsTheSavedSession proves the saved-session
// path is untouched — and in particular that it stays OFFLINE. It is a local
// state inspector; only the token session needs the network, because a bare
// token carries no identity.
func TestWhoamiWithoutEnvTokenStillReadsTheSavedSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("whoami hit the network for a saved session")
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HARBOR_TOKEN", "")
	t.Setenv("HARBOR_API_URL", srv.URL)
	resetCommandState(t)

	if err := os.MkdirAll(filepath.Join(home, ".config", "harbor"), 0700); err != nil {
		t.Fatal(err)
	}
	saved := `{"api_url":"` + srv.URL + `","email":"saved@example.com","access_token":"hbp_saved","token_type":"Bearer","expires_at":0,"device_id":"cli-abc","device_name":"laptop"}`
	if err := os.WriteFile(filepath.Join(home, ".config", "harbor", "credentials.json"), []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})
	if !strings.Contains(out, "saved@example.com") {
		t.Errorf("the saved session was not reported:\n%s", out)
	}
	if strings.Contains(out, "HARBOR_TOKEN") {
		t.Errorf("a saved session was reported as an env-token session:\n%s", out)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// cliClientID is the OAuth client the CLI authenticates as. It is seeded on the
// server to permit the authorization_code + PKCE grant and to register the
// loopback redirect URIs (http://127.0.0.1/callback, any port).
const cliClientID = "harbor-cli"

// cliScopes is the default scope set requested at login — the full catalog the
// CLI can use. The profile scope is REQUIRED so the resulting bearer can mint
// the Personal Access Token.
const cliScopes = "notes notebooks tags sync files search profile"

// patTokenPrefix is the distinctive prefix on a Personal Access Token, used to
// recognize a stored PAT (vs. a legacy session access token).
const patTokenPrefix = "hbp_"

// loginTimeout bounds how long we wait for the browser redirect before aborting.
const loginTimeout = 5 * time.Minute

// loginSummaryJSON builds the --json output for login: a curated session
// summary that omits the raw token unless showToken is set. A stored PAT never
// expires, so expires_at is 0 and never_expires is true.
func loginSummaryJSON(creds *config.Credentials, showToken bool, token string) []byte {
	m := map[string]any{
		"email":         creds.Email,
		"user_id":       creds.UserID,
		"scope":         creds.Scope,
		"api_url":       creds.BaseURL(),
		"token_type":    creds.TokenType,
		"expires_at":    creds.ExpiresAt,
		"never_expires": isNonExpiring(creds),
		"device_id":     creds.DeviceID,
	}
	if showToken {
		m["access_token"] = token
	}
	out, _ := json.Marshal(m)
	return out
}

// isNonExpiring reports whether the saved session is a long-lived credential
// (a Personal Access Token) that never expires and has no refresh token.
func isNonExpiring(creds *config.Credentials) bool {
	return creds != nil && creds.RefreshToken == "" && creds.ExpiresAt == 0 && creds.AccessToken != ""
}

// whoamiJSON builds the --json output for whoami: session status without
// secrets unless showToken is set.
func whoamiJSON(creds *config.Credentials, valid, showToken bool) []byte {
	m := map[string]any{
		"email":         creds.Email,
		"api_url":       creds.BaseURL(),
		"scope":         creds.Scope,
		"token_valid":   valid,
		"expires_at":    creds.ExpiresAt,
		"never_expires": isNonExpiring(creds),
		"device_id":     creds.DeviceID,
		"device_name":   creds.DeviceName,
	}
	if showToken {
		m["access_token"] = creds.AccessToken
	}
	out, _ := json.Marshal(m)
	return out
}

// tokenRefreshSkew is how far ahead of expiry we proactively refresh, so a
// request never races token expiry mid-flight.
const tokenRefreshSkew = 60 * time.Second

// applyToken copies a fresh token response into the credentials, computing the
// epoch-ms expiry from the OAuth expires_in (seconds).
func applyToken(creds *config.Credentials, tok *client.TokenResponse) {
	creds.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		creds.RefreshToken = tok.RefreshToken
	}
	if tok.TokenType != "" {
		creds.TokenType = tok.TokenType
	}
	if tok.Scope != "" {
		creds.Scope = tok.Scope
	}
	creds.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()
}

// refreshAndPersist rotates the refresh token and atomically persists the new
// pair. Persisting immediately is critical: a single-use refresh token that is
// rotated but not saved logs the user out. Returns the new access token and
// ok=false if the refresh failed (e.g. a revoked family → re-login needed).
func refreshAndPersist(c *client.Client, creds *config.Credentials) (string, bool) {
	_, tok, err := c.RefreshGrant(creds.EffectiveClientID(), creds.RefreshToken, "")
	if err != nil {
		return "", false
	}
	applyToken(creds, tok)
	if err := config.Save(creds); err != nil {
		return "", false
	}
	return tok.AccessToken, true
}

// newDeviceID mints a stable per-install device id (used for session/device
// tracking and sync). Generated once at first login and reused thereafter.
func newDeviceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "cli-unknown"
	}
	return "cli-" + hex.EncodeToString(b)
}

// defaultDeviceName describes this install for the session list.
func defaultDeviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return "harbor-cli on " + host
}

// sharedStdin is a single buffered reader over os.Stdin, lazily created. Multiple
// non-interactive prompts in one invocation (e.g. crypto rotate's new + confirm)
// MUST read from the same reader — a fresh bufio.Reader per call would let the
// first read buffer past its newline and leave the next call at EOF.
var sharedStdin *bufio.Reader

// stdinReader returns the process-wide buffered stdin reader.
func stdinReader() *bufio.Reader {
	if sharedStdin == nil {
		sharedStdin = bufio.NewReader(os.Stdin)
	}
	return sharedStdin
}

// promptLine reads a single trimmed line from stdin after printing a prompt.
func promptLine(label string) (string, error) {
	fmt.Print(label)
	line, err := stdinReader().ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptPassword reads a password without echoing it. On an interactive
// terminal it prints the label and reads with echo disabled. When stdin is a
// pipe (scripts, CI, AI agents), it reads a single line from stdin instead, so
// `printf 'secret\n' | harbor login --email …` works non-interactively. The
// bytes are never stored or logged.
func promptPassword(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := stdinReader().ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}
	return string(b), nil
}

// ===========================================================================
// Commands
// ===========================================================================

// authCmd is the parent for the auth lifecycle helpers (refresh, status, and
// the public email/password recovery flows).
var authCmd = &cobra.Command{
	Use:     "auth",
	Short:   "Authentication helpers (refresh, status, email/password recovery)",
	GroupID: groupAuth,
}

// loginCmd signs in through the browser (OAuth Authorization Code + PKCE) and
// stores a never-expiring Personal Access Token, or accepts a pasted token for
// headless use.
var loginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Log in through your browser",
	GroupID: groupAuth,
	Long: `Log in to Harbor by signing in through your browser.

The CLI opens Harbor's hosted sign-in page and catches the result on a local
loopback server — so passkeys, social login, and two-factor all work, and the
CLI never sees your password. It then stores a non-expiring Personal Access
Token in ~/.config/harbor/credentials.json (0600), so you stay signed in and
never have to re-authenticate.

Headless (SSH/CI) machines that can't open a browser can paste a token created
in the Harbor web app with --token, or set the HARBOR_TOKEN environment variable
to use a token for a single command without saving it.`,
	Example: `  harbor login
  harbor login --no-browser
  harbor login --token hbp_...
  harbor login --scope "notes notebooks sync profile"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")
		token, _ := cmd.Flags().GetString("token")
		noBrowser, _ := cmd.Flags().GetBool("no-browser")
		showToken, _ := cmd.Flags().GetBool("show-token")

		// Headless path: persist a pasted Personal Access Token directly.
		if strings.TrimSpace(token) != "" {
			return loginWithToken(strings.TrimSpace(token), showToken)
		}
		return loginViaBrowser(scope, noBrowser, showToken)
	},
}

// ===========================================================================
// Browser login (OAuth Authorization Code + PKCE → never-expiring PAT)
// ===========================================================================

// loginViaBrowser runs the full browser sign-in: it sends the user to Harbor's
// hosted consent page, catches the authorization code on a loopback server,
// exchanges it for a short-lived bearer, and uses that bearer to mint a
// never-expiring Personal Access Token which becomes the stored credential. The
// short-lived session is retired afterward so nothing but the PAT lingers.
func loginViaBrowser(scope string, noBrowser, showToken bool) error {
	base := resolveBaseURL(nil)

	// 1. PKCE verifier/challenge + a CSRF state.
	verifier, err := generateCodeVerifier()
	if err != nil {
		return err
	}
	state, err := generateState()
	if err != nil {
		return err
	}
	challenge := codeChallengeS256(verifier)

	// 2. Bind an ephemeral loopback port; its URL is the OAuth redirect target.
	redirectURI, resultCh, shutdown, err := startCallbackServer(state)
	if err != nil {
		return fmt.Errorf("unable to start the local sign-in listener: %w", err)
	}
	defer shutdown()

	// 3. Build the hosted authorize URL (the web origin's SPA route, not the
	//    API path) and send the user there.
	authURL := buildAuthorizeURL(client.NewClient(base, "").Origin(), redirectURI, scope, state, challenge)
	if noBrowser {
		fmt.Println("Open this URL in your browser to sign in:")
	} else {
		fmt.Println("Opening your browser to sign in…")
		if oerr := openBrowser(authURL); oerr != nil {
			fmt.Println("Couldn't open a browser automatically — open this URL manually:")
		}
	}
	fmt.Printf("\n  %s\n\n", authURL)
	fmt.Println("Waiting for you to finish signing in…")

	// 4. Wait for the loopback redirect (or Ctrl-C / timeout).
	code, err := waitForCode(resultCh)
	if err != nil {
		return err
	}

	// 5. Exchange the code for a short-lived bearer (public, no-refresh path).
	anon := client.NewClient(base, "")
	anon.Verbose = verboseFlag
	_, tok, err := anon.AuthorizationCodeGrant(cliClientID, code, redirectURI, verifier)
	if err != nil {
		return mapLoginError(err)
	}

	// 6. With that bearer, resolve identity and mint the durable PAT.
	authed := client.NewClient(base, tok.AccessToken)
	authed.Verbose = verboseFlag
	profile, err := authed.GetProfile()
	if err != nil {
		return mapLoginError(err)
	}
	email, userID := profileEmailID(profile)

	// Revoke any PAT this install previously stored BEFORE minting a new one,
	// so repeated logins don't accumulate against the per-user cap.
	revokePreviousPAT(authed)

	_, pat, err := authed.CreatePAT(defaultDeviceName(), splitScopes(tok.Scope), 0)
	if err != nil {
		return mapPATError(err)
	}

	// 7. Persist the PAT, then retire the short-lived session (its refresh token
	//    belongs to a different family, so this does not touch the PAT).
	creds, err := storePAT(base, pat.Token, tok.Scope, email, userID)
	if err != nil {
		return err
	}
	if tok.RefreshToken != "" {
		_ = anon.Revoke(tok.RefreshToken, "refresh_token")
	}

	reportLogin(creds, showToken, pat.Token)
	return nil
}

// loginWithToken validates a pasted Personal Access Token by fetching the
// profile with it, then persists it as the durable credential. This is the
// headless path for SSH/CI boxes where the loopback browser flow can't complete.
func loginWithToken(token string, showToken bool) error {
	base := resolveBaseURL(nil)
	c := client.NewClient(base, token)
	c.Verbose = verboseFlag
	profile, err := c.GetProfile()
	if err != nil {
		return mapLoginError(err)
	}
	email, userID := profileEmailID(profile)
	// The profile doesn't reveal the token's scopes; leave them blank (the
	// server enforces the PAT's real scopes regardless of what we display).
	creds, err := storePAT(base, token, "", email, userID)
	if err != nil {
		return err
	}
	reportLogin(creds, showToken, token)
	return nil
}

// storePAT builds and saves the credentials for a never-expiring Personal
// Access Token: no refresh token and ExpiresAt=0, since a PAT never expires and
// there is nothing to refresh. api_url is stored only when it differs from the
// production default, so a normal login still honors HARBOR_API_URL.
func storePAT(base, token, scope, email, userID string) (*config.Credentials, error) {
	apiToStore := base
	if apiToStore == config.DefaultBaseURL {
		apiToStore = ""
	}
	deviceID, deviceName := deviceIdentity()
	creds := &config.Credentials{
		APIURL:      apiToStore,
		ClientID:    cliClientID,
		Email:       email,
		UserID:      userID,
		AccessToken: token,
		TokenType:   "Bearer",
		Scope:       scope,
		ExpiresAt:   0, // a PAT never expires
		DeviceID:    deviceID,
		DeviceName:  deviceName,
	}
	if err := config.Save(creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// revokePreviousPAT best-effort revokes a PAT this install previously saved, so
// a re-login doesn't leave orphaned tokens counting against the per-user cap.
// It's a no-op when nothing is saved or the saved token isn't a PAT.
func revokePreviousPAT(c *client.Client) {
	existing, err := config.Load()
	if err != nil {
		return
	}
	if strings.HasPrefix(existing.AccessToken, patTokenPrefix) {
		_ = c.Revoke(existing.AccessToken, "access_token")
	}
}

// deviceIdentity returns a stable device id + name for this install, reusing any
// already saved so the device identity is consistent across logins.
func deviceIdentity() (string, string) {
	deviceID := ""
	deviceName := defaultDeviceName()
	if existing, err := config.Load(); err == nil {
		deviceID = existing.DeviceID
		if existing.DeviceName != "" {
			deviceName = existing.DeviceName
		}
	}
	if deviceID == "" {
		deviceID = newDeviceID()
	}
	return deviceID, deviceName
}

// reportLogin prints the success confirmation (human table or --json summary).
func reportLogin(creds *config.Credentials, showToken bool, token string) {
	if jsonOutput {
		printResult(loginSummaryJSON(creds, showToken, token), func([]byte) {})
		return
	}
	fmt.Printf("Logged in as %s\n", bold(creds.Email))
	if creds.Scope != "" {
		fmt.Printf("Scopes: %s\n", creds.Scope)
	}
	fmt.Println("Saved a non-expiring access token — the CLI will stay signed in.")
	if showToken {
		fmt.Printf("Access token: %s\n", token)
	}
}

// buildAuthorizeURL constructs the hosted consent URL the browser opens. It
// targets the web origin's /oauth/authorize SPA route (NOT the API path);
// anonymous visitors are bounced through the normal login and back.
func buildAuthorizeURL(origin, redirectURI, scope, state, challenge string) string {
	if strings.TrimSpace(scope) == "" {
		scope = cliScopes
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cliClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return strings.TrimRight(origin, "/") + "/oauth/authorize?" + q.Encode()
}

// generateCodeVerifier returns a high-entropy PKCE code verifier: 32 random
// bytes, base64url-encoded (43 chars, within RFC 7636's 43–128 range).
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("unable to generate PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallengeS256 derives the S256 PKCE challenge from a verifier:
// BASE64URL(SHA-256(verifier)) with no padding.
func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// generateState returns a random CSRF state parameter for the authorize request.
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("unable to generate login state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// callbackResult is what the loopback listener captured from the OAuth redirect:
// an authorization code on success, or an error (an OAuth error param, a state
// mismatch, or a missing code).
type callbackResult struct {
	code string
	err  error
}

// startCallbackServer binds an ephemeral loopback port and serves the OAuth
// redirect target at /callback. It returns the redirect URI to advertise, a
// channel that yields the single result, and a shutdown func the caller must
// defer. The path is fixed to /callback and the host to 127.0.0.1 because those
// are what the harbor-cli client's redirect URIs are registered to match.
func startCallbackServer(state string) (string, <-chan callbackResult, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	resultCh := make(chan callbackResult, 1)
	// send delivers at most one result; extra hits (page refreshes) are dropped.
	send := func(res callbackResult) {
		select {
		case resultCh <- res:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			msg := e
			if desc := q.Get("error_description"); desc != "" {
				msg = e + ": " + desc
			}
			writeCallbackPage(w, false, "Authorization was denied ("+msg+"). You can close this tab.")
			send(callbackResult{err: fmt.Errorf("authorization denied: %s", msg)})
			return
		}
		if q.Get("state") != state {
			writeCallbackPage(w, false, "Security check failed (state mismatch). You can close this tab.")
			send(callbackResult{err: errors.New("state mismatch — aborting login (possible CSRF)")})
			return
		}
		code := q.Get("code")
		if code == "" {
			writeCallbackPage(w, false, "No authorization code was returned. You can close this tab.")
			send(callbackResult{err: errors.New("no authorization code in the sign-in callback")})
			return
		}
		writeCallbackPage(w, true, "")
		send(callbackResult{code: code})
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return redirectURI, resultCh, shutdown, nil
}

// waitForCode blocks until the loopback listener reports a result, the user
// aborts with Ctrl-C, or the login times out.
func waitForCode(resultCh <-chan callbackResult) (string, error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	select {
	case res := <-resultCh:
		if res.err != nil {
			return "", res.err
		}
		return res.code, nil
	case <-sig:
		return "", errors.New("login canceled")
	case <-time.After(loginTimeout):
		return "", errors.New("timed out waiting for browser sign-in — run 'harbor login' to try again")
	}
}

// callbackHTML is the minimal self-contained landing page shown in the browser
// after the redirect. It takes a title and a body message.
const callbackHTML = `<!doctype html><html><head><meta charset="utf-8"><title>%s — Harbor CLI</title>` +
	`<style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;` +
	`display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#0f172a;color:#e2e8f0}` +
	`.card{max-width:28rem;padding:2rem;text-align:center}h1{font-size:1.25rem;margin:0 0 .5rem}` +
	`p{color:#94a3b8;line-height:1.5;margin:0}</style></head>` +
	`<body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`

// writeCallbackPage renders the browser landing page. Connection: close lets the
// local server shut down promptly instead of lingering on a keep-alive socket.
func writeCallbackPage(w http.ResponseWriter, ok bool, failMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Connection", "close")
	title, heading, body := "Signed in", "You're signed in ✓",
		"Harbor CLI is now authenticated. You can close this tab and return to your terminal."
	if !ok {
		title, heading, body = "Sign-in failed", "Sign-in failed", failMsg
	}
	fmt.Fprintf(w, callbackHTML, title, heading, body)
}

// openBrowser opens a URL in the user's default browser. It is a package var so
// tests can stub the browser step. It returns an error if the platform opener
// fails to start.
var openBrowser = func(rawURL string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default: // linux, *bsd, etc.
		name, args = "xdg-open", []string{rawURL}
	}
	return exec.Command(name, args...).Start()
}

// profileEmailID pulls the email and user id from a profile response
// (data-wrapped). Missing fields yield empty strings.
func profileEmailID(data []byte) (email, id string) {
	var p struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	_ = json.Unmarshal(client.UnwrapData(data), &p)
	return p.Email, p.ID
}

// splitScopes turns a space-delimited scope string into a slice, dropping empty
// fields and falling back to the CLI's full scope set when the grant is empty.
func splitScopes(scope string) []string {
	if strings.TrimSpace(scope) == "" {
		scope = cliScopes
	}
	return strings.Fields(scope)
}

// logoutCmd revokes the session server-side and clears local credentials.
var logoutCmd = &cobra.Command{
	Use:     "logout",
	Short:   "Log out and clear saved credentials",
	GroupID: groupAuth,
	Long:    "Revokes the current session on the server (or all sessions with --all-devices) and removes the local credentials file. Idempotent.",
	Example: `  harbor logout
  harbor logout --all-devices`,
	RunE: func(cmd *cobra.Command, args []string) error {
		allDevices, _ := cmd.Flags().GetBool("all-devices")

		creds, err := config.Load()
		if err != nil {
			fmt.Println("Not logged in.")
			return nil
		}

		c, _, err := loadClientFromConfig()
		if err == nil {
			// Best-effort server-side revocation; clear locally regardless.
			_ = c.Logout(allDevices)
			// Revoke the durable credential: a refresh token takes its whole
			// family; otherwise revoke the access token (e.g. a stored PAT).
			if creds.RefreshToken != "" {
				_ = c.Revoke(creds.RefreshToken, "refresh_token")
			} else if creds.AccessToken != "" {
				_ = c.Revoke(creds.AccessToken, "access_token")
			}
		}
		if err := config.Clear(); err != nil {
			return err
		}
		// Drop the cached (wrapped) keystore blob too, so a logout leaves no
		// account-specific encryption state behind.
		_ = config.ClearKeystoreBlob()
		if allDevices {
			fmt.Println("Logged out of all devices.")
		} else {
			fmt.Println("Logged out.")
		}
		return nil
	},
}

// whoamiCmd shows the current session status.
var whoamiCmd = &cobra.Command{
	Use:     "whoami",
	Short:   "Show the current session (alias: auth status)",
	GroupID: groupAuth,
	Long:    "Displays the logged-in email, API target, granted scopes, token expiry, and device identity.",
	RunE:    runWhoami,
}

// authStatusCmd is the `auth status` alias of whoami.
var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current session status",
	RunE:  runWhoami,
}

// runWhoami renders the saved session details (offline; no network call).
func runWhoami(cmd *cobra.Command, args []string) error {
	creds, err := config.Load()
	if err != nil {
		return err
	}
	showToken, _ := cmd.Flags().GetBool("show-token")
	// A stored PAT never expires and needs no refresh; treat it as always valid.
	neverExpires := isNonExpiring(creds)
	valid := neverExpires || !creds.IsExpired(0)

	if jsonOutput {
		printResult(whoamiJSON(creds, valid, showToken), func([]byte) {})
		return nil
	}
	expires := "never"
	if !neverExpires {
		expires = fmt.Sprintf("%s (%s)", relTime(float64(creds.ExpiresAt)), epochMS(float64(creds.ExpiresAt)))
	}
	apiURL := creds.BaseURL()
	pairs := [][2]string{
		{"Email", creds.Email},
		{"API URL", apiURL},
		{"Scopes", creds.Scope},
		{"Token valid", boolMark(valid)},
		{"Expires", expires},
		{"Device", fmt.Sprintf("%s (%s)", creds.DeviceName, creds.DeviceID)},
	}
	if showToken {
		pairs = append(pairs, [2]string{"Access token", creds.AccessToken})
	}
	printKV(pairs)
	if !valid {
		fmt.Println(dim("Access token is expired; it will refresh on the next command."))
	}
	return nil
}

// authRefreshCmd forces an immediate token refresh.
var authRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Force a token refresh now",
	Long:  "Rotates the refresh token immediately and prints the new expiry. Tokens normally refresh transparently; this is for diagnostics.",
	RunE: func(cmd *cobra.Command, args []string) error {
		creds, err := config.Load()
		if err != nil {
			return err
		}
		if creds.RefreshToken == "" {
			return errors.New("this session uses a non-expiring access token — there is nothing to refresh")
		}
		c := client.NewClient(resolveBaseURL(creds), creds.AccessToken)
		c.Verbose = verboseFlag
		data, tok, err := c.RefreshGrant(creds.EffectiveClientID(), creds.RefreshToken, "")
		if err != nil {
			return mapRefreshError(err)
		}
		applyToken(creds, tok)
		if err := config.Save(creds); err != nil {
			return err
		}
		if jsonOutput {
			printResult(data, func([]byte) {})
			return nil
		}
		fmt.Printf("Token refreshed. Expires %s (%s)\n", relTime(float64(creds.ExpiresAt)), epochMS(float64(creds.ExpiresAt)))
		return nil
	},
}

// verifyEmailCmd consumes an email-verification token (public).
var verifyEmailCmd = &cobra.Command{
	Use:     "verify-email",
	Short:   "Verify your email with a verification token",
	Example: "  harbor auth verify-email --token ev_...",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, _ := cmd.Flags().GetString("token")
		if token == "" {
			return errors.New("--token is required")
		}
		data, err := newAnonymousClient().VerifyEmail(token)
		if err != nil {
			return err
		}
		printResult(data, displayMessage("Email verified."))
		return nil
	},
}

// resendVerificationCmd requests a new verification email (anti-enumeration).
var resendVerificationCmd = &cobra.Command{
	Use:   "resend-verification",
	Short: "Resend the email-verification message",
	RunE: func(cmd *cobra.Command, args []string) error {
		email, _ := cmd.Flags().GetString("email")
		// Prefer an authenticated call when logged in; otherwise send the email.
		c := newAnonymousClient()
		if creds, err := config.Load(); err == nil && creds.AccessToken != "" {
			c = client.NewClient(resolveBaseURL(creds), creds.AccessToken)
		} else if email == "" {
			if email, err = promptLine("Email: "); err != nil {
				return err
			}
		}
		data, err := c.ResendVerification(email)
		if err != nil {
			return err
		}
		printResult(data, displayMessage("If that email exists and is unverified, a verification message was sent."))
		return nil
	},
}

// forgotPasswordCmd starts a password reset (anti-enumeration).
var forgotPasswordCmd = &cobra.Command{
	Use:     "forgot-password",
	Short:   "Request a password-reset email",
	Example: "  harbor auth forgot-password --email you@example.com",
	RunE: func(cmd *cobra.Command, args []string) error {
		email, _ := cmd.Flags().GetString("email")
		var err error
		if email == "" {
			if email, err = promptLine("Email: "); err != nil {
				return err
			}
		}
		data, err := newAnonymousClient().ForgotPassword(email)
		if err != nil {
			return err
		}
		printResult(data, displayMessage("If that email exists, a password-reset message was sent."))
		return nil
	},
}

// resetPasswordCmd completes a password reset with a token + new password.
var resetPasswordCmd = &cobra.Command{
	Use:     "reset-password",
	Short:   "Reset your password using a reset token",
	Long:    "Completes a password reset. Prompts for the new password (hidden). All existing sessions are revoked on success.",
	Example: "  harbor auth reset-password --token pr_...",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, _ := cmd.Flags().GetString("token")
		if token == "" {
			return errors.New("--token is required")
		}
		pw, err := promptPassword("New password: ")
		if err != nil {
			return err
		}
		confirm, err := promptPassword("Confirm new password: ")
		if err != nil {
			return err
		}
		if pw != confirm {
			return errors.New("passwords do not match")
		}
		data, err := newAnonymousClient().ResetPassword(token, pw)
		if err != nil {
			return mapWeakPassword(err)
		}
		printResult(data, displayMessage("Password reset. All sessions were revoked — run 'harbor login' to sign in again."))
		return nil
	},
}

// mapLoginError translates raw browser-login / token-exchange errors into
// friendly guidance.
func mapLoginError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "invalid_grant":
			return errors.New("the sign-in code was invalid or expired — run 'harbor login' and complete the browser step promptly")
		case "invalid_token":
			return errors.New("that token is invalid or has been revoked — create a new one in the Harbor web app, or run 'harbor login'")
		case "invalid_client":
			return errors.New("unknown OAuth client (harbor-cli) — your CLI build may be misconfigured")
		case "invalid_scope":
			return errors.New("requested scope exceeds what this client allows")
		}
	}
	return err
}

// mapPATError gives friendly guidance when minting the Personal Access Token
// fails — most importantly the per-user cap, which the CLI can't clear itself
// (there is no delete-token endpoint yet).
func mapPATError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "pat_limit_reached":
			return errors.New("you've reached the maximum number of access tokens — delete an old one in the Harbor web app (Settings → Tokens), then run 'harbor login' again")
		case "insufficient_scope", "forbidden":
			return errors.New("this sign-in is missing the 'profile' scope needed to create a token — run 'harbor login' again")
		}
	}
	return err
}

// mapRefreshError gives a clear "session expired" message on a dead refresh
// token (e.g. a revoked family).
func mapRefreshError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "invalid_grant" {
		return errors.New("session expired — run 'harbor login'")
	}
	return err
}

// mapWeakPassword surfaces the strength-policy details on a rejected password.
func mapWeakPassword(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == "weak_password" || apiErr.Code == "password_reused") {
		return err // renderError already prints details bullets
	}
	return err
}

func init() {
	loginCmd.Flags().String("scope", "", "Space-delimited scopes to request (default: the CLI's full set)")
	loginCmd.Flags().String("token", "", "Log in with a pasted Personal Access Token (headless; create one in the web app)")
	loginCmd.Flags().Bool("no-browser", false, "Print the sign-in URL instead of opening a browser")
	loginCmd.Flags().Bool("show-token", false, "Print the access token in the output")

	logoutCmd.Flags().Bool("all-devices", false, "Revoke every session, not just this one")

	whoamiCmd.Flags().Bool("show-token", false, "Include the access token in the output")
	authStatusCmd.Flags().Bool("show-token", false, "Include the access token in the output")

	verifyEmailCmd.Flags().String("token", "", "Email-verification token (required)")
	resendVerificationCmd.Flags().String("email", "", "Email to send to (when not logged in)")
	forgotPasswordCmd.Flags().String("email", "", "Account email")
	resetPasswordCmd.Flags().String("token", "", "Password-reset token (required)")

	authCmd.AddCommand(authRefreshCmd, authStatusCmd, verifyEmailCmd, resendVerificationCmd, forgotPasswordCmd, resetPasswordCmd)
	rootCmd.AddCommand(loginCmd, logoutCmd, whoamiCmd, authCmd)
}

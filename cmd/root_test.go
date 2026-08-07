// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-25

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestMain clears the environment the CLI reads before any test runs. `go test`
// inherits the developer's shell, and the documented way to use this CLI is to
// export HARBOR_PASSPHRASE from a secret manager — so on a working machine the
// suite would otherwise run with encryption quietly switched ON, and take
// different branches than it takes in CI. Tests that want any of these set them
// with t.Setenv, which still wins.
func TestMain(m *testing.M) {
	for _, key := range []string{"HARBOR_PASSPHRASE", "HARBOR_NEW_PASSPHRASE", "HARBOR_TOKEN", "HARBOR_API_URL"} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

// ===========================================================================
// RunE execution harness
// ===========================================================================
//
// Display functions and helpers can be called directly, but that leaves the
// WIRING untested — whether a command actually consults the guard it is supposed
// to consult, and whether it reports failure as an error (exit 1, stderr) rather
// than a printed line (exit 0, stdout). These helpers run the real cobra tree
// against a mock API so the call sites are pinned too, not just their parts.

// mockRequest is one request a command actually sent, as observed by the server.
// Query is kept separate from Path so a test can pin the filters a list command
// forwarded — dropping one silently returns the wrong rows rather than failing.
type mockRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
}

// mockReply is a canned response for a "METHOD /path" route.
type mockReply struct {
	Status int
	Body   string
}

// apiMock is a stub Harbor API. It records every request a command made — which
// is how a test proves a destructive call did NOT happen — and replies from a
// route table. An unrouted request is a test failure, not a silent 404.
type apiMock struct {
	t        *testing.T
	srv      *httptest.Server
	routes   map[string]mockReply
	requests []mockRequest

	// handler, when set, answers instead of the route table. A fixed table can
	// only say one thing per path, which cannot express an answer that CHANGES
	// between requests ("queued, then running, then completed") or one that
	// carries response headers — both of which the export commands read. Requests
	// are still recorded, so the traffic assertions work unchanged.
	handler http.HandlerFunc
}

// newAPIMock starts a stub API. Routes are keyed "METHOD /path", e.g.
// "GET /api/v1/profile" — the CLI's base URL includes the /api/v1 prefix.
func newAPIMock(t *testing.T, routes map[string]mockReply) *apiMock {
	t.Helper()
	m := &apiMock{t: t, routes: routes}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.requests = append(m.requests, mockRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: string(body),
		})
		if m.handler != nil {
			m.handler(w, r)
			return
		}
		reply, ok := m.routes[r.Method+" "+r.URL.Path]
		if !ok {
			m.t.Errorf("apiMock: unrouted request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.Status)
		_, _ = w.Write([]byte(reply.Body))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// baseURL is the value HARBOR_API_URL is set to for a run: the /api/v1 prefix is
// part of the base URL, exactly as in a real credentials file.
func (m *apiMock) baseURL() string { return m.srv.URL + "/api/v1" }

// calls renders the recorded traffic as "METHOD /path" strings, for asserting
// both what was sent and — just as importantly — what was not.
func (m *apiMock) calls() []string {
	out := make([]string, 0, len(m.requests))
	for _, r := range m.requests {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// bodyOf returns the decoded JSON body of the first request matching
// "METHOD /path", so a test can pin the exact wire payload.
func (m *apiMock) bodyOf(t *testing.T, methodAndPath string) map[string]any {
	t.Helper()
	for _, r := range m.requests {
		if r.Method+" "+r.Path == methodAndPath {
			var body map[string]any
			if r.Body == "" {
				return nil
			}
			if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
				t.Fatalf("bodyOf(%s): %v (raw %q)", methodAndPath, err, r.Body)
			}
			return body
		}
	}
	t.Fatalf("bodyOf(%s): no such request in %v", methodAndPath, m.calls())
	return nil
}

// queryOf returns the query parameters of the first request matching
// "METHOD /path", so a test can pin the filters a command actually forwarded.
func (m *apiMock) queryOf(t *testing.T, methodAndPath string) url.Values {
	t.Helper()
	for _, r := range m.requests {
		if r.Method+" "+r.Path == methodAndPath {
			return r.Query
		}
	}
	t.Fatalf("queryOf(%s): no such request in %v", methodAndPath, m.calls())
	return nil
}

// rawBodyOf returns the request body of the first request matching
// "METHOD /path" as the exact bytes sent, for the cases where the spelling
// matters and a decoded map would erase it (JSON `null` and `{}` both decode to
// an empty map).
func (m *apiMock) rawBodyOf(t *testing.T, methodAndPath string) string {
	t.Helper()
	for _, r := range m.requests {
		if r.Method+" "+r.Path == methodAndPath {
			return r.Body
		}
	}
	t.Fatalf("rawBodyOf(%s): no such request in %v", methodAndPath, m.calls())
	return ""
}

// runCLI executes the real command tree — the same rootCmd main() runs — with
// args, pointed at a mock API by a HARBOR_TOKEN/HARBOR_API_URL pair (the
// documented way to run one command without a stored session). It returns
// stdout plus the error Execute() would render to stderr and exit 1 on, so a
// test can tell "printed a notice and exited 0" apart from "failed".
//
// HOME is a temp dir so a run can never read or write the real credentials file.
func runCLI(t *testing.T, m *apiMock, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_API_URL", m.baseURL())
	t.Setenv("HARBOR_TOKEN", "hbp_test-token-not-a-real-credential")
	resetCommandState(t)
	prepareCommandTree()

	rootCmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	return out, err
}

// resetCommandState returns the shared command tree to its default state. The
// commands and the global flag variables are package-level, so cobra carries
// flag values and Changed markers from one Execute to the next; without this a
// --json or --yes from an earlier test leaks into a later one.
func resetCommandState(t *testing.T) {
	t.Helper()
	clear := func() {
		jsonOutput = false
		verboseFlag = false
		utcFlag = false
		apiURLFlag = ""
		// The notebook lookups a run memoizes are keyed by id alone, which is right
		// for a process that runs one command and wrong for a test binary that runs
		// hundreds against different stub servers — "nb1 encrypts" would outlive the
		// server that said so.
		notebookEncryptionLookups = nil
		resetFlags(rootCmd)
	}
	clear()
	t.Cleanup(clear)
}

// resetFlags restores every flag in a command tree to its declared default.
// Slice/array flags are skipped: pflag appends to those on Set, so "resetting"
// one would grow it instead.
func resetFlags(c *cobra.Command) {
	restore := func(f *pflag.Flag) {
		if strings.Contains(f.Value.Type(), "Slice") || strings.Contains(f.Value.Type(), "Array") {
			return
		}
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
	c.Flags().VisitAll(restore)
	c.PersistentFlags().VisitAll(restore)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// apiErrorBody builds an error-envelope response body for a mock route.
func apiErrorBody(code, message string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_test"}}`, code, message)
}

// ===========================================================================
// Harness self-checks
// ===========================================================================

// The harness is only worth trusting if a failing command really surfaces as an
// error rather than as printed output, since that distinction is what several
// tests below rest on.
func TestRunCLISurfacesAPIErrorsAsErrors(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/profile": {Status: 500, Body: apiErrorBody("internal", "boom")},
	})
	out, err := runCLI(t, m, "profile", "get")
	if err == nil {
		t.Fatal("a 500 must be returned as an error (Execute exits 1), not printed")
	}
	if strings.Contains(out, "boom") {
		t.Errorf("error text must not go to stdout:\n%s", out)
	}
}

// Flag state must not leak between runs, or a later test could pass only because
// an earlier one set --json.
func TestResetCommandStateClearsJSONFlag(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/profile": {Status: 200, Body: `{"data":{"id":"u1","name":"Jane"}}`},
	})
	if _, err := runCLI(t, m, "profile", "get", "--json"); err != nil {
		t.Fatalf("profile get --json: %v", err)
	}
	if !jsonOutput {
		t.Fatal("--json did not take effect")
	}
	if _, err := runCLI(t, m, "profile", "get"); err != nil {
		t.Fatalf("profile get: %v", err)
	}
	if jsonOutput {
		t.Error("--json leaked into the next run")
	}
}

// ===========================================================================
// Exit codes
// ===========================================================================
//
// A CLI is scripted, so the exit code carries as much as the message: a plan
// limit will never succeed on retry, an unreachable API very well might, and a
// script has to tell them apart without reading English. These run through the
// real command tree and classify the error exactly as Execute() does.

// TestPlanLimitedCreateExitsWithThePlanLimitCode is the scriptable half of the
// feature: a create refused at a plan cap must be distinguishable by code alone.
func TestPlanLimitedCreateExitsWithThePlanLimitCode(t *testing.T) {
	body := `{"error":{"code":"plan_limit_reached","message":"You've reached your plan's limit of 3 notebooks.",
	  "details":{"resource":"notebook","used":"3","limit":"3","plan_code":"starter","upgrade_url":"/settings/plan","gate":"plan_limit"},
	  "request_id":"req_test"}}`
	m := newAPIMock(t, map[string]mockReply{"POST /api/v1/notebooks": {Status: 403, Body: body}})

	_, err := runCLI(t, m, "notebooks", "create", "--name", "Overflow")
	if err == nil {
		t.Fatal("a refused create must fail, not print and exit 0")
	}
	if got := exitCodeFor(err); got != exitPlanLimit {
		t.Errorf("exit code = %d, want %d (plan limit)", got, exitPlanLimit)
	}
}

// TestReadOnlyAccountExitsWithThePlanLimitCode covers the other gate behind the
// same error code — the whole-account freeze — which scripts must treat the
// same way.
func TestReadOnlyAccountExitsWithThePlanLimitCode(t *testing.T) {
	body := `{"error":{"code":"plan_limit_reached","message":"Your account is read-only.",
	  "details":{"gate":"account_read_only","reason":"account_read_only","plan_code":"starter"},"request_id":"req_test"}}`
	// Every create now reads the destination notebook's default_encrypt first, so the
	// account's notebooks have to be answerable even for a create that never lands.
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/notes": {Status: 403, Body: body},
		"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[{"id":"nb1","is_default":true,"default_encrypt":false}],` +
			`"paging":{"limit":500,"offset":0,"total":1,"has_more":false}}`},
	})

	_, err := runCLI(t, m, "notes", "create", "--title", "Blocked", "--content", "x")
	if err == nil {
		t.Fatal("a frozen account must fail the create")
	}
	if got := exitCodeFor(err); got != exitPlanLimit {
		t.Errorf("exit code = %d, want %d (plan limit)", got, exitPlanLimit)
	}
}

// TestUnreachableAPIExitsWithTheNetworkCode pins the retryable class. The port
// is one nothing listens on, so the transport fails before any HTTP exchange.
func TestUnreachableAPIExitsWithTheNetworkCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_API_URL", "http://127.0.0.1:1/api/v1")
	t.Setenv("HARBOR_TOKEN", "hbp_test-token-not-a-real-credential")
	resetCommandState(t)

	rootCmd.SetArgs([]string{"usage"})
	var err error
	captureStdout(t, func() { err = rootCmd.Execute() })
	if err == nil {
		t.Fatal("an unreachable API must fail")
	}
	if got := exitCodeFor(err); got != exitNetwork {
		t.Errorf("exit code = %d, want %d (network)", got, exitNetwork)
	}
}

// TestOrdinaryFailuresKeepTheGenericExitCode is the other side of the contract:
// only the two carved-out classes get their own code, so existing scripts that
// test for a plain non-zero keep working.
func TestOrdinaryFailuresKeepTheGenericExitCode(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/notebooks": {Status: 422, Body: apiErrorBody("validation_failed", "name is required")},
	})
	_, err := runCLI(t, m, "notebooks", "create", "--name", "x")
	if err == nil {
		t.Fatal("a 422 must fail")
	}
	if got := exitCodeFor(err); got != exitError {
		t.Errorf("validation error exit code = %d, want %d", got, exitError)
	}

	if got := exitCodeFor(errors.New("nothing to update")); got != exitError {
		t.Errorf("local error exit code = %d, want %d", got, exitError)
	}
	if got := exitCodeFor(nil); got != exitOK {
		t.Errorf("nil exit code = %d, want %d", got, exitOK)
	}
}

// TestHTTPErrorIsNotMistakenForANetworkFailure guards the classification seam:
// an answer from the server — even a 503 — is not a transport failure, and
// telling a script to retry a rejection would loop it forever.
func TestHTTPErrorIsNotMistakenForANetworkFailure(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/usage": {Status: 503, Body: apiErrorBody("timeout", "upstream unavailable")},
	})
	_, err := runCLI(t, m, "usage")
	if err == nil {
		t.Fatal("a 503 must fail")
	}
	if isNetworkError(err) {
		t.Error("an HTTP error response was classified as a network failure")
	}
	if got := exitCodeFor(err); got != exitError {
		t.Errorf("exit code = %d, want %d", got, exitError)
	}
}

// ===========================================================================
// The argument contract (#69 F4)
// ===========================================================================
//
// A command that accepts input it does not understand and still exits 0 is the
// worst failure a CLI has: a wrapper script cannot detect it at all. These pin
// both halves — a subcommand that does not exist must fail, and a bare parent
// must still answer with its help.

// TestUnknownSubcommandFailsInsteadOfPrintingHelp is the regression for the
// reported bug: `harbor files delete` printed the files help and exited 0, so a
// shell probe concluded the command existed.
func TestUnknownSubcommandFailsInsteadOfPrintingHelp(t *testing.T) {
	for _, args := range [][]string{
		{"files", "delete"},
		{"notes", "bogus"},
		{"profile", "inbound-email", "bogus"},
	} {
		m := newAPIMock(t, map[string]mockReply{})
		out, err := runCLI(t, m, args...)
		if err == nil {
			t.Fatalf("%v: an unknown subcommand must not exit 0", args)
		}
		if got := exitCodeFor(err); got != exitError {
			t.Errorf("%v: exit code = %d, want %d (same as any other bad usage)", args, got, exitError)
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%v: message = %q, want an unknown-command error", args, err.Error())
		}
		if out != "" {
			t.Errorf("%v: a failure must print nothing on stdout, got:\n%s", args, out)
		}
		if len(m.calls()) != 0 {
			t.Errorf("%v: nothing should have been sent to the API, got %v", args, m.calls())
		}
	}
}

// TestBareParentCommandStillPrintsItsHelp guards the other direction: asking
// what lives under a parent is a legitimate question with a successful answer.
func TestBareParentCommandStillPrintsItsHelp(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	out, err := runCLI(t, m, "files")
	if err != nil {
		t.Fatalf("`harbor files` must still succeed: %v", err)
	}
	if !strings.Contains(out, "Available Commands:") || !strings.Contains(out, "upload") {
		t.Errorf("`harbor files` should print its help:\n%s", out)
	}
}

// TestStrayPositionalArgumentIsRefused covers the quieter half of the same bug:
// a command that never declared an Args validator silently dropped positional
// arguments, so `harbor tags list receipts` listed every tag and exited 0 as
// though it had filtered.
func TestStrayPositionalArgumentIsRefused(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	out, err := runCLI(t, m, "tags", "list", "receipts")
	if err == nil {
		t.Fatal("a stray positional argument must not be silently ignored")
	}
	if out != "" {
		t.Errorf("nothing should reach stdout, got:\n%s", out)
	}
	if len(m.calls()) != 0 {
		t.Errorf("the request must not be sent at all, got %v", m.calls())
	}
}

// TestEveryCommandDeclaresItsArgumentContract is the rule rather than the
// instances: after the tree is prepared, no command is left in the state that
// caused this bug — un-runnable with subcommands, or silently accepting any
// positional argument. It is what keeps a command added next year covered.
func TestEveryCommandDeclaresItsArgumentContract(t *testing.T) {
	prepareCommandTree()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		// `help` takes a command path, which is exactly why it is exempt.
		if c.Name() == "help" {
			return
		}
		if c.HasSubCommands() && !c.Runnable() {
			t.Errorf("%s groups subcommands but has no action, so an unknown subcommand exits 0", c.CommandPath())
		}
		if c.Args == nil {
			t.Errorf("%s accepts arbitrary positional arguments and ignores them", c.CommandPath())
		}
	}
	walk(rootCmd)
}

// TestUnknownSubcommandMessageMatchesCobra keeps one voice for one mistake: the
// text for `harbor files bogus` is the text cobra prints for `harbor bogus`,
// suggestion block included, so a person (or a script reading stderr) sees the
// same thing whichever level the typo was on.
func TestUnknownSubcommandMessageMatchesCobra(t *testing.T) {
	prepareCommandTree()
	notes, _, err := rootCmd.Find([]string{"notes"})
	if err != nil {
		t.Fatal(err)
	}
	msg := unknownSubcommandMessage(notes, "lst")
	if !strings.HasPrefix(msg, `unknown command "lst" for "harbor notes"`) {
		t.Errorf("message = %q", msg)
	}
	if !strings.Contains(msg, "Did you mean this?") || !strings.Contains(msg, "\tlist") {
		t.Errorf("a near-miss should suggest the real command: %q", msg)
	}
	if got := unknownSubcommandMessage(notes, "zzzzzzzz"); strings.Contains(got, "Did you mean") {
		t.Errorf("nothing is close to %q, so no suggestion block: %q", "zzzzzzzz", got)
	}
}

// TestExitCoderDecidesItsOwnCode pins the seam sync push relies on: an error
// that knows its own code outranks the generic classification.
func TestExitCoderDecidesItsOwnCode(t *testing.T) {
	err := &syncPushRejectedError{APIError: &client.APIError{Code: "sync_push_rejected", Message: "refused"}, planLimit: true}
	if got := exitCodeFor(err); got != exitPlanLimit {
		t.Errorf("exit code = %d, want %d", got, exitPlanLimit)
	}
	if got := exitCodeFor(fmt.Errorf("wrapped: %w", err)); got != exitPlanLimit {
		t.Errorf("wrapped exit code = %d, want %d", got, exitPlanLimit)
	}
}

// TestPreparingTheCommandTreeTwiceChangesNothing guards the property the whole
// contract rests on. The tree is package-level state shared by every run in a
// process, and prepareCommandTree is called on each one, so a rule that only
// half-applies on the first pass gives the same command two behaviours
// depending on what ran before it. That is not hypothetical: while parents were
// left with a nil Args until a SECOND pass filled it in, the guard below passed
// only when another test had already run, and the second pass quietly swapped
// the suggestion-carrying message for cobra's bare one.
func TestPreparingTheCommandTreeTwiceChangesNothing(t *testing.T) {
	prepareCommandTree()
	notes, _, err := rootCmd.Find([]string{"notes"})
	if err != nil {
		t.Fatal(err)
	}
	if notes.Args == nil {
		t.Fatal("one pass must leave a parent with an argument contract, not wait for a second")
	}
	firstRunnable, firstErr := notes.Runnable(), notes.Args(notes, []string{"lst"})

	prepareCommandTree()

	if notes.Runnable() != firstRunnable {
		t.Error("a second pass changed whether the parent is runnable")
	}
	secondErr := notes.Args(notes, []string{"lst"})
	if fmt.Sprint(firstErr) != fmt.Sprint(secondErr) {
		t.Errorf("a second pass changed the rejection:\n first: %v\nsecond: %v", firstErr, secondErr)
	}
	if firstErr == nil || !strings.Contains(firstErr.Error(), "Did you mean this?") {
		t.Errorf("an unknown subcommand should still suggest the real one: %v", firstErr)
	}
}

// TestEnvDeviceIDIsStableAndSecret pins the three properties a derived device id
// must have: the same token always yields the same device (a fresh id per run
// would register a new device on every CI job and hold back the sync GC floor),
// different tokens yield different devices, and no part of the token survives
// into the id that is sent to the server on every push.
func TestEnvDeviceIDIsStableAndSecret(t *testing.T) {
	const token = "hbp_super-secret-token-value-not-real"

	first, second := envDeviceID(token), envDeviceID(token)
	if first != second {
		t.Fatalf("not stable across calls: %q vs %q", first, second)
	}
	if other := envDeviceID(token + "x"); other == first {
		t.Error("two different tokens produced the same device id")
	}
	if !strings.HasPrefix(first, "cli-env-") {
		t.Errorf("device id = %q, want a cli-env- prefix so it is recognisable in the device list", first)
	}
	// The id is hex, so a substring check for the token can never fire and would
	// assert nothing. Prove the real property instead: it is exactly the
	// truncated digest, which is one-way.
	sum := sha256.Sum256([]byte(token))
	if want := "cli-env-" + hex.EncodeToString(sum[:])[:12]; first != want {
		t.Errorf("device id = %q, want the truncated sha256 %q", first, want)
	}
	// Long enough not to collide, short enough to read in a device list.
	if got := len(first); got != len("cli-env-")+12 {
		t.Errorf("device id length = %d, want %d", got, len("cli-env-")+12)
	}
}

// TestEnvTokenSessionCarriesADeviceID is the regression guard for the half of
// #88 that made 'harbor crypto setup' unusable headlessly: the synthesized
// credentials had no device, and sync/push rejects an empty one — so encryption
// could not be set up in CI at all.
func TestEnvTokenSessionCarriesADeviceID(t *testing.T) {
	t.Setenv("HARBOR_TOKEN", "hbp_test-token")
	t.Setenv("HARBOR_API_URL", "https://example.invalid/api/v1")
	t.Setenv("HOME", t.TempDir())

	_, creds, err := loadClientFromConfig()
	if err != nil {
		t.Fatalf("loadClientFromConfig: %v", err)
	}
	if creds.DeviceID == "" {
		t.Fatal("a HARBOR_TOKEN session has no device id — sync/push will reject every write")
	}
	if creds.DeviceName == "" {
		t.Error("a HARBOR_TOKEN session has no device name, so it is anonymous in the device list")
	}
	if creds.AccessToken != "hbp_test-token" {
		t.Errorf("access token = %q", creds.AccessToken)
	}
}

// TestEnvTokenSessionMatchesWhoami pins that whoami and the client loader agree
// about what a token session is. They are two code paths over one concept, and
// the bug in #88 was precisely that they disagreed.
func TestEnvTokenSessionMatchesWhoami(t *testing.T) {
	t.Setenv("HARBOR_TOKEN", "hbp_test-token")
	t.Setenv("HARBOR_API_URL", "https://example.invalid/api/v1")
	t.Setenv("HOME", t.TempDir())

	_, loaded, err := loadClientFromConfig()
	if err != nil {
		t.Fatalf("loadClientFromConfig: %v", err)
	}
	seen, ok := envTokenSession()
	if !ok {
		t.Fatal("envTokenSession did not see HARBOR_TOKEN")
	}
	if seen.DeviceID != loaded.DeviceID || seen.AccessToken != loaded.AccessToken || seen.BaseURL() != loaded.BaseURL() {
		t.Errorf("whoami and the client loader disagree about the session:\n whoami: %+v\n loader: %+v", seen, loaded)
	}

	// And with no token set, it must not claim a session exists.
	t.Setenv("HARBOR_TOKEN", "")
	if _, ok := envTokenSession(); ok {
		t.Error("envTokenSession reported a session with HARBOR_TOKEN unset")
	}
}

// TestEnvTokenSessionIsNeverPersisted pins HARBOR_TOKEN's own promise: the token
// is used "for this process only: never persisted".
//
// Two callers opportunistically cache a resolved user id back to disk. For a
// synthesized env session that wrote a credentials.json containing the bearer
// token — so a headless 'crypto setup' on a persistent CI runner left a working
// standing session behind, outliving the environment variable that authorized
// it. Found end-to-end while verifying #88.
//
// It drives the REAL resolvers rather than calling cacheCredentials directly.
// Testing the helper alone passes even when a call site goes back to a bare
// config.Save, which is the mistake that put the token on disk in the first
// place — the leak was never in the helper.
func TestEnvTokenSessionIsNeverPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","email":"you@example.com"}`))
	}))
	defer srv.Close()

	resolvers := map[string]func(*client.Client, *config.Credentials) (string, error){
		"crypto.resolveScopeIDValue": resolveScopeIDValue,
		"sync.resolveScopeID": func(c *client.Client, creds *config.Credentials) (string, error) {
			return resolveScopeID(c, creds, syncPushCmd)
		},
	}
	for name, resolve := range resolvers {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("HARBOR_TOKEN", "hbp_ephemeral-token")
			t.Setenv("HARBOR_API_URL", srv.URL)
			resetCommandState(t)

			_, creds, err := loadClientFromConfig()
			if err != nil {
				t.Fatalf("loadClientFromConfig: %v", err)
			}
			if _, err := resolve(client.NewClient(srv.URL, creds.AccessToken), creds); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			path := filepath.Join(home, ".config", "harbor", "credentials.json")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				body, _ := os.ReadFile(path)
				t.Fatalf("%s wrote credentials to disk for a HARBOR_TOKEN run:\n%s", name, body)
			}
		})
	}
}

// TestSavedSessionIsStillCached proves the gate did not break the case it must
// not: a real logged-in session still caches its resolved user id, which is the
// whole point of that write.
func TestSavedSessionIsStillCached(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HARBOR_TOKEN", "")

	dir := filepath.Join(home, ".config", "harbor")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"email":"you@example.com","access_token":"hbp_saved","expires_at":0}`), 0600); err != nil {
		t.Fatal(err)
	}

	creds, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	creds.UserID = "u1"
	cacheCredentials(creds)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatalf("credentials.json is not valid JSON: %v\n%s", err, body)
	}
	if saved["user_id"] != "u1" {
		t.Errorf("a saved session no longer caches its resolved user id:\n%s", body)
	}
}

// TestVersionFromBuildInfo pins which embedded module versions are real releases
// worth reporting and which are local builds.
//
// The distinction is not obvious: a plain `go build` in this repo does NOT embed
// "(devel)" — because the tree has release tags, Go synthesizes a PSEUDO-VERSION
// like v0.1.31-0.20260807051754-b683a4a261ad, which looks like a version and is
// not one. Reporting it would put a number nobody can install into bug reports.
func TestVersionFromBuildInfo(t *testing.T) {
	cases := map[string]string{
		// Real release tags — what `go install pkg@vX.Y.Z` embeds.
		"v0.1.27":    "v0.1.27",
		"v1.0.0":     "v1.0.0",
		"v0.2.0-rc1": "v0.2.0-rc1",

		// Local builds, in every shape they come in.
		"":                                      devVersion,
		"(devel)":                               devVersion,
		"v0.1.31-0.20260807051754-b683a4a261ad": devVersion, // untagged commit
		"v0.1.31-0.20260807051754-b683a4a261ad+dirty": devVersion, // untagged + modified
		"v0.0.0-20260807051754-b683a4a261ad":          devVersion, // never-tagged module
		// A clean TAG with a dirty tree. There is no pseudo-version component
		// here, so the regex never sees it and only the +dirty check catches it
		// — which is why this case has to exist: without it, deleting that check
		// left the suite green while a maintainer with a modified tree at a
		// release tag would report "v0.1.30+dirty", a number nobody can install.
		"v0.1.30+dirty": devVersion,
	}
	for in, want := range cases {
		if got := versionFromBuildInfo(in); got != want {
			t.Errorf("versionFromBuildInfo(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveVersionPrefersLdflags proves the injected value always wins, so a
// release binary reports exactly what the workflow stamped and the build-info
// fallback cannot change it.
func TestResolveVersionPrefersLdflags(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = "v9.9.9"
	if got := resolveVersion(); got != "v9.9.9" {
		t.Errorf("resolveVersion() = %q, want the injected v9.9.9", got)
	}
}

// TestVersionFallbackIsReached proves the build-info fallback is actually WIRED,
// not merely correct when called directly.
//
// The gap was real and took three attempts to close, each time one level up:
// reverting cobra's Version field, then gutting resolveVersionFrom's fallback,
// then gutting resolveVersion itself — every one of them left the suite green
// while `go install` silently regressed to reporting "dev". A grep for the call
// is no better than none, since it passes once the call is gone. So the reader
// is a seam, driven here both as a parameter and by swapping the package
// variable, which is the only way the production entry point is covered.
func TestVersionFallbackIsReached(t *testing.T) {
	fake := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}

	// Nothing injected: the answer must come from build info.
	if got := resolveVersionFrom(devVersion, fake("v0.1.27")); got != "v0.1.27" {
		t.Errorf("with no ldflags, resolveVersionFrom = %q, want the go-install tag v0.1.27", got)
	}
	// Injected: build info must be ignored entirely, so a release binary reports
	// exactly what the workflow stamped.
	if got := resolveVersionFrom("v9.9.9", fake("v0.1.27")); got != "v9.9.9" {
		t.Errorf("ldflags did not win: %q", got)
	}
	// A local build still reads "dev" through the real path, not just the helper.
	if got := resolveVersionFrom(devVersion, fake("v0.1.31-0.20260807051754-b683a4a261ad")); got != devVersion {
		t.Errorf("a pseudo-version leaked through the wired path: %q", got)
	}
	// No build info at all (a stripped or non-module binary).
	if got := resolveVersionFrom(devVersion, func() (*debug.BuildInfo, bool) { return nil, false }); got != devVersion {
		t.Errorf("missing build info should read %q, got %q", devVersion, got)
	}
	// And the PRODUCTION entry point goes through the seam. This is the
	// assertion two earlier attempts got wrong: comparing resolveVersion()
	// against resolveVersionFrom(version, readBuildInfo) is a tautology, because
	// in a test binary both sides evaluate to "dev" no matter what the function
	// does. Swap the package-level reader instead, so gutting resolveVersion to
	// `return version` — which regresses go install and every other surface —
	// actually fails.
	origVersion, origReader := version, readBuildInfo
	t.Cleanup(func() { version, readBuildInfo = origVersion, origReader })
	version = devVersion
	readBuildInfo = fake("v0.1.27")
	if got := resolveVersion(); got != "v0.1.27" {
		t.Errorf("resolveVersion() = %q, want v0.1.27 — it does not use the build-info fallback", got)
	}
}

// TestSupportBundleReportsTheResolvedVersion covers the wiring behaviourally
// rather than by grep.
//
// The support bundle exists for triage, so it is the worst place for the version
// to disagree with --version: under `go install` it would report "dev" while the
// same binary's --version reported the real tag. The source check below cannot
// see a call site that is renamed or moved, so this drives the real function
// with the reader swapped.
func TestSupportBundleReportsTheResolvedVersion(t *testing.T) {
	origVersion, origReader := version, readBuildInfo
	t.Cleanup(func() { version, readBuildInfo = origVersion, origReader })
	version = devVersion
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.27"}}, true
	}

	if got := supportMetadata()["app_version"]; got != "v0.1.27" {
		t.Errorf("support bundle app_version = %v, want v0.1.27 — it reads the raw variable, so a go-install build reports \"dev\" for triage", got)
	}
}

// TestVersionIsWiredEverywhere pins that every surface reports the SAME version.
//
// The failure mode is a new call site reading the raw variable — under
// `go install` that surface reports "dev" while --version reports the real tag.
// Support metadata exists for triage, so it is the worst place for the two to
// disagree. A source check is the only thing that catches a call site nothing
// else exercises; it is exact-string and covers the three files that surface a
// version today, so treat it as a guard, not a proof.
func TestVersionIsWiredEverywhere(t *testing.T) {
	// Behavioural, not a comparison of two expressions that are equal by
	// construction: with the reader swapped, what --version would print must be
	// the fake's tag. `rootCmd.Version != resolveVersion()` reads like a check
	// and is not one — in a test binary both sides are "dev" regardless.
	origVersion, origReader, origCobra := version, readBuildInfo, rootCmd.Version
	t.Cleanup(func() {
		version, readBuildInfo, rootCmd.Version = origVersion, origReader, origCobra
	})
	version = devVersion
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.27"}}, true
	}
	prepareCommandTree()
	if rootCmd.Version != "v0.1.27" {
		t.Errorf("--version would print %q, want v0.1.27 — cobra bypasses the resolver", rootCmd.Version)
	}

	for _, f := range []string{"support.go", "skill.go", "root.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"CLIVersion: version", `"app_version":  version`, "Version:       version,"} {
			if strings.Contains(string(src), bad) {
				t.Errorf("%s surfaces the raw version variable (%q) instead of resolveVersion(), so it reports \"dev\" under go install", f, bad)
			}
		}
	}
}

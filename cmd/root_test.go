// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-25

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
	m := newAPIMock(t, map[string]mockReply{"POST /api/v1/notes": {Status: 403, Body: body}})

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

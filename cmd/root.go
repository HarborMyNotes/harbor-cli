// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

// Package cmd is the cobra command tree for the harbor CLI. Each API domain
// lives in its own file and self-registers under rootCmd via an init function.
package cmd

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/spf13/cobra"
)

// Global persistent flags, shared by every command.
var (
	// jsonOutput emits raw JSON instead of formatted tables.
	jsonOutput bool
	// noColorFlag disables ANSI color in table output.
	noColorFlag bool
	// verboseFlag includes request_id and HTTP status on errors.
	verboseFlag bool
	// utcFlag renders timestamps in UTC rather than local time.
	utcFlag bool
	// apiURLFlag overrides the API base URL (else credentials/env/default).
	apiURLFlag string
)

// version is the CLI version, injected at build time via
// -ldflags "-X github.com/HarborMyNotes/harbor-cli/cmd.version=vX.Y.Z".
// It defaults to "dev" for local builds.
var version = "dev"

// Command group annotations, so `harbor --help` clusters related commands.
const (
	groupAuth    = "auth"
	groupContent = "content"
	groupOrg     = "organization"
	groupSync    = "sync"
	groupAccount = "account"
	groupSystem  = "system"
)

// rootCmd is the base command. All subcommands register under it.
var rootCmd = &cobra.Command{
	Use:   "harbor",
	Short: "Harbor CLI — the entire Harbor notes API from your terminal",
	Long: `Harbor is a command-line client for the Harbor notes API.

It exposes the full API surface — notebooks, notes, tags, sync, files, search,
sharing, and account management — as composable commands. Output is a styled
table by default and clean JSON with --json, so it is equally pleasant for
humans and for scripts or AI agents.

Get started:
  harbor login                       Sign in through your browser
  harbor notebooks list              See your notebooks
  harbor notes create --stdin        Pipe Markdown straight into a new note
  harbor search "tag:receipts pdf"   Full-text search
  harbor notes list --json | jq .    Machine-readable output for agents

Credentials are stored in ~/.config/harbor/credentials.json (0600) as a
non-expiring token, so you stay signed in. Set HARBOR_TOKEN to use a token for a
single command without logging in. Every command honors --json.`,
	Version:       version,
	SilenceErrors: true, // we render errors ourselves in Execute
	SilenceUsage:  true,
}

// init registers the global persistent flags and the help command groups.
func init() {
	pf := rootCmd.PersistentFlags()
	pf.BoolVar(&jsonOutput, "json", false, "Output raw JSON instead of formatted tables")
	pf.BoolVar(&noColorFlag, "no-color", false, "Disable ANSI color in output")
	pf.BoolVarP(&verboseFlag, "verbose", "v", false, "Include request_id and HTTP status on errors")
	pf.BoolVar(&utcFlag, "utc", false, "Render timestamps in UTC instead of local time")
	pf.StringVar(&apiURLFlag, "api-url", "", "Override the API base URL (maintainer use; defaults to the production endpoint)")

	rootCmd.AddGroup(
		&cobra.Group{ID: groupAuth, Title: "Authentication:"},
		&cobra.Group{ID: groupContent, Title: "Content:"},
		&cobra.Group{ID: groupOrg, Title: "Organization:"},
		&cobra.Group{ID: groupSync, Title: "Sync & Files:"},
		&cobra.Group{ID: groupAccount, Title: "Account:"},
		&cobra.Group{ID: groupSystem, Title: "System:"},
	)
}

// Exit codes. A script has to decide what to do about a failure without
// reading English, so the two classes worth reacting to differently get their
// own code: a plan limit is the user's account saying "no" (retrying will never
// help — free up room or upgrade), and an unreachable API is transient (retry
// with backoff). Everything else stays exitError, so existing scripts that only
// test for non-zero keep working, and 2 is left alone because too much of the
// world already reads it as "bad usage".
const (
	// exitOK is a successful run.
	exitOK = 0
	// exitError is any failure without a more specific code.
	exitError = 1
	// exitNetwork means the API could not be reached at all (DNS, connection
	// refused, TLS, or a client-side timeout) — nothing was decided by the
	// server, so the same command may well succeed later.
	exitNetwork = 3
	// exitPlanLimit means an entitlement gate blocked the write: a plan cap for
	// that resource, or the whole-account read-only freeze.
	exitPlanLimit = 4
)

// Execute runs the root command. It renders any error (with rich treatment for
// API errors) to stderr and exits with the matching code, without calling
// os.Exit inside RunE.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		renderError(err)
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor classifies an error into one of the documented exit codes. It
// runs centrally in Execute, so every command — every create path in the CLI —
// gets the same classification without opting in.
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	if isPlanLimitError(err) {
		return exitPlanLimit
	}
	if isNetworkError(err) {
		return exitNetwork
	}
	return exitError
}

// isNetworkError reports whether err is a transport failure rather than an
// answer from the server. Every request goes through http.Client.Do, which
// wraps transport failures in *url.Error, so that one check covers a refused
// connection, an unresolvable host, a TLS failure, and a client-side timeout.
// An HTTP error response is a *client.APIError instead and is deliberately NOT
// caught here — the server did answer.
func isNetworkError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// resolveBaseURL picks the API base URL with this precedence: --api-url flag,
// then the HARBOR_API_URL env var, then the saved credentials, then the
// production default.
func resolveBaseURL(creds *config.Credentials) string {
	if apiURLFlag != "" {
		return apiURLFlag
	}
	if env := os.Getenv("HARBOR_API_URL"); env != "" {
		return env
	}
	if creds != nil {
		return creds.BaseURL()
	}
	return config.DefaultBaseURL
}

// newAnonymousClient builds a client with no credentials, for public endpoints
// (login, password reset, public share view, operational probes). It still
// resolves the base URL from the flag/env/saved-config chain so a maintainer
// can target staging without logging in.
func newAnonymousClient() *client.Client {
	creds, _ := config.Load() // best-effort: only for the base URL
	c := client.NewClient(resolveBaseURL(creds), "")
	c.Verbose = verboseFlag
	return c
}

// loadClientFromConfig builds an authenticated client from saved credentials.
// It wires transparent token refresh (proactive when the access token is near
// expiry, and reactive on a 401 invalid_token) and returns a friendly
// "run harbor login" error when no session exists.
func loadClientFromConfig() (*client.Client, *config.Credentials, error) {
	// An explicit HARBOR_TOKEN bypasses the saved session entirely — handy for
	// CI and one-off shells. It's used as a bearer (typically a Personal Access
	// Token) for this process only: never persisted and never refreshed.
	if envTok := strings.TrimSpace(os.Getenv("HARBOR_TOKEN")); envTok != "" {
		base := resolveBaseURL(nil)
		creds := &config.Credentials{AccessToken: envTok, ClientID: cliClientID, APIURL: base}
		c := client.NewClient(base, envTok)
		c.Verbose = verboseFlag
		return c, creds, nil
	}

	creds, err := config.Load()
	if err != nil {
		return nil, nil, err
	}

	c := client.NewClient(resolveBaseURL(creds), creds.AccessToken)
	c.Verbose = verboseFlag

	// A refresh token means this is a rotating session: wire proactive +
	// reactive refresh. A non-expiring PAT has none, so we skip it — a 401 then
	// surfaces cleanly (the token was revoked; the user re-runs 'harbor login').
	if creds.RefreshToken != "" {
		// Reactive refresh: invoked by the client on a 401 invalid_token.
		c.OnUnauthorized = func() (string, bool) {
			return refreshAndPersist(c, creds)
		}
		// Proactive refresh: if the token is expired (or within the skew
		// window), refresh before the first request to avoid a guaranteed 401.
		if creds.IsExpired(tokenRefreshSkew) {
			if newTok, ok := refreshAndPersist(c, creds); ok {
				c.AccessToken = newTok
			}
		}
	}

	return c, creds, nil
}

// printResult renders an API response: pretty JSON in --json mode, otherwise
// the provided table renderer.
func printResult(data []byte, tableFunc func([]byte)) {
	if jsonOutput {
		pretty, err := client.PrettyJSON(data)
		if err != nil {
			fmt.Println(string(data))
			return
		}
		fmt.Println(pretty)
		return
	}
	tableFunc(data)
}

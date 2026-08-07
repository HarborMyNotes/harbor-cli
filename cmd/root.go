// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

// Package cmd is the cobra command tree for the harbor CLI. Each API domain
// lives in its own file and self-registers under rootCmd via an init function.
package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
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

// devVersion is the sentinel a build carries when nothing injected a version.
const devVersion = "dev"

// version is the CLI version, injected at build time via
// -ldflags "-X github.com/HarborMyNotes/harbor-cli/cmd.version=vX.Y.Z".
// It defaults to devVersion for local builds; resolveVersion recovers the real
// one for a `go install` build, which does no injection.
var version = devVersion

// resolveVersion returns the version to report.
//
// The ldflags value wins whenever it is set, so a release binary reports exactly
// what the workflow stamped and this function changes nothing about it.
//
// The fallback exists for `go install github.com/HarborMyNotes/harbor-cli@latest`
// — a documented install route that does no ldflags injection, so it used to
// report "dev" no matter which tag you asked for. Go embeds the module version
// in the binary, so it can simply be read back.
//
// A local build is reported as "dev" rather than passed through, and there are
// three shapes of that — which is more than it looks:
//
//   - "(devel)", from a build with no VCS stamping;
//   - a PSEUDO-VERSION like v0.1.31-0.20260807051754-b683a4a261ad, which Go
//     synthesizes for a commit that is not a release tag. It looks like a
//     version and is not one, so passing it through would put a number nobody
//     can install into bug reports;
//   - the same with a "+dirty" suffix, from a modified working tree.
//
// Only a real tag is reported, which is what `go install pkg@vX.Y.Z` embeds.
// A "+incompatible" tag would pass through verbatim; that is unreachable while
// this module is v0/v1, and worth revisiting if it ever goes v2.
// One consequence worth knowing: a `go build` at a CLEAN checkout of an exact
// release tag reports that tag rather than "dev", because that is genuinely what
// Go stamps. The common local case — an untagged or dirty tree — still reads
// "dev".
func resolveVersion() string {
	return resolveVersionFrom(version, readBuildInfo)
}

// readBuildInfo is debug.ReadBuildInfo as a variable so a test can substitute
// it. Grepping the source for the call is not a substitute: it passes just as
// happily once the call is gone. Swapping this variable is what lets a test
// prove the fallback is actually reached from resolveVersion, rather than only
// that it computes the right answer when called directly.
var readBuildInfo = debug.ReadBuildInfo

// resolveVersionFrom is resolveVersion with both inputs supplied: the injected
// version and the build-info reader. Splitting it out is what lets a test prove
// the fallback is actually REACHED, rather than merely that it computes the
// right answer when called directly.
func resolveVersionFrom(injected string, read func() (*debug.BuildInfo, bool)) string {
	if injected != devVersion {
		return injected
	}
	info, ok := read()
	if !ok || info == nil {
		return devVersion
	}
	return versionFromBuildInfo(info.Main.Version)
}

// versionFromBuildInfo decides whether an embedded module version is a real
// release tag worth reporting, or a local build that should read "dev". It is
// split out from resolveVersion so every shape can be tested directly rather
// than by building binaries.
func versionFromBuildInfo(v string) string {
	if v == "" || v == "(devel)" || strings.HasSuffix(v, "+dirty") || pseudoVersionRe.MatchString(v) {
		return devVersion
	}
	return v
}

// pseudoVersionRe matches the timestamp-and-hash core Go puts in every
// pseudo-version (yyyymmddhhmmss-<12 hex>), which is what separates "built from
// some commit" from "installed from a release tag".
var pseudoVersionRe = regexp.MustCompile(`[0-9]{14}-[0-9a-f]{12}`)

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
	Version:       resolveVersion(),
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
	prepareCommandTree()
	if err := rootCmd.Execute(); err != nil {
		renderError(err)
		os.Exit(exitCodeFor(err))
	}
}

// prepareCommandTree finishes assembling the command tree just before it runs.
// It is separate from Execute so a test can drive the identical tree, rather
// than a slightly different one that happens to pass.
//
// Cobra grows its own `help` and `completion` commands during Execute;
// materializing them here first means the argument contract covers them too.
// Both calls are no-ops once the command exists, so cobra's own call inside
// Execute changes nothing.
//
// Calling this twice must leave the tree exactly as one call did — every rule
// below is written to be a no-op once applied. That is not decoration: the
// tree is package-level state shared by every run in a process, so a
// second pass that "finished the job differently" would give the same command
// two different behaviours depending on what ran before it.
func prepareCommandTree() {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd(os.Args[1:]...)
	enforceArgContract(rootCmd)
}

// enforceArgContract walks the command tree and closes the two ways a command
// could be handed input it does not understand and still exit 0 — the worst
// failure mode a CLI has, because a wrapper script cannot detect it at all.
//
//  1. A command that never declared an Args validator accepts arbitrary
//     positional arguments and silently drops them, so `harbor tags list
//     receipts` lists every tag and exits 0 as though it had filtered — and
//     `harbor files delete` names a subcommand that does not exist. Every
//     command that genuinely takes positional arguments declares its own
//     validator (cobra.ExactArgs and friends), so a nil one means "takes none",
//     and rejectUnknownArgs says so in cobra's own words.
//
//  2. A parent that only groups subcommands (`harbor files`) has no action of
//     its own, so cobra considers it un-runnable and returns "print the help"
//     the moment it is reached — BEFORE it ever validates arguments, which is
//     why rule 1 alone could not fix `harbor files delete` (#69). Giving the
//     parent an action makes it runnable, so its arguments are validated like
//     any other command's and a bare `harbor files` still gets its help.
//
// Both rules are idempotent: a second walk finds the RunE and the Args already
// set and changes nothing. Doing it here, once, is what keeps the contract true
// for a domain file that forgets either rule, including commands added long
// after this was written. The `help` command is the one exception — its
// arguments are a command path.
func enforceArgContract(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		enforceArgContract(sub)
	}
	if cmd.Name() == "help" {
		return
	}
	if cmd.HasSubCommands() && !cmd.Runnable() {
		cmd.RunE = runParentCommand
		// The action exists only to print the help, so keep it out of the usage
		// line: `harbor files` is a real invocation, `harbor files [flags]` is
		// not.
		cmd.DisableFlagsInUseLine = true
	}
	if cmd.Args == nil {
		cmd.Args = rejectUnknownArgs
	}
}

// rejectUnknownArgs is the Args validator for every command that declared none.
// It refuses what the command cannot act on — an unknown subcommand under a
// parent, a stray word after a leaf — in cobra's own wording, so `harbor bogus`,
// `harbor files bogus` and `harbor tags list bogus` all read identically. A leaf
// has no subcommands to suggest, which makes this exactly cobra.NoArgs there.
func rejectUnknownArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return errors.New(unknownSubcommandMessage(cmd, args[0]))
}

// runParentCommand is the action given to every command that exists only to
// group subcommands: the user asked what lives here, so the help is the answer
// and the command succeeded. It never sees a stray argument — rejectUnknownArgs
// runs first and fails — which is the point of making the parent runnable at
// all: an un-runnable command's arguments are never validated.
func runParentCommand(cmd *cobra.Command, args []string) error {
	return cmd.Help()
}

// unknownSubcommandMessage builds the "unknown command" text for a subcommand
// that does not exist, character-for-character like cobra's own — including the
// "Did you mean this?" block, whose Levenshtein threshold cobra only defaults
// lazily on the path this one replaces.
func unknownSubcommandMessage(cmd *cobra.Command, name string) string {
	msg := fmt.Sprintf("unknown command %q for %q", name, cmd.CommandPath())
	if cmd.DisableSuggestions {
		return msg
	}
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	suggestions := cmd.SuggestionsFor(name)
	if len(suggestions) == 0 {
		return msg
	}
	return msg + "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t") + "\n"
}

// exitCoder is an error that has already decided which exit code it deserves.
// Almost nothing implements it — exitCodeFor classifies from the error itself,
// which is what keeps the codes consistent — but a failure the server reported
// INSIDE a 200 (sync push refusing individual changes) knows something no error
// envelope can express on its own.
type exitCoder interface {
	error
	ExitCode() int
}

// exitCodeFor classifies an error into one of the documented exit codes. It
// runs centrally in Execute, so every command — every create path in the CLI —
// gets the same classification without opting in.
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode()
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

// usingEnvToken reports whether this run is authenticated by HARBOR_TOKEN rather
// than a saved session.
func usingEnvToken() bool {
	return strings.TrimSpace(os.Getenv("HARBOR_TOKEN")) != ""
}

// cacheCredentials writes back an opportunistic credential update — a resolved
// user id, a refreshed token — and is a NO-OP for a HARBOR_TOKEN session.
//
// HARBOR_TOKEN promises the token is used "for this process only: never
// persisted". Two callers cache the resolved user id back to disk with a bare
// config.Save, which for a synthesized env session would write a credentials.json
// containing the bearer token — turning a deliberately ephemeral run into a
// standing session that outlives the environment variable. On a persistent CI
// runner that is a token left on disk nobody asked to store.
//
// It is silent by design: the write is a cache, so failing to make it is not
// worth interrupting the command the user actually ran.
//
// One file is still written under HARBOR_TOKEN: the keystore cache
// (config.SaveKeystoreBlob). That is the WRAPPED master key, useless without the
// passphrase, and caching it is what keeps a headless run from re-fetching the
// keystore on every command. 'harbor crypto sync' refreshes it and
// config.ClearKeystoreBlob removes it.
func cacheCredentials(creds *config.Credentials) {
	if usingEnvToken() {
		return
	}
	_ = config.Save(creds)
}

// envDeviceID derives the sync device id for a HARBOR_TOKEN session.
//
// A device id is required to push (the server rejects an empty one), and there
// is no saved session to take one from — which is why 'harbor crypto setup' was
// unusable headlessly: it writes the keystore through sync/push, and the
// synthesized credentials carried no device (issue #88).
//
// It is DERIVED FROM THE TOKEN, not random, for two reasons. A fresh id per run
// would register a new device on every CI job and fill the user's device list
// with junk that also holds back the sync GC floor. And deriving it from the
// token means two different tokens — two different machines or jobs — get two
// different devices, which is what the sync engine expects.
//
// The token is hashed and truncated, so the id is stable and collision-resistant
// without carrying any part of the secret: 12 hex characters of SHA-256 cannot
// be walked back to the token, and the id is sent to the server on every push.
func envDeviceID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "cli-env-" + hex.EncodeToString(sum[:])[:12]
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
		creds := &config.Credentials{
			AccessToken: envTok,
			ClientID:    cliClientID,
			APIURL:      base,
			DeviceID:    envDeviceID(envTok),
			DeviceName:  "harbor-cli (HARBOR_TOKEN)",
		}
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

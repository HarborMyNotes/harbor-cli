// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/spf13/cobra"
)

// ===========================================================================
// Commands
// ===========================================================================

// statusCmd reports the live health of the configured Harbor server: liveness
// (/health), readiness (/ready), and build metadata (/version), combined into
// one view. It is public — no login required — so it works as a quick "is the
// server up?" check even before authenticating.
var statusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show server health (liveness, readiness, version)",
	GroupID: groupSystem,
	Long: `Probe the configured Harbor server and print a combined health view:
liveness (/health), readiness with each dependency check and its latency
(/ready), and the build version (/version).

These are public probes, so no login is required. The command exits non-zero
when the server is not ready, making it usable in scripts and health checks.`,
	Example: `  harbor status
  harbor status --json
  harbor --api-url https://staging.harbor.my/api/v1 status`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newAnonymousClient()

		// Liveness: a bare {"status":"ok"}. Any error means the process is not
		// serving HTTP, which we surface as a down liveness rather than aborting.
		health, healthErr := c.Health()
		liveOK := healthErr == nil && operationalStatusEquals(health, "ok")

		// Readiness: 200 when healthy, 503 (as an *APIError carrying the JSON
		// body) when degraded. Recover the body either way so we can render the
		// per-dependency check table.
		ready, readyErr := c.Ready()
		readyBody := operationalReadyBody(ready, readyErr)
		readyOK := operationalStatusEquals(readyBody, "ready")

		// Version: best-effort build metadata; absence is non-fatal.
		version, _ := c.Version()

		combined := operationalCombine(health, healthErr, readyBody, version, liveOK, readyOK)

		printResult(combined, displayStatus)

		// Exit non-zero when not ready, AFTER printing, so the table is always
		// shown and scripts get a meaningful exit code.
		if !readyOK {
			return errors.New("server is not ready")
		}
		return nil
	},
}

// apiVersionCmd prints the server's build metadata from /version. Public.
var apiVersionCmd = &cobra.Command{
	Use:     "api-version",
	Short:   "Show the server build version, commit, and Go version",
	GroupID: groupSystem,
	Long: `Print the Harbor server's build metadata from the public /version
endpoint: the release version, the git commit it was built from, the build
timestamp, and the Go toolchain version. Without build-time ldflags the version
and commit fall back to "dev"/"unknown".`,
	Example: `  harbor api-version
  harbor api-version --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newAnonymousClient()
		data, err := c.Version()
		if err != nil {
			return err
		}
		printResult(data, displayAPIVersion)
		return nil
	},
}

// openapiCmd fetches the generated OpenAPI 3.0 document and writes it to a file
// or stdout. Public.
var openapiCmd = &cobra.Command{
	Use:   "openapi",
	Short: "Fetch the OpenAPI 3.0 spec for the API",
	Long: `Download the server's generated OpenAPI 3.0 document (the same spec
that powers /docs). Written to stdout by default; pass --output to save it to a
file. The spec is generated from the live router, so it never drifts from the
handlers it documents.`,
	GroupID: groupSystem,
	Example: `  harbor openapi > harbor.json
  harbor openapi --output harbor-openapi.json
  harbor openapi | jq '.info.version'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newAnonymousClient()
		data, err := c.OpenAPI()
		if err != nil {
			return err
		}

		// The spec is raw JSON bytes, not an envelope to tabulate. Both --json
		// and the default emit the document verbatim to the chosen sink ("-" =
		// stdout); only the "wrote to file" confirmation differs.
		out := stringFlag(cmd, "output")
		if out == "" {
			out = "-"
		}
		n, err := writeOutput(out, bytes.NewReader(data))
		if err != nil {
			return err
		}
		if out != "-" {
			fmt.Printf("Wrote %s of OpenAPI spec to %s\n", bytesHuman(float64(n)), out)
		}
		return nil
	},
}

// ===========================================================================
// Helpers
// ===========================================================================

// operationalStatusEquals reports whether a bare operational body (e.g.
// {"status":"ok"}) has a top-level "status" field equal to want.
func operationalStatusEquals(data []byte, want string) bool {
	return str(parseJSON(data), "status") == want
}

// operationalReadyBody recovers the readiness JSON body from a Ready() call.
// On success the body is returned as-is. On a 503 the client surfaces an
// *APIError whose Message carries the (non-enveloped) readiness JSON, which we
// recover so the command can render the per-check table even when degraded.
func operationalReadyBody(ready []byte, readyErr error) []byte {
	if readyErr == nil {
		return ready
	}
	var apiErr *client.APIError
	if errors.As(readyErr, &apiErr) && apiErr.Message != "" {
		// Only treat the error message as a body if it parses as a JSON object
		// that actually carries a status field.
		if m := parseJSON([]byte(apiErr.Message)); m != nil {
			if _, ok := m["status"]; ok {
				return []byte(apiErr.Message)
			}
		}
	}
	return ready
}

// operationalCombine assembles the single object rendered by `status` (and
// emitted in --json mode) from the three probe bodies. It is pure so the
// rendering and exit logic are unit-testable without a server.
func operationalCombine(health []byte, healthErr error, readyBody, version []byte, liveOK, readyOK bool) []byte {
	combined := map[string]any{
		"live":  liveOK,
		"ready": readyOK,
	}
	if h := parseJSON(health); h != nil {
		combined["health"] = h
	} else if healthErr != nil {
		combined["health"] = map[string]any{"error": healthErr.Error()}
	}
	if r := parseJSON(readyBody); r != nil {
		combined["readiness"] = r
	}
	if v := parseJSON(version); v != nil {
		combined["version"] = v
	}
	out, _ := json.Marshal(combined)
	return out
}

// ===========================================================================
// Display
// ===========================================================================

// displayStatus renders the combined health view: a per-dependency readiness
// table (each check's status and latency), then liveness/readiness/version
// summary lines and an overall OK/degraded line.
func displayStatus(data []byte) {
	root := parseJSON(data)
	liveOK := boolean(root, "live")
	readyOK := boolean(root, "ready")

	// Per-dependency readiness checks, sorted by name for stable output.
	checks, _ := nested(root, "readiness")["checks"].(map[string]any)
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		chk, _ := checks[name].(map[string]any)
		status := "down"
		if boolean(chk, "ok") {
			status = "ok"
		}
		latency := "—"
		if _, present := chk["latency_ms"]; present {
			latency = trimFloat(num(chk, "latency_ms")) + " ms"
		}
		rows = append(rows, []string{
			name,
			colorizeStatus(status),
			latency,
			str(chk, "error"),
		})
	}
	printTable([]string{"CHECK", "STATUS", "LATENCY", "ERROR"}, rows)

	// Summary lines.
	pairs := [][2]string{
		{"Liveness", colorizeStatus(boolWord(liveOK, "ok", "down"))},
		{"Readiness", colorizeStatus(boolWord(readyOK, "ready", "not_ready"))},
	}
	if ver := nested(root, "version"); ver != nil {
		pairs = append(pairs, [2]string{"Version", bold(str(ver, "version"))})
	}
	printKV(pairs)

	if liveOK && readyOK {
		fmt.Println(colorizeStatus("ok") + " " + dim("all systems operational"))
	} else {
		fmt.Println(colorize("DEGRADED", text.FgRed, text.Bold) + " " + dim("one or more checks are failing"))
	}
}

// displayAPIVersion renders the build metadata from /version as a detail view.
func displayAPIVersion(data []byte) {
	v := parseJSON(data)
	if v == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"Version", bold(str(v, "version"))},
		{"Commit", str(v, "commit")},
		{"Build time", str(v, "build_time")},
		{"Go version", str(v, "go_version")},
	})
}

// boolWord picks one of two words based on a boolean — a tiny helper to keep the
// status summary readable.
func boolWord(b bool, ifTrue, ifFalse string) string {
	if b {
		return ifTrue
	}
	return ifFalse
}

func init() {
	openapiCmd.Flags().StringP("output", "o", "", "Write the spec to this path (default: stdout; - is stdout)")

	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(apiVersionCmd)
	rootCmd.AddCommand(openapiCmd)
}

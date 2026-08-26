// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"golang.org/x/term"
)

// ===========================================================================
// Color management (graceful downgrade)
// ===========================================================================

// colorReady caches whether color has been configured for this process.
var colorReady bool

// useColor reports whether ANSI color should be emitted, honoring --no-color,
// the NO_COLOR convention, whether stdout is a TTY, and CLICOLOR_FORCE. It also
// flips go-pretty's global color switch the first time it runs.
//
// CLICOLOR_FORCE (the https://bixense.com/clicolors convention) overrides only
// the TTY check, so color survives a pipe into `less -R` or a CI log that
// renders ANSI. An explicit opt-out — --no-color or NO_COLOR — always wins.
func useColor() bool {
	enabled := true
	if force := os.Getenv("CLICOLOR_FORCE"); force != "" && force != "0" {
		enabled = true
	} else if !term.IsTerminal(int(os.Stdout.Fd())) {
		enabled = false
	}
	if noColorFlag {
		enabled = false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		enabled = false
	}
	if !colorReady {
		if enabled {
			text.EnableColors()
		} else {
			text.DisableColors()
		}
		colorReady = true
	}
	return enabled
}

// colorize wraps s in the given colors when color is enabled, otherwise returns
// s unchanged.
func colorize(s string, colors ...text.Color) string {
	if !useColor() || len(colors) == 0 {
		return s
	}
	return text.Colors(colors).Sprint(s)
}

// dim renders dimmed/faint text (used for ids, USNs, paging hints).
func dim(s string) string { return colorize(s, text.Faint) }

// bold renders bold text (used to emphasize ids and headline values).
func bold(s string) string { return colorize(s, text.Bold) }

// redWarn renders bold red text for prominent, hard-to-miss warnings (used by
// the irreversible encryption-setup notice).
func redWarn(s string) string { return colorize(s, text.FgRed, text.Bold) }

// amberWarn renders a yellow label for a stderr warning — something the user
// needs to act on, short of the failure itself. Distinct from redWarn, which is
// for the irreversible.
func amberWarn(s string) string { return colorize(s, text.FgYellow) }

// star renders the default-marker glyph in yellow.
func star() string { return colorize("★", text.FgYellow) }

// colorizeStatus colors a status word: green for success-ish, red for failure,
// yellow for in-between (e.g. conflict, pending, degraded).
func colorizeStatus(status string) string {
	switch status {
	case "applied", "completed", "ok", "active", "done", "ready":
		return colorize(status, text.FgGreen)
	case "rejected", "failed", "error", "expired":
		return colorize(status, text.FgRed)
	case "conflict", "pending", "running", "queued", "degraded", "stale":
		return colorize(status, text.FgYellow)
	default:
		return status
	}
}

// ===========================================================================
// JSON navigation helpers
// ===========================================================================

// parseJSON unmarshals raw JSON bytes into a generic map. Returns nil on
// failure (e.g. the payload is an array or a scalar).
func parseJSON(data []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// nested traverses a map by a sequence of keys, returning the nested map or
// nil if any hop is missing or not an object.
func nested(data map[string]any, keys ...string) map[string]any {
	current := data
	for _, key := range keys {
		val, ok := current[key]
		if !ok || val == nil {
			return nil
		}
		m, ok := val.(map[string]any)
		if !ok {
			return nil
		}
		current = m
	}
	return current
}

// toSlice converts a JSON value into a slice of object maps, accepting either a
// single object or an array of objects.
func toSlice(val any) []map[string]any {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case []any:
		result := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
		return result
	case map[string]any:
		return []map[string]any{v}
	}
	return nil
}

// toStringSlice converts a JSON array value into a slice of strings.
func toStringSlice(val any) []string {
	arr, ok := val.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			result = append(result, v)
		case float64:
			result = append(result, trimFloat(v))
		default:
			result = append(result, fmt.Sprintf("%v", v))
		}
	}
	return result
}

// str safely extracts a string representation from a map value, formatting
// numbers and bools sensibly.
func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	val, ok := m[key]
	if !ok || val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case float64:
		return trimFloat(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// num safely extracts a float64 from a map value (0 when missing/non-numeric).
func num(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	if f, ok := m[key].(float64); ok {
		return f
	}
	return 0
}

// boolean safely extracts a bool from a map value.
func boolean(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

// trimFloat renders a JSON number without a trailing ".0" for integral values.
func trimFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// ===========================================================================
// Formatting helpers
// ===========================================================================

// epochMS renders a UTC epoch-millisecond timestamp as a human-readable local
// (or UTC, with --utc) time. Zero/empty renders as an em dash.
func epochMS(ms float64) string {
	if ms == 0 {
		return "—"
	}
	t := time.UnixMilli(int64(ms))
	if utcFlag {
		return t.UTC().Format("2006-01-02 15:04 UTC")
	}
	return t.Local().Format("2006-01-02 15:04")
}

// relTime renders an epoch-ms timestamp relative to now ("3h ago", "in 2d").
func relTime(ms float64) string {
	if ms == 0 {
		return "—"
	}
	d := time.Since(time.UnixMilli(int64(ms)))
	future := d < 0
	if future {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		s = fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		s = fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		s = fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	default:
		s = fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

// commaNum renders an integer with thousands separators ("36500" → "36,500").
// Used wherever a count is large enough that the raw digits stop being readable
// at a glance — an export's note progress, for instance.
func commaNum(n int64) string {
	s := fmt.Sprintf("%d", n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}

// ordinal renders a 1-based position as an English ordinal ("1st", "2nd",
// "13th", "21st") — for places in a queue, where "3rd in line" reads as a
// position and a bare "3" reads as a count.
func ordinal(n int) string {
	suffix := "th"
	// 11th/12th/13th are the exceptions to the 1st/2nd/3rd pattern.
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// bytesHuman renders a byte count in human units (e.g. "1.2 MB").
func bytesHuman(n float64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", int64(n))
	}
	div, exp := float64(unit), 0
	for x := n / unit; x >= unit && exp < 5; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", n/div, "KMGTPE"[exp])
}

// limitBytesHuman renders a byte count for an upload-limit message: base-1024
// units with the familiar KB/MB/GB labels, and no trailing ".0" so the
// 104857600-byte cap reads as the round "100 MB" an operator actually
// configured. Both numbers in a refusal share this basis, so the comparison is
// coherent, and every Harbor client renders them the same way.
//
// It is deliberately separate from bytesHuman, which keeps its ".0" because it
// fills table columns that read better aligned.
//
// roundUp forces the value up to the next tenth. The offending file's size uses
// it and the cap does not: at one decimal place a file a single byte over a
// 100 MB cap rounds back onto it and the refusal reads "is 100 MB — the
// per-file limit is 100 MB", a sentence that contradicts itself. The cap is an
// exact figure and must render as the number the operator set.
func limitBytesHuman(n int64, roundUp bool) string {
	const unit = 1024
	f := float64(n)
	if f < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := float64(unit), 0
	for x := f / unit; x >= unit && exp < 5; x /= unit {
		div *= unit
		exp++
	}
	v := f / div
	if roundUp {
		v = math.Ceil(v*10) / 10
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + fmt.Sprintf(" %cB", "KMGTPE"[exp])
}

// truncate shortens s to at most n runes, appending an ellipsis when cut. It
// also collapses internal newlines so snippets stay on one table row.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// stripHTML removes tags and unescapes a handful of common entities, for a
// readable terminal preview of an HTML note body or a search snippet. It is a
// display convenience, not a parser — full fidelity is always available via the
// raw --json content.
func stripHTML(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	out := b.String()
	for _, rep := range [][2]string{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", "\""}, {"&#39;", "'"}, {"&nbsp;", " "},
	} {
		out = strings.ReplaceAll(out, rep[0], rep[1])
	}
	return strings.TrimSpace(out)
}

// pluralize returns singular when n == 1, otherwise plural — so a count and its
// noun agree in a message the user reads ("1 note" / "2 notes").
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// boolMark renders a boolean as a compact check/dot glyph (green/dim).
func boolMark(b bool) string {
	if b {
		return colorize("✓", text.FgGreen)
	}
	return dim("·")
}

// onOff renders a boolean switch as a colored On/Off word. Unlike boolMark's
// glyph it stays legible with color off or piped through another tool, so a
// state the user must not misread (e.g. whether the email-to-note address is
// accepting mail) is never ambiguous.
func onOff(b bool) string {
	if b {
		return colorize("On", text.FgGreen)
	}
	return colorize("Off", text.FgYellow)
}

// strike renders struck-through text — used for a value that still belongs to
// the account but is currently inactive (a disabled email-to-note address),
// mirroring how the web Settings card shows it.
func strike(s string) string { return colorize(s, text.CrossedOut) }

// shortID abbreviates a long opaque id/hash for table display, keeping the
// leading characters. Full ids are always available via --json.
func shortID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ===========================================================================
// Table & detail printing
// ===========================================================================

// newTable returns a go-pretty table writer pre-styled with the rounded,
// left-aligned house style and stdout as the output target.
func newTable() table.Writer {
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleRounded)
	t.Style().Format.Header = text.FormatDefault
	t.Style().Format.HeaderAlign = text.AlignLeft
	if useColor() {
		t.Style().Color.Header = text.Colors{text.FgCyan, text.Bold}
	}
	return t
}

// printTable renders a styled table of string rows under the given headers.
// An empty row set prints a friendly "No results." line instead.
func printTable(headers []string, rows [][]string) {
	if len(rows) == 0 {
		fmt.Println("No results.")
		return
	}
	t := newTable()
	headerRow := make(table.Row, len(headers))
	for i, h := range headers {
		headerRow[i] = h
	}
	t.AppendHeader(headerRow)
	for _, row := range rows {
		tr := make(table.Row, len(row))
		for i, cell := range row {
			tr[i] = cell
		}
		t.AppendRow(tr)
	}
	t.Render()
}

// printTableWrapped renders a table like printTable but soft-wraps one column to
// widthMax characters (1-based column number) instead of letting a long cell
// stretch the whole table past the terminal. Used where a cell holds a sentence
// rather than a field — truncating those loses the half that says what to do.
func printTableWrapped(headers []string, rows [][]string, col, widthMax int) {
	if len(rows) == 0 {
		fmt.Println("No results.")
		return
	}
	t := newTable()
	headerRow := make(table.Row, len(headers))
	for i, h := range headers {
		headerRow[i] = h
	}
	t.AppendHeader(headerRow)
	for _, row := range rows {
		tr := make(table.Row, len(row))
		for i, cell := range row {
			tr[i] = cell
		}
		t.AppendRow(tr)
	}
	// WrapSoft breaks on word boundaries; the default enforcer cuts mid-word,
	// which is what makes a wrapped sentence hard to read in the first place.
	t.SetColumnConfigs([]table.ColumnConfig{
		{Number: col, WidthMax: widthMax, WidthMaxEnforcer: text.WrapSoft},
	})
	t.Render()
}

// printKV renders a vertical key/value detail view (one row per pair).
func printKV(pairs [][2]string) {
	t := newTable()
	t.Style().Options.SeparateRows = false
	for _, p := range pairs {
		t.AppendRow(table.Row{colorize(p[0], text.Faint), p[1]})
	}
	t.Render()
}

// printPagingFooter prints a dim "showing X–Y of N" footer when the response
// carries an offset-mode paging block with more pages available.
func printPagingFooter(data []byte) {
	p, ok := client.DecodePaging(data)
	if !ok {
		return
	}
	count := len(client.CollectionItems(data))
	if count == 0 {
		return
	}
	start := p.Offset + 1
	end := p.Offset + int64(count)
	footer := fmt.Sprintf("showing %d–%d of %d", start, end, p.Total)
	if p.HasMore {
		footer += " · use --limit/--offset for more"
	}
	fmt.Println(dim(footer))
}

// ===========================================================================
// Generic displays
// ===========================================================================

// displayMessage prints a simple success line. Used by commands whose response
// body carries no resource worth tabulating (e.g. revoke, ack).
func displayMessage(msg string) func([]byte) {
	return func(_ []byte) {
		fmt.Println(msg)
	}
}

// displayKVData renders a single resource (bare or data-wrapped) as a sorted
// key/value view — a reasonable default for responses without a bespoke table.
func displayKVData(data []byte) {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		fmt.Println(string(data))
		return
	}
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, str(root, k)})
	}
	printKV(pairs)
}

// renderError prints an error to stderr — never stdout, so a failure can never
// be mistaken for data by whatever the command was piped into. *client.APIError
// gets rich treatment: a red "code: message" line, bulleted validation details,
// and (in verbose mode) a dim request id and HTTP status. In --json mode the
// error is emitted as JSON instead, so a script parsing this CLI gets the same
// shape whether the command succeeded or failed.
func renderError(err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		if jsonOutput {
			renderErrorJSON(apiErr)
			return
		}
		// A plan limit is not a normal API failure — it is a wall the user has
		// hit and needs walked through, so it gets its own explanation rather
		// than a dump of the gate's internals.
		if apiErr.Code == planLimitCode {
			renderPlanLimitError(apiErr)
			return
		}
		header := apiErr.Message
		if header == "" {
			header = apiErr.Error()
		}
		fmt.Fprintln(os.Stderr, colorize("Error: ", text.FgRed, text.Bold)+header)
		if apiErr.Code != "" {
			fmt.Fprintln(os.Stderr, dim("  code: "+apiErr.Code))
		}
		for _, line := range apiErr.DetailLines() {
			fmt.Fprintln(os.Stderr, "  • "+line)
		}
		if verboseFlag {
			if apiErr.Status != 0 {
				fmt.Fprintln(os.Stderr, dim(fmt.Sprintf("  http: %d", apiErr.Status)))
			}
			if apiErr.RequestID != "" {
				fmt.Fprintln(os.Stderr, dim("  request_id: "+apiErr.RequestID))
			}
		}
		return
	}
	if jsonOutput {
		renderErrorJSON(&client.APIError{Code: "cli_error", Message: err.Error()})
		return
	}
	fmt.Fprintln(os.Stderr, colorize("Error: ", text.FgRed, text.Bold)+err.Error())
}

// renderErrorJSON writes the API's error envelope to stderr as JSON, for
// --json mode. It goes to stderr like every other diagnostic — stdout stays
// data-only, so `harbor ... --json | jq` never has to filter out an error
// object it did not ask for. A failure to marshal falls back to the one-line
// text form rather than printing nothing at all.
func renderErrorJSON(apiErr *client.APIError) {
	out, err := json.MarshalIndent(map[string]any{"error": apiErr}, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, colorize("Error: ", text.FgRed, text.Bold)+apiErr.Error())
		return
	}
	fmt.Fprintln(os.Stderr, string(out))
}

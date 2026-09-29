// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// Metadata is a small JSON object on every note, notebook and stack, for
// scripts and integrations to label things. Harbor never shows it or acts on
// it. This file holds everything the three record types share: the write
// flags, the list filters, the display, and the `meta` subcommands.

// metadataHelp explains the write flags. It is shared by every command that
// writes metadata so the rules read the same everywhere.
const metadataHelp = `METADATA is a small JSON object for scripts and integrations; Harbor never
shows it. In KEY=VALUE, the value is read as JSON when it is valid JSON (true,
5, ["a","b"], {"x":1}) and as plain text otherwise. Quote a value as JSON to
keep it text: 'zip="02134"'.

Setting and removing keys changes only those keys (a JSON Merge Patch). Giving
a whole object replaces all of it. A metadata-only change is not an edit: the
updated time does not move and no history version is made.`

// metaFlagNames names the flags one command uses for the four kinds of
// metadata write. The record commands prefix them (--meta, --unset-meta) so
// they read clearly beside --title and --name; the `meta` subcommands do not
// need the prefix. An empty name means the command does not offer that kind.
type metaFlagNames struct {
	set, replace, unset, clear string
}

// recordMetaFlags are the metadata flags on `notes update` / `notebooks update`.
var recordMetaFlags = metaFlagNames{set: "meta", replace: "meta-json", unset: "unset-meta", clear: "clear-meta"}

// createMetaFlags are the metadata flags on `notes create` / `notebooks create`.
// A new record has nothing to remove, so only setting and replacing apply.
var createMetaFlags = metaFlagNames{set: "meta", replace: "meta-json"}

// metaCmdFlags are the write flags on the `meta` subcommands.
var metaCmdFlags = metaFlagNames{set: "set", replace: "replace", unset: "unset", clear: "clear"}

// addMetaWriteFlags registers the metadata write flags a command offers.
func addMetaWriteFlags(cmd *cobra.Command, names metaFlagNames) {
	if names.set != "" {
		cmd.Flags().StringArray(names.set, nil, "Set a metadata key: KEY=VALUE (repeatable; VALUE is JSON if valid, else a string)")
	}
	if names.replace != "" {
		cmd.Flags().String(names.replace, "", "Replace all metadata with this JSON object ('{...}', @file.json, or @- for stdin)")
	}
	if names.unset != "" {
		cmd.Flags().StringArray(names.unset, nil, "Remove a metadata key (repeatable)")
	}
	if names.clear != "" {
		cmd.Flags().Bool(names.clear, false, "Remove all metadata")
	}
}

// metaChange is one metadata write, resolved from the flags. A merge sends a
// JSON Merge Patch (nil removes a key); a replace sends the whole new object,
// and an empty one clears.
type metaChange struct {
	replace bool
	object  map[string]any
}

// readMetaChange turns a command's metadata flags into one write, or nil when
// none were given. Everything is checked here, before any request, so a typo
// never leaves a record half-updated.
//
// Setting or removing keys alone is a merge, which leaves every other key as
// it is. Once the whole object is being replaced (--meta-json or --clear-meta),
// the set and unset flags are applied to that new object locally and it goes
// out in one request.
func readMetaChange(cmd *cobra.Command, names metaFlagNames) (*metaChange, error) {
	var sets, unsets []string
	if names.set != "" {
		sets, _ = cmd.Flags().GetStringArray(names.set)
	}
	if names.unset != "" {
		unsets, _ = cmd.Flags().GetStringArray(names.unset)
	}
	replacing := names.replace != "" && cmd.Flags().Changed(names.replace)
	clearing := names.clear != "" && boolFlag(cmd, names.clear)
	if len(sets) == 0 && len(unsets) == 0 && !replacing && !clearing {
		return nil, nil
	}
	if replacing && clearing {
		return nil, fmt.Errorf("use --%s or --%s, not both — each replaces all of the metadata", names.replace, names.clear)
	}

	change := &metaChange{object: map[string]any{}, replace: replacing || clearing}
	if replacing {
		source := stringFlag(cmd, names.replace)
		// --stdin and @- would both drain standard input, and the second reader
		// would silently get nothing.
		if source == "@-" && cmd.Flags().Lookup("stdin") != nil && boolFlag(cmd, "stdin") {
			return nil, fmt.Errorf("--stdin and --%s @- cannot both read standard input", names.replace)
		}
		obj, err := readMetaObject(source)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", names.replace, err)
		}
		change.object = obj
	}

	set := map[string]bool{}
	for _, pair := range sets {
		key, value, err := parseMetaPair(pair)
		if err != nil {
			return nil, fmt.Errorf("--%s %s: %w", names.set, pair, err)
		}
		// In a merge, null deletes the key, so `--meta a=null` would do the
		// opposite of what it says. Removing is its own flag.
		if value == nil {
			msg := fmt.Sprintf("--%s cannot set %q to null", names.set, key)
			if names.unset != "" {
				msg += fmt.Sprintf(" — to remove it, use --%s %s", names.unset, key)
			}
			return nil, errors.New(msg)
		}
		change.object[key] = value
		set[key] = true
	}
	for _, key := range unsets {
		if key == "" {
			return nil, fmt.Errorf("--%s needs a key", names.unset)
		}
		if set[key] {
			return nil, fmt.Errorf("--%s and --%s both name %q — pick one", names.set, names.unset, key)
		}
		if change.replace {
			delete(change.object, key)
		} else {
			change.object[key] = nil
		}
	}
	return change, nil
}

// parseMetaPair splits a KEY=VALUE flag at its first "=" and reads the value.
// The key's format is left to the server, which owns that rule and names the
// broken part when it refuses one.
func parseMetaPair(pair string) (string, any, error) {
	key, raw, ok := strings.Cut(pair, "=")
	if !ok {
		return "", nil, errors.New("expected KEY=VALUE")
	}
	if key == "" {
		return "", nil, errors.New("the key is empty")
	}
	return key, parseMetaValue(raw), nil
}

// parseMetaValue reads a flag value as JSON when it is valid JSON and as a
// plain string otherwise, so `true`, `5` and `["a","b"]` keep their types while
// `hello` needs no quoting. Numbers keep their exact text: a 64-bit id must
// reach the server unrounded, and a float64 cannot hold one.
func parseMetaValue(raw string) any {
	if !json.Valid([]byte(raw)) {
		return raw
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return raw
	}
	return value
}

// readMetaObject reads a whole metadata object from a flag: inline JSON,
// @path for a file, or @- for standard input.
func readMetaObject(source string) (map[string]any, error) {
	data := []byte(source)
	if path, ok := strings.CutPrefix(source, "@"); ok {
		var err error
		if path == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(path)
		}
		if err != nil {
			return nil, err
		}
	}
	return decodeMetaObject(data)
}

// decodeMetaObject parses a JSON object, keeping numbers as their exact text.
func decodeMetaObject(data []byte) (map[string]any, error) {
	if !json.Valid(data) {
		return nil, errors.New("not valid JSON")
	}
	obj := decodeNumbers(data)
	if obj == nil {
		return nil, errors.New(`must be a JSON object, like {"gallery": true}`)
	}
	return obj, nil
}

// applyMetaChange sends one metadata write to a record's /metadata route and
// returns the {"metadata": {…}} answer. Clearing answers 204 with no body, so
// its answer is filled in here: the result of a clear is always empty.
func applyMetaChange(c *client.Client, path string, change *metaChange) ([]byte, error) {
	var (
		data []byte
		err  error
	)
	switch {
	case change.replace && len(change.object) == 0:
		_, err = c.ClearMetadata(path)
		data = []byte(`{"metadata":{}}`)
	case change.replace:
		data, err = c.ReplaceMetadata(path, change.object)
	default:
		data, err = c.MergeMetadata(path, change.object)
	}
	if err != nil {
		return nil, mapMetadataError(err)
	}
	return data, nil
}

// writeMetaAfterUpdate applies an update's metadata flags once the record's
// own fields are saved, and puts the new metadata into the record that is
// printed.
//
// It runs second because a note update can carry base_usn ("nothing changed
// since I read it"), and a metadata write moves the usn — so writing metadata
// first would make the note's own update refuse itself.
func writeMetaAfterUpdate(c *client.Client, path, kind string, change *metaChange, record []byte) ([]byte, error) {
	result, err := applyMetaChange(c, path, change)
	if err != nil {
		return nil, fmt.Errorf("the %s was updated, but its metadata was not: %w", kind, err)
	}
	return spliceMetadata(record, result), nil
}

// spliceMetadata sets the metadata in a record response (bare, data-wrapped,
// or a {note, usn} mutation) to the object in a /metadata answer. Only the
// metadata member is touched; every other value is copied through as the
// server sent it. On anything it cannot read it returns the record unchanged.
func spliceMetadata(record, result []byte) []byte {
	var answer struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(result, &answer) != nil || len(answer.Metadata) == 0 {
		return record
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(record, &root) != nil {
		return record
	}
	target := root
	wrapper := ""
	for _, key := range []string{"note", "data"} {
		if inner, ok := root[key]; ok {
			if json.Unmarshal(inner, &target) != nil {
				return record
			}
			wrapper = key
			break
		}
	}
	target["metadata"] = answer.Metadata
	if wrapper != "" {
		inner, err := encodeRaw(target)
		if err != nil {
			return record
		}
		root[wrapper] = inner
	}
	out, err := encodeRaw(root)
	if err != nil {
		return record
	}
	return out
}

// encodeRaw marshals without escaping <, > and &, so a note body passes
// through as the server wrote it.
func encodeRaw(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ===========================================================================
// List filters
// ===========================================================================

// addMetaFilterFlags registers the metadata filters on a list command. They
// are not called --meta because `notes list --meta` already means "leave out
// note bodies".
func addMetaFilterFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("meta-eq", nil, "Only records whose metadata KEY equals VALUE: KEY=VALUE (repeatable; all must match)")
	cmd.Flags().StringArray("meta-has", nil, "Only records whose metadata has KEY, whatever its value (repeatable)")
}

// metaFilterQuery builds a list query from the ordinary params plus the
// metadata filters. The value goes to the server as text; the server applies
// the matching rule (strings ignore case, an array matches any element).
func metaFilterQuery(cmd *cobra.Command, params map[string]string) (url.Values, error) {
	q := url.Values{}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	equals, _ := cmd.Flags().GetStringArray("meta-eq")
	for _, pair := range equals {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--meta-eq %s: expected KEY=VALUE", pair)
		}
		q.Add("meta."+key, value)
	}
	has, _ := cmd.Flags().GetStringArray("meta-has")
	for _, key := range has {
		if key == "" {
			return nil, errors.New("--meta-has needs a key")
		}
		q.Add("meta_has", key)
	}
	return q, nil
}

// ===========================================================================
// Display
// ===========================================================================

// decodeNumbers parses a JSON object keeping numbers as their exact text
// (json.Number). parseJSON turns them into float64, which is fine for counts
// and timestamps but would print a 64-bit id stored in metadata rounded.
func decodeNumbers(data []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return m
}

// recordMetadata returns the metadata object of a record response — a list
// row, a bare or data-wrapped record, a {note, usn} mutation, or a
// {"metadata": {…}} answer. Nil when there is none.
func recordMetadata(data []byte) map[string]any {
	root := decodeNumbers(client.UnwrapData(data))
	if note, ok := root["note"].(map[string]any); ok {
		root = note
	}
	m, _ := root["metadata"].(map[string]any)
	return m
}

// formatMetadata renders metadata compactly on one line: "k=v, k2=v2", keys
// sorted so the same object always reads the same.
func formatMetadata(m map[string]any) string {
	keys := sortedMetaKeys(m)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+metaValueText(m[k]))
	}
	return strings.Join(parts, ", ")
}

// metaValueText renders one metadata value: a string as itself, anything else
// as compact JSON.
func metaValueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := encodeRaw(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// sortedMetaKeys returns a map's keys in order.
func sortedMetaKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// metaColumn renders each list row's metadata for a META column, and reports
// whether any row has some. The column is shown only then, so listings of
// records nobody has labelled look exactly as they did before.
func metaColumn(items []json.RawMessage) ([]string, bool) {
	cells := make([]string, len(items))
	found := false
	for i, raw := range items {
		if m := recordMetadata(raw); len(m) > 0 {
			cells[i] = truncate(formatMetadata(m), 40)
			found = true
		}
	}
	return cells, found
}

// withMetaColumn appends the META column to a list table when any row has
// metadata.
func withMetaColumn(items []json.RawMessage, headers []string, rows [][]string) ([]string, [][]string) {
	cells, show := metaColumn(items)
	if !show {
		return headers, rows
	}
	headers = append(headers, "META")
	for i := range rows {
		rows[i] = append(rows[i], cells[i])
	}
	return headers, rows
}

// metadataPair is the detail-view row for a record's metadata, or false when
// it has none.
func metadataPair(data []byte) ([2]string, bool) {
	m := recordMetadata(data)
	if len(m) == 0 {
		return [2]string{}, false
	}
	return [2]string{"Metadata", truncate(formatMetadata(m), 80)}, true
}

// displayMetadata renders a {"metadata": {…}} answer, one key per row.
func displayMetadata(data []byte) {
	m := recordMetadata(data)
	if len(m) == 0 {
		fmt.Println(dim("No metadata."))
		return
	}
	pairs := make([][2]string, 0, len(m))
	for _, k := range sortedMetaKeys(m) {
		pairs = append(pairs, [2]string{k, metaValueText(m[k])})
	}
	printKV(pairs)
}

// mapMetadataError turns the server's refusal of a metadata write into one
// plain line. The server names the broken rule in details.metadata (too many
// keys, a bad key, too deep), so that text is kept as it is. A plan limit is
// left alone: the renderer has its own treatment and exit code for it.
func mapMetadataError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "validation_failed":
			if rule, ok := apiErr.Details["metadata"]; ok {
				return fmt.Errorf("the metadata was refused: %v", rule)
			}
		case "payload_too_large":
			return errors.New("the metadata was refused: the request is over the server's 1 MiB limit (stored metadata is capped at 16 KB)")
		case "stack_not_found":
			return errors.New("no stack has that name — see 'harbor stacks list'")
		}
	}
	return err
}

// ===========================================================================
// The `meta` subcommands
// ===========================================================================

// metaCommandLong is the help text for a `meta` subcommand on one record type.
func metaCommandLong(kind string) string {
	return fmt.Sprintf(`Read a %s's metadata, or change it with --set, --unset, --replace or --clear.

With no flags it prints the metadata. --set and --unset change only the keys
they name. --replace swaps in a whole new object and --clear removes it all.

`, kind) + metadataHelp
}

// runMetaCommand is the whole of `notes meta`, `notebooks meta` and
// `stacks meta`: read the record's metadata, or apply the write the flags ask
// for, and print the result.
func runMetaCommand(cmd *cobra.Command, path string) error {
	// The flags are checked before the credentials so a typo answers the typo.
	change, err := readMetaChange(cmd, metaCmdFlags)
	if err != nil {
		return err
	}
	c, _, err := loadClientFromConfig()
	if err != nil {
		return err
	}
	var data []byte
	if change == nil {
		data, err = c.GetMetadata(path)
		err = mapMetadataError(err)
	} else {
		data, err = applyMetaChange(c, path, change)
	}
	if err != nil {
		return err
	}
	printResult(data, displayMetadata)
	return nil
}

// notesMetaCmd reads or changes one note's metadata.
var notesMetaCmd = &cobra.Command{
	Use:   "meta <id>",
	Short: "Read or change a note's metadata",
	Args:  cobra.ExactArgs(1),
	Long:  metaCommandLong("note"),
	Example: `  harbor notes meta 9c2e...
  harbor notes meta 9c2e... --set crm_id=4411 --set gallery=true
  harbor notes meta 9c2e... --unset gallery
  harbor notes meta 9c2e... --replace @meta.json
  harbor notes meta 9c2e... --clear`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetaCommand(cmd, client.NoteMetadataPath(args[0]))
	},
}

// notebooksMetaCmd reads or changes one notebook's metadata.
var notebooksMetaCmd = &cobra.Command{
	Use:   "meta <id>",
	Short: "Read or change a notebook's metadata",
	Args:  cobra.ExactArgs(1),
	Long:  metaCommandLong("notebook"),
	Example: `  harbor notebooks meta 5b1f...
  harbor notebooks meta 5b1f... --set gallery=true
  harbor notebooks meta 5b1f... --replace '{"client":"acme","rate":150}'
  harbor notebooks meta 5b1f... --clear`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetaCommand(cmd, client.NotebookMetadataPath(args[0]))
	},
}

// init wires the `meta` subcommands under notes and notebooks. `stacks meta`
// lives with the rest of the stack commands.
func init() {
	addMetaWriteFlags(notesMetaCmd, metaCmdFlags)
	addMetaWriteFlags(notebooksMetaCmd, metaCmdFlags)
	notesCmd.AddCommand(notesMetaCmd)
	notebooksCmd.AddCommand(notebooksMetaCmd)
}

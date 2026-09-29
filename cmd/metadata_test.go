// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// metaFlagsCmd builds a throwaway command carrying one set of metadata write
// flags and parses args into it, so readMetaChange can be tested without the
// shared command tree.
func metaFlagsCmd(t *testing.T, names metaFlagNames, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	addMetaWriteFlags(cmd, names)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v): %v", args, err)
	}
	return cmd
}

// TestParseMetaValue pins the value rule: valid JSON keeps its type, anything
// else is a string, and numbers keep their exact text.
func TestParseMetaValue(t *testing.T) {
	cases := []struct {
		raw  string
		want any
	}{
		{"true", true},
		{"false", false},
		{"5", json.Number("5")},
		{"1.50", json.Number("1.50")},
		{"12345678901234567890", json.Number("12345678901234567890")},
		{`["a","b"]`, []any{"a", "b"}},
		{`{"x":1}`, map[string]any{"x": json.Number("1")}},
		{`"5"`, "5"},
		{"hello", "hello"},
		{"hello world", "hello world"},
		{"02134", "02134"},
		{"True", "True"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseMetaValue(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseMetaValue(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
	}
}

// TestParseMetaPair covers the split at the first "=" and the two malformed
// shapes.
func TestParseMetaPair(t *testing.T) {
	key, value, err := parseMetaPair("url=https://x.test/?a=b")
	if err != nil || key != "url" || value != "https://x.test/?a=b" {
		t.Errorf("got %q %#v %v", key, value, err)
	}
	for _, bad := range []string{"gallery", "=true"} {
		if _, _, err := parseMetaPair(bad); err == nil {
			t.Errorf("parseMetaPair(%q) should fail", bad)
		}
	}
}

// TestReadMetaChange covers how the four kinds of flag combine into one write.
func TestReadMetaChange(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantReplace bool
		wantObject  map[string]any
	}{
		{"set keys merge", []string{"--meta", "a=true", "--meta", "b=5"}, false,
			map[string]any{"a": true, "b": json.Number("5")}},
		{"unset sends null", []string{"--unset-meta", "a"}, false,
			map[string]any{"a": nil}},
		{"set and unset merge together", []string{"--meta", "a=x", "--unset-meta", "b"}, false,
			map[string]any{"a": "x", "b": nil}},
		{"json replaces, then set and unset apply to it", []string{"--meta-json", `{"x":1,"z":2}`, "--meta", "y=2", "--unset-meta", "x"}, true,
			map[string]any{"y": json.Number("2"), "z": json.Number("2")}},
		{"clear alone is an empty replace", []string{"--clear-meta"}, true,
			map[string]any{}},
		{"clear then set replaces with just the set keys", []string{"--clear-meta", "--meta", "a=1"}, true,
			map[string]any{"a": json.Number("1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change, err := readMetaChange(metaFlagsCmd(t, recordMetaFlags, tc.args...), recordMetaFlags)
			if err != nil {
				t.Fatalf("readMetaChange: %v", err)
			}
			if change.replace != tc.wantReplace {
				t.Errorf("replace = %v, want %v", change.replace, tc.wantReplace)
			}
			if !reflect.DeepEqual(change.object, tc.wantObject) {
				t.Errorf("object = %#v, want %#v", change.object, tc.wantObject)
			}
		})
	}
}

// TestReadMetaChangeNoFlags is the "leave metadata alone" case.
func TestReadMetaChangeNoFlags(t *testing.T) {
	change, err := readMetaChange(metaFlagsCmd(t, recordMetaFlags), recordMetaFlags)
	if err != nil || change != nil {
		t.Errorf("got %#v, %v; want nil, nil", change, err)
	}
}

// TestReadMetaChangeRefusals covers every combination that is refused before
// a request is made, and that each says what to do instead.
func TestReadMetaChangeRefusals(t *testing.T) {
	cases := []struct {
		name  string
		names metaFlagNames
		args  []string
		want  string
	}{
		{"clear and json", recordMetaFlags, []string{"--clear-meta", "--meta-json", "{}"}, "not both"},
		{"null value", recordMetaFlags, []string{"--meta", "a=null"}, "--unset-meta a"},
		{"null value on create", createMetaFlags, []string{"--meta", "a=null"}, "cannot set"},
		{"set and unset same key", recordMetaFlags, []string{"--meta", "a=1", "--unset-meta", "a"}, "both name"},
		{"missing =", recordMetaFlags, []string{"--meta", "gallery"}, "expected KEY=VALUE"},
		{"empty unset", recordMetaFlags, []string{"--unset-meta", "a", "--unset-meta", ""}, "needs a key"},
		{"json not an object", recordMetaFlags, []string{"--meta-json", "[1]"}, "must be a JSON object"},
		{"json null", recordMetaFlags, []string{"--meta-json", "null"}, "must be a JSON object"},
		{"json invalid", recordMetaFlags, []string{"--meta-json", "{nope"}, "not valid JSON"},
		{"json missing file", recordMetaFlags, []string{"--meta-json", "@/no/such/file.json"}, "--meta-json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readMetaChange(metaFlagsCmd(t, tc.names, tc.args...), tc.names)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestReadMetaChangeFromFile covers --meta-json @file.
func TestReadMetaChangeFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(path, []byte(`{"client":"acme","rate":150}`), 0o600); err != nil {
		t.Fatal(err)
	}
	change, err := readMetaChange(metaFlagsCmd(t, recordMetaFlags, "--meta-json", "@"+path), recordMetaFlags)
	if err != nil {
		t.Fatalf("readMetaChange: %v", err)
	}
	want := map[string]any{"client": "acme", "rate": json.Number("150")}
	if !change.replace || !reflect.DeepEqual(change.object, want) {
		t.Errorf("got %#v", change)
	}
}

// TestReadMetaChangeStdinClash refuses two readers of standard input.
func TestReadMetaChangeStdinClash(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addMetaWriteFlags(cmd, recordMetaFlags)
	cmd.Flags().Bool("stdin", false, "")
	if err := cmd.ParseFlags([]string{"--stdin", "--meta-json", "@-"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readMetaChange(cmd, recordMetaFlags); err == nil || !strings.Contains(err.Error(), "standard input") {
		t.Errorf("err = %v", err)
	}
}

// TestSpliceMetadata covers the three record shapes an update can answer with,
// and that everything but the metadata passes through as the server sent it.
func TestSpliceMetadata(t *testing.T) {
	result := []byte(`{"metadata":{"gallery":true}}`)
	cases := []struct {
		name   string
		record string
		get    func(map[string]any) any
	}{
		{"note mutation", `{"note":{"id":"n1","content":"<p>a & b</p>","metadata":{}},"usn":7}`,
			func(m map[string]any) any { return m["note"].(map[string]any)["metadata"] }},
		{"bare record", `{"id":"nb1","name":"Work","metadata":{"old":1}}`,
			func(m map[string]any) any { return m["metadata"] }},
		{"data-wrapped record", `{"data":{"id":"nb1","metadata":{}}}`,
			func(m map[string]any) any { return m["data"].(map[string]any)["metadata"] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := spliceMetadata([]byte(tc.record), result)
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal %s: %v", out, err)
			}
			if got := tc.get(m); !reflect.DeepEqual(got, map[string]any{"gallery": true}) {
				t.Errorf("metadata = %#v in %s", got, out)
			}
		})
	}
	out := spliceMetadata([]byte(cases[0].record), result)
	if !strings.Contains(string(out), `"<p>a & b</p>"`) || !strings.Contains(string(out), `"usn":7`) {
		t.Errorf("other members were rewritten: %s", out)
	}
	if got := spliceMetadata([]byte(`{"id":"n1"}`), []byte(`not json`)); string(got) != `{"id":"n1"}` {
		t.Errorf("an unreadable answer must leave the record alone, got %s", got)
	}
}

// TestMetaFilterQuery covers the list filters' query: repeats, an empty value
// ("equals the empty string"), and the ordinary params alongside.
func TestMetaFilterQuery(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addMetaFilterFlags(cmd)
	if err := cmd.ParseFlags([]string{"--meta-eq", "gallery=true", "--meta-eq", "note=", "--meta-has", "crm_id", "--meta-has", "owner"}); err != nil {
		t.Fatal(err)
	}
	q, err := metaFilterQuery(cmd, map[string]string{"limit": "5", "order": ""})
	if err != nil {
		t.Fatalf("metaFilterQuery: %v", err)
	}
	if q.Get("meta.gallery") != "true" || q.Get("limit") != "5" {
		t.Errorf("query = %v", q)
	}
	if v, ok := q["meta.note"]; !ok || v[0] != "" {
		t.Errorf("empty value was dropped: %v", q)
	}
	if !reflect.DeepEqual(q["meta_has"], []string{"crm_id", "owner"}) {
		t.Errorf("meta_has = %v", q["meta_has"])
	}
	if _, ok := q["order"]; ok {
		t.Errorf("an unset ordinary param was sent: %v", q)
	}

	bad := &cobra.Command{Use: "test"}
	addMetaFilterFlags(bad)
	_ = bad.ParseFlags([]string{"--meta-eq", "gallery"})
	if _, err := metaFilterQuery(bad, nil); err == nil || !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Errorf("err = %v", err)
	}
}

// TestFormatMetadata pins the one-line form: sorted keys, strings as they are,
// everything else as compact JSON with numbers exact.
func TestFormatMetadata(t *testing.T) {
	m := recordMetadata([]byte(`{"metadata":{"z":"hi","a":[1,2],"id":12345678901234567890,"ok":true,"o":{"k":"<v>"}}}`))
	want := `a=[1,2], id=12345678901234567890, o={"k":"<v>"}, ok=true, z=hi`
	if got := formatMetadata(m); got != want {
		t.Errorf("formatMetadata = %q, want %q", got, want)
	}
}

// TestRecordMetadataShapes reads metadata from each shape a response can take.
func TestRecordMetadataShapes(t *testing.T) {
	for _, data := range []string{
		`{"metadata":{"a":1}}`,
		`{"id":"nb1","metadata":{"a":1}}`,
		`{"data":{"id":"nb1","metadata":{"a":1}}}`,
		`{"note":{"id":"n1","metadata":{"a":1}},"usn":3}`,
	} {
		if m := recordMetadata([]byte(data)); m["a"] != json.Number("1") {
			t.Errorf("recordMetadata(%s) = %#v", data, m)
		}
	}
	if m := recordMetadata([]byte(`{"id":"n1"}`)); m != nil {
		t.Errorf("a record with no metadata = %#v, want nil", m)
	}
}

// TestDisplayMetadata covers the key-per-row view and the empty case.
func TestDisplayMetadata(t *testing.T) {
	out := captureStdout(t, func() { displayMetadata([]byte(`{"metadata":{"client":"acme","rate":150}}`)) })
	for _, want := range []string{"client", "acme", "rate", "150"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	out = captureStdout(t, func() { displayMetadata([]byte(`{"metadata":{}}`)) })
	if !strings.Contains(out, "No metadata.") {
		t.Errorf("empty view = %q", out)
	}
}

// TestMetaColumnOnlyWhenSomeRowHasMetadata keeps unlabelled listings exactly as
// they were.
func TestMetaColumnOnlyWhenSomeRowHasMetadata(t *testing.T) {
	without := []byte(`{"data":[{"id":"n1","title":"A","metadata":{}}],"paging":{"total":1}}`)
	if out := captureStdout(t, func() { displayNotes(without) }); strings.Contains(out, "META") {
		t.Errorf("META column shown with no metadata:\n%s", out)
	}
	with := []byte(`{"data":[{"id":"n1","title":"A","metadata":{}},{"id":"n2","title":"B","metadata":{"gallery":true}}],"paging":{"total":2}}`)
	out := captureStdout(t, func() { displayNotes(with) })
	if !strings.Contains(out, "META") || !strings.Contains(out, "gallery=true") {
		t.Errorf("META column missing:\n%s", out)
	}
}

// TestMapMetadataError covers the refusals that get one plain line, and that a
// plan limit is left for the renderer's own treatment.
func TestMapMetadataError(t *testing.T) {
	refused := &client.APIError{Code: "validation_failed", Message: "The request was invalid.",
		Details: map[string]any{"metadata": "has 65 top-level keys; the limit is 64"}}
	if got := mapMetadataError(refused).Error(); got != "the metadata was refused: has 65 top-level keys; the limit is 64" {
		t.Errorf("validation = %q", got)
	}
	other := &client.APIError{Code: "validation_failed", Details: map[string]any{"title": "too long"}}
	if got := mapMetadataError(other); got != error(other) {
		t.Errorf("an unrelated validation error was rewritten: %v", got)
	}
	if got := mapMetadataError(apiErr("payload_too_large")).Error(); !strings.Contains(got, "1 MiB") {
		t.Errorf("payload = %q", got)
	}
	if got := mapMetadataError(apiErr("stack_not_found")).Error(); !strings.Contains(got, "harbor stacks list") {
		t.Errorf("stack = %q", got)
	}
	limit := apiErr(planLimitCode)
	if got := mapMetadataError(limit); got != error(limit) {
		t.Errorf("a plan limit was rewritten: %v", got)
	}
	if mapMetadataError(nil) != nil {
		t.Error("nil must stay nil")
	}
}

// TestNotesMetaReads covers `harbor notes meta <id>` with no flags.
func TestNotesMetaReads(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes/n1/metadata": {Status: 200, Body: `{"metadata":{"crm_id":4411}}`},
	})
	out, err := runCLI(t, m, "notes", "meta", "n1")
	if err != nil {
		t.Fatalf("notes meta: %v", err)
	}
	if !strings.Contains(out, "crm_id") || !strings.Contains(out, "4411") {
		t.Errorf("output:\n%s", out)
	}
}

// TestNotesMetaMerges covers --set/--unset: one PATCH carrying a merge patch,
// with a 64-bit number sent exactly as typed.
func TestNotesMetaMerges(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/notes/n1/metadata": {Status: 200, Body: `{"metadata":{"a":true,"id":12345678901234567890}}`},
	})
	if _, err := runCLI(t, m, "notes", "meta", "n1", "--set", "a=true", "--set", "id=12345678901234567890", "--unset", "b"); err != nil {
		t.Fatalf("notes meta: %v", err)
	}
	if got := m.rawBodyOf(t, "PATCH /api/v1/notes/n1/metadata"); got != `{"a":true,"b":null,"id":12345678901234567890}` {
		t.Errorf("body = %s", got)
	}
}

// TestNotebooksMetaReplacesAndClears covers --replace (PUT) and --clear
// (DELETE, whose empty 204 still prints a result).
func TestNotebooksMetaReplacesAndClears(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PUT /api/v1/notebooks/nb1/metadata":    {Status: 200, Body: `{"metadata":{"client":"acme"}}`},
		"DELETE /api/v1/notebooks/nb1/metadata": {Status: 204, Body: ``},
	})
	if _, err := runCLI(t, m, "notebooks", "meta", "nb1", "--replace", `{"client":"acme"}`); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := m.rawBodyOf(t, "PUT /api/v1/notebooks/nb1/metadata"); got != `{"client":"acme"}` {
		t.Errorf("body = %s", got)
	}
	out, err := runCLI(t, m, "notebooks", "meta", "nb1", "--clear", "--json")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !strings.Contains(out, `"metadata": {}`) {
		t.Errorf("clear output = %q", out)
	}
}

// TestMetaCommandShowsTheRefusedRule covers a server 422 on a metadata write.
func TestMetaCommandShowsTheRefusedRule(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/notes/n1/metadata": {Status: 422, Body: `{"error":{"code":"validation_failed","message":"The request was invalid.",` +
			`"details":{"metadata":"key \"a b\" must match ^[A-Za-z0-9_-]{1,64}$"}}}`},
	})
	_, err := runCLI(t, m, "notes", "meta", "n1", "--set", "a b=1")
	if err == nil || !strings.Contains(err.Error(), "the metadata was refused: key") {
		t.Errorf("err = %v", err)
	}
}

// TestMetaCommandChecksFlagsBeforeCallingOut proves a bad flag makes no request.
func TestMetaCommandChecksFlagsBeforeCallingOut(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	if _, err := runCLI(t, m, "notes", "meta", "n1", "--set", "novalue"); err == nil {
		t.Fatal("expected an error")
	}
	if len(m.calls()) != 0 {
		t.Errorf("requests made: %v", m.calls())
	}
}

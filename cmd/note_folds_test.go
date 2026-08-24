// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-23

package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// ===========================================================================
// A Markdown write flattens a note's folds — say so
// ===========================================================================
//
// The fold marker (data-collapsed="true" on <li>/<h1>-<h3>) lives in the stored
// HTML and Markdown has nowhere to put it, so the documented read-edit-write loop
// un-folds an outline with nothing said. These prove the CLI says something, that
// it still writes, and that --format html really is the escape hatch the message
// promises.

// foldedBody is a note with three folds: a heading, a bullet with a child, and a
// checklist item. Every one of them keeps its children in the body — folding
// hides things in the app, it never removes them.
const foldedBody = `<h2 data-collapsed="true">Section</h2>` +
	`<p>Under the heading</p>` +
	`<ul><li data-collapsed="true">Parent<ul><li>Hidden child</li></ul></li></ul>` +
	`<ul data-type="checklist"><li data-checked="false" data-collapsed="true">Task<ul><li>Sub</li></ul></li></ul>`

// foldedNoteRoutes is a note carrying foldedBody and no tasks — the tasks route
// still has to answer, because the task guard reads it before every whole-body
// write.
func foldedNoteRoutes() map[string]mockReply {
	return map[string]mockReply{
		"GET /api/v1/notes/n1": {Status: 200, Body: `{"id":"n1","title":"Outline","usn":42,"is_encrypted":false,` +
			`"content":` + jsonString(foldedBody) + `}`},
		"GET /api/v1/notes/n1/tasks": {Status: 200, Body: `{"data":[],"paging":{"limit":500,"offset":0,"total":0,"has_more":false}}`},
		"PATCH /api/v1/notes/n1":     {Status: 200, Body: `{"note":{"id":"n1","title":"Outline","usn":43},"usn":43}`},
	}
}

// jsonString renders s as a JSON string literal, so a fixture body full of
// quotes can be pasted into a mock reply as-is instead of hand-escaped.
func jsonString(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		panic(err) // a string always marshals
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// countFolds — does it see what the server will see?
// ---------------------------------------------------------------------------

// The count is the server's answer, not a substring search: the marker only means
// anything on an element that can fold and only carrying `true`.
func TestCountFoldsMatchesTheServersRulesForTheMarker(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
	}{
		{"the three flavours of folded element", foldedBody, 3},
		{"h1 and h3 fold too", `<h1 data-collapsed="true">a</h1><h3 data-collapsed="true">b</h3>`, 2},
		{"false means expanded", `<li data-collapsed="false">a</li>`, 0},
		{"an unfolded note carries nothing", `<h2>Section</h2><ul><li>Parent</li></ul>`, 0},
		// The sanitizer allowlists the marker on li/h1-h3 only, so anywhere else it
		// is dropped on the way in and there is no fold to lose.
		{"elements that cannot fold", `<h4 data-collapsed="true">a</h4><p data-collapsed="true">b</p>` +
			`<div data-collapsed="true">c</div><blockquote data-collapsed="true">d</blockquote>`, 0},
		{"a value the server rejects", `<li data-collapsed="TRUE">a</li><li data-collapsed="1">b</li>`, 0},
		// bluemonday drops the CONTENT of these eleven elements, not just the tag,
		// so a marker inside one never reaches the stored body.
		{"inside a skipped subtree", `<noembed><li data-collapsed="true">a</li></noembed>`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countFolds(tc.body, "html"); got != tc.want {
				t.Errorf("countFolds(%q, html) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

// Markdown is counted the way the server reads it — converted first. Raw HTML
// inside Markdown IS passed through, so a body that carries the markers by hand
// really does keep its folds; one where they landed in a code block does not.
func TestCountFoldsReadsMarkdownTheWayTheServerWill(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
	}{
		{"a plain Markdown outline cannot express a fold", "## Section\n\n- Parent\n  - Child\n", 0},
		{"raw HTML passes through", "Before\n\n<ul><li data-collapsed=\"true\">Parent</li></ul>\n\nAfter\n", 1},
		{"four-space indentation makes it a code sample", "Here is the markup:\n\n    <li data-collapsed=\"true\">a</li>\n", 0},
		{"a fenced block is a code sample too", "```html\n<li data-collapsed=\"true\">a</li>\n```\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countFolds(tc.body, "markdown"); got != tc.want {
				t.Errorf("countFolds(%q, markdown) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The warning itself
// ---------------------------------------------------------------------------

// The warning has one job beyond existing: telling the reader how many folds go
// and how to keep them.
func TestWarnFoldFlatteningSaysHowManyAndHowToKeepThem(t *testing.T) {
	out := captureStderr(t, func() {
		warnFoldFlattening("n1", foldedBody, "# Rewritten", "markdown")
	})

	for _, want := range []string{"3 folds", "n1", "--format html"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning never mentions %q:\n%s", want, out)
		}
	}
}

// Nothing to warn about means nothing printed. A warning that fires on every
// update is a warning nobody reads.
func TestWarnFoldFlatteningStaysQuietWhenNoFoldIsLost(t *testing.T) {
	cases := []struct{ name, stored, content, format string }{
		{"html is the escape hatch, not a warning", foldedBody, "<p>rewritten</p>", "html"},
		{"the note has no folds", "<h2>Section</h2><p>body</p>", "# Rewritten", "markdown"},
		{"the Markdown carries the markers through", foldedBody,
			"<h2 data-collapsed=\"true\">Section</h2>\n\n<ul><li data-collapsed=\"true\">Parent</li></ul>\n\n" +
				"<ul><li data-collapsed=\"true\">Task</li></ul>\n", "markdown"},
		{"the write adds folds rather than dropping them", "<li data-collapsed=\"true\">a</li>",
			"<ul><li data-collapsed=\"true\">a</li><li data-collapsed=\"true\">b</li></ul>\n", "markdown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				warnFoldFlattening("n1", tc.stored, tc.content, tc.format)
			})
			if out != "" {
				t.Errorf("warned when nothing was lost:\n%s", out)
			}
		})
	}
}

// Singular reads as English, not as "1 folds".
func TestWarnFoldFlatteningAgreesWithItsCount(t *testing.T) {
	out := captureStderr(t, func() {
		warnFoldFlattening("n1", `<li data-collapsed="true">a</li>`, "# Rewritten", "markdown")
	})

	if !strings.Contains(out, "1 fold ") {
		t.Errorf("want a singular 'fold':\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Wired to the real command
// ---------------------------------------------------------------------------

// The headline case: a Markdown update against a folded note warns and STILL
// WRITES. Flattening an outline loses a view preference, not content, so
// refusing would block a legitimate edit — and prompting would hang the agents
// and scripts this CLI is driven by.
func TestNotesUpdateWarnsButProceedsWhenMarkdownFlattensTheFolds(t *testing.T) {
	m := newAPIMock(t, foldedNoteRoutes())

	var err error
	errOut := captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")
	})

	if err != nil {
		t.Fatalf("the update was refused instead of warned about: %v", err)
	}
	sent, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["content"].(string)
	if sent != "# Rewritten" {
		t.Errorf("the body the user asked for was not written: %q", sent)
	}
	for _, want := range []string{"3 folds", "--format html"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr never mentions %q:\n%s", want, errOut)
		}
	}
}

// The warning goes to stderr so a piped --json stdout stays machine-readable.
func TestNotesUpdateKeepsTheFoldWarningOffStdout(t *testing.T) {
	m := newAPIMock(t, foldedNoteRoutes())

	var out string
	var err error
	errOut := captureStderr(t, func() {
		out, err = runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten", "--json")
	})

	if err != nil {
		t.Fatalf("notes update --json: %v", err)
	}
	if strings.Contains(out, "fold") {
		t.Errorf("the warning leaked into stdout, corrupting the JSON:\n%s", out)
	}
	if !strings.Contains(errOut, "3 folds") {
		t.Errorf("the warning did not reach stderr:\n%s", errOut)
	}
}

// --format html is what the warning tells people to use, so it has to be both
// silent and lossless: the markers go out on the wire exactly as they came in.
func TestNotesUpdateWithHTMLRoundTripsTheFoldsUntouched(t *testing.T) {
	m := newAPIMock(t, foldedNoteRoutes())

	edited := strings.Replace(foldedBody, "<p>Under the heading</p>", "<p>Edited</p>", 1)
	var err error
	errOut := captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "update", "n1", "--content", edited, "--format", "html")
	})

	if err != nil {
		t.Fatalf("notes update --format html: %v", err)
	}
	if errOut != "" {
		t.Errorf("the escape hatch printed a warning of its own:\n%s", errOut)
	}
	body := m.bodyOf(t, "PATCH /api/v1/notes/n1")
	sent, _ := body["content"].(string)
	if sent != edited {
		t.Errorf("the HTML body was not written through byte-for-byte:\n got %q\nwant %q", sent, edited)
	}
	if n := strings.Count(sent, `data-collapsed="true"`); n != 3 {
		t.Errorf("the wire body carries %d fold markers, want 3:\n%s", n, sent)
	}
	if f, _ := body["content_format"].(string); f != "html" {
		t.Errorf("content_format = %q, want html", f)
	}
}

// An ordinary note has no folds and must not be nagged.
func TestNotesUpdateSaysNothingAboutANoteWithNoFolds(t *testing.T) {
	routes := foldedNoteRoutes()
	routes["GET /api/v1/notes/n1"] = mockReply{Status: 200,
		Body: `{"id":"n1","title":"Outline","usn":42,"is_encrypted":false,"content":"<h2>Section</h2><p>body</p>"}`}
	m := newAPIMock(t, routes)

	var err error
	errOut := captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")
	})

	if err != nil {
		t.Fatalf("notes update: %v", err)
	}
	if errOut != "" {
		t.Errorf("warned about a note with no folds:\n%s", errOut)
	}
}

// `notes append` adds to the end and replaces nothing, so it cannot flatten an
// existing fold — and must not pretend it can. Asserting on the traffic is what
// makes this a real test: append never reads the note at all.
func TestNotesAppendLeavesTheFoldsAloneAndSaysNothing(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/notes/n1/append": {Status: 200, Body: `{"note":{"id":"n1","title":"Outline","usn":43},"usn":43}`},
	})

	var err error
	errOut := captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "append", "n1", "--content", "- one more thing")
	})

	if err != nil {
		t.Fatalf("notes append: %v", err)
	}
	if errOut != "" {
		t.Errorf("append warned about folds it cannot touch:\n%s", errOut)
	}
	if got := m.calls(); len(got) != 1 || got[0] != "POST /api/v1/notes/n1/append" {
		t.Errorf("append read or wrote more than the append itself: %v", got)
	}
}

// ---------------------------------------------------------------------------
// The other loss point: decrypting back into Markdown
// ---------------------------------------------------------------------------

// `notes decrypt --format markdown` hands the decrypted body to the server to be
// re-rendered, and a note sealed as HTML can lose its folds on the way through.
// The fixture indents the markup, which is what makes goldmark read it as a code
// sample rather than markup.
func TestPlanDecryptWarnsWhenMarkdownWouldFlattenTheFolds(t *testing.T) {
	key := newMasterKey(t)
	sealed := "Notes:\n\n    <li data-collapsed=\"true\">Parent</li>\n"
	cm := newConvertMock(t, encryptedFixture(t, key, sealed))
	c := client.NewClient(cm.m.baseURL(), "tok")

	var body map[string]any
	var err error
	errOut := captureStderr(t, func() {
		body, err = planDecrypt(c, key, convertNoteID, "markdown", true)
	})

	if err != nil {
		t.Fatalf("planDecrypt: %v", err)
	}
	if body == nil {
		t.Fatal("the decrypt was skipped, so nothing was checked")
	}
	for _, want := range []string{"1 fold", convertNoteID, "--format html"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the decrypt warning never mentions %q:\n%s", want, errOut)
		}
	}
}

// The default, --format html, is the lossless one and says nothing.
func TestPlanDecryptSaysNothingWhenTheFoldsSurvive(t *testing.T) {
	key := newMasterKey(t)
	cm := newConvertMock(t, encryptedFixture(t, key, foldedBody))
	c := client.NewClient(cm.m.baseURL(), "tok")

	var err error
	errOut := captureStderr(t, func() {
		_, err = planDecrypt(c, key, convertNoteID, "html", true)
	})

	if err != nil {
		t.Fatalf("planDecrypt: %v", err)
	}
	if errOut != "" {
		t.Errorf("an html decrypt warned about folds it keeps:\n%s", errOut)
	}
}

// ---------------------------------------------------------------------------
// The terminal shows folded children in full
// ---------------------------------------------------------------------------

// A terminal has no chevron to click, so a fold is a marker with nothing to do
// there and the hidden children print like any other text. This pins that
// deliberately: it matches the "downloads render expanded" decision, and a
// reader who cannot see the child would think the note lost it.
func TestDisplayNoteShowsFoldedChildrenInFull(t *testing.T) {
	data := []byte(`{"id":"n1","title":"Outline","content":` + jsonString(foldedBody) + `}`)

	out := captureStdout(t, func() { displayNote(data) })

	for _, want := range []string{"Section", "Parent", "Hidden child", "Task", "Sub"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered note hides %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "data-collapsed") {
		t.Errorf("the marker itself leaked into the rendered body:\n%s", out)
	}
}

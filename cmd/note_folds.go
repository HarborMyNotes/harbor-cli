// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-23

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// ===========================================================================
// Collapsible outlines and the Markdown round trip
// ===========================================================================
//
// A folded heading or list item carries data-collapsed="true" on the element
// itself, allowlisted by the server's sanitizer on <li> and <h1>-<h3>
// (app.harbor.my#1587). Absent means expanded, so an ordinary note carries
// nothing at all.
//
// Markdown has nowhere to put an attribute, so the CLI's headline read-edit-write
// loop — `notes get --format markdown`, edit, `notes update` — hands the server a
// body with every marker gone and the note is re-rendered flat. Someone who spent
// ten minutes arranging an outline on their Mac and then fixed a typo here would
// find it flattened with nothing said.
//
// This WARNS and proceeds, where the task guard in note_tasks.go refuses. The
// difference is what is at stake: a released task is deleted, while a flattened
// outline costs a view preference and not one word of the note — every character
// stays in the body and stays searchable. Refusing would block a legitimate edit
// over a preference, and prompting would hang the agents and scripts that drive
// this CLI non-interactively.

// collapsedValuePattern is the server's own constraint on the marker: the two
// booleans and nothing else (notes.collapsedPattern). Keeping the same constraint
// here is what makes a count on this side mean the same thing as a count on that
// one.
var collapsedValuePattern = regexp.MustCompile(`^(true|false)$`)

// foldSanitizer reduces the server's sanitize step to the part that decides
// whether a fold marker survives: it lives only on <li>/<h1>-<h3>, and only
// carrying `true` or `false`. It is bluemonday, like the server's, so the eleven
// elements whose content bluemonday drops wholesale are inherited rather than
// re-derived — a marker inside one of those is stripped on both sides.
//
// Everything the server allows and this does not is stripped tag-only, children
// kept, which can neither hide nor reveal a marker.
var foldSanitizer = newFoldSanitizer()

// newFoldSanitizer builds the policy described above. It is a function so a test
// can build an independent instance rather than mutate the shared one.
func newFoldSanitizer() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("li", "h1", "h2", "h3")
	p.AllowAttrs("data-collapsed").Matching(collapsedValuePattern).OnElements("li", "h1", "h2", "h3")
	return p
}

// countFolds returns how many folded elements a body still has once the server
// has finished with it. format is "markdown" or "html" and says whether the body
// passes through goldmark first, mirroring noteTaskBlockIDs — raw HTML inside
// Markdown IS passed through (the server converts with WithUnsafe), so a body
// that carries the markers by hand really does keep its folds.
//
// Only `true` is counted. `false` is a value the sanitizer accepts from another
// client and it means expanded.
func countFolds(content, format string) int {
	scan := content
	if format == "markdown" {
		var buf bytes.Buffer
		if err := markdownConverter.Convert([]byte(scan), &buf); err != nil {
			// Unreachable in practice: goldmark errors on a misconfigured renderer,
			// never on user input, and this renderer is a package-level literal.
			// Reading the body as carrying no folds at worst prints a warning about
			// folds that would have survived, which costs one line of stderr.
			return 0
		}
		scan = buf.String()
	}
	return strings.Count(foldSanitizer.Sanitize(scan), `data-collapsed="true"`)
}

// warnFoldFlattening prints one stderr line when a whole-body Markdown write
// drops folds the stored note has. It never blocks, never prompts and never
// fails the command.
//
// Only a Markdown write is warned about. An HTML write is the escape hatch the
// message names, and someone writing HTML can see the markers in front of them —
// dropping one there is an edit, not an accident.
//
// Both sides are counted the same way so the message is true rather than merely
// cautious: a Markdown body that carries the markers through as raw HTML keeps
// its folds and says nothing.
func warnFoldFlattening(noteID, storedHTML, content, format string) {
	if format != "markdown" {
		return
	}
	stored := countFolds(storedHTML, "html")
	if stored == 0 {
		return
	}
	lost := stored - countFolds(content, format)
	if lost <= 0 {
		return
	}
	fmt.Fprintln(os.Stderr, dim(fmt.Sprintf(
		"⚠ Flattening %d %s in note %s.\n"+
			"  Markdown cannot carry a collapsed outline. No text is lost, only the folded\n"+
			"  view — read and write with --format html to keep it.",
		lost, pluralize(lost, "fold", "folds"), noteID)))
}

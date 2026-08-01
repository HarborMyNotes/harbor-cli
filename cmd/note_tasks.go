// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-31

package cmd

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// ===========================================================================
// Protecting a note's tasks from a whole-body replacement
// ===========================================================================
//
// A note's BODY is authoritative for its tasks. Each one lives in the body as a
// <harbor-task id="…"> block, and the server reconciles the note's task set
// against those blocks on every content write (notes.SyncNoteTasks): a task the
// new body no longer references is TOMBSTONED, not detached
// (HarborMyNotes/app.harbor.my#461's delete-on-release).
//
// `harbor notes update --content/--file/--stdin` replaces the body wholesale, so
// it used to delete every task on the note without a word — exit 0, no warning,
// nothing in --help (issue #62). The same write also unlinks inline attachments
// (<harbor-embed>) and drops note→note links, which the body derives the same way.
//
// The guard below REFUSES such an update rather than picking for the user, because
// both automatic behaviours are wrong in one direction: silently deleting is the
// bug, and silently re-appending the blocks would resurrect a task the user
// deliberately deleted by editing the block out — which is a supported way to
// delete a task. --keep-tasks and --allow-task-loss say which was meant.

var (
	// taskBlockAttrRe finds the id attribute of every <harbor-task …> tag in a
	// body, in all three attribute spellings (double-quoted, single-quoted, bare).
	// It is deliberately a scanner and not an HTML parse: the CLI has no HTML
	// parser dependency, and the two directions of imprecision are not equal — see
	// noteTaskBlockIDs.
	taskBlockAttrRe = regexp.MustCompile(`(?is)<harbor-task\b[^>]*?\sid\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+))`)

	// taskBlockIDRe is the server's own rule for a block id worth anything: the
	// canonical 36-character hyphenated UUID (notes.taskIDRe). A block whose id is
	// spelled any other way (braced, urn-prefixed, bare 32-hex) references no task
	// the reconciler can resolve, so it neither saves a task nor endangers one.
	taskBlockIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	// htmlCommentRe matches an HTML comment, which the server's sanitizer strips —
	// so a block inside one is text, not markup, and must not count as carried over.
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

	// mdInlineCodeRe matches a single-backtick Markdown code span. Prose about a
	// task block ("the `<harbor-task id=…>` block") is escaped by the Markdown
	// converter, so it is text too.
	mdInlineCodeRe = regexp.MustCompile("`[^`\n]*`")
)

// noteTaskBlockIDs returns the distinct task ids a body references through its
// <harbor-task id="…"> blocks, lower-cased, in order of first appearance. format
// is "markdown" or "html" and selects how much of the input is code rather than
// markup.
//
// WHY A SCANNER AND NOT A PARSER. The two ways this can be wrong are not
// symmetric, and the scanner is arranged so the cheap errors are the safe ones:
//
//   - Over-detecting in the note's CURRENT body means the guard protects a task
//     that was not really at risk — a refusal the user did not need. Harmless.
//   - Over-detecting in the NEW content means believing a task is carried over
//     when the server will not see its block — a silent deletion, i.e. the very
//     bug. So the Markdown constructs that turn markup back into text (fenced
//     code, inline code spans) and HTML comments are removed before scanning.
//
// KNOWN LIMIT, stated rather than hidden: a 4-space-indented Markdown code block
// is not stripped, because in this position indentation is far more often list
// continuation than code. A task block hidden in one would be counted as carried
// when it is not. It takes pasting your own note's task UUID into an indented code
// sample to reach; `--format html` avoids the question entirely.
func noteTaskBlockIDs(content, format string) []string {
	scan := htmlCommentRe.ReplaceAllString(content, " ")
	if format == "markdown" {
		scan = stripMarkdownFences(scan)
		scan = mdInlineCodeRe.ReplaceAllString(scan, " ")
	}

	seen := map[string]bool{}
	out := []string{}
	for _, m := range taskBlockAttrRe.FindAllStringSubmatch(scan, -1) {
		// Exactly one of the three alternatives captured; the rest are empty.
		raw := m[1] + m[2] + m[3]
		id := strings.ToLower(strings.TrimSpace(raw))
		if !taskBlockIDRe.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// stripMarkdownFences blanks out fenced code blocks (``` or ~~~) so markup quoted
// inside one is not mistaken for markup that will reach the server. It is a line
// scanner because Go's regexp has no backreferences, and a fence closes only on a
// run of the same character at least as long as the one that opened it. An
// unterminated fence runs to the end of the input, exactly as CommonMark says.
func stripMarkdownFences(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	fence := "" // the open fence's marker run, empty when not inside one
	for _, line := range lines {
		marker := markdownFenceMarker(line)
		switch {
		case fence == "" && marker != "":
			fence = marker
			out = append(out, "")
		case fence != "" && marker != "" && marker[0] == fence[0] && len(marker) >= len(fence):
			fence = ""
			out = append(out, "")
		case fence != "":
			out = append(out, "")
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// markdownFenceMarker returns the leading run of ``` or ~~~ that makes a line a
// code fence (three or more of one character, indented at most three spaces), or
// "" when the line is not a fence.
func markdownFenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "" // four or more spaces is an indented code block, not a fence
	}
	if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~") {
		return ""
	}
	ch := trimmed[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	return trimmed[:n]
}

// taskBlockHTML renders the canonical in-note block for a task id — the same
// spelling the server writes (notes.taskBlockHTML), so a block the CLI re-appends
// round-trips through the server's reconciler unchanged. Only ids that already
// passed taskBlockIDRe reach here, so there is nothing to escape.
func taskBlockHTML(taskID string) string {
	return `<harbor-task id="` + taskID + `"></harbor-task>`
}

// noteTask is one task a body replacement could destroy: its id, and its title
// when we know it (a title is what makes "is this the one I meant to drop?"
// answerable, where a bare UUID is not).
type noteTask struct {
	ID    string
	Title string
}

// noteTasksAtRisk returns every task a whole-body write of noteID could delete,
// in a stable order, given the note's CURRENT body.
//
// It is the UNION of two sources, because neither alone is the server's release
// set and each covers the other's blind spot:
//
//   - GET /notes/:id/tasks — the authoritative one. It selects
//     `deleted = 0 AND note_id = :id`, which is character-for-character the query
//     SyncNoteTasks walks to decide what to tombstone, so it also catches a task
//     LINKED to the note but no longer referenced by it (the app.harbor.my#1094
//     orphan being healed by #1098) — a task the body cannot tell us about and
//     that this write would silently take with it.
//   - The <harbor-task> blocks in the current body — the fallback. A read of the
//     task list that fails (an older server, a hiccup) must not silently downgrade
//     a data guard to nothing, and this source needs no second endpoint because
//     the note has already been read.
//
// Over-counting is the safe direction: an id in the body that owns no task row
// costs at most a refusal the user did not need, where under-counting costs the
// task. Ids are lower-cased, matching the server's canonicalization.
func noteTasksAtRisk(c *client.Client, noteID, currentBody string) []noteTask {
	out := []noteTask{}
	seen := map[string]bool{}
	for _, t := range listNoteTasks(c, noteID) {
		if t.ID == "" || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	for _, id := range noteTaskBlockIDs(currentBody, "html") {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, noteTask{ID: id})
	}
	return out
}

// listNoteTasks reads a note's tasks, in their in-note order. Any error yields an
// empty slice rather than a failure: this feeds a union whose other half needs no
// network at all, so degrading here costs precision, not protection.
func listNoteTasks(c *client.Client, noteID string) []noteTask {
	data, err := c.ListNoteTasks(noteID, map[string]string{"limit": "500"})
	if err != nil {
		return nil
	}
	items := client.CollectionItems(data)
	out := make([]noteTask, 0, len(items))
	for _, raw := range items {
		t := parseJSON(raw)
		if id := strings.ToLower(strings.TrimSpace(str(t, "id"))); id != "" {
			out = append(out, noteTask{ID: id, Title: str(t, "title")})
		}
	}
	return out
}

// guardNoteTaskLoss protects the tasks stored in a note's body from a content
// update that would replace it. It runs only for a content-carrying `notes
// update`; a metadata-only update cannot release anything and never pays for the
// extra read.
//
// It reads the note once and does three things with that read:
//
//  1. Sets base_usn on the outgoing write, so a task (or attachment, or link) that
//     lands between this read and that write is refused with 409 note_usn_stale
//     instead of being destroyed by a body that predates it. Read-then-write is
//     otherwise a real race, documented at app.harbor.my#1153; servers that predate
//     the precondition ignore the field.
//  2. Skips out for an encrypted note: the server cannot parse ciphertext, so it
//     never reconciles task blocks out of one and this write releases nothing.
//  3. Compares the tasks at risk (noteTasksAtRisk) against the blocks in the new
//     content and, when the new content drops any, applies the user's choice:
//     --keep-tasks re-appends the dropped blocks, --allow-task-loss proceeds, and
//     neither refuses the update with nothing written.
//
// body is mutated in place (base_usn, and content when keeping). The parsed note is
// returned so the caller's encryption step reuses this read instead of repeating it.
func guardNoteTaskLoss(cmd *cobra.Command, c *client.Client, noteID, format string, body map[string]any) (map[string]any, error) {
	keep, allowLoss := boolFlag(cmd, "keep-tasks"), boolFlag(cmd, "allow-task-loss")
	if keep && allowLoss {
		return nil, errors.New("--keep-tasks and --allow-task-loss cannot be used together — they ask for opposite things")
	}

	raw, err := c.GetNote(noteID, map[string]string{"format": "html"})
	if err != nil {
		return nil, mapNoteError(err)
	}
	note := parseJSON(client.UnwrapData(raw))
	if note == nil {
		// Fail closed. Proceeding here would be running the write with the guard
		// silently switched off, which is the bug this function exists to stop.
		return nil, errors.New("could not read the note before replacing its body — refusing to overwrite it (a whole-body update deletes any task the new body omits)")
	}
	if usn := int64(num(note, "usn")); usn > 0 {
		body["base_usn"] = usn
	}
	if boolean(note, "is_encrypted") {
		return note, nil
	}

	atRisk := noteTasksAtRisk(c, noteID, str(note, "content"))
	if len(atRisk) == 0 {
		return note, nil
	}
	newContent, _ := body["content"].(string)
	carried := map[string]bool{}
	for _, id := range noteTaskBlockIDs(newContent, format) {
		carried[id] = true
	}
	lost := make([]noteTask, 0, len(atRisk))
	for _, t := range atRisk {
		if !carried[t.ID] {
			lost = append(lost, t)
		}
	}
	if len(lost) == 0 {
		return note, nil
	}

	switch {
	case keep:
		body["content"] = appendTaskBlocks(newContent, lost)
		fmt.Fprintf(os.Stderr, "Kept %s: their blocks were re-appended to the end of the new body.\n", countTasks(len(lost)))
		return note, nil
	case allowLoss:
		fmt.Fprintf(os.Stderr, "%s Deleting %s that the new body no longer carries.\n",
			redWarn("Warning:"), countTasks(len(lost)))
		return note, nil
	}
	return nil, errors.New(taskLossRefusal(lost))
}

// appendTaskBlocks puts the given task blocks at the end of a body, separated from
// whatever precedes them by a blank line. That separation is what keeps the blocks
// out of a trailing Markdown paragraph or list item; the raw HTML itself survives
// the Markdown converter, which runs with raw-HTML passthrough enabled
// (goldmark WithUnsafe) and whose sanitizer allowlists <harbor-task id>.
//
// The blocks land at the END rather than in their original positions: their old
// offsets are meaningless against a body the user just rewrote, and a task's order
// within a note is carried by its own position field, not by where the block sits.
func appendTaskBlocks(content string, tasks []noteTask) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(content, "\n"))
	b.WriteString("\n\n")
	for _, t := range tasks {
		b.WriteString(taskBlockHTML(t.ID))
		b.WriteString("\n")
	}
	return b.String()
}

// taskLossRefusal builds the message for a refused update: what would be deleted,
// why replacing the body deletes it, and the two ways to proceed on purpose. A task
// whose title we could not read is named by id — a less informative refusal still
// protects the data.
func taskLossRefusal(lost []noteTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "this update would delete %s stored in the note's body:\n", countTasks(len(lost)))
	for _, t := range lost {
		if t.Title != "" {
			fmt.Fprintf(&b, "  • %s (%s)\n", t.Title, t.ID)
			continue
		}
		fmt.Fprintf(&b, "  • %s\n", t.ID)
	}
	b.WriteString("\n--content/--file/--stdin REPLACE the note's body, and a task lives in that body\n")
	b.WriteString("as a <harbor-task> block — dropping the block deletes the task, it is not\n")
	b.WriteString("detached. Nothing has been written. Re-run with one of:\n\n")
	b.WriteString("  --keep-tasks       carry those blocks into the new body (appended at the end)\n")
	b.WriteString("  --allow-task-loss  delete them; that is what you meant\n\n")
	b.WriteString("Or use 'harbor notes append' to add to the body without replacing it.")
	return b.String()
}

// countTasks renders a task count with the right plural, for messages that read
// as sentences rather than as counters.
func countTasks(n int) string {
	if n == 1 {
		return "1 task"
	}
	return fmt.Sprintf("%d tasks", n)
}

// addTaskLossFlags registers the two ways to proceed with an update that would
// drop a note's task blocks. They are opposites and the guard rejects both at once.
func addTaskLossFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("keep-tasks", false, "Carry the note's existing <harbor-task> blocks into the new body")
	cmd.Flags().Bool("allow-task-loss", false, "Proceed even though replacing the body deletes the note's tasks")
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-31

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

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

// taskBlockIDRe is the server's own rule for a block id worth anything: the
// canonical 36-character hyphenated UUID (notes.taskIDRe). A block whose id is
// spelled any other way (braced, urn-prefixed, bare 32-hex) references no task
// the reconciler can resolve, so it neither saves a task nor endangers one.
var taskBlockIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// markdownConverter is the SERVER'S Markdown converter, configured identically:
// notes.NewContent builds `goldmark.New(WithExtensions(extension.GFM),
// WithRendererOptions(ghtml.WithUnsafe()))` and both repos pin goldmark v1.8.2.
// It is a package-level value because a goldmark.Markdown is safe for concurrent
// use and compiling one per call would be pure waste.
//
// It is not "a Markdown converter" — it is THE one whose output the server then
// reads task blocks out of, which is the only reason its answers are worth
// anything here. Keep the options and the pinned version in step with
// app.harbor.my's internal/notes/sanitize.go; a divergence between them is
// exactly the class of bug this replaced (issue #66).
var markdownConverter = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(ghtml.WithUnsafe()),
)

// blockSanitizer is the server's SANITIZE step, reduced to the only part of it that
// can change this file's answer.
//
// WHY IT EXISTS. The server does not read task blocks out of the converted HTML — it
// reads them out of the SANITIZED HTML (notes.derive: convert, Sanitize, then
// extractTaskIDs). Skipping that step was a silent-deletion bug of its own: bluemonday
// deletes an unallowed element's ENTIRE SUBTREE for the eleven elements in its default
// skip-content set, so a <harbor-task> inside e.g. <object> is real markup to the HTML
// parser, counted as carried by the guard, and then removed on the way through. Seven
// such bodies were measured deleting a task while the CLI exited 0.
//
// WHY IT IS BLUEMONDAY AND NOT A CHECK OF OUR OWN. The skip is a TOKEN-STREAM rule,
// not a tree rule: bluemonday counts from a <frame> start tag to a </frame> end tag,
// so a VOID <frame> — which the tree builder does not even make an ancestor — swallows
// everything after it to the end of the body. An "is any ancestor in the skip set?"
// walk over the parsed tree looks equivalent and is not; it misses that case. Running
// the same library on the same token stream cannot.
//
// WHY THIS MINIMAL POLICY IS EQUIVALENT TO THE SERVER'S RICH ONE, for this question.
// bluemonday can only make a <harbor-task id="…"> stop being readable three ways, and
// this policy matches the server on all three:
//
//   - the element is not allowlisted → both allowlist it (AllowElements("harbor-task")).
//   - the id attribute is not allowlisted → both allowlist it, unvalidated
//     (AllowAttrs("id").OnElements("harbor-task")), and neither marks harbor-task
//     allowed-without-attrs, so a block with no id is dropped by both — it references
//     no task either way.
//   - an ancestor is in setOfElementsToSkipContent → both inherit bluemonday's default
//     set untouched (neither calls SkipElementsContent/AllowElementsContent), and none
//     of its eleven elements is allowlisted by either policy, so both skip all eleven.
//
// Everything else the server's policy allows and this one does not is stripped
// TAG-ONLY, children kept, which cannot hide or reveal a block. Copying the server's
// full allowlist would be a second implementation to keep in step forever; this is
// three properties to keep in step, and TestBlockSanitizerMatchesTheServerOnWhatMatters
// states them.
var blockSanitizer = newBlockSanitizer()

// newBlockSanitizer builds the policy described above. It is a function so the test
// can build a second, independent instance rather than mutate the shared one.
func newBlockSanitizer() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("harbor-task")
	p.AllowAttrs("id").OnElements("harbor-task")
	return p
}

// noteTaskBlockIDs returns the distinct task ids a body references through its
// <harbor-task id="…"> blocks, lower-cased, in document order. format is "markdown"
// or "html" and says how much of the input is code rather than markup.
//
// WHY A REAL PARSE. This function answers one question — "which blocks will the
// SERVER see?" — and the only reliable way to answer it is to do what the server
// does: notes.extractTaskIDs walks an html.ParseFragment tree, so this walks the
// same tree from the same library. Getting it wrong in the new content is a silent
// deletion, and a regex scanner gets it wrong in ways that are not hypothetical:
// a body ending in an unclosed <textarea>, <script>, <style>, <title> or a
// truncated tag puts everything after it inside that element as TEXT, which a
// scanner happily reads as markup and the server does not. Each of those was
// measured deleting a task while the CLI reported success. A parser handles them,
// and the ones nobody has thought of yet, by construction.
//
// MARKDOWN gets the same treatment one step earlier, for the same reason. The
// server converts it with goldmark before any of that HTML parsing happens, and
// goldmark ESCAPES what it treats as code — so this CONVERTS IT WITH GOLDMARK TOO,
// the server's own instance settings and version (markdownConverter), and parses
// the result. Comments need no special case either: the parser makes them comment
// nodes, terminated or not.
//
// WHY CONVERT RATHER THAN STRIP (issue #66). This used to blank out the code
// constructs by hand — a fence scanner plus an inline-code regex — and hand-written
// stripping is a SECOND implementation of CommonMark that has to agree with the
// first one everywhere or it is wrong. It did not. A 4-space-indented code block
// was not stripped, so a task block inside one was counted as carried, the guard
// let the update through, and the server (which sees `<pre><code>&lt;harbor-task…`,
// text) deleted the task. Adding indented blocks to the stripper would have fixed
// that one input and left the shape intact: every construct goldmark treats as code
// is another chance for the two to disagree, and each disagreement is a silent
// deletion. Converting with goldmark ends the class rather than the instance.
//
// It also fixes the OTHER direction, which is why stripping indentation was never
// the answer: four spaces under a list item is CONTINUATION, not code, and goldmark
// renders it as real markup. A stripper cannot tell those apart from the line alone;
// the parser does not have to.
//
// Constructs this now inherits, all measured against a live server (see the PR):
// fenced code (``` and ~~~, unterminated, indented up to three spaces, info string,
// longer closing runs), 4-space and tab-indented code blocks, inline code spans of
// any backtick count including multi-line ones, code fences and indentation nested
// inside blockquotes and list items, backslash escapes (\<harbor-task…), and entity
// references (&lt;harbor-task…) — against list continuation, tables, headings and
// ordinary raw-HTML passthrough, which stay markup and stay allowed.
//
// THE ORDER OF THE THREE STEPS IS THE SERVER'S ORDER — convert, sanitize, parse —
// and each was skipped once and cost a task. Every step this function leaves out is
// a step the server takes and we are guessing at.
func noteTaskBlockIDs(content, format string) []string {
	scan := content
	if format == "markdown" {
		var buf bytes.Buffer
		if err := markdownConverter.Convert([]byte(scan), &buf); err != nil {
			// Unreachable in practice — goldmark.Convert errors only on a
			// misconfigured renderer, never on user input, and this renderer is a
			// package-level literal. Fail closed anyway: for markdown this function
			// is only ever asked about NEW content, where "no blocks" means the
			// guard refuses rather than deletes.
			return []string{}
		}
		scan = buf.String()
	}
	// The server reads blocks out of the SANITIZED html, not the raw or converted
	// html, so sanitize before parsing — for BOTH formats, since an html body reaches
	// the same step without passing through goldmark.
	scan = blockSanitizer.Sanitize(scan)

	nodes, err := html.ParseFragment(strings.NewReader(scan), &html.Node{
		Type: html.ElementNode, Data: "body", DataAtom: atom.Body,
	})
	if err != nil {
		// Matching the server, which treats an unparseable fragment as "no task
		// blocks". Here that is also the safe answer: in the NEW content it means
		// nothing is considered carried, so the guard refuses rather than deletes.
		return []string{}
	}

	seen := map[string]bool{}
	out := []string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "harbor-task" {
			for _, a := range n.Attr {
				if a.Key != "id" {
					continue
				}
				id := strings.ToLower(strings.TrimSpace(a.Val))
				if taskBlockIDRe.MatchString(id) && !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
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
// in a stable order, given the note's CURRENT body. The bool reports whether the
// note's task list was read WHOLE — false means there are tasks the caller has
// not been told about, which is a refusal, not a detail (issue #67).
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
func noteTasksAtRisk(c *client.Client, noteID, currentBody string) ([]noteTask, bool) {
	out := []noteTask{}
	seen := map[string]bool{}
	tasks, complete := listNoteTasks(c, noteID)
	for _, t := range tasks {
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
	return out, complete
}

// listNoteTasks reads a note's tasks, in their in-note order, ACROSS EVERY PAGE.
// The bool reports whether the list was read whole.
//
// WHY IT PAGES (issue #67). 500 is the server's hard cap on this endpoint, so one
// request is the most a single read can return — but "the most one request can
// return" is not "all of them", and the guard this feeds is only as good as the
// tasks it can see. A note with more than 500 tasks has tasks past the cap, and a
// task the guard never sees is one it silently deletes: the referenced ones are
// still caught by the body fallback, but a STRANDED task — linked to the note and
// no longer in its body (an app.harbor.my#1094 orphan, or one left behind by
// history revert, app.harbor.my#1236) — exists ONLY in this list. Measured against
// a 501-task note, `--keep-tasks` reported "Kept 500 tasks", exited 0, and
// tombstoned the 501st without a word.
//
// It costs the common case nothing. The first page ends the walk unless the server
// says has_more, so a note with 0 — or 500 — tasks pays exactly the one request it
// paid before; only a note past the cap pays a round trip per further 500.
//
// AN ERROR IS NOT THE SAME AS A SHORT READ, and the two degrade differently:
//
//   - The FIRST page fails → (nothing, complete). Unchanged from #65, deliberately:
//     this feeds a union whose other half needs no network at all, and refusing
//     every content update over one transient hiccup would trade a data guard for
//     an outage. It costs precision on a stranded task, which is the tradeoff #65
//     documented and this does not reopen.
//   - A LATER page fails, or the page cap cuts the walk → (what we read, INCOMPLETE).
//     Here the server has already told us the list is bigger than what we hold, so
//     "we saw it all" would be a claim we know to be false. The caller refuses.
func listNoteTasks(c *client.Client, noteID string) ([]noteTask, bool) {
	out := []noteTask{}
	pages := 0
	complete, err := walkCollection(
		func(params map[string]string) ([]byte, error) {
			data, ferr := c.ListNoteTasks(noteID, params)
			if ferr == nil {
				pages++
			}
			return data, ferr
		},
		func(raw json.RawMessage) bool {
			t := parseJSON(raw)
			if id := strings.ToLower(strings.TrimSpace(str(t, "id"))); id != "" {
				out = append(out, noteTask{ID: id, Title: str(t, "title")})
			}
			return true
		})
	if err != nil {
		return out, pages == 0
	}
	return out, complete
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
//     neither refuses the update with nothing written. When the note's task list
//     could not be read whole, there is nothing to compare against and the update
//     is refused outright — see taskListIncompleteRefusal.
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

	atRisk, complete := noteTasksAtRisk(c, noteID, str(note, "content"))
	if !complete && !allowLoss {
		// The note's task list is longer than what could be read, so the at-risk
		// set is known to be short. Comparing the new content against a set we know
		// is incomplete would report "nothing lost" about tasks we never saw —
		// which is the silent deletion this whole file exists to prevent.
		return nil, errors.New(taskListIncompleteRefusal(len(atRisk)))
	}
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
		// Verify the assembled body rather than assume appending worked. The blocks
		// are concatenated onto content the user wrote, and content that ends inside
		// an unterminated code fence (or any other construct that swallows what
		// follows) turns them into text — the server then sees no reference and
		// deletes the very tasks --keep-tasks was asked to save, while the CLI
		// reports success. Re-scanning catches that whatever the cause, where a list
		// of known-bad constructs would only catch the ones already thought of.
		merged := appendTaskBlocks(newContent, lost)
		if missing := taskBlocksMissingFrom(merged, format, lost); len(missing) > 0 {
			return nil, errors.New(keepFailedRefusal(missing, format))
		}
		body["content"] = merged
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

// taskBlocksMissingFrom returns the tasks whose blocks a finished body does NOT
// reference, judged by the same scanner that decides what the new content carries.
// It is the post-condition on --keep-tasks: appending a block is only useful if the
// server will read it back as markup.
func taskBlocksMissingFrom(content, format string, want []noteTask) []noteTask {
	present := map[string]bool{}
	for _, id := range noteTaskBlockIDs(content, format) {
		present[id] = true
	}
	missing := make([]noteTask, 0, len(want))
	for _, t := range want {
		if !present[t.ID] {
			missing = append(missing, t)
		}
	}
	return missing
}

// keepFailedRefusal builds the message for a --keep-tasks that could not be made
// safe. The user asked for the right thing and it could not be delivered, so the
// message leads with why rather than with what to type: silently writing the body
// anyway would delete these tasks, which is exactly what they asked to prevent.
func keepFailedRefusal(missing []noteTask, format string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--keep-tasks could not save %s, so nothing has been written:\n", countTasks(len(missing)))
	for _, t := range missing {
		if t.Title != "" {
			fmt.Fprintf(&b, "  • %s (%s)\n", t.Title, t.ID)
			continue
		}
		fmt.Fprintf(&b, "  • %s\n", t.ID)
	}
	b.WriteString("\nTheir <harbor-task> blocks were appended to the new body, but the body\n")
	b.WriteString("swallows them — anything after its last unclosed construct is text, not\n")
	if format == "markdown" {
		b.WriteString("markup. The usual cause is content ending inside an unterminated ``` fence.\n")
		b.WriteString("Saving it would delete these tasks anyway. Fix it with one of:\n\n")
		b.WriteString("  close the fence     so the body does not end inside a code block\n")
		b.WriteString("  --format html       if the content is really HTML\n")
	} else {
		b.WriteString("markup. The usual cause is an unclosed comment, or an element whose\n")
		b.WriteString("contents are not parsed as markup. Saving it would delete these tasks\n")
		b.WriteString("anyway. Fix it with one of:\n\n")
		b.WriteString("  close the element   so the body does not end inside it\n")
	}
	b.WriteString("  --allow-task-loss   delete them; that is what you meant\n\n")
	b.WriteString(appendCaveat)
	return b.String()
}

// appendCaveat is the honest version of "just use notes append instead".
//
// Every refusal in this file used to offer `harbor notes append` as the safe
// alternative, full stop. It is not one. Append keeps the existing body, so every
// task the body still REFERENCES survives it — but a task the body has stopped
// referencing is released by ANY save (app.harbor.my#461's delete-on-release), and
// append is a save. For the STRANDED task — the class this guard exists for, the
// one only the task list can see (issue #67) — append is exactly as destructive as
// the update just refused. Offering it unqualified at the moment of a refusal
// walks the user straight into the loss they were spared a line earlier.
const appendCaveat = "'harbor notes append' adds to the body instead of replacing it, which saves\n" +
	"every task the body still references — but NOT one it has stopped referencing:\n" +
	"any save releases that, append included."

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
	b.WriteString(appendCaveat)
	return b.String()
}

// taskListIncompleteRefusal builds the message for an update refused because the
// note's task list could not be read whole (issue #67). It names no tasks on
// purpose: the problem is precisely the ones that could not be named, and a list
// of the ones we did read would read as the full accounting it is not.
//
// --keep-tasks is not offered as a way forward. It can only carry the blocks it
// was told about, so on a short read it would report success while deleting the
// rest — the failure mode this refusal exists to stop.
//
// NOT AN OVERSIGHT: --allow-task-loss is the only way through, so "keep the ones
// you did read and accept losing the rest" cannot be asked for. Combining the two
// flags is rejected earlier as contradictory, and it would be — the half that says
// keep cannot cover a task nobody enumerated, so the pair would promise a safety
// it cannot deliver. Re-running (the list usually reads fine the second time) is
// the way to get everything; --allow-task-loss is the way to stop caring.
//
// The append caveat matters more here than anywhere else in this file: elsewhere
// the user can at least see which tasks are at stake, and here the whole problem
// is the ones that could not be named. None of them can be shown to be safe under
// an append, so it is offered with the same qualification and no more.
func taskListIncompleteRefusal(read int) string {
	var b strings.Builder
	b.WriteString("could not read this note's task list in full, so nothing has been written.\n\n")
	fmt.Fprintf(&b, "The server reports more tasks on this note than the CLI could read (it got %s),\n", countTasks(read))
	b.WriteString("and a task it cannot see is a task it cannot protect: --content/--file/--stdin\n")
	b.WriteString("REPLACE the body, and a task whose <harbor-task> block the new body omits is\n")
	b.WriteString("deleted, not detached. Re-run to try again, or:\n\n")
	b.WriteString("  --allow-task-loss    proceed anyway, deleting whatever the new body omits\n\n")
	b.WriteString(appendCaveat)
	b.WriteString("\nThe tasks that could not be read cannot be sorted into those two groups, so\n")
	b.WriteString("append is not a safe way around this particular refusal either.\n")
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

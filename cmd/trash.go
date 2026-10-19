// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// trashCmd is the parent for the recycle bin — list, restore, expunge, and
// empty the trash. Deleting a note (`harbor notes delete`) moves it here by
// default; these commands operate on what is already trashed.
var trashCmd = &cobra.Command{
	Use:     "trash",
	Aliases: []string{"recycle", "bin"},
	Short:   "Manage the recycle bin (list, restore, expunge, empty)",
	GroupID: groupContent,
	Long: `The trash is a recoverable recycle bin for notes. Deleting a note moves it
here by default (run 'harbor notes delete <id>'); from here you can restore a
note, expunge a single note permanently, or empty the whole bin.`,
}

// trashListCmd lists the notes currently in the trash.
var trashListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notes currently in the trash",
	Long:  "List the notes sitting in the recycle bin (most-recently-trashed first), paged. Restore one with 'harbor trash restore <id>'.",
	Example: `  harbor trash list
  harbor trash list --order title --limit 50
  harbor trash list --json | jq '.data[] | {id, title, trashed_at}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListTrash(pagingParams(cmd))
		if err != nil {
			return mapTrashError(err)
		}
		data = decryptResult(c, creds, data)
		printResult(data, displayTrash)
		return nil
	},
}

// trashRestoreCmd restores a note from the trash back to the live set.
var trashRestoreCmd = &cobra.Command{
	Use:     "restore <note-id>",
	Short:   "Restore a note from the trash",
	Args:    cobra.ExactArgs(1),
	Long:    "Return a note from the trash to the live set. If its original notebook was deleted while it sat in the trash, it lands in your default notebook.",
	Example: "  harbor trash restore 9c2e7b10-...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.RestoreNote(args[0])
		if err != nil {
			return mapTrashError(err)
		}
		printResult(data, displayRestoredNote)
		return nil
	},
}

// trashExpungeCmd permanently deletes a single note.
var trashExpungeCmd = &cobra.Command{
	Use:   "expunge <note-id>",
	Short: "Permanently delete a single note",
	Args:  cobra.ExactArgs(1),
	Long: `Permanently delete a note (it cannot be restored). Works whether or not the
note is currently in the trash. Attachment bytes left with no remaining
reference are reclaimed.

This cannot be undone, so you will be asked to confirm by typing "yes" unless
you pass --yes. In --json or non-interactive use, --yes is required.`,
	Example: `  harbor trash expunge 9c2e7b10-...
  harbor trash expunge 9c2e7b10-... --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if err := trashConfirmExpunge(boolFlag(cmd, "yes")); err != nil {
			return err
		}
		if _, err := c.ExpungeNote(args[0]); err != nil {
			return mapTrashError(err)
		}
		fmt.Println("Note permanently deleted.")
		return nil
	},
}

// trashEmptyCmd expunges every note in the trash. It is destructive, so it
// requires confirmation: an interactive "yes" prompt, or --yes to skip it
// (--yes is mandatory in --json or non-interactive use).
var trashEmptyCmd = &cobra.Command{
	Use:   "empty",
	Short: "Permanently delete every note in the trash",
	Args:  cobra.NoArgs,
	Long: `Empty the recycle bin: permanently delete EVERY note currently in it. This
cannot be undone. You will be asked to confirm by typing "yes" unless you pass
--yes. In --json or non-interactive use, --yes is required.

A big trash (more than 200 notes) is emptied by a job on the server. The
command waits for that job to finish, shows progress on a terminal, and only
then says the trash is empty. Ctrl-C stops the waiting, not the emptying.`,
	Example: `  harbor trash empty
  harbor trash empty --yes
  harbor trash empty --yes --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if err := trashConfirmEmpty(boolFlag(cmd, "yes")); err != nil {
			return err
		}
		data, err := c.EmptyTrash()
		if err != nil {
			return mapTrashError(err)
		}
		data, err = trashFinishEmpty(c, data)
		// On a failed or unfinished job only --json prints: a script still gets the
		// job as far as it got, while a person reads the error alone.
		if err != nil && !jsonOutput {
			return err
		}
		printResult(data, displayEmptyTrash)
		return err
	},
}

// trashEmptyConfirmation is what `harbor trash empty` asks before it destroys
// anything. It is a value so the wording — and the fact that only the exact
// word "yes" proceeds — can be asserted without a terminal.
var trashEmptyConfirmation = registerConfirmation("harbor trash empty", confirmation{
	Warning:     "This permanently deletes every note in the trash. This cannot be undone.",
	Prompt:      `Type "yes" to confirm: `,
	Affirmative: "yes",
	Unattended:  "refusing to empty the trash without confirmation — pass --yes",
	Aborted:     "aborted — the trash was not emptied",
})

// trashExpungeConfirmation gates the single-note expunge. It destroys exactly
// as permanently as emptying the whole bin does — the only difference is how
// many notes go — so it asks the same question rather than relying on the id
// having been typed deliberately.
var trashExpungeConfirmation = registerConfirmation("harbor trash expunge", confirmation{
	Warning:     "This permanently deletes the note. It cannot be restored.",
	Prompt:      `Type "yes" to confirm: `,
	Affirmative: "yes",
	Unattended:  "refusing to permanently delete a note without confirmation — pass --yes",
	Aborted:     "aborted — the note was not deleted",
})

// trashConfirmEmpty gates the destructive empty operation. It resolves the
// ambient state — is --json set, is stdin a terminal, how do we read a line —
// and hands the decision to confirmDestructive, which is where every branch
// (including the typed-wrong-answer one) is pinned by tests.
func trashConfirmEmpty(yes bool) error {
	return confirmDestructive(trashEmptyConfirmation, jsonOutput, stdinIsInteractive(), yes, askLine)
}

// trashConfirmExpunge gates the single-note permanent delete the same way.
func trashConfirmExpunge(yes bool) error {
	return confirmDestructive(trashExpungeConfirmation, jsonOutput, stdinIsInteractive(), yes, askLine)
}

// mapTrashError gives friendly messages for the trash-specific codes.
func mapTrashError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "not_in_trash":
			return errors.New("that note is not in the trash")
		case "validation_failed":
			return errors.New("invalid sort field — use one of: trashed_at, updated_at, created_at, title (prefix - for descending)")
		}
	}
	return err
}

// ===========================================================================
// Waiting for a big Empty Trash
// ===========================================================================

// trashEmptyPollInterval is how long the wait pauses between job checks. A
// variable so tests can run the loop without spending real seconds in it.
var trashEmptyPollInterval = time.Second

// trashEmptyRetryWindow is how long checks may keep failing before the wait gives
// up. A failed check does not mean a failed job — the job runs on the server
// either way — so a short outage or a deploy is waited out. It is a span of
// time, not a count, because one check can take the client's full timeout to
// fail. A variable so tests can shorten it.
var trashEmptyRetryWindow = 2 * time.Minute

// trashEmptyFailedFallback is shown when the server marks the job failed but
// sends no sentence of its own.
const trashEmptyFailedFallback = "Some notes are still in the trash. Run 'harbor trash empty' again."

// errTrashStoppedWaiting is the Ctrl-C answer. It says what kept going, because
// the person pressing Ctrl-C may think they stopped the delete.
var errTrashStoppedWaiting = errors.New("Stopped waiting. The trash is still being emptied on the server.")

// stderrIsInteractive reports whether stderr is a terminal, where a progress
// line can be redrawn in place. A variable so a test can take that branch.
var stderrIsInteractive = func() bool { return term.IsTerminal(int(os.Stderr.Fd())) }

// trashFinishEmpty waits for the job a big Empty Trash answers with and returns
// the answer with that job as it last stood. A small trash has no job and comes
// back unchanged. The error is non-nil when the job failed or the wait ended
// before the job did.
func trashFinishEmpty(c *client.Client, data []byte) ([]byte, error) {
	if trashEmptyJobOf(data) == nil {
		return data, nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	data, err := trashWaitForEmpty(ctx, c.GetTrashEmptyJob, data, newTrashEmptyProgress())
	if err != nil {
		return data, err
	}
	return data, trashEmptyJobFailure(data)
}

// trashWaitForEmpty polls the job in an Empty Trash answer until it completes
// or fails, keeping the answer's "expunged" and swapping in each new copy of the
// job. It stops early on Ctrl-C (ctx), on a check that cannot succeed by
// retrying, or when checks have kept failing for trashEmptyRetryWindow. The
// progress line is erased on every way out, so the next line printed starts
// clean.
func trashWaitForEmpty(ctx context.Context, fetch func(id string) ([]byte, error), data []byte, progress *trashEmptyProgress) ([]byte, error) {
	defer progress.clear()
	job := trashEmptyJobOf(data)
	id := str(job, "id")
	var failingSince time.Time
	for !trashEmptyJobDone(str(job, "status")) {
		if id == "" {
			return data, errors.New("the server is emptying the trash in the background but sent no job to follow — run 'harbor trash list' to see what is left")
		}
		progress.show(job, !failingSince.IsZero())
		started := time.Now()
		raw, err := trashPollOnce(ctx, func() ([]byte, error) { return fetch(id) })
		if ctx.Err() != nil {
			return data, errTrashStoppedWaiting
		}
		if err == nil && parseJSON(client.UnwrapData(raw)) == nil {
			err = errors.New("the server sent a job that could not be read")
		}
		if err != nil {
			if failingSince.IsZero() {
				failingSince = started
			}
			if !trashPollRetryable(err) || time.Since(failingSince) >= trashEmptyRetryWindow {
				return data, trashPollFailed(err)
			}
			continue
		}
		failingSince = time.Time{}
		data = trashEmptyWithJob(data, client.UnwrapData(raw))
		job = trashEmptyJobOf(data)
	}
	return data, nil
}

// trashPollOnce waits one poll interval, then fetches the job. Both halves give
// way to Ctrl-C at once, so a request that hangs cannot hold the terminal for
// its full timeout. The fetch's result channel is buffered, so an abandoned
// fetch can still finish and exit.
func trashPollOnce(ctx context.Context, fetch func() ([]byte, error)) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(trashEmptyPollInterval):
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		d, err := fetch()
		done <- result{d, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		return r.data, r.err
	}
}

// trashEmptyJobOf returns the job in an Empty Trash answer, or nil when there is
// none — the small-trash answer, whose notes are already gone.
func trashEmptyJobOf(data []byte) map[string]any {
	job, _ := parseJSON(data)["job"].(map[string]any)
	return job
}

// trashEmptyJobDone reports whether a job status is final. Any other status,
// including one this binary does not know, means keep waiting.
func trashEmptyJobDone(status string) bool {
	return status == "completed" || status == "failed"
}

// trashEmptyWithJob returns the Empty Trash answer with its job replaced by a
// newer copy. Every other field, "expunged" included, is kept, so --json prints
// the same shape the server first sent.
func trashEmptyWithJob(data, job []byte) []byte {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return data
	}
	doc["job"] = json.RawMessage(job)
	out, err := json.Marshal(doc)
	if err != nil {
		return data
	}
	return out
}

// trashEmptyJobFailure turns a failed job into the command's error, using the
// server's sentence. Any other status is not a failure.
func trashEmptyJobFailure(data []byte) error {
	job := trashEmptyJobOf(data)
	if str(job, "status") != "failed" {
		return nil
	}
	msg := str(job, "error")
	if msg == "" {
		msg = trashEmptyFailedFallback
	}
	return &client.APIError{Code: "trash_empty_failed", Message: msg}
}

// trashPollRetryable reports whether a failed check is worth repeating: the
// network dropped, the server was busy, or it hit a server error. Anything else
// (signed out, no such job) will fail the same way again.
func trashPollRetryable(err error) bool {
	if isNetworkError(err) {
		return true
	}
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && (apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests)
}

// trashWaitError ends the wait without an answer from the job. It carries the
// exit code of the check that failed but not the error itself, so the error
// printer shows this message rather than only the bare API error.
type trashWaitError struct {
	msg  string
	code int
}

// Error returns the message shown to the user.
func (e *trashWaitError) Error() string { return e.msg }

// ExitCode keeps the failed check's exit code, so a network failure still exits
// 3 and a script can retry. A retry is safe: it joins the running job.
func (e *trashWaitError) ExitCode() int { return e.code }

// trashPollFailed builds the error for a wait that could not check on the job.
// It says the trash may still be emptying, because the job does not depend on
// this process.
func trashPollFailed(err error) error {
	return &trashWaitError{
		msg: fmt.Sprintf("Stopped waiting: could not check on the trash (%v). It may still be emptying on the server — "+
			"run 'harbor trash list' to see what is left.", err),
		code: exitCodeFor(err),
	}
}

// trashEmptyProgress draws "Emptying the trash… X of Y notes" on stderr,
// redrawn in place. It is off under --json and off a terminal, so a pipe or a
// log gets only the result.
type trashEmptyProgress struct {
	w    io.Writer
	on   bool
	last string
}

// newTrashEmptyProgress returns a progress line for this run's stderr.
func newTrashEmptyProgress() *trashEmptyProgress {
	return &trashEmptyProgress{w: os.Stderr, on: !jsonOutput && stderrIsInteractive()}
}

// show redraws the line for job, noting when the last check failed and is
// being retried. It writes nothing when the text has not changed, so an idle
// poll does not flicker.
func (p *trashEmptyProgress) show(job map[string]any, retrying bool) {
	if !p.on {
		return
	}
	line := trashEmptyProgressLine(job, retrying)
	if line == p.last {
		return
	}
	p.last = line
	fmt.Fprint(p.w, "\r\033[K"+dim(line))
}

// clear erases the progress line, if one was drawn.
func (p *trashEmptyProgress) clear() {
	if p.last == "" {
		return
	}
	fmt.Fprint(p.w, "\r\033[K")
	p.last = ""
}

// trashEmptyProgressLine is the progress text for one copy of the job. The
// retry note keeps a wait on a dead connection from looking frozen.
func trashEmptyProgressLine(job map[string]any, retrying bool) string {
	line := "Emptying the trash…"
	if total := int64(num(job, "total_notes")); total > 0 {
		done := int64(num(job, "processed_notes"))
		line = fmt.Sprintf("Emptying the trash… %s of %s %s",
			commaNum(done), commaNum(total), pluralize(int(total), "note", "notes"))
	}
	if retrying {
		line += " (retrying…)"
	}
	return line
}

// ===========================================================================
// Display
// ===========================================================================

// displayTrash renders the trash collection as a table, including when each
// note was trashed (relative time, since that drives the auto-purge).
func displayTrash(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "TITLE", "NOTEBOOK", "🔒", "TRASHED", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		n := parseJSON(raw)
		rows = append(rows, []string{
			str(n, "id"),
			truncate(str(n, "title"), 40),
			shortID(str(n, "notebook_id"), 8),
			lockMark(boolean(n, "is_encrypted")),
			relTime(num(n, "trashed_at")),
			dim(str(n, "usn")),
			epochMS(num(n, "updated_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayRestoredNote confirms a restore and renders the restored note (a bare
// note object) as a detail view.
func displayRestoredNote(data []byte) {
	n := parseJSON(client.UnwrapData(data))
	if n == nil {
		fmt.Println(string(data))
		return
	}
	fmt.Println(colorizeStatus("restored") + " " + bold(str(n, "title")))
	printKV([][2]string{
		{"ID", bold(str(n, "id"))},
		{"Title", str(n, "title")},
		{"Notebook", str(n, "notebook_id")},
		{"In trash", boolMark(boolean(n, "in_trash"))},
		{"Encrypted", boolMark(boolean(n, "is_encrypted"))},
		{"USN", str(n, "usn")},
		{"Updated", epochMS(num(n, "updated_at"))},
	})
}

// displayEmptyTrash prints how many notes were expunged when the trash was
// emptied. It reads only "expunged", so it serves both the small-trash answer
// and a big trash's answer once its job has completed.
func displayEmptyTrash(data []byte) {
	root := parseJSON(data)
	n := int64(num(root, "expunged"))
	if n == 1 {
		fmt.Println("Emptied the trash — 1 note permanently deleted.")
		return
	}
	fmt.Printf("Emptied the trash — %s notes permanently deleted.\n", commaNum(n))
}

func init() {
	addPagingFlags(trashListCmd)

	trashEmptyCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")
	trashExpungeCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")

	trashCmd.AddCommand(trashListCmd, trashRestoreCmd, trashExpungeCmd, trashEmptyCmd)
	rootCmd.AddCommand(trashCmd)
}

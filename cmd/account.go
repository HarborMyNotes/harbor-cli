// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// accountDeleteConfirmPhrase is the exact phrase a user must type (verbatim — not
// trimmed, not case-folded) to confirm an account deletion. It mirrors the
// server's ACCOUNT_DELETE_CONFIRM_PHRASE default.
const accountDeleteConfirmPhrase = "DELETE MY ACCOUNT"

// accountCmd is the parent for destructive, whole-account operations: the GDPR
// data export and the grace-period account deletion (issue #27).
var accountCmd = &cobra.Command{
	Use:     "account",
	Short:   "Export or delete your entire account",
	GroupID: groupAccount,
	Long: `Whole-account operations.

  export        start an export job (ENEX or HTML, whole account or one notebook)
  exports       show what this account's export slots hold right now
  export-status poll an export job and download the ZIP when it completes
  export-delete delete an export's archive, freeing that format's slot
  delete        schedule account deletion after a grace period (destructive)
  cancel-delete cancel a pending deletion within the grace window

You hold at most one live export PER FORMAT — one ENEX and one HTML — and
scoping an export to a single notebook does not create a third slot. Exports run
one at a time server-wide, so a new one may sit "queued" (waiting its turn)
before it starts. A finished archive is kept for 72 hours, then deleted.

Deletion is a soft-delete: a confirmed request records a purge date and revokes
your other sessions, but destroys nothing until the grace window elapses — you
can cancel until then.`,
}

// accountExportCmd starts an export job for one format, optionally scoped to a
// single notebook. A queued/running export of the same format is handed back
// instead of starting a second one; a completed, unexpired one is refused.
var accountExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Start a data export (ENEX or HTML, whole account or one notebook)",
	Args:  cobra.NoArgs,
	Long: `Start an asynchronous export job.

  --format enex   (the default) a ZIP holding one ENEX document per notebook,
                  every attachment's original bytes, and a manifest.json
  --format html   a ZIP holding a self-contained, browsable offline website —
                  unzip it and open index.html, no server and no internet

With --notebook the export covers a single notebook instead of the whole
account, for either format. It is still an ordinary export job, so it gets the
same progress, ready email, 72-hour retention, delete action and export slot.

You hold one export per format. Starting a second of the SAME format while one
is ready is refused — delete it or wait for it to expire; the other format is
unaffected. Because exports run one at a time server-wide, a new job may report
"queued" (waiting its turn) for a while before it starts building.

Nothing has to stay running here. The archive is built on the server and a link
to it is emailed to you when it is done, so you can close this terminal — and
'harbor account exports' finds the job again afterwards.

Poll it with 'harbor account export-status <id>', or pass --wait to poll here
until it finishes. --download implies --wait and saves the ZIP straight to disk.`,
	Example: `  harbor account export
  harbor account export --format html
  harbor account export --format enex --notebook 5b1f2c9a
  harbor account export --wait --download harbor-export.zip
  harbor account export --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}

		format, err := accountExportFormat(cmd)
		if err != nil {
			return err
		}
		notebook := stringFlag(cmd, "notebook")

		data, err := c.StartAccountExport(format, notebook)
		if err != nil {
			return mapAccountExportStartError(err, notebook)
		}

		// The 202 echoes the scope of the job you were GIVEN, which is not
		// necessarily the one you asked for: an already in-flight export of this
		// format is handed back as-is. Say so rather than let the user believe
		// their --notebook took effect.
		accountWarnScopeMismatch(data, notebook)

		out := stringFlag(cmd, "download")
		if !boolFlag(cmd, "wait") && out == "" {
			printResult(data, displayExportJob)
			return nil
		}

		job := parseJSON(client.UnwrapData(data))
		id := str(job, "export_job_id")
		if id == "" {
			id = str(job, "id")
		}
		return accountWaitAndMaybeDownload(cmd, c, id, out)
	},
}

// accountExportsCmd shows what this account's export slots hold right now: the
// latest export per format, straight from the server.
var accountExportsCmd = &cobra.Command{
	Use:   "exports",
	Short: "Show this account's current exports (one per format)",
	Args:  cobra.NoArgs,
	Long: `List the most recent export per format — at most two rows, one ENEX and one
HTML. A format you have never exported is simply absent.

Export state lives on the server, not in this shell: this is how you find the
export you started on another machine (or before you closed the terminal and
lost the job id). A row in a state that does not hold the slot — failed, expired
or deleted — is still shown, because it explains where a download went.`,
	Example: `  harbor account exports
  harbor account exports --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListAccountExports()
		if err != nil {
			return mapAccountError(err)
		}
		printResult(data, displayExportList)
		return nil
	},
}

// accountExportStatusCmd polls an export job and, with --download, saves the ZIP
// when the job is completed (by following the presigned URL).
var accountExportStatusCmd = &cobra.Command{
	Use:   "export-status <id>",
	Short: "Poll an export job and optionally download the ZIP",
	Args:  cobra.ExactArgs(1),
	Long: `Poll an export job by id.

Progress is reported in NOTES ("4,120 / 36,500 notes"). A queued job is waiting
its turn behind the server-wide one-at-a-time lock and says where it is in the
line. A completed job reports when it finished and when its archive expires,
plus a short-lived presigned download URL.

Pass --download <path> to fetch and save the ZIP (use - for stdout); if <path>
is a directory the server's own filename is used. --wait polls until the job
reaches a final state first, so --wait --download is a one-shot "export and
save". Lost the id? 'harbor account exports' reads it back off the server.`,
	Example: `  harbor account export-status 0f9c2b1e-...
  harbor account export-status 0f9c2b1e-... --download account-export.zip
  harbor account export-status 0f9c2b1e-... --wait --download ~/Downloads`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}

		out := stringFlag(cmd, "download")
		if boolFlag(cmd, "wait") || out != "" {
			return accountWaitAndMaybeDownload(cmd, c, args[0], out)
		}

		data, err := c.GetAccountExport(args[0])
		if err != nil {
			return mapAccountError(err)
		}
		printResult(data, displayExportJob)
		return nil
	},
}

// accountExportDeleteCmd deletes an export's archive, freeing that format's slot
// so a new export of the same format can start immediately.
var accountExportDeleteCmd = &cobra.Command{
	Use:   "export-delete <id>",
	Short: "Delete an export's archive and free that format's slot",
	Args:  cobra.ExactArgs(1),
	Long: `Delete a finished export. The archive is removed from storage, any emailed
link stops working, and that format's slot is freed so you can export again
straight away. The other format is untouched, and nothing in your account is
deleted — an export is a disposable copy.

This cannot be undone (the archive is gone; you would have to export again), so
you will be asked to confirm by typing "yes" unless you pass --yes. In --json or
non-interactive use, --yes is required.

Deleting an export whose archive has already gone is not an error — it reports
the state it is already in. An export that is still being BUILT is refused
rather than cancelled: wait for it to finish, then delete it.`,
	Example: `  harbor account export-delete 0f9c2b1e-...
  harbor account export-delete 0f9c2b1e-... --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if err := accountConfirmExportDelete(boolFlag(cmd, "yes")); err != nil {
			return err
		}
		data, err := c.DeleteAccountExport(args[0])
		if err != nil {
			return mapAccountError(err)
		}
		printResult(data, displayExportDeleted)
		return nil
	},
}

// accountDeleteCmd schedules a grace-period account deletion. It is destructive
// and requires both the current password (re-auth) and the exact confirmation
// phrase typed verbatim.
var accountDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Schedule deletion of your entire account (destructive)",
	Long: fmt.Sprintf(`Schedule deletion of your entire account.

This is a SOFT delete with a grace period: it records a purge date and revokes
your other sessions, but destroys nothing until the window elapses. You can
cancel until then with 'harbor account cancel-delete'.

You must confirm by typing the phrase %q exactly and entering your current
password. In --json or non-interactive mode the command refuses unless you pass
both --confirm %q and --yes (your password is still read from stdin).`,
		accountDeleteConfirmPhrase, accountDeleteConfirmPhrase),
	Example: `  harbor account delete
  printf 'my-password\n' | harbor account delete --confirm "DELETE MY ACCOUNT" --yes --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}

		// Resolve the confirmation phrase: typed interactively, or pre-supplied
		// via --confirm under the non-interactive guard.
		confirm, err := accountResolveConfirm(cmd)
		if err != nil {
			return err
		}

		// Re-auth proof. promptPassword reads piped stdin non-interactively, so
		// scripts/agents can supply the password without a TTY.
		pw, err := promptPassword("Current password: ")
		if err != nil {
			return err
		}

		data, err := c.RequestAccountDeletion(pw, confirm)
		if err != nil {
			return mapAccountError(err)
		}
		printResult(data, displayDeletionScheduled)
		return nil
	},
}

// accountCancelDeleteCmd cancels a pending deletion within the grace window. It
// re-authenticates via the current password.
var accountCancelDeleteCmd = &cobra.Command{
	Use:     "cancel-delete",
	Short:   "Cancel a pending account deletion (within the grace window)",
	Long:    "Cancel a pending account deletion and reactivate the account. Only works within the grace window. Prompts for your current password.",
	Example: "  harbor account cancel-delete",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		pw, err := promptPassword("Current password: ")
		if err != nil {
			return err
		}
		data, err := c.CancelAccountDeletion(pw)
		if err != nil {
			return mapAccountError(err)
		}
		printResult(data, displayMessage("Account deletion cancelled. Your account is active again."))
		return nil
	},
}

// ===========================================================================
// Export lifecycle helpers
// ===========================================================================

// accountExportKeepsBuilding is the reassurance every Harbor client shows while
// an export is queued or running, rendered verbatim (epic app.harbor.my#1216).
// The one approved deviation for this platform is "close this" in place of
// "close Harbor", which is meaningless in a terminal.
//
// The first half is unconditionally true, which is why it can be said plainly:
// the export is an ordinary background job — POST /account/export commits an
// account_jobs row and enqueues the work, and a worker pool claims it from the
// database — so nothing about the build is tied to the HTTP request or to this
// process, and exiting the CLI does not touch it.
//
// The SECOND half is a best effort, not a guarantee, which is why it is never the
// only route offered. The server does send export_ready on success and
// export_failed on a build that fails for good (internal/account/export.go,
// htmlexport.go), but the send is fire-and-forget: a delivery error is swallowed
// and logged, an instance with no mailer wired returns silently, and an export
// deleted or expired mid-build sends nothing at all. So the line that follows
// this one leads with the command, which needs no mail to arrive — see
// accountExportNextStep.
//
// It deliberately promises no TIME. Exports run one at a time server-wide, so the
// wait is however long every export ahead of yours takes; a single very large
// account has held that slot for the better part of a day (app.harbor.my#1242),
// and an estimate would be most wrong for exactly the people who most need it to
// be right.
const accountExportKeepsBuilding = "You can close this — the export keeps building on our servers, and we'll email you a link when it's ready."

// accountExportNextStep is the route back to a finished export, and it leads with
// the command rather than the email ON PURPOSE.
//
// The email is the nicer path when it lands, but it is the half of the promise
// this CLI cannot vouch for (see accountExportKeepsBuilding). 'harbor account
// exports' depends on none of it: export state lives on the server, so the
// command reads the job — and its id — straight back off the API. Someone whose
// plan is "wait for the email" has no move at all when no email comes; someone
// whose plan is the command is never stuck, and the email just saves them a step.
//
// The parenthetical is there because the emailed link is NOT a download either.
// It points back at Harbor, which re-authenticates the reader before minting a
// fresh short-lived URL (exportDownloadPageURL), so a forwarded email cannot
// fetch someone's archive — and anyone told only "we'll email you a link" would
// reasonably expect to curl it.
const accountExportNextStep = "Pick it up any time with 'harbor account exports' — that works whether or not the email arrives (the emailed link opens Harbor in a browser)."

// accountExportQueueSlipNote explains a queue position that got WORSE.
//
// The line is server-wide and ordered by priority before arrival time: a queued
// ENEX export outranks a queued HTML one however late it was started. So a
// position genuinely walks backwards while you watch it, and a number that goes
// 3 → 4 with no explanation reads as a bug in this CLI rather than as somebody
// else's export jumping the line. Printed once per wait — after the first time,
// the point has been made.
const accountExportQueueSlipNote = "Another export moved ahead of yours — the line is shared with every other account, so a position can go up as well as down."

// accountExportFormats are the archive kinds the server accepts. The CLI checks
// the value itself so a typo costs a round-trip and a validation error rather
// than an export of the wrong thing.
var accountExportFormats = []string{"enex", "html"}

// accountExportTerminalStates are the statuses an export never moves out of.
// completed/failed are the job's own outcomes; expired/deleted are terminal
// states of the RESULT — the archive is gone and that format's slot is free.
var accountExportTerminalStates = []string{"completed", "failed", "expired", "deleted"}

// accountExportFormat reads and validates --format. An unset flag returns the
// empty string, which is then omitted from the request body so the server's own
// default (enex) applies rather than one this CLI has hardcoded and could drift
// from.
func accountExportFormat(cmd *cobra.Command) (string, error) {
	format := strings.ToLower(strings.TrimSpace(stringFlag(cmd, "format")))
	if format == "" {
		return "", nil
	}
	for _, valid := range accountExportFormats {
		if format == valid {
			return format, nil
		}
	}
	return "", fmt.Errorf("--format must be one of %s", strings.Join(accountExportFormats, "|"))
}

// accountExportIsTerminal reports whether an export status is final, i.e. polling
// it again will never show anything new.
func accountExportIsTerminal(status string) bool {
	for _, s := range accountExportTerminalStates {
		if status == s {
			return true
		}
	}
	return false
}

// accountWarnScopeMismatch prints a notice when the job the server handed back
// does not cover the notebook that was asked for.
//
// Starting an export while one of that format is already queued or running
// returns the EXISTING job, so the 202 describes a scope the caller may not have
// requested. Silently rendering it would tell someone their notebook export
// started when what they actually have is the whole-account export from ten
// minutes ago.
func accountWarnScopeMismatch(data []byte, wantNotebook string) {
	if wantNotebook == "" || jsonOutput {
		return
	}
	job := parseJSON(client.UnwrapData(data))
	got := str(job, "notebook_id")
	if got == wantNotebook {
		return
	}
	fmt.Fprintf(os.Stderr, "%s an export of this format was already running, so that job was returned instead — it covers %s, not the notebook you asked for.\n",
		colorize("note:", text.FgYellow), accountExportScopeLabel(job))
}

// accountWaitAndMaybeDownload polls an export to a final state and then, when
// --download was given, saves the archive. It is the shared body of
// 'export --wait' and 'export-status --wait/--download' so the two cannot drift.
//
// The final status is rendered on stdout exactly as a plain poll would render it
// (so --json still emits the job), and a download report follows it — EXCEPT
// when the archive itself is stdout. Both entry points reach here only after
// --wait or --download was given, so the poll always waits.
func accountWaitAndMaybeDownload(cmd *cobra.Command, c *client.Client, id, out string) error {
	data, err := accountPollExport(c, id, durationFlag(cmd, "poll-interval"), durationFlag(cmd, "timeout"), true)
	if err != nil {
		return err
	}
	// With --download - the archive IS stdout, so nothing human may go there: a
	// status card in front of the ZIP bytes produces a file that is not a ZIP.
	// Progress already went to stderr, and a failure still comes back as an error.
	if out != "-" {
		display := displayExportJob
		if out != "" {
			display = displayExportJobDownloading
		}
		printResult(data, display)
	}
	if out == "" {
		// The poll ran to a TERMINAL state, which is not the same as a good
		// one: failed/expired/deleted all end the wait. Without this the card
		// says "Status failed" and the command exits 0, so a script that waited
		// an hour for an archive carries on as though it had one — the same
		// defect as a push whose changes were refused. --download reaches the
		// identical check inside accountDownloadExport; this is the branch that
		// only asked to wait.
		if status := str(parseJSON(client.UnwrapData(data)), "status"); status != "completed" {
			return errors.New(accountExportNotReadyReason(status, id))
		}
		return nil
	}
	return accountDownloadExport(c, id, out, data)
}

// accountPollExport polls an export until it reaches a final state, returning the
// last status body. When wait is false it fetches once and returns immediately.
//
// Progress goes to STDERR, not stdout: a job that takes an hour should show
// movement, but 'harbor account export --wait --json | jq' must still receive
// nothing but the final JSON. Each distinct progress line is printed once, so a
// slow export scrolls at the speed it actually changes rather than the speed it
// is polled. A zero timeout means "no limit" — the export is done when it is
// done, and Ctrl-C is always available.
//
// The first poll that finds work still to do says, once, that waiting is
// OPTIONAL. This is the command someone leaves running, and it is the one place
// in the CLI where a person can lose a whole day: exports run one at a time
// server-wide, so a single very large account ahead of you holds the slot for as
// long as it takes to build (ten hours, in app.harbor.my#1242). Someone who does
// not know the server finishes on its own will simply sit there for it.
func accountPollExport(c *client.Client, id string, interval, timeout time.Duration, wait bool) ([]byte, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	last := ""
	lastPos := 0
	slipped := false
	announced := false
	for {
		data, err := c.GetAccountExport(id)
		if err != nil {
			return nil, mapAccountError(err)
		}
		job := parseJSON(client.UnwrapData(data))
		if !wait || accountExportIsTerminal(str(job, "status")) {
			return data, nil
		}
		// Only now is it certain this run will actually wait on something — the
		// job could have been finished before the first poll, and telling someone
		// they may leave a wait that is already over is just noise.
		if !announced {
			for _, line := range accountExportWaitPreamble() {
				if !jsonOutput {
					fmt.Fprintln(os.Stderr, dim(line))
				}
			}
			announced = true
		}
		if line := accountExportProgressLine(job); line != "" && line != last {
			if !jsonOutput {
				fmt.Fprintln(os.Stderr, dim(line))
			}
			last = line
		}
		// A queue position that got WORSE, explained the first time it happens —
		// see accountExportQueueSlipNote. The check is against the last position
		// SEEN, so it fires on the move itself rather than on every later poll of
		// the same number.
		if pos := int(num(job, "queue_position")); pos > 0 {
			if lastPos > 0 && pos > lastPos && !slipped {
				if !jsonOutput {
					fmt.Fprintln(os.Stderr, dim(accountExportQueueSlipNote))
				}
				slipped = true
			}
			lastPos = pos
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, fmt.Errorf("gave up waiting after %s (the export is still %s; poll it with: harbor account export-status %s)",
				timeout, str(job, "status"), id)
		}
		time.Sleep(interval)
	}
}

// accountDownloadExport streams a completed export's archive to out ("-" for
// stdout, an existing directory to save under the server's own filename).
//
// A 403/404 from the presigned URL is NOT reported as a broken download: those
// links are clamped to the archive's remaining retention and answer in raw S3
// XML, so the honest reading is "the object is gone" — the export may have been
// deleted or expired from another device since the URL was minted. Re-read the
// status endpoint and report whatever it now says.
func accountDownloadExport(c *client.Client, id, out string, data []byte) error {
	job := parseJSON(client.UnwrapData(data))
	status := str(job, "status")
	if status != "completed" {
		return errors.New(accountExportNotReadyReason(status, id))
	}
	url := str(job, "download_url")
	if url == "" {
		return errors.New("the archive is no longer available — start a new export")
	}

	path := accountExportOutputPath(out, str(job, "filename"))
	resp, err := c.FetchURL(url)
	if err != nil {
		var derr *client.DownloadError
		if errors.As(err, &derr) && derr.Gone() {
			return accountExportGoneError(c, id)
		}
		return err
	}
	defer resp.Body.Close()

	n, err := writeOutput(path, resp.Body)
	if err != nil {
		return err
	}
	if path != "-" {
		fmt.Printf("Wrote %s to %s\n", bytesHuman(float64(n)), path)
		// The archive still holds that format's slot until it is deleted or
		// expires, so freeing it is the one useful next step after a saved copy
		// exists. It goes here rather than in the card above so it reads as a
		// follow-up to the write, not as an instruction to download again.
		fmt.Println(dim("Delete it: harbor account export-delete " + id))
	}
	return nil
}

// accountExportGoneError re-reads an export whose download URL 403/404'd and
// describes the state it is actually in, so the user is told "you deleted this
// export" rather than "HTTP 403".
func accountExportGoneError(c *client.Client, id string) error {
	data, err := c.GetAccountExport(id)
	if err != nil {
		return errors.New("the export archive is no longer available — start a new export")
	}
	status := str(parseJSON(client.UnwrapData(data)), "status")
	return errors.New(accountExportNotReadyReason(status, id))
}

// accountExportNotReadyReason explains, per state, why an archive cannot be
// downloaded and what to do instead.
func accountExportNotReadyReason(status, id string) string {
	switch status {
	case "queued":
		return "the export has not started yet — it is waiting its turn (add --wait to poll until it is ready)"
	case "running":
		return "the export is still being built (add --wait to poll until it is ready)"
	case "failed":
		return fmt.Sprintf("the export failed, so there is nothing to download — start a new one (see 'harbor account export-status %s' for the reason)", id)
	case "expired":
		return "the archive was deleted at the end of its 72-hour retention window — start a new export"
	case "deleted":
		return "this export was deleted — start a new export"
	case "completed":
		// Only reachable from the 403/404 re-check: the link was refused but the
		// export still reports itself ready, so the archive is probably fine and
		// the LINK is the problem. Saying "not ready (status: completed)" would be
		// a contradiction with no way out; a fresh link is minted on every status
		// read, so asking for one again is the actual fix.
		return fmt.Sprintf("the download link was refused, but the export still reports itself ready — the link is short-lived, so try again for a fresh one: harbor account export-status %s --download .", id)
	case "":
		return "the export is not ready to download"
	default:
		return fmt.Sprintf("the export is not ready to download (status: %s)", status)
	}
}

// accountExportOutputPath resolves where an archive is written. "-" is stdout;
// an existing directory is joined with the server's own filename (which names
// the format and scope, e.g. harbor-export-recipes-2026-07-31.zip) so a download
// into ~/Downloads does not have to be named by hand; anything else is used
// verbatim.
func accountExportOutputPath(out, filename string) string {
	if out == "-" || filename == "" {
		return out
	}
	info, err := os.Stat(out)
	if err != nil || !info.IsDir() {
		return out
	}
	return filepath.Join(out, filename)
}

// accountConfirmExportDelete gates the export delete. With --yes it returns nil
// immediately. In --json mode or when stdin is not a terminal (scripts, CI, AI
// agents) it refuses rather than prompting; on an interactive terminal it
// requires the user to type exactly "yes".
func accountConfirmExportDelete(yes bool) error {
	if yes {
		return nil
	}
	if jsonOutput || !accountIsInteractive() {
		return errors.New("refusing to delete an export without confirmation — pass --yes")
	}
	fmt.Println("This deletes the export archive and any emailed link to it. Your notes are not affected, but you would have to export again.")
	answer, err := promptLine(`Type "yes" to confirm: `)
	if err != nil {
		return err
	}
	if answer != "yes" {
		return errors.New("aborted — the export was not deleted")
	}
	return nil
}

// ===========================================================================
// Confirmation / non-interactive guard
// ===========================================================================

// accountResolveConfirm decides how the destructive delete confirmation phrase
// is obtained. In interactive mode (a TTY, not --json) it prompts the user to
// type the phrase. Otherwise it enforces the non-interactive guard: the caller
// MUST have passed both --confirm (matching the phrase verbatim) and --yes.
func accountResolveConfirm(cmd *cobra.Command) (string, error) {
	supplied := ""
	if cmd.Flags().Changed("confirm") {
		supplied = stringFlag(cmd, "confirm")
	}
	phrase, err := accountDeleteGuard(jsonOutput, accountIsInteractive(), supplied, boolFlag(cmd, "yes"))
	if err != nil {
		return "", err
	}
	if phrase != "" {
		// Phrase was pre-supplied and validated by the guard.
		return phrase, nil
	}
	// Interactive path: prompt for the phrase and check it verbatim.
	typed, perr := promptLine(fmt.Sprintf("This is destructive. Type %q to confirm: ", accountDeleteConfirmPhrase))
	if perr != nil {
		return "", perr
	}
	if typed != accountDeleteConfirmPhrase {
		return "", errors.New("confirmation phrase did not match; aborting")
	}
	return typed, nil
}

// accountDeleteGuard enforces the destructive-delete confirmation policy and is
// kept free of I/O so it can be unit-tested. It returns the confirmation phrase
// to send when the caller pre-supplied it (non-interactive / --json path), or an
// empty string when the command should fall through to an interactive prompt.
//
// Rules:
//   - When NOT interactive (or --json is set), the caller MUST pass both a
//     --confirm value matching the phrase verbatim AND --yes; anything else is
//     refused so a script can never delete an account by accident.
//   - When interactive, a pre-supplied --confirm must still match verbatim if
//     present (a typo should fail fast); an empty --confirm defers to the prompt.
func accountDeleteGuard(jsonMode, interactive bool, suppliedConfirm string, yes bool) (string, error) {
	nonInteractive := jsonMode || !interactive
	if nonInteractive {
		if !yes {
			return "", errors.New("refusing to delete in non-interactive/--json mode without --yes")
		}
		if suppliedConfirm != accountDeleteConfirmPhrase {
			return "", fmt.Errorf("refusing to delete: pass --confirm %q exactly", accountDeleteConfirmPhrase)
		}
		return suppliedConfirm, nil
	}
	// Interactive: a wrong pre-supplied phrase is a hard error; an empty one
	// means "ask me".
	if suppliedConfirm != "" {
		if suppliedConfirm != accountDeleteConfirmPhrase {
			return "", fmt.Errorf("--confirm did not match; type %q exactly", accountDeleteConfirmPhrase)
		}
		return suppliedConfirm, nil
	}
	return "", nil
}

// accountIsInteractive reports whether stdin is an interactive terminal (so we
// can safely prompt the user). Scripts and CI pipe stdin, which reads false.
func accountIsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// ===========================================================================
// Error mapping
// ===========================================================================

// mapAccountError gives friendly messages for the account-domain error codes.
func mapAccountError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "confirmation_mismatch":
			return errors.New("the confirmation phrase did not match exactly")
		case "already_scheduled":
			return errors.New("an account deletion is already pending (cancel it first)")
		case "not_scheduled":
			return errors.New("no account deletion is pending")
		case "grace_expired":
			return errors.New("the cancellation window has passed; the account is being purged")
		case "reauth_required":
			return errors.New("incorrect current password")
		case "export_exists":
			return errors.New(accountExportExistsMessage(apiErr.Details))
		case "export_in_progress":
			return errors.New("that export is still being built — wait for it to finish, then delete it")
		case "not_found":
			return errors.New("no such export job (or it is not yours)")
		}
	}
	return err
}

// mapAccountExportStartError maps the errors specific to STARTING an export.
// A 404 here means the --notebook does not exist rather than "no such export
// job", which is what the shared mapper would otherwise say — the same code,
// two entirely different things to fix.
func mapAccountExportStartError(err error, notebook string) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "not_found" && notebook != "" {
		return fmt.Errorf("no such notebook %q (or it is not yours)", notebook)
	}
	return mapAccountError(err)
}

// accountExportExistsMessage turns the 409 refusal into an actionable sentence.
//
// The details carry everything needed to say WHAT is in the way — the format,
// whether it covers the whole account or one notebook (and which), the id to
// delete, and when it expires on its own — so the user is never told merely
// "conflict" and left to go looking. There is no list endpoint, so for a client
// that did not start the export this refusal is how it learns the export exists
// at all; throwing the details away would cost a second round-trip to say
// anything useful.
func accountExportExistsMessage(details map[string]any) string {
	d := map[string]any(details)
	format := accountExportFormatLabel(str(d, "format"))
	scope := "of your whole account"
	if name := str(d, "notebook_name"); name != "" {
		scope = fmt.Sprintf("of the notebook %q", name)
	} else if id := str(d, "notebook_id"); id != "" {
		scope = fmt.Sprintf("of notebook %s", id)
	} else if str(d, "scope") == "notebook" {
		// scope is the authoritative field and the other two are the detail. If
		// the server says this export covers ONE notebook but names neither, an
		// unhelpful answer beats a confident wrong one: telling someone their
		// whole account is already exported sends them to delete the wrong thing.
		scope = "of a single notebook"
	}

	msg := fmt.Sprintf("you already have %s %s export %s ready to download", accountArticle(format), format, scope)
	if expires := accountDetailEpoch(d, "result_expires_at"); expires > 0 {
		msg += fmt.Sprintf("; it expires on its own at %s (%s)", epochMS(expires), relTime(expires))
	}
	if id := str(d, "export_job_id"); id != "" {
		msg += fmt.Sprintf("\nDownload it:  harbor account export-status %s --download .", id)
		msg += fmt.Sprintf("\nOr delete it: harbor account export-delete %s", id)
	}
	return msg
}

// accountDetailEpoch reads an epoch-ms timestamp out of an error's details.
//
// The wire contract is that result_expires_at is a NUMBER everywhere — the
// status body and the 409 details alike (app.harbor.my#1107 fixed the details
// copy, which used to be a string). The string branch is therefore only a
// tolerance for an older server still in the wild, not the shape to expect:
// reading a real date either way beats silently dropping the expiry out of the
// refusal message.
func accountDetailEpoch(d map[string]any, key string) float64 {
	if v := num(d, key); v > 0 {
		return v
	}
	if s := str(d, key); s != "" {
		if ms, err := strconv.ParseFloat(s, 64); err == nil {
			return ms
		}
	}
	return 0
}

// ===========================================================================
// Display
// ===========================================================================

// displayExportJob renders one export job: its format and scope, the state it is
// in, and whatever that state has to say — a queue position, progress in notes,
// the completed date and expiry, the failure reason, the download URL — followed
// by the next thing to do about it.
//
// It renders both shapes the API returns for an export: the 202 from starting
// one (export_job_id, no progress yet) and the full status view.
func displayExportJob(data []byte) { displayExportJobCard(data, false) }

// displayExportJobDownloading renders the same card for a run that is saving the
// archive in the same breath. That run is a receipt rather than a menu: the
// presigned URL is noise when nobody has to paste it anywhere, and the follow-up
// lines would either instruct a download that is already happening or repeat,
// word for word, the error the caller is about to return.
func displayExportJobDownloading(data []byte) { displayExportJobCard(data, true) }

// displayExportJobCard is the shared body of the two views above; downloading
// selects the receipt wording.
func displayExportJobCard(data []byte, downloading bool) {
	j := parseJSON(client.UnwrapData(data))
	if j == nil {
		fmt.Println(string(data))
		return
	}
	// A freshly-started job (POST /export) returns export_job_id; a polled job
	// (GET /export/:id) returns id. Accept either.
	id := str(j, "id")
	if id == "" {
		id = str(j, "export_job_id")
	}
	status := str(j, "status")

	pairs := [][2]string{{"Job ID", bold(id)}}
	if format := str(j, "format"); format != "" {
		pairs = append(pairs, [2]string{"Format", accountExportFormatLabel(format)})
	}
	pairs = append(pairs,
		[2]string{"Scope", accountExportScopeLabel(j)},
		[2]string{"Status", colorizeStatus(status)},
	)

	// A queued export is waiting its turn behind the server-wide one-at-a-time
	// lock — that is a position in a line, not a stalled job and not 0% progress.
	if status == "queued" {
		if pos := int(num(j, "queue_position")); pos > 0 {
			pairs = append(pairs, [2]string{"Queue", fmt.Sprintf("%s in line", ordinal(pos))})
		}
	}
	if progress := accountExportProgress(j); progress != "" && (status == "running" || status == "completed") {
		pairs = append(pairs, [2]string{"Progress", progress})
	}
	if done := num(j, "completed_at"); done > 0 {
		pairs = append(pairs, [2]string{"Completed", fmt.Sprintf("%s (%s)", epochMS(done), relTime(done))})
	}
	if expires := num(j, "result_expires_at"); expires > 0 {
		pairs = append(pairs, [2]string{"Expires", fmt.Sprintf("%s (%s)", epochMS(expires), relTime(expires))})
	}
	if filename := str(j, "filename"); filename != "" {
		pairs = append(pairs, [2]string{"File", filename})
	}
	if et := str(j, "error_text"); et != "" {
		pairs = append(pairs, [2]string{"Error", et})
	}
	printKV(pairs)

	// The presigned URL is several hundred characters, so it goes BELOW the table
	// rather than in a cell: inside one it stretches every row to the width of the
	// signature and makes the whole view unreadable.
	if url := str(j, "download_url"); url != "" && !downloading {
		fmt.Println(dim("Download URL (short-lived):"))
		fmt.Println(url)
	}

	for _, line := range accountExportHints(j, id, downloading) {
		fmt.Println(dim(line))
	}
}

// displayExportList renders the account's export slots — at most two rows, one
// per format. An account with no exports gets a plain sentence and the command
// that starts one, not an empty table.
func displayExportList(data []byte) {
	items := client.CollectionItems(data)
	if len(items) == 0 {
		fmt.Println("No exports. Start one with: harbor account export")
		return
	}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		j := parseJSON(raw)
		if j == nil {
			continue
		}
		rows = append(rows, []string{
			accountExportFormatLabel(str(j, "format")),
			truncate(accountExportScopeLabel(j), 28),
			colorizeStatus(str(j, "status")),
			accountExportListDetail(j),
			// The full id, not an abbreviation: every follow-up command takes it,
			// and a truncated one cannot be copied into any of them.
			str(j, "id"),
		})
	}
	printTable([]string{"FORMAT", "SCOPE", "STATUS", "DETAIL", "ID"}, rows)
	for _, line := range accountExportListFooters(items) {
		fmt.Println(dim(line))
	}
}

// accountExportListFooters picks the next step(s) that fit what is actually in
// the table, rather than always offering to poll.
//
// This command is the way back for someone who closed their terminal, so by the
// time they run it the export they came for has usually FINISHED — and "Poll one
// with:" tells them to keep watching something that is already done. It reads as
// though the archive is not there yet, which is precisely the wrong first
// impression for the one screen that is meant to hand it over.
//
// Both lines can appear at once, because both can be true: an account holds one
// export per format, so a ready HTML archive and a still-building ENEX one is an
// ordinary pair. When nothing is downloadable and nothing is coming — every row
// failed, expired or deleted — the only honest next step is to start again.
func accountExportListFooters(items []json.RawMessage) []string {
	ready, waiting := false, false
	for _, raw := range items {
		switch str(parseJSON(raw), "status") {
		case "completed":
			ready = true
		case "queued", "running":
			waiting = true
		}
	}

	lines := []string{}
	if ready {
		lines = append(lines, "Download one with: harbor account export-status <id> --download .")
	}
	if waiting {
		lines = append(lines, "Poll one with: harbor account export-status <id>")
	}
	if len(lines) == 0 {
		lines = append(lines, "Start a new one with: harbor account export")
	}
	return lines
}

// displayExportDeleted confirms a delete, wording it by the status the server
// reports back rather than assuming the archive was the caller's to destroy —
// the endpoint is idempotent, so an export that had already expired on its own
// answers "expired" and should not be reported as "you deleted this".
func displayExportDeleted(data []byte) {
	j := parseJSON(client.UnwrapData(data))
	if j == nil {
		fmt.Println(string(data))
		return
	}
	switch str(j, "status") {
	case "expired":
		fmt.Println("That export had already expired — its archive was gone. The slot is free.")
	default:
		fmt.Println("Export deleted. The archive is gone and that format's slot is free.")
	}
	fmt.Println(dim("Start a new one with: harbor account export"))
}

// accountExportFormatLabel renders a format code for humans. An export written
// before the format column existed reports an empty format and is an ENEX one.
func accountExportFormatLabel(format string) string {
	switch format {
	case "html":
		return "HTML"
	case "enex", "":
		return "ENEX"
	default:
		return format
	}
}

// accountArticle picks "a" or "an" for a format label. The labels are acronyms,
// so what decides it is the sound of the first LETTER'S NAME — "an HTML" (aitch),
// "an ENEX" (ee) — not merely whether that letter is a vowel.
func accountArticle(label string) string {
	if label == "" {
		return "a"
	}
	if strings.ContainsRune("AEFHILMNORSX", rune(strings.ToUpper(label)[0])) {
		return "an"
	}
	return "a"
}

// accountExportScopeLabel says what an export covers. It prefers the notebook
// NAME, which the server freezes at export time — so an export still describes
// the archive it produced even after the notebook has been renamed or deleted.
func accountExportScopeLabel(j map[string]any) string {
	if name := str(j, "notebook_name"); name != "" {
		return "notebook " + name
	}
	if id := str(j, "notebook_id"); id != "" {
		return "notebook " + id
	}
	return "whole account"
}

// accountExportProgress renders progress in NOTES — "4,120 / 36,500 notes" —
// with a percentage once there is a total to divide by. The unit matters: these
// counters used to be notebooks, which reported a 36,500-note account as "1/79".
func accountExportProgress(j map[string]any) string {
	total, ok := j["total_units"]
	if !ok || total == nil {
		return ""
	}
	done, all := int64(num(j, "done_units")), int64(num(j, "total_units"))
	out := fmt.Sprintf("%s / %s notes", commaNum(done), commaNum(all))
	if all > 0 {
		out += fmt.Sprintf(" (%d%%)", done*100/all)
	}
	return out
}

// accountExportWaitPreamble is what a --wait run says once, before it settles in
// to poll: that the wait is optional, and how to leave it without losing the
// export.
//
// Ctrl-C is spelled out because it is the thing a person actually reaches for and
// the thing they are most likely to be afraid of — this poll is a read loop over
// the status endpoint, so interrupting it cancels nothing on the server. The
// route back is 'harbor account exports' rather than 'export-status <id>': the
// id was on a screen they have just closed.
func accountExportWaitPreamble() []string {
	return []string{
		accountExportKeepsBuilding,
		accountExportNextStep,
		"Ctrl-C stops the waiting, not the export.",
	}
}

// accountExportProgressLine is the one-line progress report --wait prints while
// an export builds. It returns "" for a state with nothing to report.
func accountExportProgressLine(j map[string]any) string {
	switch str(j, "status") {
	case "queued":
		if pos := int(num(j, "queue_position")); pos > 0 {
			return fmt.Sprintf("Waiting to start — %s in line (we build one export at a time).", ordinal(pos))
		}
		return "Waiting to start — we build one export at a time."
	case "running":
		if progress := accountExportProgress(j); progress != "" {
			return "Building your export… " + progress + "."
		}
		return "Building your export…"
	default:
		return ""
	}
}

// accountExportHints returns the follow-up lines for an export's current state:
// what it is doing and what to do about it. Every state gets one, so an export
// that is waiting, gone or broken explains itself rather than leaving a bare
// status word on screen.
//
// The two UNFINISHED states additionally get the "you can close this" pair: an
// export nobody has to sit through is worth nothing if the person watching it
// does not know they can leave. The four terminal states get neither line —
// completed/failed/expired/deleted have nothing left to wait for, so "we'll email
// you when it's ready" would be describing an email that has already been sent or
// will never come.
//
// A run that is downloading gets none of them. "Download it" is what is already
// happening, and for every state that CANNOT be downloaded the caller returns an
// error carrying the same sentence the hint would have printed — so a hint here
// would tell an expired export's owner twice, in consecutive lines, that their
// archive aged out. The successful case is picked back up after the write.
func accountExportHints(j map[string]any, id string, downloading bool) []string {
	if downloading {
		return nil
	}
	switch str(j, "status") {
	case "queued":
		return []string{
			"Waiting to start — we build one export at a time.",
			accountExportKeepsBuilding,
			accountExportNextStep,
			"Poll it with: harbor account export-status " + id,
		}
	case "running":
		return []string{
			accountExportKeepsBuilding,
			accountExportNextStep,
			"Poll it with: harbor account export-status " + id + " --wait",
		}
	case "completed":
		if str(j, "download_url") == "" {
			return []string{"The archive is no longer available — start a new export."}
		}
		return []string{
			"Download it: harbor account export-status " + id + " --download .",
			"Delete it:   harbor account export-delete " + id,
		}
	case "failed":
		return []string{"Start a new export to try again: harbor account export"}
	case "expired":
		return []string{"The archive was deleted at the end of its 72-hour retention window. Start a new export with: harbor account export"}
	case "deleted":
		return []string{"You deleted this export. Start a new one with: harbor account export"}
	default:
		return nil
	}
}

// accountExportListDetail is the one-cell summary of an export row's state for
// the list table: how far along it is, when it is ready until, or why it failed.
func accountExportListDetail(j map[string]any) string {
	switch str(j, "status") {
	case "queued":
		if pos := int(num(j, "queue_position")); pos > 0 {
			return fmt.Sprintf("%s in line", ordinal(pos))
		}
		return "waiting its turn"
	case "running":
		return accountExportProgress(j)
	case "completed":
		if expires := num(j, "result_expires_at"); expires > 0 {
			return fmt.Sprintf("ready %s · expires %s", relTime(num(j, "completed_at")), relTime(expires))
		}
		return "ready"
	case "failed":
		return truncate(str(j, "error_text"), 40)
	default:
		return "—"
	}
}

// displayDeletionScheduled renders the scheduled-deletion response, surfacing the
// purge date and grace window so the user knows their cancel deadline.
func displayDeletionScheduled(data []byte) {
	d := parseJSON(client.UnwrapData(data))
	if d == nil {
		fmt.Println(string(data))
		return
	}
	pairs := [][2]string{
		{"Status", colorizeStatus(str(d, "status"))},
		{"Purge after", fmt.Sprintf("%s (%s)", epochMS(num(d, "purge_after")), relTime(num(d, "purge_after")))},
		{"Grace days", str(d, "grace_days")},
		{"Cancel until", fmt.Sprintf("%s (%s)", epochMS(num(d, "can_cancel_until")), relTime(num(d, "can_cancel_until")))},
	}
	printKV(pairs)
	fmt.Println(dim("Other sessions have been signed out. Run 'harbor account cancel-delete' before the purge date to keep your account."))
}

func init() {
	accountExportCmd.Flags().String("format", "", "Archive format: enex (default) or html")
	accountExportCmd.Flags().String("notebook", "", "Export only this notebook id (default: the whole account)")
	accountExportCmd.Flags().Bool("wait", false, "Poll until the export finishes instead of returning the job id")
	accountExportCmd.Flags().String("download", "", "Save the finished ZIP here (implies --wait; - for stdout, a directory to use the server's filename)")
	accountExportCmd.Flags().Duration("poll-interval", 5*time.Second, "How often to poll while waiting")
	accountExportCmd.Flags().Duration("timeout", 0, "Give up waiting after this long (0 = no limit)")

	accountExportStatusCmd.Flags().String("download", "", "Save the completed export ZIP here (- for stdout, a directory to use the server's filename)")
	accountExportStatusCmd.Flags().Bool("wait", false, "Poll until the export reaches a final state")
	accountExportStatusCmd.Flags().Duration("poll-interval", 5*time.Second, "How often to poll while waiting")
	accountExportStatusCmd.Flags().Duration("timeout", 0, "Give up waiting after this long (0 = no limit)")

	accountExportDeleteCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")

	accountDeleteCmd.Flags().String("confirm", "", fmt.Sprintf("Confirmation phrase (must equal %q); required with --yes in non-interactive/--json mode", accountDeleteConfirmPhrase))
	accountDeleteCmd.Flags().Bool("yes", false, "Skip the interactive prompt (required, with --confirm, in non-interactive/--json mode)")

	accountCmd.AddCommand(accountExportCmd, accountExportsCmd, accountExportStatusCmd,
		accountExportDeleteCmd, accountDeleteCmd, accountCancelDeleteCmd)
	rootCmd.AddCommand(accountCmd)
}

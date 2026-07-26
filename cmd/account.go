// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/HarborMyNotes/harbor-cli/client"
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

  export        start a full-account GDPR data export (async job)
  export-status poll an export job and download the ZIP when it completes
  delete        schedule account deletion after a grace period (destructive)
  cancel-delete cancel a pending deletion within the grace window

Deletion is a soft-delete: a confirmed request records a purge date and revokes
your other sessions, but destroys nothing until the grace window elapses — you
can cancel until then.`,
}

// accountExportCmd starts a full-account export job. If a job is already
// queued/running the server returns it instead of starting another.
var accountExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Start a full-account data export",
	Long:  "Start an asynchronous full-account export (one ENEX per notebook, attachment bytes, and a manifest, packaged as a ZIP). Poll it with 'harbor account export-status <id>' and download the ZIP when it completes.",
	Example: `  harbor account export
  harbor account export --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.StartAccountExport()
		if err != nil {
			return mapAccountError(err)
		}
		printResult(data, displayExportJob)
		return nil
	},
}

// accountExportStatusCmd polls an export job and, with --download, saves the ZIP
// when the job is completed (by following the presigned URL).
var accountExportStatusCmd = &cobra.Command{
	Use:   "export-status <id>",
	Short: "Poll an export job and optionally download the ZIP",
	Args:  cobra.ExactArgs(1),
	Long:  "Poll an export job by id. When the job is completed and the result has not expired the response includes a short-lived presigned download URL. Pass --download <path> to fetch and save the ZIP (use - for stdout).",
	Example: `  harbor account export-status 0f9c2b1e-...
  harbor account export-status 0f9c2b1e-... --download account-export.zip`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetAccountExport(args[0])
		if err != nil {
			return mapAccountError(err)
		}

		// With --download, follow the presigned ZIP URL (unauthenticated fetch)
		// and stream it to the path; otherwise just render the job status.
		out := stringFlag(cmd, "download")
		if out == "" {
			printResult(data, displayExportJob)
			return nil
		}

		job := parseJSON(client.UnwrapData(data))
		if status := str(job, "status"); status != "completed" {
			return fmt.Errorf("export is not ready to download (status: %s)", status)
		}
		url := str(job, "download_url")
		if url == "" {
			return errors.New("the export result has expired; start a new export")
		}
		resp, ferr := c.FetchURL(url)
		if ferr != nil {
			return ferr
		}
		defer resp.Body.Close()
		n, werr := writeOutput(out, resp.Body)
		if werr != nil {
			return werr
		}
		if out != "-" {
			fmt.Printf("Wrote %s to %s\n", bytesHuman(float64(n)), out)
		}
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
		case "not_found":
			return errors.New("no such export job (or it is not yours)")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayExportJob renders an export job's status and, when present, its
// progress and presigned download URL.
func displayExportJob(data []byte) {
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
	pairs := [][2]string{
		{"Job ID", bold(id)},
		{"Status", colorizeStatus(str(j, "status"))},
	}
	if _, ok := j["total_units"]; ok {
		pairs = append(pairs, [2]string{"Progress", fmt.Sprintf("%d/%d notebooks", int(num(j, "done_units")), int(num(j, "total_units")))})
	}
	if url := str(j, "download_url"); url != "" {
		pairs = append(pairs, [2]string{"Download URL", url})
		pairs = append(pairs, [2]string{"URL expires", epochMS(num(j, "result_expires_at"))})
	}
	if et := str(j, "error_text"); et != "" {
		pairs = append(pairs, [2]string{"Error", et})
	}
	if str(j, "status") == "completed" && str(j, "download_url") == "" {
		// Completed but the result blob has expired (no URL).
		pairs = append(pairs, [2]string{"Note", "result expired — start a new export"})
	}
	printKV(pairs)
	if str(j, "status") == "completed" && str(j, "download_url") != "" {
		fmt.Println(dim("Download with: harbor account export-status " + id + " --download account-export.zip"))
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
	accountExportStatusCmd.Flags().String("download", "", "Save the completed export ZIP to this path (- for stdout)")

	accountDeleteCmd.Flags().String("confirm", "", fmt.Sprintf("Confirmation phrase (must equal %q); required with --yes in non-interactive/--json mode", accountDeleteConfirmPhrase))
	accountDeleteCmd.Flags().Bool("yes", false, "Skip the interactive prompt (required, with --confirm, in non-interactive/--json mode)")

	accountCmd.AddCommand(accountExportCmd, accountExportStatusCmd, accountDeleteCmd, accountCancelDeleteCmd)
	rootCmd.AddCommand(accountCmd)
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
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
--yes. In --json or non-interactive use, --yes is required.`,
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
		printResult(data, displayEmptyTrash)
		return nil
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
// emptied (the bare {"expunged": N} response).
func displayEmptyTrash(data []byte) {
	root := parseJSON(data)
	n := int64(num(root, "expunged"))
	if n == 1 {
		fmt.Println("Emptied the trash — 1 note permanently deleted.")
		return
	}
	fmt.Printf("Emptied the trash — %d notes permanently deleted.\n", n)
}

func init() {
	addPagingFlags(trashListCmd)

	trashEmptyCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")
	trashExpungeCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")

	trashCmd.AddCommand(trashListCmd, trashRestoreCmd, trashExpungeCmd, trashEmptyCmd)
	rootCmd.AddCommand(trashCmd)
}

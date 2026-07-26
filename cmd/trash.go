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
	Use:     "expunge <note-id>",
	Short:   "Permanently delete a single note",
	Args:    cobra.ExactArgs(1),
	Long:    "Permanently delete a note (it cannot be restored). Works whether or not the note is currently in the trash. Attachment bytes left with no remaining reference are reclaimed.",
	Example: "  harbor trash expunge 9c2e7b10-...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
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

// trashConfirmEmpty gates the destructive empty operation. With --yes it
// returns nil immediately. Otherwise, in --json mode or when stdin is not a
// terminal (scripts, CI, AI agents), it refuses rather than prompting; on an
// interactive terminal it requires the user to type exactly "yes".
func trashConfirmEmpty(yes bool) error {
	if yes {
		return nil
	}
	if jsonOutput || !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("refusing to empty the trash without confirmation — pass --yes")
	}
	fmt.Println("This permanently deletes every note in the trash. This cannot be undone.")
	answer, err := promptLine(`Type "yes" to confirm: `)
	if err != nil {
		return err
	}
	if answer != "yes" {
		return errors.New("aborted — the trash was not emptied")
	}
	return nil
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

	trashCmd.AddCommand(trashListCmd, trashRestoreCmd, trashExpungeCmd, trashEmptyCmd)
	rootCmd.AddCommand(trashCmd)
}

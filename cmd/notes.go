// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/spf13/cobra"
)

// notesCmd is the parent for note content commands. It also hosts the note↔tag
// and read-only insight subcommands (registered in their own files).
var notesCmd = &cobra.Command{
	Use:     "notes",
	Aliases: []string{"note", "n"},
	Short:   "Manage notes (list, get, create, update, delete, append)",
	GroupID: groupContent,
	Long: `Create and manage notes. Bodies accept Markdown (default) or HTML via
--format, supplied with --content, --file, or piped via --stdin — convenient
for both humans and AI agents.`,
}

// notesListCmd lists notes.
var notesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notes",
	Example: `  harbor notes list
  harbor notes list --notebook 5b1f... --order -created_at
  harbor notes list --meta --json | jq '.data[] | {id, title}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		if s := stringFlag(cmd, "notebook"); s != "" {
			params["notebook_id"] = s
		}
		if s := stringFlag(cmd, "tag"); s != "" {
			params["tag"] = s
		}
		if s := stringFlag(cmd, "updated-since"); s != "" {
			params["updated_since"] = s
		}
		if boolFlag(cmd, "deleted") {
			params["deleted"] = "true"
		}
		if boolFlag(cmd, "meta") {
			params["fields"] = "meta"
		}
		data, err := c.ListNotes(params)
		if err != nil {
			return err
		}
		data = decryptResult(c, creds, data)
		printResult(data, displayNotes)
		return nil
	},
}

// notesGetCmd fetches one note, defaulting to readable Markdown content.
var notesGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Get a note by id",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor notes get 9c2e... --format markdown",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := map[string]string{}
		format := stringFlag(cmd, "format")
		if format != "" {
			params["format"] = format
		}
		if boolFlag(cmd, "deleted") {
			params["deleted"] = "true"
		}
		data, err := c.GetNote(args[0], params)
		if err != nil {
			return err
		}
		data = decryptResult(c, creds, data)
		printResult(data, displayNote)
		return nil
	},
}

// notesCreateCmd creates a note.
var notesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a note",
	Long: `Create a note. Provide the body with --content, --file, or --stdin (Markdown by
default; --format html for HTML).

ENCRYPTION. A notebook can be marked "encrypt new notes by default", and that
applies to your default notebook too. Creating a note in one needs your
passphrase: set HARBOR_PASSPHRASE and the note is sealed before it leaves this
machine. WITHOUT IT THE CREATE IS REFUSED and nothing is written — the CLI will
not quietly land a plaintext note in a notebook you asked to be encrypted. To
put an unencrypted note there on purpose, say so with --plaintext.`,
	Example: `  harbor notes create --title "Plan" --content "# Goals\n\n- ship it"
  echo "# Notes" | harbor notes create --title Standup --stdin
  harbor notes create --title Recipe --file recipe.md --notebook 5b1f...
  harbor notes create --title Draft --content "wip" --plaintext   # unencrypted, on purpose`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		content, format, hasContent, err := readContent(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "title", "title")
		notebookID := stringFlag(cmd, "notebook")
		if notebookID != "" {
			body["notebook_id"] = notebookID
		}
		addStringIfChanged(cmd, body, "source-url", "source_url")
		addStringIfChanged(cmd, body, "author", "author")
		if hasContent {
			body["content"] = content
			body["content_format"] = format
		}
		// Encrypt client-side when --encrypt is set or the target notebook defaults
		// to encryption. The note id is generated first because the field AAD binds
		// to it; an encrypted note must carry a content envelope. This also REFUSES
		// the create outright when the destination encrypts by default and there is
		// no passphrase to do it with — the error arrives before CreateNote, so a
		// refusal writes nothing.
		encrypt, err := shouldEncryptCreate(cmd, c, notebookID)
		if err != nil {
			return err
		}
		if encrypt {
			if !hasContent {
				return errors.New("an encrypted note needs content (the server requires a content envelope)")
			}
			if err := encryptCreateBody(c, creds, body); err != nil {
				return err
			}
		}
		data, err := c.CreateNote(body)
		if err != nil {
			return mapNoteError(err)
		}
		data = decryptResult(c, creds, data)
		printResult(data, displayNote)
		return nil
	},
}

// notesUpdateCmd partially updates a note. A content flag replaces the whole
// body, which is where the note's tasks live — see guardNoteTaskLoss.
var notesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a note (only the flags you pass are changed)",
	Args:  cobra.ExactArgs(1),
	Long: `Update a note. Only the fields you pass are changed.

The content flags (--content / --file / --stdin) REPLACE the note's body — they
do not merge into it. That matters more than it sounds, because the body is
where a note's TASKS live: each task is a <harbor-task> block, and saving a body
that drops the block DELETES that task (it is tombstoned, not detached). Inline
attachments and note-to-note links are derived from the body the same way.

So an update that would drop tasks is REFUSED and nothing is written. Re-run it
with --keep-tasks to carry those blocks into the new body, or with
--allow-task-loss to delete them on purpose. To add to a body without replacing
it, use 'harbor notes append'.

ENCRYPTION FOLLOWS THE NOTEBOOK. --notebook is how a note is moved, and moving
one into a notebook marked "encrypt new notes by default" ENCRYPTS it as part of
the move: the ciphertext, the encryption flag and the new notebook go out in a
single write, so the note is never sitting in that notebook readable. That needs
your passphrase — set HARBOR_PASSPHRASE, or the move is REFUSED and the note
stays plaintext where it was. Passing --notebook "" means your default notebook,
and the same applies if that is the one that encrypts.

A note that is ALREADY encrypted is moved as it is, in either direction: nothing
is re-encrypted and nothing is re-keyed. Moving one OUT of an encrypting notebook
does not decrypt it — encryption is per-note. Use 'harbor notes decrypt' for
that, which is itself refused while the note is still inside such a notebook.

What sealing a note does to it, and what it does NOT cover — both worth knowing
BEFORE you move one:

` + bulletCaveat(historyLossCaveat) + `
` + bulletCaveat(attachmentCaveat) + `

So a note moved into an encrypting notebook loses every earlier version of itself
and is unreadable from then on, but any file attached to it is not. Both caveats
are printed after the move.`,
	Example: `  harbor notes update 9c2e... --title "Plan (final)"
  harbor notes update 9c2e... --file updated.md
  harbor notes update 9c2e... --content "# Rewritten" --keep-tasks
  harbor notes update 9c2e... --notebook 5b1f...   # sealed if 5b1f encrypts by default`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		content, format, hasContent, err := readContent(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "title", "title")
		addStringIfChanged(cmd, body, "notebook", "notebook_id")
		addStringIfChanged(cmd, body, "source-url", "source_url")
		addStringIfChanged(cmd, body, "author", "author")
		if hasContent {
			body["content"] = content
			body["content_format"] = format
		}
		if len(body) == 0 {
			return errors.New("nothing to update — pass --title, content, or another field")
		}
		// Replacing the body releases every task the new body does not carry, and
		// the server deletes a released task. Refuse rather than do that silently
		// (issue #62); only a content-carrying update can trigger it. The note it
		// read is handed to the steps below so the note is not fetched twice.
		var note map[string]any
		if hasContent {
			note, err = guardNoteTaskLoss(cmd, c, args[0], format, body)
			if err != nil {
				return err
			}
		}
		// Encryption follows the notebook: --notebook into a notebook that encrypts
		// by default SEALS the note, and the ciphertext rides out in this same PATCH
		// rather than in a second one — so the note is never sitting in an encrypting
		// notebook readable. A refusal here writes nothing and moves nothing.
		move, err := prepareNoteMove(c, creds, args[0], body, note)
		if err != nil {
			return err
		}
		// Only when the move did not already seal it. Re-sealing what was just
		// sealed would encrypt an envelope, and re-reading the note to find that out
		// would ask a question already answered.
		if !move.sealed {
			// If the note is encrypted, re-seal any title/content we are sending.
			// Refuse to overwrite ciphertext with plaintext when no passphrase is set.
			if err := encryptUpdateBody(c, creds, args[0], body, note); err != nil {
				return err
			}
		}
		data, err := writeNoteUpdate(c, args[0], body, move)
		if err != nil {
			return err
		}
		data = decryptResult(c, creds, data)
		printResult(data, displayNote)
		move.announce()
		return nil
	},
}

// notesAppendCmd appends a fragment to a note's body.
var notesAppendCmd = &cobra.Command{
	Use:   "append <id>",
	Short: "Append content to the end of a note",
	Args:  cobra.ExactArgs(1),
	Example: `  harbor notes append 9c2e... --content "- one more thing"
  date | harbor notes append 9c2e... --stdin`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		content, format, hasContent, err := readContent(cmd)
		if err != nil {
			return err
		}
		if !hasContent {
			return errors.New("append requires content — pass --content, --file, or --stdin")
		}
		data, err := c.AppendNote(args[0], map[string]any{"content": content, "content_format": format})
		if err != nil {
			return mapNoteError(err)
		}
		printResult(data, displayNote)
		return nil
	},
}

// notesDeletePermanentConfirmation gates `notes delete --permanent`, which is a
// second route to exactly the expunge `trash expunge` performs — same call, same
// irreversibility. Being a flag on an otherwise recoverable command is what let
// it sit unguarded: `notes delete` is the safe, everyday command, and the one
// character that makes it permanent is easy to overlook when reading a script.
var notesDeletePermanentConfirmation = registerConfirmation("harbor notes delete", confirmation{
	Warning:     "This permanently deletes the note. It does NOT go to the trash and cannot be restored.",
	Prompt:      `Type "yes" to confirm: `,
	Affirmative: "yes",
	Unattended:  "refusing to permanently delete a note without confirmation — pass --yes",
	Aborted:     "aborted — the note was not deleted",
})

// notesDeleteCmd trashes (or permanently expunges) a note.
var notesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a note (trash by default, --permanent to expunge)",
	Args:  cobra.ExactArgs(1),
	Long: `Move a note to the trash (recoverable with 'harbor trash restore'), or expunge
it permanently with --permanent.

The default is recoverable and asks nothing. --permanent is not: the note is
gone, so you will be asked to confirm by typing "yes" unless you pass --yes, and
in --json or non-interactive use --yes is required.`,
	Example: `  harbor notes delete 9c2e...
  harbor notes delete 9c2e... --permanent
  harbor notes delete 9c2e... --permanent --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		permanent := boolFlag(cmd, "permanent")
		// Only the irreversible half is gated. Trashing is recoverable, and
		// making the everyday delete prompt would train people to type "yes"
		// without reading — which is how the prompt that matters gets ignored.
		if permanent {
			if err := notesConfirmPermanentDelete(boolFlag(cmd, "yes")); err != nil {
				return err
			}
		}
		if _, err := c.DeleteNote(args[0], permanent); err != nil {
			return err
		}
		if permanent {
			fmt.Println("Note permanently deleted.")
		} else {
			fmt.Println("Note moved to trash.")
		}
		return nil
	},
}

// notesConfirmPermanentDelete gates `notes delete --permanent`, resolving the
// ambient state and handing the decision to the shared confirmDestructive.
func notesConfirmPermanentDelete(yes bool) error {
	return confirmDestructive(notesDeletePermanentConfirmation, jsonOutput, stdinIsInteractive(), yes, askLine)
}

// mapNoteError gives friendly messages for note-specific codes.
func mapNoteError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "note_title_too_long":
			return errors.New("the note title is too long (max 255 characters)")
		case "note_too_large":
			return errors.New("the note body is too large (max 5 MiB)")
		case "append_not_supported_encrypted":
			return errors.New("cannot append to an encrypted note")
		case "cannot_move_plaintext_into_encrypted":
			// The server's own backstop on this CLI's move guard. Nothing was written
			// and no usn was spent, so re-running is always the fix — but WHY the local
			// guard did not fire first decides what else to say, and getting that wrong
			// sends someone hunting for a key they are already holding.
			//
			// With a passphrase set, the local guard ran and was told the destination
			// does not encrypt. That is the race this backstop exists for: the flag was
			// flipped from another device between this command's notebook read and its
			// write. Naming the passphrase there is simply wrong. Without one, the
			// destination genuinely could not be sealed and the passphrase IS the gap —
			// an old binary, or some other path that skipped the guard.
			if encryptionEnabled() {
				return errors.New("that notebook keeps its notes encrypted and this note was still plaintext, so nothing was written and the note was not moved.\n" +
					"       its encrypt-by-default setting changed after this command read the notebook — run the same command again")
			}
			return fmt.Errorf("that notebook keeps its notes encrypted and this note is still plaintext, so nothing was written and the note was not moved.\n"+
				"       set %s and run the same command again — the note is then sealed and moved in one write", passphraseEnv)
		case "note_usn_stale":
			// The base_usn precondition guardNoteTaskLoss sends. Retrying the same
			// body is exactly the clobber the server just refused, so say to merge.
			return errors.New("the note changed after this command read it, and nothing was written — something else edited it in between. Re-read it ('harbor notes get <id> --format html'), merge your change into the current body, and update again; re-running this command as-is would overwrite whatever landed in between")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// extractNote unwraps a note from either a bare note object or a {note, usn}
// mutation envelope, returning the note map and the new USN string (if any).
func extractNote(data []byte) (map[string]any, string) {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		return nil, ""
	}
	if n := nested(root, "note"); n != nil {
		return n, str(root, "usn")
	}
	return root, ""
}

// displayNotes renders a note collection as a table.
func displayNotes(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "TITLE", "NOTEBOOK", "🔒", "WORDS", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		n := parseJSON(raw)
		rows = append(rows, []string{
			str(n, "id"),
			truncate(str(n, "title"), 40),
			shortID(str(n, "notebook_id"), 8),
			lockMark(boolean(n, "is_encrypted")),
			str(n, "word_count"),
			dim(str(n, "usn")),
			epochMS(num(n, "updated_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayNote renders a single note (bare or mutation) as a detail view plus
// its body. Encrypted bodies are shown as a placeholder.
func displayNote(data []byte) {
	n, usn := extractNote(data)
	if n == nil {
		fmt.Println(string(data))
		return
	}
	pairs := [][2]string{
		{"ID", bold(str(n, "id"))},
		{"Title", str(n, "title")},
		{"Notebook", str(n, "notebook_id")},
		{"Encrypted", boolMark(boolean(n, "is_encrypted"))},
		{"Words", str(n, "word_count")},
		{"USN", str(n, "usn")},
		{"Updated", epochMS(num(n, "updated_at"))},
	}
	// Show the clip/import provenance when present, mirroring the web app's
	// source-URL chip. Only-when-non-empty keeps unclipped notes clean.
	if a := str(n, "author"); a != "" {
		pairs = append(pairs, [2]string{"Author", a})
	}
	if u := str(n, "source_url"); u != "" {
		pairs = append(pairs, [2]string{"Source", u})
	}
	if usn != "" {
		pairs = append(pairs, [2]string{"New USN", bold(usn)})
	}
	printKV(pairs)

	fmt.Println()
	body := str(n, "content")
	// An encrypted note shows a placeholder unless it was actually decrypted: no
	// passphrase set, an empty body, or a body still in envelope form all mean we
	// have only ciphertext. Once decryptResult has replaced the body with
	// plaintext (passphrase set + unwrapped) it falls through and renders normally.
	if boolean(n, "is_encrypted") && (!encryptionEnabled() || body == "" || crypto.IsEnvelope(body)) {
		fmt.Println(dim("[encrypted]"))
		return
	}
	if strings.Contains(body, "<") && strings.Contains(body, ">") {
		body = stripHTML(body)
	}
	if body != "" {
		fmt.Println(body)
	}
}

// lockMark renders the encryption indicator for note lists.
func lockMark(encrypted bool) string {
	if encrypted {
		return "🔒"
	}
	return dim("·")
}

func init() {
	addPagingFlags(notesListCmd)
	notesListCmd.Flags().String("notebook", "", "Filter to one notebook id")
	notesListCmd.Flags().String("tag", "", "Filter to notes carrying this tag id")
	notesListCmd.Flags().String("updated-since", "", "Only notes updated at or after this epoch-ms")
	notesListCmd.Flags().Bool("deleted", false, "Include trashed notes")
	notesListCmd.Flags().Bool("meta", false, "Omit note content for lighter listings")

	notesGetCmd.Flags().String("format", "markdown", "Content format to return: markdown or html")
	notesGetCmd.Flags().Bool("deleted", false, "Return the note even if trashed")

	notesCreateCmd.Flags().String("title", "", "Note title")
	notesCreateCmd.Flags().String("notebook", "", "Notebook id (defaults to your default notebook)")
	notesCreateCmd.Flags().String("source-url", "", "Source URL attribute")
	notesCreateCmd.Flags().String("author", "", "Author attribute")
	notesCreateCmd.Flags().Bool("encrypt", false, "Encrypt this note end-to-end (requires HARBOR_PASSPHRASE)")
	notesCreateCmd.Flags().Bool("plaintext", false, "Create an unencrypted note in a default_encrypt notebook (otherwise refused without HARBOR_PASSPHRASE)")
	addContentFlags(notesCreateCmd)

	notesUpdateCmd.Flags().String("title", "", "New title")
	notesUpdateCmd.Flags().String("notebook", "", "Move to this notebook id")
	notesUpdateCmd.Flags().String("source-url", "", "Source URL attribute")
	notesUpdateCmd.Flags().String("author", "", "Author attribute")
	addContentFlags(notesUpdateCmd)
	addTaskLossFlags(notesUpdateCmd)

	addContentFlags(notesAppendCmd)

	notesDeleteCmd.Flags().Bool("permanent", false, "Expunge permanently instead of trashing")
	notesDeleteCmd.Flags().Bool("yes", false, "Skip the --permanent confirmation prompt (required in --json/non-interactive use)")

	notesCmd.AddCommand(notesListCmd, notesGetCmd, notesCreateCmd, notesUpdateCmd, notesAppendCmd, notesDeleteCmd)
	rootCmd.AddCommand(notesCmd)
}

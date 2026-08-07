// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// notebooksCmd is the parent for notebook management.
var notebooksCmd = &cobra.Command{
	Use:     "notebooks",
	Aliases: []string{"notebook", "nb"},
	Short:   "Manage notebooks (list, get, create, update, delete)",
	GroupID: groupOrg,
	Long:    "Notebooks are containers for notes. Each account has exactly one default notebook.",
}

// notebooksListCmd lists notebooks.
var notebooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notebooks",
	Example: `  harbor notebooks list
  harbor notebooks list --stack Projects --order -updated_at
  harbor notebooks list --json | jq '.data[].name'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		if s := stringFlag(cmd, "stack"); s != "" {
			params["stack"] = s
		}
		if boolFlag(cmd, "include-deleted") {
			params["include_deleted"] = "true"
		}
		data, err := c.ListNotebooks(params)
		if err != nil {
			return err
		}
		printResult(data, displayNotebooks)
		return nil
	},
}

// notebooksGetCmd fetches a single notebook.
var notebooksGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Get a notebook by id",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor notebooks get 5b1f2c9a-...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetNotebook(args[0], boolFlag(cmd, "include-deleted"))
		if err != nil {
			return err
		}
		printResult(data, displayNotebook)
		return nil
	},
}

// notebooksCreateCmd creates a notebook.
var notebooksCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a notebook",
	Example: `  harbor notebooks create --name "Work" --stack Projects
  harbor notebooks create --name "Secrets" --default-encrypt`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		name := stringFlag(cmd, "name")
		if name == "" {
			return errors.New("--name is required")
		}
		body := map[string]any{"name": name}
		addStringIfChanged(cmd, body, "stack", "stack")
		addBoolIfChanged(cmd, body, "default-encrypt", "default_encrypt")
		data, err := c.CreateNotebook(body)
		if err != nil {
			return mapNotebookError(err)
		}
		printResult(data, displayNotebook)
		return nil
	},
}

// notebooksUpdateCmd partially updates a notebook.
var notebooksUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a notebook (only the flags you pass are changed)",
	Args:  cobra.ExactArgs(1),
	Long: `Update a notebook. Only the fields you pass are modified.

Use --make-default to promote this notebook to the account default (the prior
default is demoted automatically). There must always be exactly one default, so
a notebook cannot be un-defaulted directly — promote a different one instead.

The default notebook can never also be encrypt-by-default: forwarded email,
imports, and notes created with no notebook all land in the default, and none of
those writers can encrypt. So --default-encrypt is refused on the default
notebook, and --make-default is refused on a notebook that encrypts — unless you
turn it off in the same command, e.g. --make-default --default-encrypt=false.`,
	Example: `  harbor notebooks update 5b1f... --name "Work — Active"
  harbor notebooks update 5b1f... --stack Archive --public=false
  harbor notebooks update 5b1f... --make-default
  harbor notebooks update 5b1f... --make-default --default-encrypt=false`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "name", "name")
		addStringIfChanged(cmd, body, "stack", "stack")
		addBoolIfChanged(cmd, body, "default-encrypt", "default_encrypt")
		addBoolIfChanged(cmd, body, "public", "is_public")
		if boolFlag(cmd, "make-default") {
			body["is_default"] = true
		}
		if len(body) == 0 {
			return errors.New("nothing to update — pass at least one field flag")
		}
		// Refuse the banned pair before spending a request on a guaranteed 422.
		if err := guardDefaultNotebookEncrypt(c, args[0], body); err != nil {
			return err
		}
		data, err := c.UpdateNotebook(args[0], body)
		if err != nil {
			return mapNotebookError(err)
		}
		printResult(data, displayNotebook)
		return nil
	},
}

// notebooksDeleteCmd deletes (tombstones) a notebook.
var notebooksDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a notebook",
	Args:  cobra.ExactArgs(1),
	Long:  "Tombstone a notebook. Its live notes are moved to the default notebook (default) or trashed (--notes trash). The default notebook cannot be deleted.",
	Example: `  harbor notebooks delete 5b1f...
  harbor notebooks delete 5b1f... --notes trash`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		notesPolicy := stringFlag(cmd, "notes")
		if _, err := c.DeleteNotebook(args[0], notesPolicy); err != nil {
			return mapNotebookError(err)
		}
		fmt.Println("Notebook deleted.")
		return nil
	},
}

// mapNotebookError gives friendly messages for the notebook-specific codes.
// default_notebook_cannot_encrypt is deliberately absent from the switch below.
// Its server message is user-facing copy shared with the web app, and the CLI's
// default renderer prints an APIError's message verbatim — so paraphrasing it
// here is the one way to get it wrong. Pinned by
// TestDefaultCannotEncryptServerMessageIsNotParaphrased.
func mapNotebookError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "notebook_name_exists":
			return errors.New("a notebook with that name already exists")
		case "cannot_delete_default":
			return errors.New("the default notebook cannot be deleted — promote another notebook first")
		case "cannot_unset_default":
			return errors.New("there must always be a default notebook — promote a different one instead")
		}
	}
	return err
}

// defaultCannotEncryptMessage is the server's own wording for the banned pair,
// quoted rather than paraphrased.
//
// The rule: a notebook can never be both the account default AND encrypt-by-
// default. The default is where forwarded email, imports, and notes created with
// no notebook land, and none of those writers can client-side encrypt — so a
// note the user believes is sealed would arrive there in the clear.
//
// The CLI refuses locally instead of letting the request 422 so the user gets the
// reason without a round trip, and so the failure reads the same offline as on.
// The wording matches app.harbor.my internal/notebooks/notebooks.go exactly
// (issue app.harbor.my#1338); web shows the same sentence.
const defaultCannotEncryptMessage = "the default notebook can't encrypt notes by default — forwarded email, imports, and notes with no notebook land there; encrypt a different notebook instead"

// guardDefaultNotebookEncrypt refuses an update whose RESULTING state would make
// one notebook both the default and encrypt-by-default.
//
// Judged on the resulting state, not on the fields present, because the server
// judges it that way: `--make-default --default-encrypt=false` legally promotes a
// notebook while switching encryption off, and refusing that because the request
// mentions both flags would block the one command that fixes the situation.
//
// It fetches the notebook only when the answer genuinely depends on the current
// state — when the request sets both fields, or sets neither to a banned value,
// no round trip happens.
func guardDefaultNotebookEncrypt(c *client.Client, id string, body map[string]any) error {
	wantEncrypt, encryptSet := body["default_encrypt"].(bool)
	wantDefault, defaultSet := body["is_default"].(bool)

	// Turning encryption OFF can never produce the pair, whatever else changes.
	if encryptSet && !wantEncrypt {
		return nil
	}
	// Both stated in one request: decidable with no fetch.
	if encryptSet && wantEncrypt && defaultSet && wantDefault {
		return errors.New(defaultCannotEncryptMessage)
	}
	// Only one side stated; the other comes from the notebook as it stands.
	needCurrent := (encryptSet && wantEncrypt) || (defaultSet && wantDefault)
	if !needCurrent {
		return nil
	}
	data, err := c.GetNotebook(id, false)
	if err != nil {
		// A notebook we cannot read is not one we can clear. Let the write go and
		// let the server judge it — it enforces the same rule, and failing the
		// command here would turn a transient read error into a refusal to write.
		return nil
	}
	nb := parseJSON(client.UnwrapData(data))
	if encryptSet && wantEncrypt && boolean(nb, "is_default") {
		return errors.New(defaultCannotEncryptMessage)
	}
	if defaultSet && wantDefault && boolean(nb, "default_encrypt") {
		return errors.New(defaultCannotEncryptMessage + " — switch it off with '--default-encrypt=false' in the same command to promote this notebook")
	}
	return nil
}

// ===========================================================================
// Display
// ===========================================================================

// displayNotebooks renders a notebook collection as a table.
func displayNotebooks(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "NAME", "STACK", "DEFAULT", "ENCRYPT", "PUBLIC", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		nb := parseJSON(raw)
		rows = append(rows, []string{
			str(nb, "id"),
			str(nb, "name"),
			str(nb, "stack"),
			defaultStar(boolean(nb, "is_default")),
			boolMark(boolean(nb, "default_encrypt")),
			boolMark(boolean(nb, "is_public")),
			dim(str(nb, "usn")),
			epochMS(num(nb, "updated_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayNotebook renders one notebook as a key/value detail view.
func displayNotebook(data []byte) {
	nb := parseJSON(client.UnwrapData(data))
	if nb == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"ID", bold(str(nb, "id"))},
		{"Name", str(nb, "name")},
		{"Stack", str(nb, "stack")},
		{"Default", defaultStar(boolean(nb, "is_default"))},
		{"Encrypt new notes", boolMark(boolean(nb, "default_encrypt"))},
		{"Public", boolMark(boolean(nb, "is_public"))},
		{"USN", str(nb, "usn")},
		{"Deleted", boolMark(boolean(nb, "deleted"))},
		{"Updated", epochMS(num(nb, "updated_at"))},
		{"Created", epochMS(num(nb, "created_at"))},
	})
}

// defaultStar renders the default-notebook marker.
func defaultStar(isDefault bool) string {
	if isDefault {
		return star()
	}
	return dim("·")
}

func init() {
	addPagingFlags(notebooksListCmd)
	notebooksListCmd.Flags().String("stack", "", "Filter to one stack")
	notebooksListCmd.Flags().Bool("include-deleted", false, "Include tombstoned notebooks")

	notebooksGetCmd.Flags().Bool("include-deleted", false, "Return the notebook even if tombstoned")

	notebooksCreateCmd.Flags().String("name", "", "Notebook name (required)")
	notebooksCreateCmd.Flags().String("stack", "", "Stack (grouping label)")
	notebooksCreateCmd.Flags().Bool("default-encrypt", false, "Encrypt new notes in this notebook by default")

	notebooksUpdateCmd.Flags().String("name", "", "New name")
	notebooksUpdateCmd.Flags().String("stack", "", "New stack")
	notebooksUpdateCmd.Flags().Bool("default-encrypt", false, "Encrypt new notes by default (never allowed on the default notebook)")
	notebooksUpdateCmd.Flags().Bool("public", false, "Make the notebook public")
	notebooksUpdateCmd.Flags().Bool("make-default", false, "Promote this notebook to the account default (refused if it encrypts by default)")

	notebooksDeleteCmd.Flags().String("notes", "", "What to do with its notes: move_to_default (default) or trash")

	notebooksCmd.AddCommand(notebooksListCmd, notebooksGetCmd, notebooksCreateCmd, notebooksUpdateCmd, notebooksDeleteCmd)
	rootCmd.AddCommand(notebooksCmd)
}

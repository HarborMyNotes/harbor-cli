// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// shortcutsCmd is the parent for sidebar-shortcut management.
var shortcutsCmd = &cobra.Command{
	Use:     "shortcuts",
	Aliases: []string{"shortcut", "sc"},
	Short:   "Manage sidebar shortcuts (list, get, create, update, delete, reorder)",
	GroupID: groupOrg,
	Long: `Shortcuts are user-curated, ordered sidebar pointers.

A shortcut either points at a record — a note, notebook, or tag (by --target-id)
— or holds a saved search query (--query). They are ordered by a fractional
position (lower sorts higher); use 'reorder' to renumber the whole list.`,
}

// shortcutsListCmd lists shortcuts ordered by position.
var shortcutsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sidebar shortcuts",
	Long:  "List the user's sidebar shortcuts, ordered by position ascending. Tombstoned shortcuts are excluded unless --include-deleted is given.",
	Example: `  harbor shortcuts list
  harbor shortcuts list --order -usn --limit 50
  harbor shortcuts list --include-deleted
  harbor shortcuts list --json | jq '.data[].label'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		if boolFlag(cmd, "include-deleted") {
			params["include_deleted"] = "true"
		}
		data, err := c.ListShortcuts(params)
		if err != nil {
			return mapShortcutError(err)
		}
		printResult(data, displayShortcuts)
		return nil
	},
}

// shortcutsGetCmd fetches a single shortcut.
var shortcutsGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Get a shortcut by id",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor shortcuts get 8d9e0f1a-...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetShortcut(args[0], boolFlag(cmd, "include-deleted"))
		if err != nil {
			return mapShortcutError(err)
		}
		printResult(data, displayShortcut)
		return nil
	},
}

// shortcutsCreateCmd creates a shortcut, enforcing type↔field consistency
// client-side before the request is sent.
var shortcutsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a shortcut",
	Long: `Create a sidebar shortcut.

The --type determines which target flag is required:
  note | notebook | tag  → requires --target-id (and must NOT set --query)
  search                 → requires --query     (and must NOT set --target-id)

When --position is omitted the shortcut is appended to the end of the list.`,
	Example: `  harbor shortcuts create --type note --target-id 9c2e... --label "Quarterly plan"
  harbor shortcuts create --type notebook --target-id 5b1f...
  harbor shortcuts create --type search --query "tag:receipts year:2026" --label Receipts`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		typ := stringFlag(cmd, "type")
		targetID := stringFlag(cmd, "target-id")
		query := stringFlag(cmd, "query")
		// Validate the type and that the right target field is set client-side.
		if err := shortcutValidateTypeFields(typ, targetID, query); err != nil {
			return err
		}
		body := map[string]any{"type": typ}
		if typ == "search" {
			body["saved_query"] = query
		} else {
			body["target_id"] = targetID
		}
		addStringIfChanged(cmd, body, "label", "label")
		if cmd.Flags().Changed("position") {
			v, _ := cmd.Flags().GetFloat64("position")
			body["position"] = v
		}
		data, err := c.CreateShortcut(body)
		if err != nil {
			return mapShortcutError(err)
		}
		printResult(data, displayShortcut)
		return nil
	},
}

// shortcutsUpdateCmd partially updates a shortcut. Only the flags passed are
// changed; --target-id and --query remain mutually exclusive.
var shortcutsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a shortcut (only the flags you pass are changed)",
	Args:  cobra.ExactArgs(1),
	Long: `Update a shortcut. Only the fields you pass are modified.

--target-id is only valid for a record shortcut (note/notebook/tag); --query is
only valid for a search shortcut. You cannot pass both, and the server re-checks
type consistency.`,
	Example: `  harbor shortcuts update 8d9e... --label "Q3 plan"
  harbor shortcuts update 8d9e... --target-id 1f0b...
  harbor shortcuts update 8d9e... --query "tag:receipts" --position 150`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		// A shortcut points at a record OR holds a query, never both.
		if cmd.Flags().Changed("target-id") && cmd.Flags().Changed("query") {
			return errors.New("pass at most one of --target-id or --query (a shortcut points at a record or holds a query, not both)")
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "label", "label")
		addStringIfChanged(cmd, body, "target-id", "target_id")
		addStringIfChanged(cmd, body, "query", "saved_query")
		if cmd.Flags().Changed("position") {
			v, _ := cmd.Flags().GetFloat64("position")
			body["position"] = v
		}
		if len(body) == 0 {
			return errors.New("nothing to update — pass at least one field flag")
		}
		data, err := c.UpdateShortcut(args[0], body)
		if err != nil {
			return mapShortcutError(err)
		}
		printResult(data, displayShortcut)
		return nil
	},
}

// shortcutsDeleteCmd deletes (tombstones) a shortcut.
var shortcutsDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a shortcut",
	Args:    cobra.ExactArgs(1),
	Long:    "Tombstone a shortcut so the removal propagates via sync.",
	Example: "  harbor shortcuts delete 8d9e...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.DeleteShortcut(args[0]); err != nil {
			return mapShortcutError(err)
		}
		fmt.Println("Shortcut deleted.")
		return nil
	},
}

// shortcutsReorderCmd renumbers the whole shortcut list to the given order.
var shortcutsReorderCmd = &cobra.Command{
	Use:   "reorder",
	Short: "Reorder the whole shortcut list",
	Long: `Bulk-reorder shortcuts, renumbering to clean integer positions (100, 200, …).

--order must list EVERY live shortcut id exactly once, in the desired order. An
unknown, duplicated, or missing id is rejected (the full set must be supplied,
since a partial renumber would interleave unpredictably with the untouched rows).
Run 'harbor shortcuts list --json | jq -r ".data[].id"' to get the current ids.`,
	Example: `  harbor shortcuts reorder --order 8d9e...,2b3c...,f0e1...
  harbor shortcuts reorder --order "$(harbor shortcuts list --json | jq -r '[.data[].id] | join(",")')"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("order") {
			return errors.New("--order is required (the full ordered list of live shortcut ids)")
		}
		order := splitCSV(stringFlag(cmd, "order"))
		if len(order) == 0 {
			return errors.New("--order must contain at least one shortcut id")
		}
		data, err := c.ReorderShortcuts(order)
		if err != nil {
			return mapShortcutError(err)
		}
		printResult(data, displayShortcuts)
		return nil
	},
}

// shortcutValidateTypeFields enforces the type↔target consistency rules on the
// client so the user gets a clear error before any request is made: record
// types require --target-id (and forbid --query); search requires --query (and
// forbids --target-id).
func shortcutValidateTypeFields(typ, targetID, query string) error {
	switch typ {
	case "note", "notebook", "tag":
		if targetID == "" {
			return fmt.Errorf("--target-id is required for a %q shortcut", typ)
		}
		if query != "" {
			return fmt.Errorf("--query is not valid for a %q shortcut (use --target-id)", typ)
		}
	case "search":
		if query == "" {
			return errors.New("--query is required for a \"search\" shortcut")
		}
		if targetID != "" {
			return errors.New("--target-id is not valid for a \"search\" shortcut (use --query)")
		}
	case "":
		return errors.New("--type is required (one of note, notebook, tag, search)")
	default:
		return fmt.Errorf("invalid --type %q (must be one of note, notebook, tag, search)", typ)
	}
	return nil
}

// mapShortcutError gives friendlier messages for the shortcut-specific error
// codes returned by the API.
func mapShortcutError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "validation_failed":
			// The reorder endpoint reports duplicate/unknown/missing ids under
			// details.order; surface that pointed message when present.
			if msg, ok := apiErr.Details["order"]; ok {
				return fmt.Errorf("reorder rejected: %v (pass every live shortcut id exactly once)", msg)
			}
			if msg, ok := apiErr.Details["target_id"]; ok {
				return fmt.Errorf("invalid target: %v (it must be a live note, notebook, or tag)", msg)
			}
		case "conflict":
			return errors.New("a shortcut with that id already exists")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// shortcutTarget renders a shortcut's pointer column: the saved query for a
// search shortcut, otherwise the (shortened) target id.
func shortcutTarget(sc map[string]any) string {
	if str(sc, "type") == "search" {
		return truncate(str(sc, "saved_query"), 40)
	}
	return shortID(str(sc, "target_id"), 12)
}

// displayShortcuts renders a shortcut collection as a table.
func displayShortcuts(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "POS", "TYPE", "LABEL", "TARGET / QUERY", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		sc := parseJSON(raw)
		rows = append(rows, []string{
			str(sc, "id"),
			str(sc, "position"),
			str(sc, "type"),
			truncate(str(sc, "label"), 30),
			shortcutTarget(sc),
			dim(str(sc, "usn")),
			epochMS(num(sc, "updated_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayShortcut renders one shortcut as a key/value detail view.
func displayShortcut(data []byte) {
	sc := parseJSON(client.UnwrapData(data))
	if sc == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"ID", bold(str(sc, "id"))},
		{"Type", str(sc, "type")},
		{"Label", str(sc, "label")},
		{"Target ID", str(sc, "target_id")},
		{"Saved query", str(sc, "saved_query")},
		{"Position", str(sc, "position")},
		{"USN", str(sc, "usn")},
		{"Deleted", boolMark(boolean(sc, "deleted"))},
		{"Updated", epochMS(num(sc, "updated_at"))},
		{"Created", epochMS(num(sc, "created_at"))},
	})
}

func init() {
	addPagingFlags(shortcutsListCmd)
	shortcutsListCmd.Flags().Bool("include-deleted", false, "Include tombstoned shortcuts")

	shortcutsGetCmd.Flags().Bool("include-deleted", false, "Return the shortcut even if tombstoned")

	shortcutsCreateCmd.Flags().String("type", "", "Shortcut type: note | notebook | tag | search (required)")
	shortcutsCreateCmd.Flags().String("target-id", "", "Target record id (required for note/notebook/tag)")
	shortcutsCreateCmd.Flags().String("query", "", "Saved search query (required for type search)")
	shortcutsCreateCmd.Flags().String("label", "", "Optional display label")
	shortcutsCreateCmd.Flags().Float64("position", 0, "Explicit fractional position (appended when omitted)")

	shortcutsUpdateCmd.Flags().String("label", "", "New display label")
	shortcutsUpdateCmd.Flags().String("target-id", "", "New target record id (non-search shortcuts only)")
	shortcutsUpdateCmd.Flags().String("query", "", "New saved search query (search shortcuts only)")
	shortcutsUpdateCmd.Flags().Float64("position", 0, "New fractional position")

	shortcutsReorderCmd.Flags().String("order", "", "Comma-separated full ordered list of live shortcut ids (required)")

	shortcutsCmd.AddCommand(shortcutsListCmd, shortcutsGetCmd, shortcutsCreateCmd, shortcutsUpdateCmd, shortcutsDeleteCmd, shortcutsReorderCmd)
	rootCmd.AddCommand(shortcutsCmd)
}

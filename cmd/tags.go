// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// tagsCmd is the parent for tag management. It also hosts `tags notes`
// (registered in note_tags.go).
var tagsCmd = &cobra.Command{
	Use:     "tags",
	Aliases: []string{"tag"},
	Short:   "Manage tags (list, get, create, update, delete)",
	GroupID: groupOrg,
	Long:    "Hierarchical, Evernote-style nested tags. A tag's parent is set with --parent (or --top-level for a root tag).",
}

// tagsListCmd lists tags, optionally filtered by parent.
var tagsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tags",
	Long:  "List tags. With no parent filter, all tags are returned. Use --top-level for root tags only, or --parent <id> for the direct children of a tag.",
	Example: `  harbor tags list
  harbor tags list --top-level
  harbor tags list --parent 1f0b...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		q := url.Values{}
		if cmd.Flags().Changed("limit") {
			q.Set("limit", strconv.Itoa(intFlag(cmd, "limit")))
		}
		if cmd.Flags().Changed("offset") {
			q.Set("offset", strconv.Itoa(intFlag(cmd, "offset")))
		}
		if s := stringFlag(cmd, "order"); s != "" {
			q.Set("order", s)
		}
		// parent_id has three modes: absent, empty (top-level), or a value.
		if boolFlag(cmd, "top-level") {
			q.Set("parent_id", "")
		} else if p := stringFlag(cmd, "parent"); p != "" {
			q.Set("parent_id", p)
		}
		if boolFlag(cmd, "include-deleted") {
			q.Set("include_deleted", "true")
		}
		data, err := c.ListTags(q)
		if err != nil {
			return err
		}
		printResult(data, displayTags)
		return nil
	},
}

// tagsGetCmd fetches one tag.
var tagsGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a tag by id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetTag(args[0], boolFlag(cmd, "include-deleted"))
		if err != nil {
			return err
		}
		printResult(data, displayTag)
		return nil
	},
}

// tagsCreateCmd creates a tag.
var tagsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a tag",
	Example: `  harbor tags create --name Receipts
  harbor tags create --name 2026 --parent 1f0b...`,
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
		if p := stringFlag(cmd, "parent"); p != "" {
			body["parent_id"] = p
		}
		data, err := c.CreateTag(body)
		if err != nil {
			return mapTagError(err)
		}
		printResult(data, displayTag)
		return nil
	},
}

// tagsUpdateCmd renames and/or re-parents a tag.
var tagsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a tag (rename and/or re-parent)",
	Args:  cobra.ExactArgs(1),
	Example: `  harbor tags update 7a3c... --name "Receipts 2026"
  harbor tags update 7a3c... --top-level`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "name", "name")
		if boolFlag(cmd, "top-level") {
			body["parent_id"] = ""
		} else if cmd.Flags().Changed("parent") {
			body["parent_id"] = stringFlag(cmd, "parent")
		}
		if len(body) == 0 {
			return errors.New("nothing to update — pass --name, --parent, or --top-level")
		}
		data, err := c.UpdateTag(args[0], body)
		if err != nil {
			return mapTagError(err)
		}
		printResult(data, displayTag)
		return nil
	},
}

// tagsDeleteCmd tombstones a tag.
var tagsDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a tag",
	Args:    cobra.ExactArgs(1),
	Long:    "Tombstone a tag, untagging every note that carries it. Its children are reparented to its grandparent (default) or orphaned (--children orphan).",
	Example: "  harbor tags delete 7a3c... --children orphan",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.DeleteTag(args[0], stringFlag(cmd, "children")); err != nil {
			return mapTagError(err)
		}
		fmt.Println("Tag deleted.")
		return nil
	},
}

// mapTagError gives friendly messages for tag-specific codes.
func mapTagError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "tag_name_exists":
			return errors.New("a tag with that name already exists")
		case "tag_cycle":
			return errors.New("that change would create a tag cycle (a tag cannot be its own ancestor)")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayTags renders a tag collection as a table.
func displayTags(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "NAME", "PARENT", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		tg := parseJSON(raw)
		parent := str(tg, "parent_id")
		if parent == "" {
			parent = dim("(top-level)")
		} else {
			parent = shortID(parent, 8)
		}
		rows = append(rows, []string{
			str(tg, "id"),
			str(tg, "name"),
			parent,
			dim(str(tg, "usn")),
			epochMS(num(tg, "updated_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayTag renders one tag as a detail view.
func displayTag(data []byte) {
	tg := parseJSON(client.UnwrapData(data))
	if tg == nil {
		fmt.Println(string(data))
		return
	}
	parent := str(tg, "parent_id")
	if parent == "" {
		parent = dim("(top-level)")
	}
	printKV([][2]string{
		{"ID", bold(str(tg, "id"))},
		{"Name", str(tg, "name")},
		{"Parent", parent},
		{"USN", str(tg, "usn")},
		{"Deleted", boolMark(boolean(tg, "deleted"))},
		{"Updated", epochMS(num(tg, "updated_at"))},
		{"Created", epochMS(num(tg, "created_at"))},
	})
}

func init() {
	addPagingFlags(tagsListCmd)
	tagsListCmd.Flags().String("parent", "", "List the direct children of this tag id")
	tagsListCmd.Flags().Bool("top-level", false, "List only top-level (root) tags")
	tagsListCmd.Flags().Bool("include-deleted", false, "Include tombstoned tags")

	tagsGetCmd.Flags().Bool("include-deleted", false, "Return the tag even if tombstoned")

	tagsCreateCmd.Flags().String("name", "", "Tag name (required; no commas)")
	tagsCreateCmd.Flags().String("parent", "", "Parent tag id (omit for a top-level tag)")

	tagsUpdateCmd.Flags().String("name", "", "New name")
	tagsUpdateCmd.Flags().String("parent", "", "Re-parent under this tag id")
	tagsUpdateCmd.Flags().Bool("top-level", false, "Make this tag top-level")

	tagsDeleteCmd.Flags().String("children", "", "Child policy: reparent_to_grandparent (default) or orphan")

	tagsCmd.AddCommand(tagsListCmd, tagsGetCmd, tagsCreateCmd, tagsUpdateCmd, tagsDeleteCmd)
	rootCmd.AddCommand(tagsCmd)
}

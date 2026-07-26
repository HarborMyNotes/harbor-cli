// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// notesTagsCmd lists the tags attached to a note.
var notesTagsCmd = &cobra.Command{
	Use:     "tags <note-id>",
	Short:   "List the tags attached to a note",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor notes tags 9c2e...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListNoteTags(args[0], pagingParams(cmd))
		if err != nil {
			return err
		}
		printResult(data, displayTags)
		return nil
	},
}

// notesTagCmd attaches a tag to a note (by id or name).
var notesTagCmd = &cobra.Command{
	Use:   "tag <note-id>",
	Short: "Attach a tag to a note (by id or name)",
	Args:  cobra.ExactArgs(1),
	Long:  "Attach a tag to a note. Use --tag-id for an existing tag, or --tag-name to attach by name (creating the tag if it does not exist). Idempotent.",
	Example: `  harbor notes tag 9c2e... --tag-name Receipts
  harbor notes tag 9c2e... --tag-id 7a3c...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		tagID := stringFlag(cmd, "tag-id")
		tagName := stringFlag(cmd, "tag-name")
		if (tagID == "") == (tagName == "") {
			return errors.New("pass exactly one of --tag-id or --tag-name")
		}
		body := map[string]any{}
		if tagID != "" {
			body["tag_id"] = tagID
		} else {
			body["tag_name"] = tagName
		}
		data, created, err := c.AttachTag(args[0], body)
		if err != nil {
			return err
		}
		if jsonOutput {
			printResult(data, func([]byte) {})
			return nil
		}
		if created {
			fmt.Println("Tag attached.")
		} else {
			fmt.Println("Tag already attached (no change).")
		}
		return nil
	},
}

// notesSetTagsCmd replaces a note's complete tag set.
var notesSetTagsCmd = &cobra.Command{
	Use:   "set-tags <note-id>",
	Short: "Replace a note's complete tag set",
	Args:  cobra.ExactArgs(1),
	Long:  "Replace all tags on a note with the given comma-separated tag ids. Pass --tags \"\" to remove every tag.",
	Example: `  harbor notes set-tags 9c2e... --tags 7a3c...,1f0b...
  harbor notes set-tags 9c2e... --tags ""`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("tags") {
			return errors.New("--tags is required (use --tags \"\" to clear all tags)")
		}
		data, err := c.SetNoteTags(args[0], splitCSV(stringFlag(cmd, "tags")))
		if err != nil {
			return err
		}
		printResult(data, displayNoteTagJunctions)
		return nil
	},
}

// notesUntagCmd detaches a tag from a note.
var notesUntagCmd = &cobra.Command{
	Use:     "untag <note-id>",
	Short:   "Detach a tag from a note",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor notes untag 9c2e... --tag-id 7a3c...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		tagID := stringFlag(cmd, "tag-id")
		if tagID == "" {
			return errors.New("--tag-id is required")
		}
		if _, err := c.DetachTag(args[0], tagID); err != nil {
			return err
		}
		fmt.Println("Tag detached.")
		return nil
	},
}

// tagsNotesCmd lists the notes carrying a tag.
var tagsNotesCmd = &cobra.Command{
	Use:     "notes <tag-id>",
	Short:   "List the notes carrying a tag",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor tags notes 7a3c... --notebook 5b1f...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		if s := stringFlag(cmd, "notebook"); s != "" {
			params["notebook_id"] = s
		}
		data, err := c.ListTagNotes(args[0], params)
		if err != nil {
			return err
		}
		printResult(data, displayNotes)
		return nil
	},
}

// splitCSV splits a comma-separated flag value into a trimmed, non-empty slice.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// displayNoteTagJunctions renders the junction collection returned by set-tags.
func displayNoteTagJunctions(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"JUNCTION ID", "NOTE ID", "TAG ID", "USN"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		j := parseJSON(raw)
		rows = append(rows, []string{
			str(j, "id"),
			shortID(str(j, "note_id"), 8),
			str(j, "tag_id"),
			dim(str(j, "usn")),
		})
	}
	printTable(headers, rows)
	if len(rows) == 0 {
		fmt.Println(dim("Note has no tags."))
	}
}

func init() {
	addPagingFlags(notesTagsCmd)

	notesTagCmd.Flags().String("tag-id", "", "Existing tag id to attach")
	notesTagCmd.Flags().String("tag-name", "", "Tag name to attach (created if missing)")

	notesSetTagsCmd.Flags().String("tags", "", "Comma-separated tag ids (empty string clears all)")

	notesUntagCmd.Flags().String("tag-id", "", "Tag id to detach (required)")

	addPagingFlags(tagsNotesCmd)
	tagsNotesCmd.Flags().String("notebook", "", "Further filter to one notebook id")

	notesCmd.AddCommand(notesTagsCmd, notesTagCmd, notesSetTagsCmd, notesUntagCmd)
	tagsCmd.AddCommand(tagsNotesCmd)
}

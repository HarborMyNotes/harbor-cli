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

// historyCmd is the parent for a note's version-history commands. History is
// server-owned bookkeeping (it is not synced): every content/attribute change
// captures a deduped snapshot you can list, inspect, and revert to.
var historyCmd = &cobra.Command{
	Use:     "history",
	Aliases: []string{"hist"},
	Short:   "Browse and restore a note's version history",
	GroupID: groupContent,
	Long: `Inspect the snapshots captured each time a note's content or attributes
change. List the versions, show the full content of any one snapshot, or revert
a note to a past version (which is restored as a brand-new current version —
reverting is forward-only and never rewrites a past snapshot).

History is NOT permanent, though: encrypting or decrypting a note discards every
earlier version of it, server-side and unrecoverably. See 'harbor notes encrypt'.`,
}

// historyListCmd lists a note's version history (metadata only).
var historyListCmd = &cobra.Command{
	Use:   "list <note-id>",
	Short: "List a note's version history (newest first)",
	Args:  cobra.ExactArgs(1),
	Long: `List the snapshots captured for a note, newest first. This is a
metadata-only view (no bodies); use 'harbor history show' to read a snapshot's
content. History is readable even for a trashed note.`,
	Example: `  harbor history list 9c2e...
  harbor history list 9c2e... --order usn_at_snapshot --limit 50
  harbor history list 9c2e... --json | jq '.data[].id'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListNoteHistory(args[0], pagingParams(cmd))
		if err != nil {
			return mapHistoryError(err)
		}
		printResult(data, displayHistory)
		return nil
	},
}

// historyShowCmd fetches one snapshot and renders its content.
var historyShowCmd = &cobra.Command{
	Use:     "show <note-id> <version-id>",
	Aliases: []string{"get"},
	Short:   "Show a single version's full snapshot, including content",
	Args:    cobra.ExactArgs(2),
	Long: `Fetch one version's full snapshot — title, content, and attributes —
and render its body. HTML bodies are shown as readable text by default; pass
--format html to see the raw HTML. Encrypted snapshots show a placeholder (the
server never holds the plaintext).`,
	Example: `  harbor history show 9c2e... 7b3e...
  harbor history show 9c2e... 7b3e... --format html
  harbor history show 9c2e... 7b3e... --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetNoteHistoryVersion(args[0], args[1])
		if err != nil {
			return mapHistoryError(err)
		}
		format := stringFlag(cmd, "format")
		printResult(data, func(d []byte) { displayHistoryVersion(d, format) })
		return nil
	},
}

// historyRevertCmd restores a past version as a new current version.
var historyRevertCmd = &cobra.Command{
	Use:   "revert <note-id> <version-id>",
	Short: "Restore a past version as a new current version",
	Args:  cobra.ExactArgs(2),
	Long: `Restore a past version's title and content onto the live note as a
brand-new current version. A fresh USN is allocated so the revert syncs like a
normal edit, and a new snapshot of the reverted content is captured. The note
must be live — restore it from the trash first if it is trashed.`,
	Example: `  harbor history revert 9c2e... 7b3e...
  harbor history revert 9c2e... 7b3e... --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.RevertNoteHistoryVersion(args[0], args[1])
		if err != nil {
			return mapHistoryError(err)
		}
		printResult(data, displayHistoryRevert)
		return nil
	},
}

// mapHistoryError gives friendly messages for history-specific error codes.
func mapHistoryError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "note_in_trash":
			return errors.New("the note is in the trash — restore it from trash first, then revert")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayHistory renders a version-history collection as a metadata table.
func displayHistory(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"VERSION", "USN@SNAP", "🔒", "HASH", "DEVICE", "CREATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		v := parseJSON(raw)
		rows = append(rows, []string{
			str(v, "id"),
			dim(str(v, "usn_at_snapshot")),
			lockMark(boolean(v, "is_encrypted")),
			shortID(str(v, "content_hash"), 12),
			historyDevice(str(v, "source_device")),
			epochMS(num(v, "created_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayHistoryVersion renders one snapshot as a detail view plus its body.
// The body is rendered per format: with "html" the raw HTML is printed,
// otherwise HTML is stripped to readable text. Encrypted snapshots show a
// placeholder.
func displayHistoryVersion(data []byte, format string) {
	v := parseJSON(client.UnwrapData(data))
	if v == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"Version", bold(str(v, "id"))},
		{"Note", str(v, "note_id")},
		{"Title", str(v, "title")},
		{"USN at snapshot", str(v, "usn_at_snapshot")},
		{"Encrypted", boolMark(boolean(v, "is_encrypted"))},
		{"Content hash", str(v, "content_hash")},
		{"Source device", historyDevice(str(v, "source_device"))},
		{"Created", epochMS(num(v, "created_at"))},
	})

	fmt.Println()
	if boolean(v, "is_encrypted") {
		fmt.Println(dim("[encrypted]"))
		return
	}
	body := str(v, "content")
	if format != "html" && strings.Contains(body, "<") && strings.Contains(body, ">") {
		body = stripHTML(body)
	}
	if body != "" {
		fmt.Println(body)
	}
}

// displayHistoryRevert renders the {note, usn} envelope returned by a revert as
// a compact detail view confirming the restored note and its new USN. It does
// not print the body (the snapshot's content is available via 'history show').
func displayHistoryRevert(data []byte) {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		fmt.Println(string(data))
		return
	}
	n := nested(root, "note")
	usn := str(root, "usn")
	if n == nil {
		n = root
	}
	pairs := [][2]string{
		{"ID", bold(str(n, "id"))},
		{"Title", str(n, "title")},
		{"Notebook", str(n, "notebook_id")},
		{"Encrypted", boolMark(boolean(n, "is_encrypted"))},
		{"USN", str(n, "usn")},
		{"Updated", epochMS(num(n, "updated_at"))},
	}
	if usn != "" {
		pairs = append(pairs, [2]string{"New USN", bold(usn)})
	}
	printKV(pairs)
	fmt.Println()
	fmt.Println("Reverted — the note now matches this version (restored as a new current version).")
}

// historyDevice renders the snapshot's source device, marking server-side or
// local edits (which carry no device id) as a dim placeholder.
func historyDevice(device string) string {
	if device == "" {
		return dim("—")
	}
	return device
}

func init() {
	addPagingFlags(historyListCmd)

	historyShowCmd.Flags().String("format", "markdown", "Body rendering: markdown (strip HTML to text) or html (raw)")

	historyCmd.AddCommand(historyListCmd, historyShowCmd, historyRevertCmd)
	rootCmd.AddCommand(historyCmd)
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// auditActions is the fixed set of audit event actions the API accepts as the
// --action filter (and emits in the action column).
var auditActions = []string{"create", "update", "append", "delete", "restore", "tag", "move", "share"}

// auditOrderFields is the set of sortable fields the audit endpoint accepts,
// each optionally prefixed with "-" for descending.
var auditOrderFields = map[string]bool{"created_at": true, "usn": true}

// ===========================================================================
// Commands
// ===========================================================================

// notesLinksCmd lists a note's outgoing harbor:note links (the edges its body
// contains), flagging broken targets and summarizing each resolved target.
var notesLinksCmd = &cobra.Command{
	Use:   "links <note-id>",
	Short: "List a note's outgoing links",
	Args:  cobra.ExactArgs(1),
	Long: `List the notes a note links TO — the harbor:note anchors in its body.

Links are derived by the server from note content and are read-only. An edge is
BROKEN when its target no longer resolves (never existed or was permanently
expunged); a merely trashed target is not broken and is shown with IN-TRASH set.`,
	Example: `  harbor notes links 9c2e...
  harbor notes links 9c2e... --limit 50 --offset 50
  harbor notes links 9c2e... --json | jq '.data[] | select(.broken)'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load an authenticated client (notes scope required).
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		// Fetch the outgoing edges with the standard paging params.
		data, err := c.ListNoteLinks(args[0], pagingParams(cmd))
		if err != nil {
			return mapInsightError(err)
		}
		printResult(data, displayNoteLinks)
		return nil
	},
}

// notesBacklinksCmd lists the live notes that link TO a note — its incoming
// edges. Trashed and expunged source notes are excluded by the server.
var notesBacklinksCmd = &cobra.Command{
	Use:   "backlinks <note-id>",
	Short: "List the notes that link to a note",
	Args:  cobra.ExactArgs(1),
	Long: `List "what links here" — the live notes whose body links TO this note.

Only LIVE source notes are listed; a source in the trash (or expunged) is not
shown, since a note in the bin should not appear as an active referrer.`,
	Example: `  harbor notes backlinks 9c2e...
  harbor notes backlinks 9c2e... --limit 100
  harbor notes backlinks 9c2e... --json | jq '.data[].source_note_id'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load an authenticated client (notes scope required).
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		// Fetch the incoming edges with the standard paging params.
		data, err := c.ListNoteBacklinks(args[0], pagingParams(cmd))
		if err != nil {
			return mapInsightError(err)
		}
		printResult(data, displayNoteBacklinks)
		return nil
	},
}

// notesAuditCmd lists a note's append-only change log (what happened, by which
// device, the note's USN at the time, a small summary, and when).
var notesAuditCmd = &cobra.Command{
	Use:   "audit <note-id>",
	Short: "Show a note's change history (audit log)",
	Args:  cobra.ExactArgs(1),
	Long: `Show a note's audit log: a lightweight, append-only record of what happened
to it (create, update, append, delete, restore, tag, move, share), by which
device, at what USN, and when. The metadata column is a short summary and never
contains note text.

The trail outlives the note — a trashed or expunged note still lists its events.
Filter to one action with --action, and sort with --order (created_at or usn,
prefix - for descending; default -created_at = newest first).`,
	Example: `  harbor notes audit 9c2e...
  harbor notes audit 9c2e... --action delete
  harbor notes audit 9c2e... --order usn --limit 200
  harbor notes audit 9c2e... --json | jq '.data[] | {action, usn}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load an authenticated client (notes scope required).
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		// Build the query params, validating the action filter and sort order
		// up front so the user gets a friendly message before any round-trip.
		params, err := notesAuditParams(cmd)
		if err != nil {
			return err
		}
		data, err := c.ListNoteAudit(args[0], params)
		if err != nil {
			return mapInsightError(err)
		}
		printResult(data, displayNoteAudit)
		return nil
	},
}

// ===========================================================================
// Helpers
// ===========================================================================

// notesAuditParams assembles the audit query map from the command flags. It
// validates --action against the fixed action set and --order against the
// sortable fields, returning a friendly error for either before any request is
// sent. Unset flags are omitted so the server defaults apply.
func notesAuditParams(cmd *cobra.Command) (map[string]string, error) {
	params := map[string]string{}

	// Standard offset/limit paging (omitted when unset → server defaults).
	if cmd.Flags().Changed("limit") {
		params["limit"] = strconv.Itoa(intFlag(cmd, "limit"))
	}
	if cmd.Flags().Changed("offset") {
		params["offset"] = strconv.Itoa(intFlag(cmd, "offset"))
	}

	// Optional single-action filter, validated against the known set.
	if cmd.Flags().Changed("action") {
		action := stringFlag(cmd, "action")
		if !auditContains(auditActions, action) {
			return nil, fmt.Errorf("invalid --action %q (one of: %s)", action, strings.Join(auditActions, ", "))
		}
		params["action"] = action
	}

	// Optional sort order, validated (the leading "-" means descending).
	if cmd.Flags().Changed("order") {
		order := stringFlag(cmd, "order")
		field := strings.TrimPrefix(order, "-")
		if !auditOrderFields[field] {
			return nil, fmt.Errorf("invalid --order %q (sortable fields: created_at, usn; prefix - for descending)", order)
		}
		params["order"] = order
	}

	return params, nil
}

// auditContains reports whether slice s holds the value v. Domain-prefixed to
// avoid colliding with helpers from other concurrently-built command domains;
// used only for the audit action whitelist.
func auditContains(s []string, v string) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

// mapInsightError gives friendly messages for the codes these read-only
// endpoints can return (a missing note, or an unknown audit sort field).
func mapInsightError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "not_found":
			return errors.New("note not found (it may have been permanently deleted)")
		case "validation_failed":
			return errors.New("invalid query — check --order and --action")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayNoteLinks renders a note's outgoing links as a table: the target id,
// whether the edge is broken, and the resolved target's title and trash flag.
func displayNoteLinks(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"TARGET-ID", "BROKEN", "TARGET-TITLE", "IN-TRASH"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		link := parseJSON(raw)
		broken := boolean(link, "broken")
		// The target summary is null for a broken/dangling edge.
		target := nested(link, "target")
		title := "—"
		inTrash := dim("—")
		if target != nil {
			title = truncate(str(target, "title"), 40)
			inTrash = boolMark(boolean(target, "in_trash"))
		}
		rows = append(rows, []string{
			str(link, "target_note_id"),
			boolMark(broken),
			title,
			inTrash,
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayNoteBacklinks renders a note's incoming links as a table: the live
// source note's id and title.
func displayNoteBacklinks(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"SOURCE-ID", "SOURCE-TITLE"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		link := parseJSON(raw)
		// The source summary describes the live referring note.
		source := nested(link, "source")
		title := "—"
		if source != nil {
			title = truncate(str(source, "title"), 50)
		}
		rows = append(rows, []string{
			str(link, "source_note_id"),
			title,
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayNoteAudit renders a note's audit log as a table: the action, actor
// device, the anchoring USN, a short metadata summary, and when it happened.
func displayNoteAudit(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ACTION", "DEVICE", "USN", "METADATA", "CREATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		ev := parseJSON(raw)
		rows = append(rows, []string{
			colorizeStatus(str(ev, "action")),
			str(ev, "device_id"),
			dim(str(ev, "usn")),
			auditMetaSummary(ev["metadata"]),
			epochMS(num(ev, "created_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// auditMetaSummary renders the small, action-specific metadata object as a
// compact "key=value" summary for the table. It is null for events that record
// no metadata (shown as a dim dash) and never contains note text. Keys are
// sorted for deterministic output; arrays are rendered as their length and
// nested objects are flattened to a count.
func auditMetaSummary(meta any) string {
	if meta == nil {
		return dim("·")
	}
	m, ok := meta.(map[string]any)
	if !ok || len(m) == 0 {
		return dim("·")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+auditMetaValue(m[k]))
	}
	return truncate(strings.Join(parts, " "), 48)
}

// auditMetaValue renders a single metadata value compactly: scalars as text,
// arrays as their element count, and nested objects as a field count.
func auditMetaValue(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		return trimFloat(val)
	case []any:
		return fmt.Sprintf("[%d]", len(val))
	case map[string]any:
		return fmt.Sprintf("{%d}", len(val))
	default:
		return fmt.Sprintf("%v", val)
	}
}

func init() {
	// links / backlinks use the standard paging flags (limit/offset/order).
	addPagingFlags(notesLinksCmd)
	addPagingFlags(notesBacklinksCmd)

	// audit registers limit/offset manually plus a tailored --order (with the
	// server's newest-first default surfaced) and an --action filter.
	notesAuditCmd.Flags().Int("limit", 0, "Maximum results to return (default 100, cap 500)")
	notesAuditCmd.Flags().Int("offset", 0, "Number of results to skip")
	notesAuditCmd.Flags().String("order", "", "Sort order: created_at or usn (- = descending; default -created_at)")
	notesAuditCmd.Flags().String("action", "", "Filter to one action: "+strings.Join(auditActions, ", "))

	// Attach all three as subcommands of the existing notes command.
	notesCmd.AddCommand(notesLinksCmd, notesBacklinksCmd, notesAuditCmd)
}

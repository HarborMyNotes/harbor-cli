// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// remindersCmd is the parent for note-reminder commands. A reminder is a field
// on a note (its reminder_time / reminder_done_time), not a separate record, so
// every mutation here returns the materialized note plus its new USN.
var remindersCmd = &cobra.Command{
	Use:     "reminders",
	Aliases: []string{"reminder", "rem"},
	Short:   "List, set, complete, and clear note reminders",
	GroupID: groupContent,
	Long: `Work with note reminders. A reminder is simply a due time stored on a
note, so setting, completing, or clearing one is a normal note change: it
allocates a fresh USN and syncs to every device.

Times accept epoch milliseconds, an RFC3339 timestamp, a plain date
(YYYY-MM-DD), or a relative offset like "in 2h" — all are converted to UTC
epoch-ms before being sent.`,
}

// remindersListCmd lists notes that currently have a reminder.
var remindersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notes that have reminders",
	Long: `List notes whose reminder is set. By default only active (not-yet-completed)
reminders are shown; use --status to include done or all reminders. Use
--due-before to narrow to reminders due at or before a moment (an overdue view).`,
	Example: `  harbor reminders list
  harbor reminders list --status all --order -updated_at
  harbor reminders list --due-before "in 2h"
  harbor reminders list --status done --json | jq '.data[] | {id, title}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		if s := stringFlag(cmd, "status"); s != "" {
			params["status"] = s
		}
		// --due-before accepts any human time form; the API wants epoch-ms.
		if cmd.Flags().Changed("due-before") {
			ms, perr := parseTimeToEpochMS(stringFlag(cmd, "due-before"))
			if perr != nil {
				return fmt.Errorf("invalid --due-before: %w", perr)
			}
			params["due_before"] = fmt.Sprintf("%d", ms)
		}
		data, err := c.ListReminders(params)
		if err != nil {
			return mapReminderError(err)
		}
		data = decryptResult(c, creds, data)
		printResult(data, displayReminders)
		return nil
	},
}

// remindersSetCmd sets or updates a note's reminder time.
var remindersSetCmd = &cobra.Command{
	Use:   "set <note-id>",
	Short: "Set or update a note's reminder time",
	Args:  cobra.ExactArgs(1),
	Long: `Set (or change) the reminder on a note. Setting a time does not clear an
existing completion — re-arm a completed reminder by clearing it first.`,
	Example: `  harbor reminders set 9c2e... --time "in 2h"
  harbor reminders set 9c2e... --time 2026-07-01
  harbor reminders set 9c2e... --time 1750100000000`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		ms, err := reminderTimeFlagToMS(cmd, "time", true)
		if err != nil {
			return err
		}
		data, err := c.SetReminder(args[0], ms)
		if err != nil {
			return mapReminderError(err)
		}
		printResult(data, displayReminderMutation)
		return nil
	},
}

// remindersCompleteCmd marks a note's reminder done.
var remindersCompleteCmd = &cobra.Command{
	Use:   "complete <note-id>",
	Short: "Mark a note's reminder as done",
	Args:  cobra.ExactArgs(1),
	Long: `Mark a note's reminder completed. The reminder stays on the note (listed
under "done"). Pass --time to record a specific completion moment; otherwise the
server uses the current time. The note must already have a reminder set.`,
	Example: `  harbor reminders complete 9c2e...
  harbor reminders complete 9c2e... --time "2026-06-22T09:00:00Z"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		var body map[string]any
		// --time is optional here; when given it becomes done_time (epoch-ms).
		if cmd.Flags().Changed("time") {
			ms, terr := reminderTimeFlagToMS(cmd, "time", true)
			if terr != nil {
				return terr
			}
			body = map[string]any{"done_time": ms}
		}
		data, err := c.CompleteReminder(args[0], body)
		if err != nil {
			return mapReminderError(err)
		}
		printResult(data, displayReminderMutation)
		return nil
	},
}

// remindersClearCmd removes a note's reminder entirely.
var remindersClearCmd = &cobra.Command{
	Use:     "clear <note-id>",
	Short:   "Remove a note's reminder",
	Args:    cobra.ExactArgs(1),
	Long:    "Remove a note's reminder (clears both the due time and any completion). Idempotent — clearing a note with no reminder still succeeds.",
	Example: "  harbor reminders clear 9c2e...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ClearReminder(args[0])
		if err != nil {
			return mapReminderError(err)
		}
		printResult(data, displayReminderMutation)
		return nil
	},
}

// reminderTimeFlagToMS reads a time-shaped flag and converts it to UTC epoch
// milliseconds via the shared parser. When required is true an unset/empty flag
// is an error; the conversion itself maps any human form (epoch-ms, RFC3339,
// YYYY-MM-DD, or "in 2h") to epoch-ms so the API only ever receives epoch-ms.
func reminderTimeFlagToMS(cmd *cobra.Command, flag string, required bool) (int64, error) {
	s := stringFlag(cmd, flag)
	if s == "" {
		if required {
			return 0, fmt.Errorf("--%s is required (epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")", flag)
		}
		return 0, nil
	}
	ms, err := parseTimeToEpochMS(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s: %w", flag, err)
	}
	return ms, nil
}

// mapReminderError gives friendly messages for reminder-specific error codes.
func mapReminderError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "note_in_trash":
			return errors.New("that note is in the trash — restore it first (harbor trash restore <id>) before changing its reminder")
		case "not_a_reminder":
			return errors.New("that note has no reminder set, so it cannot be completed — set one first with 'harbor reminders set <note-id>'")
		case "not_found":
			return errors.New("no such note (it may have been deleted)")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayReminders renders a collection of reminder notes as a table: title,
// the due time as a human timestamp plus a relative hint, completion state, and
// the note's USN/id. Encrypted titles arrive as ciphertext (the times are not
// secret) and are flagged with the lock glyph.
func displayReminders(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "TITLE", "🔒", "DUE", "WHEN", "DONE", "USN"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		n := parseJSON(raw)
		encrypted := boolean(n, "is_encrypted")
		title := str(n, "title")
		if encrypted {
			title = dim("[encrypted]")
		} else {
			title = truncate(title, 40)
		}
		rows = append(rows, []string{
			shortID(str(n, "id"), 8),
			title,
			lockMark(encrypted),
			epochMS(num(n, "reminder_time")),
			relTime(num(n, "reminder_time")),
			reminderDoneMark(n),
			dim(str(n, "usn")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// reminderDoneMark renders a reminder's completion state. A reminder counts as
// done when reminder_done_time is set (non-zero); done rows also show when.
func reminderDoneMark(n map[string]any) string {
	if num(n, "reminder_done_time") != 0 {
		return colorizeStatus("done") + " " + dim(relTime(num(n, "reminder_done_time")))
	}
	return dim("·")
}

// displayReminderMutation renders the {note, usn} envelope returned by set /
// complete / clear: it surfaces the note id, its (possibly cleared) reminder
// state, and the freshly-allocated USN. Kept local so the reminders domain does
// not couple to notes.go's displayNote.
func displayReminderMutation(data []byte) {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		fmt.Println(string(data))
		return
	}
	n := nested(root, "note")
	usn := str(root, "usn")
	if n == nil {
		// Unexpected shape: fall back to treating the body as the note itself.
		n = root
	}
	reminderTime := num(n, "reminder_time")
	pairs := [][2]string{
		{"ID", bold(str(n, "id"))},
		{"Title", reminderTitle(n)},
	}
	if reminderTime != 0 {
		pairs = append(pairs,
			[2]string{"Reminder", epochMS(reminderTime) + dim(" ("+relTime(reminderTime)+")")},
			[2]string{"Done", reminderDoneDetail(n)},
		)
	} else {
		pairs = append(pairs, [2]string{"Reminder", dim("(none)")})
	}
	pairs = append(pairs, [2]string{"USN", str(n, "usn")})
	if usn != "" {
		pairs = append(pairs, [2]string{"New USN", bold(usn)})
	}
	printKV(pairs)
}

// reminderTitle returns a note's title for the detail view, hiding the
// ciphertext of an encrypted note behind a placeholder.
func reminderTitle(n map[string]any) string {
	if boolean(n, "is_encrypted") {
		return dim("[encrypted]")
	}
	return str(n, "title")
}

// reminderDoneDetail renders the completion line for the detail view: the
// completion time when done, otherwise a dim "not done".
func reminderDoneDetail(n map[string]any) string {
	if dt := num(n, "reminder_done_time"); dt != 0 {
		return colorizeStatus("done") + " " + epochMS(dt) + dim(" ("+relTime(dt)+")")
	}
	return dim("not done")
}

func init() {
	addPagingFlags(remindersListCmd)
	remindersListCmd.Flags().String("status", "", "Which reminders to list: active (default), done, or all")
	remindersListCmd.Flags().String("due-before", "", "Only reminders due at or before this time (epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")

	remindersSetCmd.Flags().String("time", "", "When the reminder is due (epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")

	remindersCompleteCmd.Flags().String("time", "", "Completion time (defaults to now; epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")

	remindersCmd.AddCommand(remindersListCmd, remindersSetCmd, remindersCompleteCmd, remindersClearCmd)
	rootCmd.AddCommand(remindersCmd)
}

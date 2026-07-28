// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-28

package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/spf13/cobra"
)

// tasksCmd is the parent for task management. A task is a first-class syncable
// record (not a field on a note), so every mutation allocates a fresh USN and
// propagates to every device — the same contract notes and notebooks use.
var tasksCmd = &cobra.Command{
	Use:     "tasks",
	Aliases: []string{"task"},
	Short:   "Manage tasks (list, get, create, update, done, undone, delete)",
	GroupID: groupContent,
	Long: `Work with Harbor tasks. A task is a standalone to-do — a title plus an
optional due date, reminder, recurrence rule, priority and flag — that syncs to
every device like a note does.

Times accept epoch milliseconds, an RFC3339 timestamp, a plain date
(YYYY-MM-DD), or a relative offset like "in 2h". A plain date is treated as
date-only: the task shows a day with no time (see --due-has-time).

Priorities are none, low, medium, or high.

Recurrence rules are daily, weekly, monthly, yearly, every:N:days|weeks|months|
years, or an RFC 5545 RRULE (e.g. FREQ=WEEKLY;BYDAY=FR). Completing a recurring
task rolls it forward to its next occurrence instead of closing it.`,
}

// ===========================================================================
// Attaching a task to a note (deliberately absent)
// ===========================================================================
//
// There is NO --note flag on `create` or `update`, and nothing below ever sends
// note_id. That is not an oversight.
//
// A note's body is authoritative for its tasks: the server claims tasks
// referenced by the note's <harbor-task id="…"> blocks and tombstones any
// linked task whose block is missing. Setting note_id without also writing the
// block therefore leaves a task linked-but-unreferenced — and the note's very
// next save silently deletes it.
//
// Attaching is now done through POST /api/v1/notes/:id/tasks, which writes the
// block and the link in one transaction (HarborMyNotes/app.harbor.my#1094). A
// `harbor tasks attach` command belongs on that endpoint and is tracked
// separately; it is out of scope here.
//
// `list --note <note-id>` below is a different thing entirely: it READS
// GET /notes/:id/tasks and changes nothing.

// tasksListCmd lists tasks, either across the account or within one note.
var tasksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks",
	Long: `List tasks. By default only active (not-yet-completed) tasks are shown;
use --status to switch to today, done, or all. Use --due-before for an
overdue/upcoming view.

With --note, the tasks belonging to that note are returned instead, whole and in
their in-note order — the account-wide status and due filters do not apply
there. This is a read; it cannot move a task between notes.

Sort keys for --order are due, priority, created, updated, position, and usn
(prefix with - for descending). Within a note: position, created, updated, due,
and usn. Anything else is rejected by the server.`,
	Example: `  harbor tasks list
  harbor tasks list --status today
  harbor tasks list --status all --order -priority
  harbor tasks list --due-before "in 24h"
  harbor tasks list --note 9c2e...
  harbor tasks list --status done --json | jq '.data[] | {id, title}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)

		// --note reads the note's own task list (GET /notes/:id/tasks). That
		// endpoint returns the note's tasks whole, so the account-wide filters
		// have nowhere to apply — say so rather than silently dropping them.
		if noteID := stringFlag(cmd, "note"); noteID != "" {
			if verr := validateTaskNoteFilters(cmd); verr != nil {
				return verr
			}
			data, lerr := c.ListNoteTasks(noteID, params)
			if lerr != nil {
				return mapTaskError(lerr)
			}
			printResult(data, displayTasks)
			return nil
		}

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
		data, err := c.ListTasks(params)
		if err != nil {
			return mapTaskError(err)
		}
		printResult(data, displayTasks)
		return nil
	},
}

// tasksGetCmd fetches a single task by id.
var tasksGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Get a task by id",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor tasks get 3f1a2b7c-...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetTask(args[0])
		if err != nil {
			return mapTaskError(err)
		}
		printResult(data, displayTask)
		return nil
	},
}

// tasksCreateCmd creates a standalone task. It never sends note_id — see the
// "Attaching a task to a note" block above for why.
var tasksCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a task",
	Long: `Create a standalone task. Only --title is required.

A --due given as a plain date (2026-08-01) is stored date-only, so clients show
the day without a time. A --due carrying a time (RFC3339, epoch-ms, or "in 2h")
is stored timed. Pass --due-has-time explicitly to override that.

Tasks cannot be attached to a note here; a note's body owns its tasks.`,
	Example: `  harbor tasks create --title "Pay the invoice" --due 2026-08-01
  harbor tasks create --title "Standup" --due "in 2h" --recurrence weekly
  harbor tasks create --title "File taxes" --due 2026-04-15 --priority high --flag`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		title := stringFlag(cmd, "title")
		if title == "" {
			return errors.New("--title is required")
		}
		body := map[string]any{"title": title}
		if err := addTaskDueToBody(cmd, body); err != nil {
			return err
		}
		if err := addTaskReminderToBody(cmd, body); err != nil {
			return err
		}
		addStringIfChanged(cmd, body, "recurrence", "recurrence")
		addStringIfChanged(cmd, body, "priority", "priority")
		addBoolIfChanged(cmd, body, "flag", "flag")
		addIntIfChanged(cmd, body, "position", "position")

		data, err := c.CreateTask(body)
		if err != nil {
			return mapTaskError(err)
		}
		printResult(data, displayTask)
		return nil
	},
}

// tasksUpdateCmd partially updates a task: only the flags passed are changed.
// Like create, it never sends note_id — the server rejects it outright.
var tasksUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a task (only the flags you pass are changed)",
	Args:  cobra.ExactArgs(1),
	Long: `Update a task. Only the fields you pass are modified.

Use --clear-due, --clear-reminder, or --clear-recurrence to remove a value
rather than change it. --due-has-time toggles a due date between date-only and
date+time without moving it.

Completion is not set here — use 'harbor tasks done' / 'harbor tasks undone' so
recurring tasks advance correctly. A task cannot be moved between notes.`,
	Example: `  harbor tasks update 3f1a... --title "Pay the invoice (final notice)"
  harbor tasks update 3f1a... --due 2026-08-05 --priority high
  harbor tasks update 3f1a... --due-has-time=false
  harbor tasks update 3f1a... --clear-due --clear-recurrence`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if verr := validateTaskClearFlags(cmd); verr != nil {
			return verr
		}

		body := map[string]any{}
		addStringIfChanged(cmd, body, "title", "title")
		if err := addTaskDueToBody(cmd, body); err != nil {
			return err
		}
		if boolFlag(cmd, "clear-due") {
			body["clear_due_at"] = true
		}
		if err := addTaskReminderToBody(cmd, body); err != nil {
			return err
		}
		if boolFlag(cmd, "clear-reminder") {
			body["clear_reminder_at"] = true
		}
		addStringIfChanged(cmd, body, "recurrence", "recurrence")
		if boolFlag(cmd, "clear-recurrence") {
			body["clear_recurrence"] = true
		}
		addStringIfChanged(cmd, body, "priority", "priority")
		addBoolIfChanged(cmd, body, "flag", "flag")
		addIntIfChanged(cmd, body, "position", "position")

		if len(body) == 0 {
			return errors.New("nothing to update — pass at least one field flag")
		}
		data, err := c.UpdateTask(args[0], body)
		if err != nil {
			return mapTaskError(err)
		}
		printResult(data, displayTask)
		return nil
	},
}

// tasksDoneCmd completes a task — or, for a recurring one, advances it.
var tasksDoneCmd = &cobra.Command{
	Use:   "done <id>",
	Short: "Complete a task (a recurring task advances instead)",
	Args:  cobra.ExactArgs(1),
	Long: `Mark a task done. Pass --time to record a specific completion moment;
otherwise the server uses the current time.

A RECURRING task is not closed by this: it rolls forward to its next occurrence
(its due date and reminder move, and it stays open). The output says which
happened.`,
	Example: `  harbor tasks done 3f1a...
  harbor tasks done 3f1a... --time "2026-07-28T09:00:00Z"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		var body map[string]any
		// --time is optional; when given it becomes done_time (epoch-ms).
		if cmd.Flags().Changed("time") {
			ms, terr := parseTimeToEpochMS(stringFlag(cmd, "time"))
			if terr != nil {
				return fmt.Errorf("invalid --time: %w", terr)
			}
			body = map[string]any{"done_time": ms}
		}
		data, err := c.CompleteTask(args[0], body)
		if err != nil {
			return mapTaskError(err)
		}
		printResult(data, displayTaskCompletion)
		return nil
	},
}

// tasksUndoneCmd reopens a completed task.
var tasksUndoneCmd = &cobra.Command{
	Use:     "undone <id>",
	Aliases: []string{"reopen"},
	Short:   "Reopen a completed task",
	Args:    cobra.ExactArgs(1),
	Long:    "Clear a task's completion so it is active again. Idempotent, and it does not rewind a recurring task that has already advanced.",
	Example: "  harbor tasks undone 3f1a...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.UncompleteTask(args[0])
		if err != nil {
			return mapTaskError(err)
		}
		printResult(data, displayTask)
		return nil
	},
}

// tasksDeleteCmd deletes (tombstones) a task.
var tasksDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete a task",
	Args:    cobra.ExactArgs(1),
	Long:    "Tombstone a task so the deletion syncs to every device. If the task belongs to a note, its block is removed from that note's body too.",
	Example: "  harbor tasks delete 3f1a...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.DeleteTask(args[0]); err != nil {
			return mapTaskError(err)
		}
		fmt.Println("Task deleted.")
		return nil
	},
}

// ===========================================================================
// Flag → body helpers
// ===========================================================================

// parseTaskDue converts a --due value to UTC epoch milliseconds and reports
// whether the input carried a TIME. A bare YYYY-MM-DD is date-only (false), so
// the CLI can send due_has_time=false and the task renders as a day rather than
// a spurious midnight. Every other accepted form (epoch-ms, RFC3339, "in 2h")
// names a moment and is timed.
func parseTaskDue(s string) (int64, bool, error) {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(s)); err == nil {
		return t.UTC().UnixMilli(), false, nil
	}
	ms, err := parseTimeToEpochMS(s)
	return ms, true, err
}

// addTaskDueToBody copies --due into body as due_at plus the due_has_time flag
// implied by its shape, then lets an explicit --due-has-time override that. The
// server defaults an absent due_has_time to true, so sending it is what keeps a
// date-only due from becoming a timed one.
func addTaskDueToBody(cmd *cobra.Command, body map[string]any) error {
	if cmd.Flags().Changed("due") {
		ms, hasTime, err := parseTaskDue(stringFlag(cmd, "due"))
		if err != nil {
			return fmt.Errorf("invalid --due: %w", err)
		}
		body["due_at"] = ms
		body["due_has_time"] = hasTime
	}
	addBoolIfChanged(cmd, body, "due-has-time", "due_has_time")
	return nil
}

// addTaskReminderToBody copies --reminder into body as reminder_at (epoch-ms).
// A reminder is always a moment, so it has no date-only variant.
func addTaskReminderToBody(cmd *cobra.Command, body map[string]any) error {
	if !cmd.Flags().Changed("reminder") {
		return nil
	}
	ms, err := parseTimeToEpochMS(stringFlag(cmd, "reminder"))
	if err != nil {
		return fmt.Errorf("invalid --reminder: %w", err)
	}
	body["reminder_at"] = ms
	return nil
}

// validateTaskNoteFilters rejects the account-wide list filters when --note is
// in play. The per-note endpoint returns a note's tasks whole and in note
// order, so status/due filtering has nowhere to happen — silently dropping the
// flags would hand back a list that quietly ignores what was asked for.
func validateTaskNoteFilters(cmd *cobra.Command) error {
	for _, f := range []string{"status", "due-before"} {
		if cmd.Flags().Changed(f) {
			return fmt.Errorf("--%s cannot be combined with --note: a note's task list is returned whole, in note order", f)
		}
	}
	return nil
}

// validateTaskClearFlags rejects setting and clearing the same field in one
// update. The server resolves that contradiction by letting the set win, which
// makes a --clear-due that did nothing look like it worked.
func validateTaskClearFlags(cmd *cobra.Command) error {
	for _, pair := range [][2]string{
		{"due", "clear-due"},
		{"reminder", "clear-reminder"},
		{"recurrence", "clear-recurrence"},
	} {
		if cmd.Flags().Changed(pair[0]) && boolFlag(cmd, pair[1]) {
			return fmt.Errorf("--%s and --%s cannot be used together", pair[0], pair[1])
		}
	}
	return nil
}

// addTaskFieldFlags registers the task field flags shared by create and update,
// so the two commands can never drift apart.
func addTaskFieldFlags(cmd *cobra.Command) {
	cmd.Flags().String("due", "", "When the task is due (epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")
	cmd.Flags().Bool("due-has-time", true, "Whether the due date carries a time; inferred from --due, pass =false for date-only")
	cmd.Flags().String("reminder", "", "When to be reminded (epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")
	cmd.Flags().String("recurrence", "", "Recurrence rule: daily, weekly, monthly, yearly, every:N:days|weeks|months|years, or an RRULE")
	cmd.Flags().String("priority", "", "Priority: none, low, medium, or high")
	cmd.Flags().Bool("flag", false, "Flag (star) the task")
	cmd.Flags().Int("position", 0, "Ordering index within its list (lower sorts first)")
}

// mapTaskError gives friendly messages for task-specific error codes. Codes
// that carry useful server-side details (validation_failed) fall through
// untouched so the default renderer can print those details verbatim.
func mapTaskError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "not_found":
			return errors.New("no such task or note (it may have been deleted)")
		case "invalid_priority":
			return errors.New("invalid --priority — use one of none, low, medium, or high")
		case "invalid_recurrence":
			return errors.New("invalid --recurrence — use daily, weekly, monthly, yearly, every:N:days|weeks|months|years, or an RRULE such as FREQ=WEEKLY;BYDAY=FR")
		case "plan_limit_reached":
			return errors.New("your plan's task limit is reached (or the account is read-only) — upgrade your plan, or delete a task to free room")
		case "email_unverified_limit":
			return errors.New("verify your email address to create more tasks — check your inbox, or run 'harbor auth resend-verification'")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayTasks renders a task collection as a table: completion marker, title,
// due date, priority, flag, and whether the task belongs to a note.
func displayTasks(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "✓", "TITLE", "DUE", "PRIORITY", "⚑", "NOTE", "USN"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		t := parseJSON(raw)
		rows = append(rows, []string{
			shortID(str(t, "id"), 8),
			boolMark(taskIsDone(t)),
			truncate(str(t, "title"), 40),
			taskDue(t),
			taskPriority(str(t, "priority")),
			taskFlagMark(boolean(t, "flag")),
			taskNoteMark(str(t, "note_id")),
			dim(str(t, "usn")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayTask renders the {task, usn} envelope returned by get / create /
// update / undone as a key/value detail view.
//
// The envelope's usn is deliberately not shown as a separate row: every task
// endpoint stamps the task with the USN it allocated, so the two are always the
// same number and a second "New USN" line would read as a change on a plain
// get. The USN row below already carries the freshly-allocated value, and
// --json surfaces both.
func displayTask(data []byte) {
	t := taskFromEnvelope(data)
	if t == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"ID", bold(str(t, "id"))},
		{"Title", str(t, "title")},
		{"Status", taskStatus(t)},
		{"Due", taskDueDetail(t)},
		{"Reminder", taskMoment(num(t, "reminder_at"))},
		{"Recurrence", taskRecurrence(t)},
		{"Priority", taskPriority(str(t, "priority"))},
		{"Flagged", boolMark(boolean(t, "flag"))},
		{"Position", str(t, "position")},
		{"Note", taskNoteDetail(str(t, "note_id"))},
		{"USN", bold(str(t, "usn"))},
		{"Updated", epochMS(num(t, "updated_at"))},
		{"Created", epochMS(num(t, "created_at"))},
	})
}

// displayTaskCompletion renders the result of `harbor tasks done`. A recurring
// task is NOT completed by that call — the server rolls it forward to its next
// occurrence and leaves it open — so the headline says which of the two
// happened before the usual detail view. Without it, an advanced task looks
// like a completion that failed to stick.
func displayTaskCompletion(data []byte) {
	t := taskFromEnvelope(data)
	switch {
	case t == nil:
		// Unexpected shape: fall through to the raw body via displayTask.
	case str(t, "recurrence") != "" && !taskIsDone(t):
		fmt.Println(colorize("Recurring task advanced", text.FgGreen, text.Bold) +
			" — next occurrence " + taskDueDetail(t) + ". It is still open.")
	default:
		fmt.Println(colorize("Task completed", text.FgGreen, text.Bold) + ".")
	}
	displayTask(data)
}

// taskFromEnvelope pulls the task object out of a {task, usn} response.
// Mutating and single-get endpoints both use that shape; an unexpected body
// falls back to treating the payload as the task itself, so a server tweak
// degrades to a rough render rather than to nothing at all.
func taskFromEnvelope(data []byte) map[string]any {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		return nil
	}
	if t := nested(root, "task"); t != nil {
		return t
	}
	return root
}

// taskIsDone reports whether a task is complete. done_at is omitted from the
// JSON while the task is open, so a zero/absent value means "not done".
func taskIsDone(t map[string]any) bool { return num(t, "done_at") != 0 }

// taskStatus renders a task's completion state for the detail view: the
// completion moment when done, otherwise a green "active".
func taskStatus(t map[string]any) string {
	if d := num(t, "done_at"); d != 0 {
		return colorizeStatus("done") + " " + epochMS(d) + dim(" ("+relTime(d)+")")
	}
	return colorizeStatus("active")
}

// taskDue renders a due date for the list table, honoring due_has_time: a
// date-only due shows just the day, a timed due shows the timestamp.
func taskDue(t map[string]any) string {
	ms := num(t, "due_at")
	if ms == 0 {
		return dim("—")
	}
	if !boolean(t, "due_has_time") {
		return taskDateOnly(ms)
	}
	return epochMS(ms)
}

// taskDueDetail renders a due date for the detail view — the same value as
// taskDue plus a relative hint ("in 3d") and a marker when the date carries no
// meaningful time.
func taskDueDetail(t map[string]any) string {
	ms := num(t, "due_at")
	if ms == 0 {
		return dim("(none)")
	}
	if !boolean(t, "due_has_time") {
		return taskDateOnly(ms) + dim(" (date only, "+relTime(ms)+")")
	}
	return epochMS(ms) + dim(" ("+relTime(ms)+")")
}

// taskDateOnly renders a date-only due as its calendar day. Such a due is
// stored as UTC midnight, so it is formatted in UTC on purpose: rendering it in
// a negative-offset local zone would show the previous day.
func taskDateOnly(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02")
}

// taskMoment renders an optional epoch-ms moment (a reminder) with a relative
// hint, or a dim placeholder when unset.
func taskMoment(ms float64) string {
	if ms == 0 {
		return dim("(none)")
	}
	return epochMS(ms) + dim(" ("+relTime(ms)+")")
}

// taskRecurrence renders a task's recurrence rule, or a dim placeholder for a
// one-off task.
func taskRecurrence(t map[string]any) string {
	if r := str(t, "recurrence"); r != "" {
		return r
	}
	return dim("(none)")
}

// taskPriority renders a priority with a severity color, so a high-priority row
// is findable at a glance.
func taskPriority(p string) string {
	switch p {
	case "high":
		return colorize("high", text.FgRed, text.Bold)
	case "medium":
		return colorize("medium", text.FgYellow)
	case "low":
		return colorize("low", text.FgCyan)
	case "", "none":
		return dim("none")
	}
	return p
}

// taskFlagMark renders the flagged/starred marker for a table cell.
func taskFlagMark(flagged bool) string {
	if flagged {
		return colorize("⚑", text.FgYellow)
	}
	return dim("·")
}

// taskNoteMark renders the owning-note cell of the list table: a shortened note
// id for a task that lives in a note, a dim dot for a standalone one.
func taskNoteMark(noteID string) string {
	if noteID == "" {
		return dim("·")
	}
	return dim(shortID(noteID, 8))
}

// taskNoteDetail renders the owning note for the detail view, spelling out that
// a task with no note is standalone.
func taskNoteDetail(noteID string) string {
	if noteID == "" {
		return dim("(standalone)")
	}
	return noteID
}

func init() {
	addPagingFlags(tasksListCmd)
	tasksListCmd.Flags().String("status", "", "Which tasks to list: active (default), today, done, or all")
	tasksListCmd.Flags().String("due-before", "", "Only tasks due at or before this time (epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")
	tasksListCmd.Flags().String("note", "", "List the tasks belonging to this note instead (read-only)")

	tasksCreateCmd.Flags().String("title", "", "Task title (required)")
	addTaskFieldFlags(tasksCreateCmd)

	tasksUpdateCmd.Flags().String("title", "", "New title")
	addTaskFieldFlags(tasksUpdateCmd)
	tasksUpdateCmd.Flags().Bool("clear-due", false, "Remove the due date")
	tasksUpdateCmd.Flags().Bool("clear-reminder", false, "Remove the reminder")
	tasksUpdateCmd.Flags().Bool("clear-recurrence", false, "Remove the recurrence rule")

	tasksDoneCmd.Flags().String("time", "", "Completion time (defaults to now; epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")")

	tasksCmd.AddCommand(tasksListCmd, tasksGetCmd, tasksCreateCmd, tasksUpdateCmd, tasksDoneCmd, tasksUndoneCmd, tasksDeleteCmd)
	rootCmd.AddCommand(tasksCmd)
}

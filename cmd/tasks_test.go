// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-28

package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newTaskFieldCmd builds a throwaway command carrying the shared task field
// flags, optionally pre-setting some of them, so the flag→body helpers can be
// exercised without a server.
func newTaskFieldCmd(set map[string]string) *cobra.Command {
	cmd := &cobra.Command{Use: "x"}
	addTaskFieldFlags(cmd)
	cmd.Flags().Bool("clear-due", false, "")
	cmd.Flags().Bool("clear-reminder", false, "")
	cmd.Flags().Bool("clear-recurrence", false, "")
	for k, v := range set {
		_ = cmd.Flags().Set(k, v)
	}
	return cmd
}

// TestParseTaskDueDateOnly verifies a bare YYYY-MM-DD is reported as date-only
// so the CLI can send due_has_time=false.
func TestParseTaskDueDateOnly(t *testing.T) {
	ms, hasTime, err := parseTaskDue("2026-08-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasTime {
		t.Error("a plain date must be date-only")
	}
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if ms != want {
		t.Errorf("ms = %d, want %d", ms, want)
	}
}

// TestParseTaskDueTimed verifies the forms that name a moment are reported as
// timed.
func TestParseTaskDueTimed(t *testing.T) {
	for _, in := range []string{"2026-08-01T09:30:00Z", "1785576600000", "in 2h"} {
		ms, hasTime, err := parseTaskDue(in)
		if err != nil {
			t.Fatalf("parseTaskDue(%q) error: %v", in, err)
		}
		if !hasTime {
			t.Errorf("parseTaskDue(%q) reported date-only, want timed", in)
		}
		if ms == 0 {
			t.Errorf("parseTaskDue(%q) = 0", in)
		}
	}
}

// TestParseTaskDueInvalid verifies an unparseable value errors.
func TestParseTaskDueInvalid(t *testing.T) {
	if _, _, err := parseTaskDue("next tuesday-ish"); err == nil {
		t.Fatal("expected an error for an unparseable --due")
	}
}

// TestAddTaskDueToBodyDateOnly verifies a date-only --due sends an explicit
// due_has_time=false. Without it the server defaults to true and every
// date-only due comes back looking timed.
func TestAddTaskDueToBodyDateOnly(t *testing.T) {
	body := map[string]any{}
	if err := addTaskDueToBody(newTaskFieldCmd(map[string]string{"due": "2026-08-01"}), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["due_has_time"] != false {
		t.Errorf("due_has_time = %v, want false", body["due_has_time"])
	}
	if body["due_at"] != time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("due_at = %v", body["due_at"])
	}
}

// TestAddTaskDueToBodyTimed verifies a timed --due sends due_has_time=true.
func TestAddTaskDueToBodyTimed(t *testing.T) {
	body := map[string]any{}
	if err := addTaskDueToBody(newTaskFieldCmd(map[string]string{"due": "2026-08-01T09:30:00Z"}), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["due_has_time"] != true {
		t.Errorf("due_has_time = %v, want true", body["due_has_time"])
	}
}

// TestAddTaskDueToBodyExplicitOverride verifies an explicit --due-has-time wins
// over whatever the --due shape implied, in both directions.
func TestAddTaskDueToBodyExplicitOverride(t *testing.T) {
	body := map[string]any{}
	cmd := newTaskFieldCmd(map[string]string{"due": "2026-08-01T09:30:00Z", "due-has-time": "false"})
	if err := addTaskDueToBody(cmd, body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["due_has_time"] != false {
		t.Errorf("explicit --due-has-time=false must win, got %v", body["due_has_time"])
	}

	body = map[string]any{}
	cmd = newTaskFieldCmd(map[string]string{"due": "2026-08-01", "due-has-time": "true"})
	if err := addTaskDueToBody(cmd, body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["due_has_time"] != true {
		t.Errorf("explicit --due-has-time=true must win, got %v", body["due_has_time"])
	}
}

// TestAddTaskDueToBodyAbsent verifies an unset --due touches nothing, so a
// PATCH that only renames a task does not move its due date.
func TestAddTaskDueToBodyAbsent(t *testing.T) {
	body := map[string]any{}
	if err := addTaskDueToBody(newTaskFieldCmd(nil), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %v, want empty", body)
	}
}

// TestAddTaskReminderToBody verifies --reminder becomes reminder_at in epoch-ms
// and that an unset flag adds nothing.
func TestAddTaskReminderToBody(t *testing.T) {
	body := map[string]any{}
	if err := addTaskReminderToBody(newTaskFieldCmd(map[string]string{"reminder": "1750100000000"}), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["reminder_at"] != int64(1750100000000) {
		t.Errorf("reminder_at = %v", body["reminder_at"])
	}

	body = map[string]any{}
	if err := addTaskReminderToBody(newTaskFieldCmd(nil), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %v, want empty", body)
	}
}

// TestAddTaskReminderToBodyInvalid verifies an unparseable --reminder errors.
func TestAddTaskReminderToBodyInvalid(t *testing.T) {
	err := addTaskReminderToBody(newTaskFieldCmd(map[string]string{"reminder": "whenever"}), map[string]any{})
	if err == nil {
		t.Fatal("expected an error for an unparseable --reminder")
	}
	if !strings.Contains(err.Error(), "--reminder") {
		t.Errorf("error should name the flag, got %q", err)
	}
}

// TestValidateTaskClearFlags verifies setting and clearing the same field in
// one update is rejected, while either alone is fine.
func TestValidateTaskClearFlags(t *testing.T) {
	conflicts := []map[string]string{
		{"due": "2026-08-01", "clear-due": "true"},
		{"reminder": "in 2h", "clear-reminder": "true"},
		{"recurrence": "weekly", "clear-recurrence": "true"},
	}
	for _, set := range conflicts {
		if err := validateTaskClearFlags(newTaskFieldCmd(set)); err == nil {
			t.Errorf("expected a conflict error for %v", set)
		}
	}
	for _, set := range []map[string]string{
		{"due": "2026-08-01"},
		{"clear-due": "true"},
		nil,
	} {
		if err := validateTaskClearFlags(newTaskFieldCmd(set)); err != nil {
			t.Errorf("unexpected error for %v: %v", set, err)
		}
	}
}

// TestValidateTaskNoteFilters verifies the account-wide filters are rejected
// alongside --note rather than silently ignored.
func TestValidateTaskNoteFilters(t *testing.T) {
	newListCmd := func(set map[string]string) *cobra.Command {
		cmd := &cobra.Command{Use: "list"}
		cmd.Flags().String("status", "", "")
		cmd.Flags().String("due-before", "", "")
		for k, v := range set {
			_ = cmd.Flags().Set(k, v)
		}
		return cmd
	}
	for _, f := range []string{"status", "due-before"} {
		err := validateTaskNoteFilters(newListCmd(map[string]string{f: "x"}))
		if err == nil {
			t.Errorf("expected --%s to be rejected alongside --note", f)
		} else if !strings.Contains(err.Error(), "--note") {
			t.Errorf("error should mention --note, got %q", err)
		}
	}
	if err := validateTaskNoteFilters(newListCmd(nil)); err != nil {
		t.Errorf("unexpected error with no filters set: %v", err)
	}
}

// TestTaskCreateUpdateHaveNoNoteFlag guards the rule that a task is never
// linked to a note through create/update. A note_id without the note's
// <harbor-task> block is deleted by the note's next save; attaching belongs on
// POST /notes/:id/tasks (HarborMyNotes/app.harbor.my#1094).
func TestTaskCreateUpdateHaveNoNoteFlag(t *testing.T) {
	for _, cmd := range []*cobra.Command{tasksCreateCmd, tasksUpdateCmd} {
		if f := cmd.Flags().Lookup("note"); f != nil {
			t.Errorf("%q must not define a --note flag", cmd.Name())
		}
	}
	// The read filter on list, by contrast, is expected.
	if f := tasksListCmd.Flags().Lookup("note"); f == nil {
		t.Error("tasks list should offer --note as a read filter")
	}
}

// TestMapTaskError verifies the domain-specific codes map to friendly messages
// and that codes carrying server details pass through untouched.
func TestMapTaskError(t *testing.T) {
	cases := map[string]string{
		"not_found":              "no such task",
		"invalid_priority":       "none, low, medium",
		"invalid_recurrence":     "daily, weekly",
		"plan_limit_reached":     "task limit",
		"email_unverified_limit": "verify your email",
	}
	for code, sub := range cases {
		got := mapTaskError(apiErr(code))
		if !strings.Contains(got.Error(), sub) {
			t.Errorf("mapTaskError(%s) = %q, want substring %q", code, got.Error(), sub)
		}
	}
	// validation_failed keeps its APIError so the renderer can print the
	// server's per-field details; unrelated codes pass through too.
	for _, code := range []string{"validation_failed", "rate_limited"} {
		e := apiErr(code)
		if mapTaskError(e) != e {
			t.Errorf("%s should pass through unchanged", code)
		}
	}
}

// TestDisplayTasksTable verifies the list table surfaces the completion marker,
// title, due date, priority, flag, and note ownership.
func TestDisplayTasksTable(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"3f1a2b7c-1111","title":"Pay the invoice","due_at":1785542400000,"due_has_time":false,"priority":"high","flag":true,"note_id":"","usn":95},
		{"id":"4a2b3c8d-2222","title":"Ship the release","due_at":1785576600000,"due_has_time":true,"priority":"none","flag":false,"note_id":"9c2e1111-3333","done_at":1753000000000,"usn":96}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displayTasks(data) })
	for _, want := range []string{"Pay the invoice", "Ship the release", "2026-08-01", "high", "9c2e1111"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// A date-only due must render as a bare day, never as a midnight timestamp.
	if strings.Contains(out, "2026-08-01 00:00") {
		t.Errorf("date-only due should not show a time:\n%s", out)
	}
	// The completed row carries the done marker.
	if !strings.Contains(out, "✓") {
		t.Errorf("done marker missing:\n%s", out)
	}
}

// TestDisplayTaskDetail verifies the {task, usn} envelope renders the task's
// fields and the freshly-allocated USN.
func TestDisplayTaskDetail(t *testing.T) {
	data := []byte(`{"task":{"id":"3f1a2b7c-1111","title":"Pay the invoice","due_at":1785542400000,"due_has_time":false,"reminder_at":1785500000000,"recurrence":"weekly","priority":"high","flag":true,"position":3,"note_id":"","usn":96,"updated_at":1753000000000,"created_at":1752000000000},"usn":96}`)
	out := captureStdout(t, func() { displayTask(data) })
	for _, want := range []string{"3f1a2b7c-1111", "Pay the invoice", "active", "date only", "weekly", "high", "(standalone)", "96"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// TestDisplayTaskDetailDone verifies a completed task reports the completion
// moment rather than "active".
func TestDisplayTaskDetailDone(t *testing.T) {
	data := []byte(`{"task":{"id":"t1","title":"Done thing","done_at":1753000000000,"usn":99},"usn":99}`)
	out := captureStdout(t, func() { displayTask(data) })
	if !strings.Contains(out, "done") {
		t.Errorf("completion state missing:\n%s", out)
	}
	if strings.Contains(out, "active") {
		t.Errorf("a completed task must not read as active:\n%s", out)
	}
}

// TestDisplayTaskCompletionRecurring verifies completing a RECURRING task is
// reported as an advance to the next occurrence, not as a completion — the
// server rolls the task forward and leaves it open.
func TestDisplayTaskCompletionRecurring(t *testing.T) {
	data := []byte(`{"task":{"id":"t1","title":"Standup","due_at":1785576600000,"due_has_time":true,"recurrence":"weekly","usn":97},"usn":97}`)
	out := captureStdout(t, func() { displayTaskCompletion(data) })
	if !strings.Contains(out, "advanced") {
		t.Errorf("recurring completion should report the advance:\n%s", out)
	}
	if !strings.Contains(out, "still open") {
		t.Errorf("recurring completion should say the task stays open:\n%s", out)
	}
	if strings.Contains(out, "Task completed") {
		t.Errorf("a recurring task must not report a plain completion:\n%s", out)
	}
}

// TestDisplayTaskCompletionPlain verifies a non-recurring task reports a plain
// completion.
func TestDisplayTaskCompletionPlain(t *testing.T) {
	data := []byte(`{"task":{"id":"t1","title":"Pay the invoice","done_at":1753000000000,"usn":97},"usn":97}`)
	out := captureStdout(t, func() { displayTaskCompletion(data) })
	if !strings.Contains(out, "Task completed") {
		t.Errorf("plain completion missing:\n%s", out)
	}
	if strings.Contains(out, "advanced") {
		t.Errorf("a one-off task must not report an advance:\n%s", out)
	}
}

// TestDisplayTaskCompletionEndedRecurrence verifies a recurring task whose rule
// has ENDED (the server completes it instead of advancing) reports the
// completion — the headline follows done_at, not the presence of a rule.
func TestDisplayTaskCompletionEndedRecurrence(t *testing.T) {
	data := []byte(`{"task":{"id":"t1","title":"Last one","recurrence":"FREQ=WEEKLY;COUNT=3","done_at":1753000000000,"usn":98},"usn":98}`)
	out := captureStdout(t, func() { displayTaskCompletion(data) })
	if !strings.Contains(out, "Task completed") {
		t.Errorf("an ended recurrence should report completion:\n%s", out)
	}
}

// TestDisplayTaskUnexpectedShape verifies a body that is not the {task, usn}
// envelope still prints something rather than nothing.
func TestDisplayTaskUnexpectedShape(t *testing.T) {
	out := captureStdout(t, func() { displayTask([]byte(`[1,2,3]`)) })
	if !strings.Contains(out, "[1,2,3]") {
		t.Errorf("unexpected shape should fall back to the raw body:\n%s", out)
	}
}

// TestTaskDateOnlyRendersInUTC verifies a date-only due keeps its calendar day.
// It is stored as UTC midnight, so rendering it in a negative-offset local zone
// would show the day before.
func TestTaskDateOnlyRendersInUTC(t *testing.T) {
	ms := float64(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	if got := taskDateOnly(ms); got != "2026-08-01" {
		t.Errorf("taskDateOnly = %q, want 2026-08-01", got)
	}
}

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
	// Pinned behind UTC so the date-only column is asserted in the zone where
	// dropping the UTC render would move it to the previous day.
	withLocalZone(t, "America/Los_Angeles")
	data := []byte(`{"data":[
		{"id":"3f1a2b7c-1111","title":"Pay the invoice","due_at":1785542400000,"due_has_time":false,"priority":"high","flag":true,"note_id":"","usn":95},
		{"id":"4a2b3c8d-2222","title":"Ship the release","due_at":1785922200000,"due_has_time":true,"priority":"none","flag":false,"note_id":"9c2e1111-3333","done_at":1753000000000,"usn":96}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displayTasks(data) })
	// 2026-08-01 can only come from the date-only row: the timed row is four
	// days later on purpose, so it cannot satisfy that assertion on its behalf.
	for _, want := range []string{"Pay the invoice", "Ship the release", "2026-08-01", "2026-08-05", "high", "9c2e1111"} {
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

// withLocalZone pins time.Local for the duration of a test. Setting the TZ env
// var is not enough — Go resolves time.Local once, so a later TZ change is
// invisible — and the zone is the whole point here: a date-only due only
// misrenders in a zone behind UTC, and CI runs in UTC.
func withLocalZone(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })
}

// TestTaskDateOnlyRendersInUTC verifies a date-only due keeps its calendar day.
// It is stored as UTC midnight, so rendering it in a negative-offset local zone
// would show the day before — which is exactly the zone this test pins.
func TestTaskDateOnlyRendersInUTC(t *testing.T) {
	withLocalZone(t, "America/Los_Angeles")
	ms := float64(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	if got := taskDateOnly(ms); got != "2026-08-01" {
		t.Errorf("taskDateOnly = %q, want 2026-08-01", got)
	}
}

// ===========================================================================
// RunE wiring
// ===========================================================================
//
// The tests above exercise the extracted helpers. These run the REAL command
// tree against a mock API, because that is the only thing that pins the call
// sites: a guard that is never consulted, or a field that reaches the wire from
// somewhere other than a flag, is invisible to a helper test.
//
// The note_id pair is the load-bearing one. A task linked to a note without the
// note's matching <harbor-task> block is tombstoned by that note's next save
// (HarborMyNotes/app.harbor.my#1094), so "the CLI never sends note_id" is a
// data-loss guarantee, and it has to be asserted against the bytes that leave
// the process — not against the flag set, and not against a map a test built
// itself.

// taskReply is the {task, usn} body the mock API returns for a task mutation.
const taskReply = `{"task":{"id":"3f1a2b7c-9d4e-4a1b-8c2d-5e6f7a8b9c0d","title":"X","priority":"none","usn":7},"usn":7}`

// canonicalTaskID is a well-formed id used across the RunE tests; the mock's
// route table is keyed on the path it produces.
const canonicalTaskID = "3f1a2b7c-9d4e-4a1b-8c2d-5e6f7a8b9c0d"

// TestTasksCreateNeverSendsNoteIDOnTheWire pins the #1094 guarantee for create
// against the actual request body. Injecting note_id into the create RunE turns
// this red.
func TestTasksCreateNeverSendsNoteIDOnTheWire(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/tasks": {Status: 201, Body: taskReply},
	})
	if _, err := runCLI(t, m, "tasks", "create", "--title", "X"); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := m.bodyOf(t, "POST /api/v1/tasks")
	if v, ok := body["note_id"]; ok {
		t.Errorf("note_id reached the wire (%v) — a link without the note's block is a delayed delete", v)
	}
}

// TestTasksUpdateNeverSendsNoteIDOnTheWire is the same guarantee for update,
// where the server now rejects note_id outright — so sending it would break the
// command as well as risk the task.
func TestTasksUpdateNeverSendsNoteIDOnTheWire(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/tasks/" + canonicalTaskID: {Status: 200, Body: taskReply},
	})
	if _, err := runCLI(t, m, "tasks", "update", canonicalTaskID, "--title", "Renamed"); err != nil {
		t.Fatalf("update: %v", err)
	}
	body := m.bodyOf(t, "PATCH /api/v1/tasks/"+canonicalTaskID)
	if v, ok := body["note_id"]; ok {
		t.Errorf("note_id reached the wire (%v) — moving a task between notes is not supported", v)
	}
}

// TestTasksCreateAndUpdateRejectNoteFlag pins the other half of the same rule at
// the CLI surface: --note is not a thing you can pass to a write command, so it
// fails before any request is made.
func TestTasksCreateAndUpdateRejectNoteFlag(t *testing.T) {
	for _, args := range [][]string{
		{"tasks", "create", "--title", "X", "--note", canonicalTaskID},
		{"tasks", "update", canonicalTaskID, "--note", canonicalTaskID},
	} {
		m := newAPIMock(t, map[string]mockReply{})
		_, err := runCLI(t, m, args...)
		if err == nil {
			t.Errorf("%v: --note must not be accepted on a write command", args)
		}
		if len(m.calls()) != 0 {
			t.Errorf("%v: no request should be made, got %v", args, m.calls())
		}
	}
}

// TestTasksListNoteRejectsFiltersBeforeCallingAPI pins the validateTaskNoteFilters
// CALL SITE. Deleting the call leaves the helper's own test green while the
// command silently returns a list that ignored the filter.
func TestTasksListNoteRejectsFiltersBeforeCallingAPI(t *testing.T) {
	for _, flag := range []string{"--status", "--due-before"} {
		m := newAPIMock(t, map[string]mockReply{})
		out, err := runCLI(t, m, "tasks", "list", "--note", canonicalTaskID, flag, "done")
		if err == nil {
			t.Errorf("%s alongside --note must fail", flag)
		}
		if len(m.calls()) != 0 {
			t.Errorf("%s: nothing should be requested, got %v", flag, m.calls())
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("%s: nothing should reach stdout:\n%s", flag, out)
		}
	}
}

// TestTasksUpdateRejectsSetAndClearBeforeCallingAPI pins the
// validateTaskClearFlags call site: without it the request goes out and the
// server lets the set win, so a --clear-due that did nothing looks like it
// worked.
func TestTasksUpdateRejectsSetAndClearBeforeCallingAPI(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	_, err := runCLI(t, m, "tasks", "update", canonicalTaskID, "--due", "2026-08-01", "--clear-due")
	if err == nil {
		t.Fatal("--due with --clear-due must fail")
	}
	if len(m.calls()) != 0 {
		t.Errorf("nothing should be requested, got %v", m.calls())
	}
}

// TestTasksCreateRequiresTitle pins the required-title guard: without it an
// empty title is sent and a blank task is created.
func TestTasksCreateRequiresTitle(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	_, err := runCLI(t, m, "tasks", "create")
	if err == nil {
		t.Fatal("create without --title must fail")
	}
	if len(m.calls()) != 0 {
		t.Errorf("no task should be created, got %v", m.calls())
	}
}

// TestTasksUpdateRequiresAField pins the empty-update guard: an empty PATCH
// still allocates a USN and syncs a no-op change to every device.
func TestTasksUpdateRequiresAField(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	_, err := runCLI(t, m, "tasks", "update", canonicalTaskID)
	if err == nil {
		t.Fatal("update with no field flags must fail")
	}
	if len(m.calls()) != 0 {
		t.Errorf("no request should be made, got %v", m.calls())
	}
}

// TestTasksListForwardsFilters pins that --status and --due-before actually
// reach the query string, and that an unset --status sends nothing so the
// server's own default applies.
func TestTasksListForwardsFilters(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/tasks": {Status: 200, Body: `{"data":[],"paging":{}}`},
	})
	if _, err := runCLI(t, m, "tasks", "list", "--status", "today", "--due-before", "1785542400000"); err != nil {
		t.Fatalf("list: %v", err)
	}
	q := m.queryOf(t, "GET /api/v1/tasks")
	if q.Get("status") != "today" {
		t.Errorf("status = %q, want today", q.Get("status"))
	}
	if q.Get("due_before") != "1785542400000" {
		t.Errorf("due_before = %q", q.Get("due_before"))
	}

	m2 := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/tasks": {Status: 200, Body: `{"data":[],"paging":{}}`},
	})
	if _, err := runCLI(t, m2, "tasks", "list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, ok := m2.queryOf(t, "GET /api/v1/tasks")["status"]; ok {
		t.Error("an unset --status must send nothing, so the server default applies")
	}
}

// TestTasksListNoteUsesTheNoteEndpoint pins the routing: a --note listing is a
// read of the note's own task list, not a filtered account-wide list.
func TestTasksListNoteUsesTheNoteEndpoint(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes/" + canonicalTaskID + "/tasks": {Status: 200, Body: `{"data":[],"paging":{}}`},
	})
	if _, err := runCLI(t, m, "tasks", "list", "--note", canonicalTaskID); err != nil {
		t.Fatalf("list --note: %v", err)
	}
	want := "GET /api/v1/notes/" + canonicalTaskID + "/tasks"
	if calls := m.calls(); len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %v, want [%s]", calls, want)
	}
}

// TestTasksDoneForwardsTime pins that --time becomes done_time, and that an
// omitted --time posts an empty object rather than null so the server stamps
// the completion itself.
func TestTasksDoneForwardsTime(t *testing.T) {
	route := "POST /api/v1/tasks/" + canonicalTaskID + "/done"
	m := newAPIMock(t, map[string]mockReply{route: {Status: 200, Body: taskReply}})
	if _, err := runCLI(t, m, "tasks", "done", canonicalTaskID, "--time", "1785542400000"); err != nil {
		t.Fatalf("done: %v", err)
	}
	if got := m.bodyOf(t, route)["done_time"]; got != float64(1785542400000) {
		t.Errorf("done_time = %v, want 1785542400000", got)
	}

	m2 := newAPIMock(t, map[string]mockReply{route: {Status: 200, Body: taskReply}})
	if _, err := runCLI(t, m2, "tasks", "done", canonicalTaskID); err != nil {
		t.Fatalf("done: %v", err)
	}
	if got := m2.rawBodyOf(t, route); got != "{}" {
		t.Errorf("body = %q, want {} so the server defaults done_time to now", got)
	}
}

// TestTasksUpdateClearSentinels pins the clear_* booleans in both directions:
// passing --clear-due sends the sentinel, and NOT passing it must not. An
// inverted condition breaks exactly one of those, so both are asserted.
func TestTasksUpdateClearSentinels(t *testing.T) {
	route := "PATCH /api/v1/tasks/" + canonicalTaskID
	for flag, key := range map[string]string{
		"--clear-due":        "clear_due_at",
		"--clear-reminder":   "clear_reminder_at",
		"--clear-recurrence": "clear_recurrence",
	} {
		m := newAPIMock(t, map[string]mockReply{route: {Status: 200, Body: taskReply}})
		if _, err := runCLI(t, m, "tasks", "update", canonicalTaskID, flag); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if got := m.bodyOf(t, route)[key]; got != true {
			t.Errorf("%s: %s = %v, want true", flag, key, got)
		}

		m2 := newAPIMock(t, map[string]mockReply{route: {Status: 200, Body: taskReply}})
		if _, err := runCLI(t, m2, "tasks", "update", canonicalTaskID, "--title", "Renamed"); err != nil {
			t.Fatalf("%s (unset): %v", flag, err)
		}
		if _, ok := m2.bodyOf(t, route)[key]; ok {
			t.Errorf("%s: %s must be absent when the flag is not passed", flag, key)
		}
	}
}

// TestTasksCreateSendsDueHasTimeForDateOnly is the end-to-end version of the
// headline behaviour: a plain-date --due must put due_has_time=false on the
// wire, because the server defaults an ABSENT flag to true and the task would
// come back looking timed.
func TestTasksCreateSendsDueHasTimeForDateOnly(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/tasks": {Status: 201, Body: taskReply},
	})
	if _, err := runCLI(t, m, "tasks", "create", "--title", "X", "--due", "2026-08-01"); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := m.bodyOf(t, "POST /api/v1/tasks")
	if body["due_has_time"] != false {
		t.Errorf("due_has_time = %v, want false", body["due_has_time"])
	}
	if body["due_at"] != float64(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMilli()) {
		t.Errorf("due_at = %v", body["due_at"])
	}
}

// TestTasksUndoneAndDeleteRoutes pins the two DELETE verbs, which differ only by
// a path suffix — reopening a task and destroying it must not be confusable.
func TestTasksUndoneAndDeleteRoutes(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"DELETE /api/v1/tasks/" + canonicalTaskID + "/done": {Status: 200, Body: taskReply},
	})
	if _, err := runCLI(t, m, "tasks", "undone", canonicalTaskID); err != nil {
		t.Fatalf("undone: %v", err)
	}
	if want := "DELETE /api/v1/tasks/" + canonicalTaskID + "/done"; m.calls()[0] != want {
		t.Errorf("undone called %v, want %s", m.calls(), want)
	}

	m2 := newAPIMock(t, map[string]mockReply{
		"DELETE /api/v1/tasks/" + canonicalTaskID: {Status: 204, Body: ``},
	})
	out, err := runCLI(t, m2, "tasks", "delete", canonicalTaskID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if want := "DELETE /api/v1/tasks/" + canonicalTaskID; m2.calls()[0] != want {
		t.Errorf("delete called %v, want %s", m2.calls(), want)
	}
	if !strings.Contains(out, "deleted") {
		t.Errorf("delete should confirm:\n%s", out)
	}
}

// TestTasksIDArgIsCanonicalized verifies the accepted UUID spellings all reach
// the same canonical path. The server canonicalizes ids only on create, so an
// id typed in upper case or pasted with braces would otherwise 404 on a live
// task and be reported as a deletion.
func TestTasksIDArgIsCanonicalized(t *testing.T) {
	for _, spelling := range []string{
		strings.ToUpper(canonicalTaskID),
		strings.ReplaceAll(canonicalTaskID, "-", ""),
		"{" + canonicalTaskID + "}",
		"urn:uuid:" + canonicalTaskID,
		"  " + canonicalTaskID + "  ",
	} {
		m := newAPIMock(t, map[string]mockReply{
			"GET /api/v1/tasks/" + canonicalTaskID: {Status: 200, Body: taskReply},
		})
		if _, err := runCLI(t, m, "tasks", "get", spelling); err != nil {
			t.Errorf("get %q: %v", spelling, err)
			continue
		}
		if want := "GET /api/v1/tasks/" + canonicalTaskID; m.calls()[0] != want {
			t.Errorf("get %q called %v, want %s", spelling, m.calls(), want)
		}
	}
}

// TestTasksMalformedIDIsReportedAsSuch verifies a value that is not an id is
// reported as a spelling problem before any request — telling someone their
// note was deleted when they mistyped the id sends them looking for a data loss
// that never happened.
func TestTasksMalformedIDIsReportedAsSuch(t *testing.T) {
	for _, bad := range []string{"3f1a2b7c", "not-a-uuid", canonicalTaskID + "extra", "3f1a2b7c-9d4e-4a1b-8c2d-5e6f7a8b9c0z"} {
		m := newAPIMock(t, map[string]mockReply{})
		_, err := runCLI(t, m, "tasks", "get", bad)
		if err == nil {
			t.Errorf("get %q should fail", bad)
			continue
		}
		if !strings.Contains(err.Error(), "is not a task id") {
			t.Errorf("get %q: err = %q, want it to name the id as malformed", bad, err)
		}
		if len(m.calls()) != 0 {
			t.Errorf("get %q: nothing should be requested, got %v", bad, m.calls())
		}
	}
	// The same rule for --note, which names a note rather than a task.
	m := newAPIMock(t, map[string]mockReply{})
	_, err := runCLI(t, m, "tasks", "list", "--note", "nope")
	if err == nil || !strings.Contains(err.Error(), "is not a note id") {
		t.Errorf("list --note nope: err = %v, want it to name the note id as malformed", err)
	}
}

// TestCanonicalUUID covers the normalizer directly, including the near-misses
// that must NOT be accepted — a hyphen in the wrong place is a typo, and
// silently "fixing" it would target a different record.
func TestCanonicalUUID(t *testing.T) {
	for _, in := range []string{
		canonicalTaskID,
		strings.ToUpper(canonicalTaskID),
		strings.ReplaceAll(canonicalTaskID, "-", ""),
		"{" + canonicalTaskID + "}",
		"urn:uuid:" + strings.ToUpper(canonicalTaskID),
	} {
		got, ok := canonicalUUID(in)
		if !ok || got != canonicalTaskID {
			t.Errorf("canonicalUUID(%q) = %q, %v; want %q, true", in, got, ok, canonicalTaskID)
		}
	}
	for _, in := range []string{
		"", "3f1a2b7c", "not-a-uuid",
		"3f1a2b7c9d4e4a1b8c2d5e6f7a8b9c0d0",     // one hex digit too many
		"3f1a2b7c-9d4e-4a1b-8c2d-5e6f7a8b9c0z",  // non-hex
		"3f1a2b7c9-d4e-4a1b-8c2d-5e6f7a8b9c0d",  // hyphens misplaced
		"{3f1a2b7c-9d4e-4a1b-8c2d-5e6f7a8b9c0d", // unbalanced brace
	} {
		if got, ok := canonicalUUID(in); ok {
			t.Errorf("canonicalUUID(%q) = %q, true; want rejected", in, got)
		}
	}
}

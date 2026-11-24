// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newReminderTimeCmd builds a throwaway command carrying a single --time flag,
// used to exercise reminderTimeFlagToMS in isolation.
func newReminderTimeCmd(value string, set bool) *cobra.Command {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("time", "", "")
	if set {
		_ = cmd.Flags().Set("time", value)
	}
	return cmd
}

// TestReminderTimeFlagToMSEpoch verifies a raw epoch-ms value passes through
// unchanged (the API only ever receives epoch-ms).
func TestReminderTimeFlagToMSEpoch(t *testing.T) {
	cmd := newReminderTimeCmd("1750100000000", true)
	ms, err := reminderTimeFlagToMS(cmd, "time", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms != 1750100000000 {
		t.Errorf("ms = %d, want 1750100000000", ms)
	}
}

// TestReminderTimeFlagToMSDate verifies a YYYY-MM-DD date converts to the
// start of that day on this computer, not UTC midnight, which is the afternoon
// before in the pinned zone.
func TestReminderTimeFlagToMSDate(t *testing.T) {
	withLocalZone(t, "America/Los_Angeles")
	cmd := newReminderTimeCmd("2026-07-01", true)
	ms, err := reminderTimeFlagToMS(cmd, "time", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local).UnixMilli()
	if ms != want {
		t.Errorf("ms = %d, want %d", ms, want)
	}
}

// TestReminderTimeFlagToMSRelative verifies a relative offset ("in 2h") lands
// roughly two hours in the future.
func TestReminderTimeFlagToMSRelative(t *testing.T) {
	cmd := newReminderTimeCmd("in 2h", true)
	ms, err := reminderTimeFlagToMS(cmd, "time", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	target := time.Now().Add(2 * time.Hour).UnixMilli()
	// Allow a generous skew for test execution time.
	if diff := ms - target; diff < -60000 || diff > 60000 {
		t.Errorf("ms = %d not within 1 minute of %d", ms, target)
	}
}

// TestReminderTimeFlagToMSRequiredMissing verifies an unset required flag errors.
func TestReminderTimeFlagToMSRequiredMissing(t *testing.T) {
	cmd := newReminderTimeCmd("", false)
	if _, err := reminderTimeFlagToMS(cmd, "time", true); err == nil {
		t.Fatal("expected error for missing required --time")
	}
}

// TestReminderTimeFlagToMSOptionalMissing verifies an unset optional flag
// returns 0 with no error (the complete command sends no done_time then).
func TestReminderTimeFlagToMSOptionalMissing(t *testing.T) {
	cmd := newReminderTimeCmd("", false)
	ms, err := reminderTimeFlagToMS(cmd, "time", false)
	if err != nil || ms != 0 {
		t.Errorf("ms=%d err=%v, want 0, nil", ms, err)
	}
}

// TestReminderTimeFlagToMSInvalid verifies an unparseable value errors.
func TestReminderTimeFlagToMSInvalid(t *testing.T) {
	cmd := newReminderTimeCmd("not-a-time", true)
	if _, err := reminderTimeFlagToMS(cmd, "time", true); err == nil {
		t.Fatal("expected error for unparseable --time")
	}
}

// TestMapReminderError verifies the domain-specific codes map to friendly
// messages and that unrelated errors pass through unchanged.
func TestMapReminderError(t *testing.T) {
	cases := map[string]string{
		"note_in_trash":  "trash",
		"not_a_reminder": "no reminder set",
		"not_found":      "no such note",
	}
	for code, sub := range cases {
		got := mapReminderError(apiErr(code))
		if !strings.Contains(got.Error(), sub) {
			t.Errorf("mapReminderError(%s) = %q, want substring %q", code, got.Error(), sub)
		}
	}
	// An unrelated code is returned as-is.
	other := apiErr("rate_limited")
	if mapReminderError(other) != other {
		t.Errorf("unrelated error should pass through unchanged")
	}
}

// TestDisplayRemindersTable verifies the list table surfaces title, due time,
// done state, and hides ciphertext for encrypted notes.
func TestDisplayRemindersTable(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"n1","title":"Pay invoice","is_encrypted":false,"reminder_time":1750100000000,"reminder_done_time":0,"usn":95},
		{"id":"n2","title":"sealed","is_encrypted":true,"reminder_time":1750100000000,"reminder_done_time":1750090000000,"usn":96}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displayReminders(data) })
	if !strings.Contains(out, "Pay invoice") {
		t.Errorf("title missing:\n%s", out)
	}
	if !strings.Contains(out, "[encrypted]") || strings.Contains(out, "sealed") {
		t.Errorf("encrypted title should be hidden:\n%s", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("done state missing for completed reminder:\n%s", out)
	}
}

// TestDisplayReminderMutationShowsNewUSN verifies the mutation envelope renders
// the note id, reminder time, and the freshly-allocated USN.
func TestDisplayReminderMutationShowsNewUSN(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","title":"Pay invoice","is_encrypted":false,"reminder_time":1750100000000,"reminder_done_time":0,"usn":96},"usn":96}`)
	out := captureStdout(t, func() { displayReminderMutation(data) })
	if !strings.Contains(out, "n1") {
		t.Errorf("note id missing:\n%s", out)
	}
	if !strings.Contains(out, "New USN") {
		t.Errorf("new USN missing:\n%s", out)
	}
	if !strings.Contains(out, "Pay invoice") {
		t.Errorf("title missing:\n%s", out)
	}
}

// TestDisplayReminderMutationCleared verifies a cleared reminder (no
// reminder_time) renders "(none)" rather than a bogus epoch.
func TestDisplayReminderMutationCleared(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","title":"Pay invoice","reminder_time":0,"usn":98},"usn":98}`)
	out := captureStdout(t, func() { displayReminderMutation(data) })
	if !strings.Contains(out, "(none)") {
		t.Errorf("cleared reminder should show (none):\n%s", out)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newContentCmd builds a throwaway command with the shared content flags, for
// testing readContent in isolation.
func newContentCmd() *cobra.Command {
	c := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	addContentFlags(c)
	return c
}

func TestReadContentLiteral(t *testing.T) {
	c := newContentCmd()
	c.SetArgs([]string{"--content", "# Hi", "--format", "markdown"})
	_ = c.Execute()
	content, format, has, err := readContent(c)
	if err != nil || !has {
		t.Fatalf("readContent: has=%v err=%v", has, err)
	}
	if content != "# Hi" || format != "markdown" {
		t.Errorf("content=%q format=%q", content, format)
	}
}

func TestReadContentFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "body.md")
	_ = os.WriteFile(path, []byte("from file"), 0644)
	c := newContentCmd()
	c.SetArgs([]string{"--file", path})
	_ = c.Execute()
	content, _, has, err := readContent(c)
	if err != nil || !has || content != "from file" {
		t.Fatalf("file content: %q has=%v err=%v", content, has, err)
	}
}

func TestReadContentNoneIsValid(t *testing.T) {
	c := newContentCmd()
	c.SetArgs([]string{})
	_ = c.Execute()
	_, _, has, err := readContent(c)
	if err != nil || has {
		t.Errorf("no content should be valid with has=false: has=%v err=%v", has, err)
	}
}

func TestReadContentRejectsMultipleSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.md")
	_ = os.WriteFile(path, []byte("x"), 0644)
	c := newContentCmd()
	c.SetArgs([]string{"--content", "a", "--file", path})
	_ = c.Execute()
	if _, _, _, err := readContent(c); err == nil {
		t.Error("expected error when both --content and --file are set")
	}
}

func TestReadContentRejectsBadFormat(t *testing.T) {
	c := newContentCmd()
	c.SetArgs([]string{"--content", "a", "--format", "rtf"})
	_ = c.Execute()
	if _, _, _, err := readContent(c); err == nil {
		t.Error("expected error for invalid --format")
	}
}

func TestParseTimeToEpochMS(t *testing.T) {
	// Raw epoch-ms passes through.
	if got, _ := parseTimeToEpochMS("1750000000000"); got != 1750000000000 {
		t.Errorf("epoch-ms = %d", got)
	}
	// RFC3339.
	want := time.Date(2026, 6, 22, 15, 4, 5, 0, time.UTC).UnixMilli()
	if got, err := parseTimeToEpochMS("2026-06-22T15:04:05Z"); err != nil || got != want {
		t.Errorf("rfc3339 = %d err=%v", got, err)
	}
	// Plain date: the start of that day on this computer, pinned to a zone
	// behind UTC, where UTC midnight would be the afternoon before.
	withLocalZone(t, "America/Los_Angeles")
	wantDay := time.Date(2026, 6, 22, 0, 0, 0, 0, time.Local).UnixMilli()
	if got, _ := parseTimeToEpochMS("2026-06-22"); got != wantDay {
		t.Errorf("date = %d, want %d", got, wantDay)
	}
	// Relative offset is ~2h in the future.
	got, err := parseTimeToEpochMS("in 2h")
	if err != nil {
		t.Fatalf("relative err: %v", err)
	}
	delta := got - time.Now().UnixMilli()
	if delta < int64(110*time.Minute/time.Millisecond) || delta > int64(130*time.Minute/time.Millisecond) {
		t.Errorf("relative delta = %dms, want ~2h", delta)
	}
	// "in 3d" supports the day unit (time.ParseDuration does not).
	if _, err := parseTimeToEpochMS("in 3d"); err != nil {
		t.Errorf("in 3d err: %v", err)
	}
	// Garbage errors.
	if _, err := parseTimeToEpochMS("not a time"); err == nil {
		t.Error("expected error for unparseable time")
	}
}

// TestParseTimeToEpochMSPlainDateSkippedMidnight verifies a plain date whose
// midnight the local clocks skip still means that day: it starts at the jump
// (01:00), not at 23:00 the evening before.
func TestParseTimeToEpochMSPlainDateSkippedMidnight(t *testing.T) {
	for _, c := range []struct{ zone, day string }{
		{"America/Santiago", "2026-09-06"},
		{"America/Havana", "2026-03-08"},
	} {
		withLocalZone(t, c.zone)
		ms, err := parseTimeToEpochMS(c.day)
		if err != nil {
			t.Fatalf("%s %s: %v", c.zone, c.day, err)
		}
		got := time.UnixMilli(ms).In(time.Local)
		if got.Format("2006-01-02 15:04") != c.day+" 01:00" {
			t.Errorf("%s %s: = %s, want %s 01:00", c.zone, c.day, got.Format("2006-01-02 15:04 MST"), c.day)
		}
	}
}

// TestParseDueBeforeToEpochMS verifies a plain-date cut-off covers the whole
// local day, and that every other form is read exactly as parseTimeToEpochMS
// reads it.
func TestParseDueBeforeToEpochMS(t *testing.T) {
	withLocalZone(t, "America/Los_Angeles")
	ms, err := parseDueBeforeToEpochMS("2026-04-16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := time.Date(2026, 4, 16, 23, 59, 59, 999_000_000, time.Local).UnixMilli(); ms != want {
		t.Errorf("plain date = %d, want %d", ms, want)
	}
	for _, in := range []string{"1750000000000", "2026-06-22T15:04:05Z"} {
		got, err := parseDueBeforeToEpochMS(in)
		want, _ := parseTimeToEpochMS(in)
		if err != nil || got != want {
			t.Errorf("%q = %d err=%v, want %d", in, got, err, want)
		}
	}
	if _, err := parseDueBeforeToEpochMS("someday"); err == nil {
		t.Error("an unparseable value should error")
	}
}

// TestParsePlainDate verifies only a bare YYYY-MM-DD is a plain date; a
// timestamp or a relative offset is not.
func TestParsePlainDate(t *testing.T) {
	y, m, d, ok := parsePlainDate(" 2026-04-16 ")
	if !ok || y != 2026 || m != time.April || d != 16 {
		t.Errorf("parsePlainDate = %d-%d-%d ok=%v", y, m, d, ok)
	}
	for _, in := range []string{"2026-04-16T09:00:00Z", "in 2h", "1750000000000", "2026-13-01"} {
		if _, _, _, ok := parsePlainDate(in); ok {
			t.Errorf("parsePlainDate(%q) should not be a plain date", in)
		}
	}
}

// TestStartAndEndOfLocalDay verifies the two ends of an ordinary local day.
func TestStartAndEndOfLocalDay(t *testing.T) {
	withLocalZone(t, "Australia/Sydney")
	start := startOfLocalDay(2026, time.April, 16)
	end := endOfLocalDay(2026, time.April, 16)
	if got := start.Format("2006-01-02 15:04:05.000"); got != "2026-04-16 00:00:00.000" {
		t.Errorf("start = %s", got)
	}
	if got := end.Format("2006-01-02 15:04:05.000"); got != "2026-04-16 23:59:59.999" {
		t.Errorf("end = %s", got)
	}
}

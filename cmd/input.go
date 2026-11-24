// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// parseTimeToEpochMS converts a flexible time input into UTC epoch
// milliseconds, accepting: a raw epoch-ms integer; an RFC3339 timestamp
// (2026-06-22T15:04:05Z); a date (2026-06-22), which is the start of that day
// in this computer's time zone; or a relative offset like "in 2h", "in 30m",
// "in 3d". Used by reminder/time flags across commands.
func parseTimeToEpochMS(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty time value")
	}

	// Raw epoch milliseconds.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}

	// Relative offset: "in <N><unit>" where unit is m/h/d.
	if rel := strings.TrimSpace(strings.TrimPrefix(s, "in ")); rel != s {
		if d, err := parseSimpleDuration(rel); err == nil {
			return time.Now().Add(d).UnixMilli(), nil
		}
	}

	// RFC3339 timestamp.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().UnixMilli(), nil
	}

	// Plain date: the start of that day on this computer, because the user
	// means the day on their own calendar. UTC midnight is the afternoon before
	// in the Americas.
	if y, m, d, ok := parsePlainDate(s); ok {
		return startOfLocalDay(y, m, d).UnixMilli(), nil
	}

	return 0, fmt.Errorf("could not parse time %q (use epoch-ms, RFC3339, YYYY-MM-DD, or \"in 2h\")", s)
}

// parseDueBeforeToEpochMS reads a --due-before value. A plain date covers that
// whole day, so "--due-before 2026-04-16" still lists what is due on the 16th;
// a date-only task due sits at the day's last millisecond, so an earlier
// cut-off would miss it. Every other form is read by parseTimeToEpochMS.
func parseDueBeforeToEpochMS(s string) (int64, error) {
	if y, m, d, ok := parsePlainDate(s); ok {
		return endOfLocalDay(y, m, d).UnixMilli(), nil
	}
	return parseTimeToEpochMS(s)
}

// parsePlainDate reads a bare YYYY-MM-DD as a calendar day. The parts come from
// the text, not from a moment in the local zone, so a day whose midnight the
// local clocks skip is still that day.
func parsePlainDate(s string) (int, time.Month, int, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, 0, false
	}
	y, m, d := t.Date()
	return y, m, d, true
}

// startOfLocalDay is the first moment of a calendar day in this computer's
// time zone. Where the clocks jump forward at midnight, 00:00 does not exist
// and Go resolves it to the evening before; the day then starts at the jump.
func startOfLocalDay(y int, m time.Month, d int) time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	if ty, tm, td := t.Date(); ty != y || tm != m || td != d {
		if _, next := t.ZoneBounds(); !next.IsZero() {
			return next
		}
	}
	return t
}

// endOfLocalDay is the last millisecond of a calendar day in this computer's
// time zone. It is the moment the web app stores for a date-only task due, so
// "due today" and overdue work on the server without knowing the user's zone.
func endOfLocalDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 23, 59, 59, int(999*time.Millisecond), time.Local)
}

// parseSimpleDuration parses durations including the day unit ("3d"), which
// time.ParseDuration does not support, falling back to it for h/m/s.
func parseSimpleDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// defaultContentFormat is the CLI's default interpretation of body content.
// Markdown is the friendliest input for humans and agents; the server converts
// it to sanitized HTML. Override per command with --format html.
const defaultContentFormat = "markdown"

// addContentFlags registers the shared body-input flags (--content / --file /
// --stdin / --format) on a command that creates or updates note-like bodies.
func addContentFlags(cmd *cobra.Command) {
	cmd.Flags().String("content", "", "Body content as a literal string")
	cmd.Flags().String("file", "", "Read body content from a file")
	cmd.Flags().Bool("stdin", false, "Read body content from standard input")
	cmd.Flags().String("format", defaultContentFormat, "Content format: markdown or html")
}

// readContent resolves body content from the mutually-exclusive --content /
// --file / --stdin flags. It returns the content, the chosen format, and a
// hasContent flag (false when none of the three was provided, which is valid
// for updates that change only metadata). At most one source may be set.
func readContent(cmd *cobra.Command) (content, format string, hasContent bool, err error) {
	format = stringFlag(cmd, "format")
	if format == "" {
		format = defaultContentFormat
	}
	if format != "markdown" && format != "html" {
		return "", "", false, fmt.Errorf("invalid --format %q (want markdown or html)", format)
	}

	literal := cmd.Flags().Changed("content")
	file := stringFlag(cmd, "file")
	stdin := boolFlag(cmd, "stdin")

	sources := 0
	if literal {
		sources++
	}
	if file != "" {
		sources++
	}
	if stdin {
		sources++
	}
	if sources > 1 {
		return "", "", false, errors.New("use only one of --content, --file, or --stdin")
	}
	if sources == 0 {
		return "", format, false, nil
	}

	switch {
	case literal:
		content = stringFlag(cmd, "content")
	case file != "":
		b, rerr := os.ReadFile(file)
		if rerr != nil {
			return "", "", false, fmt.Errorf("failed to read --file: %w", rerr)
		}
		content = string(b)
	case stdin:
		b, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			return "", "", false, fmt.Errorf("failed to read stdin: %w", rerr)
		}
		content = string(b)
	}
	return content, format, true, nil
}

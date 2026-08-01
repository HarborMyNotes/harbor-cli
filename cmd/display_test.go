// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// apiErr builds a *client.APIError with the given code, for error-mapping tests.
func apiErr(code string) *client.APIError {
	return &client.APIError{Code: code, Message: code, Status: 422}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. Shared across cmd display tests. Color is disabled for stable output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	noColorFlag = true
	colorReady = false
	defer func() { noColorFlag = false; colorReady = false }()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

// captureStderr is captureStdout's counterpart, for the notices a command sends
// to stderr precisely so they cannot corrupt a piped stdout. Colour is left
// alone: this is meant to nest inside runCLI, which already disables it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestEpochMS(t *testing.T) {
	utcFlag = true
	defer func() { utcFlag = false }()

	if got := epochMS(0); got != "—" {
		t.Errorf("epochMS(0) = %q, want em dash", got)
	}
	// 1750000000000 ms = 2025-06-15 15:06:40 UTC.
	got := epochMS(1750000000000)
	if got != "2025-06-15 15:06 UTC" {
		t.Errorf("epochMS = %q", got)
	}
}

func TestRelTime(t *testing.T) {
	now := time.Now()
	if got := relTime(float64(now.Add(-2 * time.Hour).UnixMilli())); got != "2h ago" {
		t.Errorf("relTime(-2h) = %q", got)
	}
	// A small buffer past the exact 3d mark so truncation doesn't read "in 2d".
	if got := relTime(float64(now.Add(3*24*time.Hour + time.Minute).UnixMilli())); got != "in 3d" {
		t.Errorf("relTime(+3d) = %q", got)
	}
	if got := relTime(float64(now.Add(-10 * time.Second).UnixMilli())); got != "just now" {
		t.Errorf("relTime(-10s) = %q", got)
	}
	if got := relTime(0); got != "—" {
		t.Errorf("relTime(0) = %q", got)
	}
}

func TestBytesHuman(t *testing.T) {
	cases := map[float64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1048576: "1.0 MB",
	}
	for in, want := range cases {
		if got := bytesHuman(in); got != want {
			t.Errorf("bytesHuman(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestCommaNum(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		4120:    "4,120",
		36500:   "36,500",
		-36500:  "-36,500",
		1000000: "1,000,000",
	}
	for in, want := range cases {
		if got := commaNum(in); got != want {
			t.Errorf("commaNum(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestOrdinal covers the teens, which are the whole reason this is a function
// and not a lookup on the last digit.
func TestOrdinal(t *testing.T) {
	cases := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th",
		11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd",
		101: "101st", 111: "111th",
	}
	for in, want := range cases {
		if got := ordinal(in); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short truncate = %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("line1\nline2", 100); got != "line1 line2" {
		t.Errorf("newline collapse = %q", got)
	}
}

func TestBoolMark(t *testing.T) {
	// Non-TTY in tests → color disabled → plain glyphs.
	noColorFlag = true
	defer func() { noColorFlag = false; colorReady = false }()
	colorReady = false
	if got := boolMark(true); got != "✓" {
		t.Errorf("boolMark(true) = %q", got)
	}
	if got := boolMark(false); got != "·" {
		t.Errorf("boolMark(false) = %q", got)
	}
}

func TestJSONNavHelpers(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","words":42,"locked":true,"tags":["a","b",3]},"items":[{"x":1},{"x":2}]}`)
	root := parseJSON(data)
	if root == nil {
		t.Fatal("parseJSON returned nil")
	}
	note := nested(root, "note")
	if note == nil {
		t.Fatal("nested(note) nil")
	}
	if str(note, "id") != "n1" {
		t.Errorf("str id = %q", str(note, "id"))
	}
	if num(note, "words") != 42 {
		t.Errorf("num words = %v", num(note, "words"))
	}
	if !boolean(note, "locked") {
		t.Error("boolean locked = false")
	}
	tags := toStringSlice(note["tags"])
	if len(tags) != 3 || tags[0] != "a" || tags[2] != "3" {
		t.Errorf("toStringSlice = %v", tags)
	}
	items := toSlice(root["items"])
	if len(items) != 2 || num(items[1], "x") != 2 {
		t.Errorf("toSlice = %v", items)
	}
}

func TestStrFormatsNumbersWithoutTrailingZero(t *testing.T) {
	m := map[string]any{"a": float64(5), "b": float64(5.5)}
	if str(m, "a") != "5" {
		t.Errorf("str int = %q", str(m, "a"))
	}
	if str(m, "b") != "5.5" {
		t.Errorf("str float = %q", str(m, "b"))
	}
	if str(m, "missing") != "" {
		t.Errorf("str missing = %q", str(m, "missing"))
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("abcdef123456", 6); got != "abcdef…" {
		t.Errorf("shortID = %q", got)
	}
	if got := shortID("abc", 6); got != "abc" {
		t.Errorf("shortID short = %q", got)
	}
}

// ===========================================================================
// Error rendering
// ===========================================================================

// TestRenderErrorJSONModeEmitsTheEnvelopeOnStderr covers the --json contract on
// the failure path: a script that parses this CLI should not have to switch to
// scraping English the moment something goes wrong. stdout stays empty either
// way, so a pipeline never sees the error as data.
func TestRenderErrorJSONModeEmitsTheEnvelopeOnStderr(t *testing.T) {
	jsonOutput = true
	defer func() { jsonOutput = false }()

	failure := &client.APIError{
		Code:      "plan_limit_reached",
		Message:   "You've reached your plan's limit of 3 notebooks.",
		Details:   map[string]any{"resource": "notebook", "used": "3", "limit": "3"},
		RequestID: "req_test",
		Status:    403,
	}

	var errOut string
	out := captureStdout(t, func() {
		errOut = captureStderr(t, func() { renderError(failure) })
	})
	if out != "" {
		t.Errorf("error leaked onto stdout: %q", out)
	}

	var decoded struct {
		Error struct {
			Code      string            `json:"code"`
			Message   string            `json:"message"`
			Details   map[string]string `json:"details"`
			RequestID string            `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(errOut), &decoded); err != nil {
		t.Fatalf("stderr is not JSON in --json mode: %v\n%s", err, errOut)
	}
	if decoded.Error.Code != "plan_limit_reached" || decoded.Error.Details["limit"] != "3" {
		t.Errorf("envelope lost the code or details: %+v", decoded.Error)
	}
	if decoded.Error.RequestID != "req_test" {
		t.Errorf("request_id = %q, want req_test", decoded.Error.RequestID)
	}
}

// TestRenderErrorJSONModeWrapsLocalErrors keeps --json uniform: an error raised
// by the CLI itself (a missing flag, no saved session) must be parseable too,
// or a script has to handle two shapes.
func TestRenderErrorJSONModeWrapsLocalErrors(t *testing.T) {
	jsonOutput = true
	defer func() { jsonOutput = false }()

	errOut := captureStderr(t, func() { renderError(errors.New("nothing to update")) })
	var decoded map[string]map[string]any
	if err := json.Unmarshal([]byte(errOut), &decoded); err != nil {
		t.Fatalf("local error is not JSON in --json mode: %v\n%s", err, errOut)
	}
	if decoded["error"]["message"] != "nothing to update" {
		t.Errorf("message lost: %v", decoded["error"])
	}
}

// TestRenderErrorPlainModeStaysHumanReadable is the counterweight: outside
// --json the operator still gets prose, not an envelope.
func TestRenderErrorPlainModeStaysHumanReadable(t *testing.T) {
	errOut := captureStderr(t, func() { renderError(apiErr("validation_failed")) })
	if !strings.HasPrefix(errOut, "Error: ") {
		t.Errorf("plain-mode error is not the human form:\n%s", errOut)
	}
	if strings.Contains(errOut, `"error"`) {
		t.Errorf("plain-mode error rendered as JSON:\n%s", errOut)
	}
}

// TestRenderErrorRoutesPlanLimitsToTheirOwnExplanation pins the branch in
// renderError itself: without it a plan limit would fall back to a bullet list
// of the gate's internals (gate, remediation_action, current…).
func TestRenderErrorRoutesPlanLimitsToTheirOwnExplanation(t *testing.T) {
	failure := &client.APIError{
		Code:    planLimitCode,
		Message: "You've reached your plan's limit of 3 notebooks.",
		Details: map[string]any{"resource": "notebook", "used": "3", "limit": "3", "gate": "plan_limit"},
		Status:  403,
	}
	errOut := captureStderr(t, func() { renderError(failure) })

	if !strings.Contains(errOut, "harbor usage") {
		t.Errorf("plan limit did not get the explanation:\n%s", errOut)
	}
	if strings.Contains(errOut, "gate: plan_limit") {
		t.Errorf("the gate's internals were dumped at the user:\n%s", errOut)
	}
}

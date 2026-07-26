// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"io"
	"os"
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

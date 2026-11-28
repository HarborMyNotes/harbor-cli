// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
)

func TestDisplayTags(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"t1","name":"Receipts","parent_id":"","usn":12,"updated_at":1750000000000},
		{"id":"t2","name":"2026","parent_id":"t1xxxxxx","usn":13,"updated_at":1750000000000}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displayTags(data) })
	if !strings.Contains(out, "Receipts") || !strings.Contains(out, "2026") {
		t.Errorf("tag names missing:\n%s", out)
	}
	if !strings.Contains(out, "(top-level)") {
		t.Errorf("top-level marker missing:\n%s", out)
	}
}

func TestDisplayTagDetail(t *testing.T) {
	data := []byte(`{"id":"t1","name":"Receipts","parent_id":"","usn":12,"updated_at":1750000000000,"created_at":1749000000000}`)
	out := captureStdout(t, func() { displayTag(data) })
	if !strings.Contains(out, "Receipts") || !strings.Contains(out, "(top-level)") {
		t.Errorf("tag detail missing fields:\n%s", out)
	}
}

func TestMapTagError(t *testing.T) {
	if got := mapTagError(apiErr("tag_name_exists")); !strings.Contains(got.Error(), "already exists") {
		t.Errorf("tag_name_exists = %q", got.Error())
	}
	if got := mapTagError(apiErr("tag_cycle")); !strings.Contains(got.Error(), "cycle") {
		t.Errorf("tag_cycle = %q", got.Error())
	}
}

// TestDisplayTagsShowsColorName covers the COLOR column: the palette name for a
// colored tag, blank for one with none.
func TestDisplayTagsShowsColorName(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"t1","name":"Urgent","parent_id":"","color":"#ef4444","usn":12,"updated_at":1750000000000},
		{"id":"t2","name":"Plain","parent_id":"","color":"","usn":13,"updated_at":1750000000000}
	],"paging":{"offset":0,"total":2}}`)
	out := captureStdout(t, func() { displayTags(data) })
	if !strings.Contains(out, "COLOR") || !strings.Contains(out, "red") {
		t.Errorf("color column missing:\n%s", out)
	}
	if strings.Contains(out, "#ef4444") {
		t.Errorf("table shows the hex instead of the name:\n%s", out)
	}
}

// TestDisplayTagDetailShowsColorName covers the Color row of `tags get`.
func TestDisplayTagDetailShowsColorName(t *testing.T) {
	out := captureStdout(t, func() {
		displayTag([]byte(`{"id":"t1","name":"Receipts","parent_id":"","color":"#1184f0"}`))
	})
	if !strings.Contains(out, "Color") || !strings.Contains(out, "harbor-blue") {
		t.Errorf("color row missing:\n%s", out)
	}
}

// TestTagsCreateSendsColor covers a name turned into the stored hex.
func TestTagsCreateSendsColor(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/tags": {Status: 201, Body: `{"id":"t1","name":"Urgent","color":"#ef4444"}`},
	})
	if _, err := runCLI(t, m, "tags", "create", "--name", "Urgent", "--color", "Red"); err != nil {
		t.Fatalf("tags create: %v", err)
	}
	if got := m.rawBodyOf(t, "POST /api/v1/tags"); got != `{"color":"#ef4444","name":"Urgent"}` {
		t.Errorf("body = %s", got)
	}
}

// TestTagsCreateWithoutColorSendsNoColorKey proves a create without --color
// leaves the key out.
func TestTagsCreateWithoutColorSendsNoColorKey(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/tags": {Status: 201, Body: `{"id":"t1","name":"Plain"}`},
	})
	if _, err := runCLI(t, m, "tags", "create", "--name", "Plain"); err != nil {
		t.Fatalf("tags create: %v", err)
	}
	if _, ok := m.bodyOf(t, "POST /api/v1/tags")["color"]; ok {
		t.Errorf("a create without --color sent a color key")
	}
}

// TestTagsUpdateRenameSendsNoColorKey proves a rename leaves the color key out,
// so it can never clear a stored color.
func TestTagsUpdateRenameSendsNoColorKey(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/tags/t1": {Status: 200, Body: `{"id":"t1","name":"Renamed","color":"#ef4444"}`},
	})
	if _, err := runCLI(t, m, "tags", "update", "t1", "--name", "Renamed"); err != nil {
		t.Fatalf("tags update: %v", err)
	}
	if got := m.rawBodyOf(t, "PATCH /api/v1/tags/t1"); got != `{"name":"Renamed"}` {
		t.Errorf("body = %s", got)
	}
}

// TestTagsUpdateColorOnly covers --color as the only flag, by hex and by none.
func TestTagsUpdateColorOnly(t *testing.T) {
	for in, want := range map[string]string{
		"#A855F7": `{"color":"#a855f7"}`,
		"none":    `{"color":""}`,
	} {
		m := newAPIMock(t, map[string]mockReply{
			"PATCH /api/v1/tags/t1": {Status: 200, Body: `{"id":"t1","name":"Receipts"}`},
		})
		if _, err := runCLI(t, m, "tags", "update", "t1", "--color", in); err != nil {
			t.Fatalf("tags update --color %s: %v", in, err)
		}
		if got := m.rawBodyOf(t, "PATCH /api/v1/tags/t1"); got != want {
			t.Errorf("--color %s: body = %s, want %s", in, got, want)
		}
	}
}

// TestTagsBadColorFailsBeforeAnyRequest covers a value outside the palette on
// both create and update: no request is sent, and the error lists the names.
func TestTagsBadColorFailsBeforeAnyRequest(t *testing.T) {
	for _, args := range [][]string{
		{"tags", "create", "--name", "Urgent", "--color", "blue"},
		{"tags", "update", "t1", "--name", "Renamed", "--color", "blue"},
	} {
		m := newAPIMock(t, map[string]mockReply{})
		_, err := runCLI(t, m, args...)
		if err == nil || !strings.Contains(err.Error(), "harbor-blue") {
			t.Errorf("%v: err = %v", args, err)
		}
		if calls := m.calls(); len(calls) != 0 {
			t.Errorf("%v: a bad color still sent %v", args, calls)
		}
	}
}

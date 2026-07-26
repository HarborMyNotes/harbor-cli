// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

func TestRunSyncPullPagesAllChunks(t *testing.T) {
	// Two chunks: first has_more=true (usn 1,2), second has_more=false (usn 3).
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		calls++
		if calls == 1 {
			if int(body["after_usn"].(float64)) != 0 {
				t.Errorf("first after_usn = %v", body["after_usn"])
			}
			_, _ = w.Write([]byte(`{"scope_id":"u1","scope_max_usn":3,"has_more":true,"chunk":[{"type":"note","id":"n1","usn":1},{"type":"tag","id":"t1","usn":2}]}`))
			return
		}
		if int(body["after_usn"].(float64)) != 2 {
			t.Errorf("second after_usn = %v, want 2", body["after_usn"])
		}
		_, _ = w.Write([]byte(`{"scope_id":"u1","scope_max_usn":3,"has_more":false,"chunk":[{"type":"note","id":"n2","usn":3}]}`))
	}))
	defer srv.Close()

	c := client.NewClient(srv.URL, "at_test")
	out, err := runSyncPull(c, "u1", "d1", 0, 0, true)
	if err != nil {
		t.Fatalf("runSyncPull error: %v", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2", calls)
	}
	var merged map[string]any
	_ = json.Unmarshal(out, &merged)
	chunk, _ := merged["chunk"].([]any)
	if len(chunk) != 3 {
		t.Errorf("merged chunk = %d, want 3", len(chunk))
	}
	if merged["has_more"] != false {
		t.Errorf("merged has_more = %v, want false", merged["has_more"])
	}
}

func TestRunSyncPullSingleChunkWithoutAll(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"scope_id":"u1","scope_max_usn":9,"has_more":true,"chunk":[{"type":"note","id":"n1","usn":1}]}`))
	}))
	defer srv.Close()
	c := client.NewClient(srv.URL, "at_test")
	out, err := runSyncPull(c, "u1", "", 0, 0, false)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no paging without --all)", calls)
	}
	var merged map[string]any
	_ = json.Unmarshal(out, &merged)
	if merged["has_more"] != true {
		t.Error("has_more should be preserved as true when not paging")
	}
}

func TestMaxChunkUSN(t *testing.T) {
	chunk := []json.RawMessage{
		json.RawMessage(`{"usn":3}`),
		json.RawMessage(`{"usn":7}`),
		json.RawMessage(`{"usn":5}`),
	}
	if got := maxChunkUSN(chunk, 0); got != 7 {
		t.Errorf("maxChunkUSN = %d, want 7", got)
	}
	if got := maxChunkUSN(nil, 42); got != 42 {
		t.Errorf("maxChunkUSN(empty) = %d, want fallback 42", got)
	}
}

func TestReadChangesAcceptsArrayAndObject(t *testing.T) {
	dir := t.TempDir()
	arrayFile := dir + "/arr.json"
	if err := os.WriteFile(arrayFile, []byte(`[{"type":"note","id":"n1","change_id":"c1"}]`), 0644); err != nil {
		t.Fatal(err)
	}
	ch, err := readChanges(arrayFile)
	if err != nil || len(ch) != 1 {
		t.Fatalf("array form: ch=%v err=%v", ch, err)
	}

	objFile := dir + "/obj.json"
	if err := os.WriteFile(objFile, []byte(`{"changes":[{"type":"tag","id":"t1","change_id":"c2"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	ch2, err := readChanges(objFile)
	if err != nil || len(ch2) != 1 {
		t.Fatalf("object form: ch=%v err=%v", ch2, err)
	}
}

func TestDisplaySyncPushAndDevices(t *testing.T) {
	push := []byte(`{"scope_max_usn":97,"results":[
		{"change_id":"c-7f1a","id":"n1","type":"note","status":"applied","new_usn":96},
		{"change_id":"c-9a2b","id":"n2","type":"note","status":"conflict","server_record":{"id":"n2"}}
	]}`)
	out := captureStdout(t, func() { displaySyncPush(push) })
	if !strings.Contains(out, "applied") || !strings.Contains(out, "conflict") {
		t.Errorf("push results missing statuses:\n%s", out)
	}

	devices := []byte(`{"data":[{"device_id":"d1","name":"iPhone","platform":"ios","last_seen":1750000000000,"last_acked_usn":95,"stale":false}],"scope_max_usn":97,"gc_floor":95}`)
	out2 := captureStdout(t, func() { displaySyncDevices(devices) })
	if !strings.Contains(out2, "gc_floor: 95") {
		t.Errorf("devices footer missing:\n%s", out2)
	}
}

func TestMapSyncError(t *testing.T) {
	if got := mapSyncError(apiErr("resync_required")); !strings.Contains(got.Error(), "full sync") {
		t.Errorf("resync_required = %q", got.Error())
	}
	if got := mapSyncError(apiErr("scope_forbidden")); !strings.Contains(got.Error(), "scope") {
		t.Errorf("scope_forbidden = %q", got.Error())
	}
}

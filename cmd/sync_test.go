// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// ===========================================================================
// A push the server refused (#69 F3)
// ===========================================================================
//
// sync push is the only write in the CLI that cannot fail loudly: the endpoint
// answers 200 and reports each change's outcome inside the body, so before this
// a push where every change was refused exited 0 and a wrapper script could not
// tell it apart from a clean push.

// TestSyncPushRejectionFlagsAPlanLimit pins both halves of a plan-limit
// refusal: it is an error at all, and it carries the code that tells a script
// retrying will never help.
func TestSyncPushRejectionFlagsAPlanLimit(t *testing.T) {
	t.Setenv("HARBOR_API_URL", "https://harbor.example/api/v1")
	body := []byte(`{"results":[
	  {"change_id":"c1","type":"note","id":"n1","status":"applied","new_usn":11},
	  {"change_id":"c2","type":"note","id":"n2","status":"rejected","error":"plan_limit_reached"}
	],"scope_max_usn":11}`)

	err := syncPushRejection(body)
	if err == nil {
		t.Fatal("a refused change must be an error, not a silent success")
	}
	if got := exitCodeFor(err); got != exitPlanLimit {
		t.Errorf("exit code = %d, want %d (plan limit)", got, exitPlanLimit)
	}
	msg := err.Error()
	for _, want := range []string{"refused 1 of the 2 changes", "plan limit", "harbor usage", "https://harbor.example/settings/plan"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}

	// It has to render and JSON-encode like every other failure, which is what
	// the wrapped envelope buys.
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("the error must unwrap to an *client.APIError so it renders like any other")
	}
	if apiErr.Code != syncPushRejectedCode {
		t.Errorf("code = %q, want %q", apiErr.Code, syncPushRejectedCode)
	}
	if got := apiErr.Details["c2"]; got != "plan_limit_reached" {
		t.Errorf("details must name the refused change by its change_id: %v", apiErr.Details)
	}
}

// TestSyncPushRejectionWithoutAPlanLimitIsAnOrdinaryFailure keeps the dedicated
// code honest: 4 means "your account said no", so any other refusal is a 1.
func TestSyncPushRejectionWithoutAPlanLimitIsAnOrdinaryFailure(t *testing.T) {
	body := []byte(`{"results":[{"change_id":"c1","type":"note","id":"n1","status":"rejected","error":"note_too_large"}]}`)
	err := syncPushRejection(body)
	if err == nil {
		t.Fatal("a refused change must be an error")
	}
	if got := exitCodeFor(err); got != exitError {
		t.Errorf("exit code = %d, want %d", got, exitError)
	}
	if !strings.Contains(err.Error(), "refused 1 of the 1 change") {
		t.Errorf("message should count the refusals: %q", err.Error())
	}
}

// TestSyncPushRejectionIgnoresAppliedAndConflicted is the other half of the
// contract. A conflict is not a refusal: the change did not apply, but the
// server handed back the record needed to resolve it, which is a routine step
// of the sync protocol rather than a failed command.
func TestSyncPushRejectionIgnoresAppliedAndConflicted(t *testing.T) {
	cases := map[string]string{
		"all applied": `{"results":[{"change_id":"c1","status":"applied","new_usn":11}]}`,
		"a conflict":  `{"results":[{"change_id":"c1","status":"conflict","server_record":{"id":"n1"}}]}`,
		"no results":  `{"results":[],"scope_max_usn":11}`,
		"not JSON":    `<html>nope</html>`,
	}
	for name, body := range cases {
		if err := syncPushRejection([]byte(body)); err != nil {
			t.Errorf("%s: want no error, got %v", name, err)
		}
	}
}

// TestSyncPushCommandExitsNonZeroAndStillPrintsResults is the end-to-end
// regression: the whole command, not the helper. The results have to stay on
// stdout — they are how a client learns which USNs landed and which changes to
// re-resolve — while the exit code says the push did not fully succeed.
func TestSyncPushCommandExitsNonZeroAndStillPrintsResults(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/sync/push": {Status: 200, Body: `{"scope_max_usn":11,"results":[
		  {"change_id":"c1","type":"note","id":"n1","status":"applied","new_usn":11},
		  {"change_id":"c2","type":"note","id":"n2","status":"rejected","error":"plan_limit_reached"}
		]}`},
	})
	file := t.TempDir() + "/changes.json"
	if err := os.WriteFile(file, []byte(`[{"type":"note","id":"n2","change_id":"c2","record":{"id":"n2"}}]`), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, m, "sync", "push", "--file", file, "--device-id", "cli-test", "--scope-id", "u1")
	if err == nil {
		t.Fatal("a push the server refused must not exit 0")
	}
	if got := exitCodeFor(err); got != exitPlanLimit {
		t.Errorf("exit code = %d, want %d", got, exitPlanLimit)
	}
	if !strings.Contains(out, "rejected") || !strings.Contains(out, "c2") {
		t.Errorf("the per-change results must still be printed — a client reconciles from them:\n%s", out)
	}
}

// TestSyncPushCommandExitsZeroWhenEverythingApplied guards the other direction:
// the new exit code must not turn a clean push into a failure.
func TestSyncPushCommandExitsZeroWhenEverythingApplied(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/sync/push": {Status: 200, Body: `{"scope_max_usn":11,"results":[{"change_id":"c1","type":"note","id":"n1","status":"applied","new_usn":11}]}`},
	})
	file := t.TempDir() + "/changes.json"
	if err := os.WriteFile(file, []byte(`[{"type":"note","id":"n1","change_id":"c1","record":{"id":"n1"}}]`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, m, "sync", "push", "--file", file, "--device-id", "cli-test", "--scope-id", "u1"); err != nil {
		t.Fatalf("a clean push must exit 0: %v", err)
	}
}

// TestWarnDefaultNotebookEncrypt proves the push path says something when a
// pushed notebook record carries the banned pair, and stays quiet otherwise.
//
// It warns rather than refuses because sync push COERCES server-side (unlike
// PATCH /notebooks/:id, which 422s): the record lands with default_encrypt
// forced off and comes back corrected on the next pull. Refusing would reject a
// batch the server would have accepted; silence would let a flag the user set
// disappear without explanation.
func TestWarnDefaultNotebookEncrypt(t *testing.T) {
	banned := []any{map[string]any{
		"type": "notebook", "id": "nb1",
		"record": map[string]any{"id": "nb1", "is_default": true, "default_encrypt": true},
	}}
	out := captureStderr(t, func() { warnDefaultNotebookEncrypt(banned) })
	for _, want := range []string{"default", "force", "next pull"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning does not mention %q:\n%s", want, out)
		}
	}

	quiet := []any{
		map[string]any{"type": "notebook", "id": "nb2",
			"record": map[string]any{"id": "nb2", "is_default": true, "default_encrypt": false}},
		map[string]any{"type": "notebook", "id": "nb3",
			"record": map[string]any{"id": "nb3", "is_default": false, "default_encrypt": true}},
		map[string]any{"type": "note", "id": "n1",
			"record": map[string]any{"id": "n1", "is_default": true, "default_encrypt": true}},
		map[string]any{"type": "notebook", "id": "nb4"}, // no record at all
		"not an envelope",
	}
	if out := captureStderr(t, func() { warnDefaultNotebookEncrypt(quiet) }); out != "" {
		t.Errorf("warned about a legal push:\n%s", out)
	}
}

// TestWarnDefaultNotebookEncryptSpeaksOnce proves a batch full of offending
// notebooks produces one warning, not one per record — a push is a batch, and a
// wall of identical warnings is how people learn to scroll past them.
func TestWarnDefaultNotebookEncryptSpeaksOnce(t *testing.T) {
	var changes []any
	for i := 0; i < 5; i++ {
		changes = append(changes, map[string]any{
			"type": "notebook", "id": "nb",
			"record": map[string]any{"id": "nb", "is_default": true, "default_encrypt": true},
		})
	}
	out := captureStderr(t, func() { warnDefaultNotebookEncrypt(changes) })
	if n := strings.Count(out, "cannot store that pair"); n != 1 {
		t.Errorf("warned %d times for one batch, want 1:\n%s", n, out)
	}
}

// TestNoNotebookRecordsAreConstructedByTheCLI is the CLI's whole "the sync engine
// cannot produce the banned pair" obligation, discharged structurally.
//
// The CLI keeps no offline queue: `sync push` forwards a JSON file the user
// wrote, and the only sync record the CLI builds itself is the crypto keystore.
// So there is no client-side state that could hold a default notebook with
// default_encrypt on. This test fails the day that stops being true — if someone
// adds code that constructs a "notebook" sync record, the guarantee needs
// rethinking rather than silently lapsing.
func TestNoNotebookRecordsAreConstructedByTheCLI(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// The keystore record in crypto.go is the one the CLI legitimately builds.
		for _, banned := range []string{`"type":      "notebook"`, `"type": "notebook"`} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s constructs a notebook sync record — the CLI's 'no local state can hold the banned pair' guarantee no longer holds by construction", f)
			}
		}
	}
}

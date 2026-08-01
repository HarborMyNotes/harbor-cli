// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-31

package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===========================================================================
// The regression: `notes update --content` used to delete the note's tasks
// ===========================================================================
//
// These run the REAL command tree against a stub API (runCLI/apiMock in
// root_test.go) and assert on the traffic, because the only thing that made
// issue #62 a data-loss bug was a request the command sent. A test that reads the
// source, or that calls the guard directly, would still pass if `notes update`
// stopped calling the guard at all — which is precisely the failure being
// guarded against. Every one of these goes red against the code before the fix:
// there, the PATCH is sent and the command exits 0.

const (
	// fixtureTaskID is the task the note fixture carries. It has to be a canonical
	// hyphenated UUID: any other spelling references no task the server's
	// reconciler can resolve, so the guard would rightly ignore it.
	fixtureTaskID  = "a1b2c3d4-11e5-4f11-8a11-1234567890ab"
	fixtureTaskID2 = "b2c3d4e5-22f6-4a22-9b22-abcdef123456"
)

// noteWithTaskRoutes is a note that owns one task, in both the places that
// matter: a <harbor-task> block in its body, and a row in its task list.
func noteWithTaskRoutes() map[string]mockReply {
	return map[string]mockReply{
		"GET /api/v1/notes/n1": {Status: 200, Body: `{"id":"n1","title":"Plan","usn":42,"is_encrypted":false,` +
			`"content":"<p>the plan</p><harbor-task id=\"` + fixtureTaskID + `\"></harbor-task>"}`},
		"GET /api/v1/notes/n1/tasks": {Status: 200, Body: `{"data":[{"id":"` + fixtureTaskID + `","title":"Buy milk"}],` +
			`"paging":{"limit":500,"offset":0,"total":1,"has_more":false}}`},
		"PATCH /api/v1/notes/n1": {Status: 200, Body: `{"note":{"id":"n1","title":"Plan","usn":43},"usn":43}`},
	}
}

// assertNoWrite fails the test when a note write reached the server. This is the
// assertion that actually encodes "the task still exists": the CLI cannot query
// the task afterwards through a stub, but the ONLY thing that would have deleted
// it is this PATCH, so proving the PATCH never happened proves the task survived.
func assertNoWrite(t *testing.T, m *apiMock) {
	t.Helper()
	for _, call := range m.calls() {
		if strings.HasPrefix(call, "PATCH ") {
			t.Fatalf("the note was written after the guard refused — the task is gone; traffic: %v", m.calls())
		}
	}
}

// A whole-body replacement that drops the note's task block must be refused with
// nothing written, and must say which task it was about to delete.
func TestNotesUpdateRefusesToDeleteTasksWithTheBody(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")

	if err == nil {
		t.Fatal("notes update --content dropped a task block and still succeeded — this is issue #62")
	}
	assertNoWrite(t, m)
	msg := err.Error()
	for _, want := range []string{"1 task", "Buy milk", fixtureTaskID, "--keep-tasks", "--allow-task-loss"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal never mentions %q:\n%s", want, msg)
		}
	}
}

// --keep-tasks writes the new body with the dropped block re-appended, so the
// server's reconciler still sees the task referenced and keeps it.
func TestNotesUpdateKeepTasksCarriesTheBlockIntoTheNewBody(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	var err error
	errOut := captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten", "--keep-tasks")
	})
	if err != nil {
		t.Fatalf("notes update --keep-tasks: %v", err)
	}

	sent, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["content"].(string)
	if !strings.Contains(sent, taskBlockHTML(fixtureTaskID)) {
		t.Errorf("--keep-tasks wrote a body with no task block, so the task dies anyway:\n%s", sent)
	}
	if !strings.Contains(sent, "# Rewritten") {
		t.Errorf("--keep-tasks lost the content the user asked for:\n%s", sent)
	}
	if !strings.Contains(errOut, "Kept 1 task") {
		t.Errorf("keeping tasks happened silently; stderr was:\n%s", errOut)
	}
}

// --keep-tasks must not report success while deleting the task. Appending a block
// onto content that ends inside an unterminated ``` fence puts the block INSIDE the
// fence: the Markdown converter escapes it to text, the server reads back no
// reference, and the task is tombstoned — by the very flag asked to save it. The
// remedy the refusal message recommends must not be able to destroy what it exists
// to protect, so the assembled body is re-scanned and a body that swallows the
// blocks is refused with nothing written.
func TestNotesUpdateKeepTasksRefusesWhenTheBodySwallowsTheBlocks(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1", "--keep-tasks",
		"--content", "# Notes\n\nHere is some code:\n\n```go\nfunc main() {}\n")

	if err == nil {
		t.Fatal("--keep-tasks appended a block into an unterminated fence and reported success — the task is deleted")
	}
	assertNoWrite(t, m)
	for _, want := range []string{"could not save", "Buy milk", "close the fence", "--allow-task-loss"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never mentions %q:\n%s", want, err)
		}
	}
}

// The same post-condition in HTML mode. Each of these bodies ends inside something
// that swallows what follows, so the appended block becomes TEXT and the server
// reads back no reference. Every one was MEASURED against a real server deleting the
// task while the CLI exited 0 — they are recorded failures, not hypotheticals.
func TestNotesUpdateKeepTasksRefusesWhenHTMLSwallowsTheBlocks(t *testing.T) {
	cases := map[string]string{
		"unclosed comment":  "<p>new</p><!-- todo: finish this",
		"unclosed textarea": "<p>new</p><textarea>",
		"unclosed script":   "<p>new</p><script>",
		"unclosed style":    "<p>new</p><style>",
		"unclosed title":    "<p>new</p><title>",
		"truncated tag":     "<p>new</p><div",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			m := newAPIMock(t, noteWithTaskRoutes())

			_, err := runCLI(t, m, "notes", "update", "n1", "--keep-tasks", "--format", "html", "--content", content)

			if err == nil {
				t.Fatal("--keep-tasks reported success on a body that swallows the block — the task is deleted")
			}
			assertNoWrite(t, m)
			if !strings.Contains(err.Error(), "could not save") {
				t.Errorf("wrong refusal:\n%s", err)
			}
		})
	}
}

// The post-condition must not fire on ordinary content — a body that ends in a
// CLOSED fence is fine, and refusing it would make --keep-tasks useless.
func TestNotesUpdateKeepTasksStillWorksAfterAClosedFence(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1", "--keep-tasks",
		"--content", "# Notes\n\n```go\nfunc main() {}\n```\n")
	if err != nil {
		t.Fatalf("a closed fence was wrongly refused: %v", err)
	}
	sent, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["content"].(string)
	if !strings.Contains(sent, taskBlockHTML(fixtureTaskID)) {
		t.Errorf("the block was dropped from a perfectly good body:\n%s", sent)
	}
}

// --allow-task-loss is the escape hatch: it proceeds with exactly the body the
// user wrote, but says on stderr what it is deleting. The notice goes to stderr
// so it cannot corrupt a piped --json stdout.
func TestNotesUpdateAllowTaskLossProceedsAndWarns(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	var err error
	errOut := captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten", "--allow-task-loss")
	})
	if err != nil {
		t.Fatalf("notes update --allow-task-loss: %v", err)
	}

	sent, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["content"].(string)
	if sent != "# Rewritten" {
		t.Errorf("--allow-task-loss must write the body verbatim, got %q", sent)
	}
	if !strings.Contains(errOut, "Deleting 1 task") {
		t.Errorf("the deletion was not announced; stderr was:\n%s", errOut)
	}
}

// The two flags ask for opposite things. Taking both is a mistake worth catching
// before anything is read or written, not a silent precedence rule.
func TestNotesUpdateRejectsBothTaskFlagsTogether(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1", "--content", "x", "--keep-tasks", "--allow-task-loss")

	if err == nil {
		t.Fatal("--keep-tasks with --allow-task-loss was accepted")
	}
	if len(m.calls()) != 0 {
		t.Errorf("contradictory flags still hit the API: %v", m.calls())
	}
}

// A new body that carries the block forward is not a loss, so it proceeds
// untouched — the guard must not turn every content update into a refusal.
func TestNotesUpdateProceedsWhenTheNewBodyCarriesTheBlock(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1",
		"--format", "html", "--content", `<p>new</p>`+taskBlockHTML(fixtureTaskID))
	if err != nil {
		t.Fatalf("an update that kept the block was refused: %v", err)
	}
	sent, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["content"].(string)
	if !strings.Contains(sent, fixtureTaskID) {
		t.Errorf("the body reached the server without its block: %q", sent)
	}
}

// The guard reads the note it is about to overwrite and sends that note's usn as
// a base_usn precondition, so a task attached between the read and the write is
// refused by the server (409 note_usn_stale) instead of being destroyed by a body
// that predates it — app.harbor.my#1153.
func TestNotesUpdateSendsTheBaseUSNPrecondition(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1",
		"--format", "html", "--content", `<p>new</p>`+taskBlockHTML(fixtureTaskID))
	if err != nil {
		t.Fatalf("notes update: %v", err)
	}
	if got, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["base_usn"].(float64); got != 42 {
		t.Errorf("base_usn = %v, want the usn the guard read (42); without it the read-then-write race deletes tasks", got)
	}
}

// A metadata-only update cannot release anything, so it must not pay for the
// guard's read — and must not send a precondition the user never asked for.
func TestNotesUpdateWithoutContentIsNotGuarded(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/notes/n1": {Status: 200, Body: `{"note":{"id":"n1","usn":43},"usn":43}`},
	})

	if _, err := runCLI(t, m, "notes", "update", "n1", "--notebook", "nb2"); err != nil {
		t.Fatalf("metadata-only update: %v", err)
	}
	if len(m.calls()) != 1 {
		t.Errorf("a metadata-only update read the note it never replaces: %v", m.calls())
	}
	if _, sent := m.bodyOf(t, "PATCH /api/v1/notes/n1")["base_usn"]; sent {
		t.Error("base_usn was sent for an update that carries no body")
	}
}

// The task list is the authoritative at-risk set: it is the same query the
// server's reconciler releases by (note_id = :id AND deleted = 0). So a task
// LINKED to the note but no longer referenced by its body — the app.harbor.my
// #1094 orphan — is protected too, even though the body says nothing about it.
func TestNotesUpdateGuardsATaskTheBodyNoLongerReferences(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes/n1": {Status: 200, Body: `{"id":"n1","usn":42,"content":"<p>no blocks here</p>"}`},
		"GET /api/v1/notes/n1/tasks": {Status: 200, Body: `{"data":[{"id":"` + fixtureTaskID + `","title":"Orphan"}],` +
			`"paging":{"total":1,"has_more":false}}`},
		"PATCH /api/v1/notes/n1": {Status: 200, Body: `{"note":{"id":"n1"},"usn":43}`},
	})

	_, err := runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")

	if err == nil {
		t.Fatal("a task linked to the note was deleted without a word")
	}
	assertNoWrite(t, m)
	if !strings.Contains(err.Error(), "Orphan") {
		t.Errorf("the refusal never names the task:\n%s", err)
	}
}

// If the task list cannot be read, the guard falls back to the blocks in the
// note's own body rather than downgrading to no protection at all. The task is
// then named by id, because the title lived in the response that failed.
func TestNotesUpdateFallsBackToTheBodyWhenTheTaskListFails(t *testing.T) {
	routes := noteWithTaskRoutes()
	routes["GET /api/v1/notes/n1/tasks"] = mockReply{Status: 500, Body: apiErrorBody("internal", "boom")}
	m := newAPIMock(t, routes)

	_, err := runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")

	if err == nil {
		t.Fatal("an unreadable task list silently disabled the guard")
	}
	assertNoWrite(t, m)
	if !strings.Contains(err.Error(), fixtureTaskID) {
		t.Errorf("the refusal never names the task id:\n%s", err)
	}
}

// ===========================================================================
// Issue #67: the guard used to read ONE page of the note's task list
// ===========================================================================
//
// Everything above holds for a note whose tasks fit in a single 500-row page,
// which is why the hole stayed invisible. These four run the same command tree
// against a note whose list needs two pages, and the state that matters is the
// one the body cannot describe: a task LINKED to the note and absent from its
// body, sitting past the cap. Against the single-page read, the first of these
// writes the note, exits 0, and the task is gone.

// strandedTaskID is that task: on page 2 of the note's task list, referenced by
// nothing in the note's body, and therefore visible to the guard through exactly
// one source — the page a single-page read never asks for.
const strandedTaskID = "c3d4e5f6-33a7-4b33-8c33-fedcba654321"

// pagedTaskListMock serves a note with more tasks than one page holds. Page 1 is a
// full page of tasks the note's body DOES reference; page 2 carries only
// strandedTaskID, which it does not. page2 overrides the second page's reply, for
// the tests about a walk that cannot finish.
func pagedTaskListMock(t *testing.T, page2 *mockReply) *apiMock {
	t.Helper()
	rows := make([]string, 0, collectionPageSize)
	blocks := make([]string, 0, collectionPageSize)
	for i := 0; i < collectionPageSize; i++ {
		id := fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i)
		rows = append(rows, fmt.Sprintf(`{"id":%q,"title":"task %d"}`, id, i))
		blocks = append(blocks, taskBlockHTML(id))
	}
	note := fmt.Sprintf(`{"id":"n1","usn":42,"is_encrypted":false,"content":%q}`,
		"<p>the plan</p>"+strings.Join(blocks, ""))
	first := fmt.Sprintf(`{"data":[%s],"paging":{"limit":500,"offset":0,"total":%d,"has_more":true}}`,
		strings.Join(rows, ","), collectionPageSize+1)
	second := fmt.Sprintf(`{"data":[{"id":%q,"title":"Stranded"}],`+
		`"paging":{"limit":500,"offset":500,"total":%d,"has_more":false}}`, strandedTaskID, collectionPageSize+1)

	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/notes/n1/tasks" && r.URL.Query().Get("offset") == "500":
			if page2 != nil {
				w.WriteHeader(page2.Status)
				_, _ = w.Write([]byte(page2.Body))
				return
			}
			_, _ = w.Write([]byte(second))
		case r.URL.Path == "/api/v1/notes/n1/tasks":
			_, _ = w.Write([]byte(first))
		case r.URL.Path == "/api/v1/notes/n1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(note))
		case r.URL.Path == "/api/v1/notes/n1" && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"note":{"id":"n1","usn":43},"usn":43}`))
		default:
			t.Errorf("pagedTaskListMock: unrouted request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return m
}

// THE #67 REGRESSION. --keep-tasks has to carry the block for a task on page 2, or
// the server's reconciler sees no reference and tombstones it — while the CLI
// reports "Kept 500 tasks" and exits 0. Against the single-page read this fails:
// the written body carries 500 blocks and the 501st task is deleted.
func TestNotesUpdateKeepTasksCarriesATaskPastTheFirstPage(t *testing.T) {
	m := pagedTaskListMock(t, nil)

	_, err := runCLI(t, m, "notes", "update", "n1", "--format", "html",
		"--content", "<p>Rewritten</p>", "--keep-tasks")
	if err != nil {
		t.Fatalf("notes update --keep-tasks: %v", err)
	}

	sent, _ := m.bodyOf(t, "PATCH /api/v1/notes/n1")["content"].(string)
	if !strings.Contains(sent, taskBlockHTML(strandedTaskID)) {
		t.Errorf("--keep-tasks wrote a body carrying no block for the task on page 2 of the "+
			"task list, so the server deletes it — and the CLI said it kept everything (issue #67). "+
			"Body was %d bytes, %d blocks", len(sent), strings.Count(sent, "<harbor-task"))
	}
}

// The refusal has to account for the whole list too. Naming the 500 tasks it
// happened to read and staying silent about the 501st is a report the user cannot
// act on — they would --keep-tasks, be told it worked, and lose the task anyway.
func TestNotesUpdateRefusalNamesATaskPastTheFirstPage(t *testing.T) {
	m := pagedTaskListMock(t, nil)

	_, err := runCLI(t, m, "notes", "update", "n1", "--format", "html", "--content", "<p>Rewritten</p>")

	if err == nil {
		t.Fatal("a whole-body write dropped 501 task blocks and still succeeded")
	}
	assertNoWrite(t, m)
	if !strings.Contains(err.Error(), strandedTaskID) {
		t.Error("the refusal accounts for page 1 and says nothing about the task on page 2 (issue #67)")
	}
}

// A short read is not a clean read. When the walk cannot finish, the at-risk set is
// KNOWN to be missing rows, so there is nothing honest to compare the new content
// against — the update is refused rather than measured against a partial list.
// --keep-tasks cannot rescue it either: it can only carry what it was told about.
func TestNotesUpdateRefusesWhenTheTaskListCannotBeReadWhole(t *testing.T) {
	m := pagedTaskListMock(t, &mockReply{Status: 500, Body: apiErrorBody("internal", "boom")})

	_, err := runCLI(t, m, "notes", "update", "n1", "--format", "html",
		"--content", "<p>Rewritten</p>", "--keep-tasks")

	if err == nil {
		t.Fatal("the guard ran against a task list it could not finish reading, and wrote the note")
	}
	assertNoWrite(t, m)
	for _, want := range []string{"in full", "--allow-task-loss"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never mentions %q:\n%s", want, err)
		}
	}
}

// --allow-task-loss still means "delete them, that is what I meant" — including the
// ones the CLI could not enumerate. It is the way past a list that will not read.
func TestNotesUpdateAllowTaskLossProceedsThroughAShortRead(t *testing.T) {
	m := pagedTaskListMock(t, &mockReply{Status: 500, Body: apiErrorBody("internal", "boom")})

	var err error
	captureStderr(t, func() {
		_, err = runCLI(t, m, "notes", "update", "n1", "--format", "html",
			"--content", "<p>Rewritten</p>", "--allow-task-loss")
	})
	if err != nil {
		t.Fatalf("--allow-task-loss was blocked by a partial task list: %v", err)
	}
	m.bodyOf(t, "PATCH /api/v1/notes/n1") // fails the test if the write never happened
}

// Paging costs the common note NOTHING. A first page the server marks
// `has_more: false` ends the walk, so the ordinary update pays the single request
// it paid before — which is the objection that kept #65 on one page.
func TestNotesUpdateReadsOnePageWhenTheServerSaysThereIsNoMore(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1",
		"--format", "html", "--content", `<p>new</p>`+taskBlockHTML(fixtureTaskID))
	if err != nil {
		t.Fatalf("notes update: %v", err)
	}
	reads := 0
	for _, call := range m.calls() {
		if call == "GET /api/v1/notes/n1/tasks" {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("the task list was read %d times, want 1: %v", reads, m.calls())
	}
}

// If the note itself cannot be read the guard fails CLOSED. Proceeding would run
// the write with the protection silently switched off, which is the bug.
func TestNotesUpdateRefusesWhenTheNoteCannotBeRead(t *testing.T) {
	routes := noteWithTaskRoutes()
	routes["GET /api/v1/notes/n1"] = mockReply{Status: 200, Body: `not json at all`}
	m := newAPIMock(t, routes)

	_, err := runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")

	if err == nil {
		t.Fatal("the note was overwritten without ever being read")
	}
	assertNoWrite(t, m)
}

// --file and --stdin replace the body exactly as --content does, so the guard has
// to sit behind all three. Wiring it to one flag would leave two open doors.
func TestNotesUpdateGuardsFileContentToo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.md")
	if err := os.WriteFile(path, []byte("# From a file"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1", "--file", path)

	if err == nil {
		t.Fatal("--file replaced the body and took the task with it")
	}
	assertNoWrite(t, m)
}

// An encrypted note is skipped: the server cannot parse ciphertext, so it never
// reconciles task blocks out of one and this write releases nothing. The proof is
// that the task list is never consulted — the guard returned before that. (The
// command then stops for an unrelated reason: no passphrase is set, so the CLI
// refuses to write plaintext over ciphertext.)
func TestNotesUpdateDoesNotGuardAnEncryptedNote(t *testing.T) {
	t.Setenv("HARBOR_PASSPHRASE", "")
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes/n1": {Status: 200, Body: `{"id":"n1","usn":42,"is_encrypted":true,"content":"HRBC2.…"}`},
	})

	_, err := runCLI(t, m, "notes", "update", "n1", "--content", "# Rewritten")

	if err == nil || !strings.Contains(err.Error(), "HARBOR_PASSPHRASE") {
		t.Fatalf("want the encrypted-note refusal, got %v", err)
	}
	for _, call := range m.calls() {
		if strings.HasSuffix(call, "/tasks") {
			t.Errorf("an encrypted note was scanned for task blocks: %v", m.calls())
		}
	}
	assertNoWrite(t, m)
}

// The note read costs one request, not two: the guard hands what it read to the
// encryption step instead of making it fetch the same note again.
func TestNotesUpdateReadsTheNoteOnce(t *testing.T) {
	m := newAPIMock(t, noteWithTaskRoutes())

	_, err := runCLI(t, m, "notes", "update", "n1",
		"--format", "html", "--content", `<p>new</p>`+taskBlockHTML(fixtureTaskID))
	if err != nil {
		t.Fatalf("notes update: %v", err)
	}
	reads := 0
	for _, call := range m.calls() {
		if call == "GET /api/v1/notes/n1" {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("the note was read %d times, want 1: %v", reads, m.calls())
	}
}

// --help has to tell the truth about what a content flag does. The bug was as much
// a documentation failure as a code one: nothing anywhere said "this deletes your
// tasks", so nobody could have known.
func TestNotesUpdateHelpExplainsTheBodyReplacement(t *testing.T) {
	help := notesUpdateCmd.Long + "\n" + notesUpdateCmd.Example
	for _, want := range []string{"REPLACE", "TASKS", "--keep-tasks", "--allow-task-loss", "notes append"} {
		if !strings.Contains(help, want) {
			t.Errorf("notes update --help never mentions %q", want)
		}
	}
	for _, name := range []string{"keep-tasks", "allow-task-loss"} {
		if notesUpdateCmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is not registered on notes update", name)
		}
	}
}

// ===========================================================================
// The scanner
// ===========================================================================

// noteTaskBlockIDs decides what the SERVER will see. Getting it wrong in the new
// content is a silent deletion, so the cases that turn markup back into text are
// the ones that matter most here.
func TestNoteTaskBlockIDs(t *testing.T) {
	cases := []struct {
		name    string
		content string
		format  string
		want    []string
	}{
		{"none", "<p>just prose</p>", "html", []string{}},
		{"double quoted", `<harbor-task id="` + fixtureTaskID + `"></harbor-task>`, "html", []string{fixtureTaskID}},
		{"single quoted", `<harbor-task id='` + fixtureTaskID + `'>`, "html", []string{fixtureTaskID}},
		{"bare", `<harbor-task id=` + fixtureTaskID + `>`, "html", []string{fixtureTaskID}},
		// The fixture ids carry hex LETTERS on purpose: with an all-digit uuid
		// this case is vacuous, and a mutation dropping the lower-casing survived it.
		{"upper cased is canonicalized", `<harbor-task id="` + strings.ToUpper(fixtureTaskID) + `">`, "html", []string{fixtureTaskID}},
		{"other attributes first", `<harbor-task data-x="1" id="` + fixtureTaskID + `">`, "html", []string{fixtureTaskID}},
		// A quoted attribute keeps its padding, and the server trims before matching
		// (notes.extractTaskIDs) — so this id resolves there and must resolve here.
		{"padded id is trimmed", `<harbor-task id="  ` + fixtureTaskID + `  ">`, "html", []string{fixtureTaskID}},
		{"repeat collapses", `<harbor-task id="` + fixtureTaskID + `"><harbor-task id="` + fixtureTaskID + `">`, "html", []string{fixtureTaskID}},
		{"order preserved", `<harbor-task id="` + fixtureTaskID2 + `"><harbor-task id="` + fixtureTaskID + `">`, "html",
			[]string{fixtureTaskID2, fixtureTaskID}},

		// Ids the server's reconciler cannot resolve reference no task, so they
		// neither save one nor endanger one.
		{"non-uuid id ignored", `<harbor-task id="not-a-uuid">`, "html", []string{}},
		{"braced uuid ignored", `<harbor-task id="{` + fixtureTaskID + `}">`, "html", []string{}},
		{"empty id ignored", `<harbor-task id="">`, "html", []string{}},
		{"no id attribute", `<harbor-task></harbor-task>`, "html", []string{}},

		// Text, not markup: the sanitizer drops comments and the Markdown
		// converter escapes code, so none of these reaches the server as a block.
		{"html comment", `<!-- <harbor-task id="` + fixtureTaskID + `"> -->`, "html", []string{}},
		{"unterminated html comment", `<p>x</p><!-- <harbor-task id="` + fixtureTaskID + `">`, "html", []string{}},

		// Raw-text elements swallow their contents as TEXT. A regex scanner reads
		// these as markup and the server does not — the divergence that let
		// --keep-tasks report success while the task was deleted.
		{"inside textarea", `<textarea><harbor-task id="` + fixtureTaskID + `"></textarea>`, "html", []string{}},
		{"inside script", `<script><harbor-task id="` + fixtureTaskID + `"></script>`, "html", []string{}},
		{"inside style", `<style><harbor-task id="` + fixtureTaskID + `"></style>`, "html", []string{}},
		{"inside title", `<title><harbor-task id="` + fixtureTaskID + `"></title>`, "html", []string{}},
		{"after an unclosed textarea", `<p>x</p><textarea><harbor-task id="` + fixtureTaskID + `">`, "html", []string{}},

		// …but a block nested in ordinary markup is markup, wherever it sits.
		{"nested in a list item", `<ul><li><harbor-task id="` + fixtureTaskID + `"></li></ul>`, "html", []string{fixtureTaskID}},
		{"inside pre", `<pre><harbor-task id="` + fixtureTaskID + `"></pre>`, "html", []string{fixtureTaskID}},
		{"fenced code", "```\n<harbor-task id=\"" + fixtureTaskID + "\">\n```", "markdown", []string{}},
		{"tilde fenced code", "~~~\n<harbor-task id=\"" + fixtureTaskID + "\">\n~~~", "markdown", []string{}},
		{"inline code", "the `<harbor-task id=\"" + fixtureTaskID + "\">` block", "markdown", []string{}},
		{"markdown outside a fence still counts", "```\nx\n```\n<harbor-task id=\"" + fixtureTaskID + "\">", "markdown",
			[]string{fixtureTaskID}},

		// In HTML mode Markdown code constructs are not constructs at all — a
		// backtick is just a character, and the block really does reach the server.
		{"backticks are literal in html", "`<harbor-task id=\"" + fixtureTaskID + "\">`", "html", []string{fixtureTaskID}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := noteTaskBlockIDs(tc.content, tc.format)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("noteTaskBlockIDs(%q, %s) = %v, want %v", tc.content, tc.format, got, tc.want)
			}
		})
	}
}

// A fence closes only on a run of the same character at least as long as the one
// that opened it, and an unterminated fence runs to the end of the input.
func TestStripMarkdownFences(t *testing.T) {
	cases := []struct{ name, in, wantGone, wantKept string }{
		{"simple", "keep\n```\ngone\n```\nkeep2", "gone", "keep2"},
		{"longer fence inside", "```` \ngone ```\n````\nkeep", "gone", "keep"},
		{"shorter run does not close", "````\ngone ```\ngone2\n````\nkeep", "gone2", "keep"},
		{"different char does not close", "```\ngone ~~~\ngone2\n```\nkeep", "gone2", "keep"},
		{"unterminated runs to the end", "keep\n```\ngone\ngone2", "gone2", "keep"},
		{"indented three spaces is a fence", "   ```\ngone\n   ```\nkeep", "gone", "keep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMarkdownFences(tc.in)
			if strings.Contains(got, tc.wantGone) {
				t.Errorf("fenced %q survived:\n%s", tc.wantGone, got)
			}
			if !strings.Contains(got, tc.wantKept) {
				t.Errorf("unfenced %q was stripped:\n%s", tc.wantKept, got)
			}
		})
	}
}

// Four or more leading spaces is an indented code block, not a fence — so it must
// not open one and swallow the rest of the body.
func TestMarkdownFenceMarker(t *testing.T) {
	cases := []struct{ line, want string }{
		{"```", "```"},
		{"```go", "```"},
		{"~~~~", "~~~~"},
		{"   ```", "```"},
		{"    ```", ""},
		{"``", ""},
		{"text ```", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := markdownFenceMarker(tc.line); got != tc.want {
			t.Errorf("markdownFenceMarker(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// The re-appended blocks must be separated from the body by a blank line, or the
// Markdown converter folds them into a trailing paragraph or list item.
func TestAppendTaskBlocks(t *testing.T) {
	got := appendTaskBlocks("- a list item\n", []noteTask{{ID: fixtureTaskID}, {ID: fixtureTaskID2}})

	if !strings.Contains(got, "\n\n"+taskBlockHTML(fixtureTaskID)) {
		t.Errorf("no blank line before the first block:\n%q", got)
	}
	if !strings.Contains(got, taskBlockHTML(fixtureTaskID2)) {
		t.Errorf("the second block was dropped:\n%q", got)
	}
	if !strings.HasPrefix(got, "- a list item") {
		t.Errorf("the user's content was mangled:\n%q", got)
	}
	// The block spelling has to match the server's own (notes.taskBlockHTML) so a
	// re-appended block round-trips through the reconciler unchanged.
	if taskBlockHTML(fixtureTaskID) != `<harbor-task id="`+fixtureTaskID+`"></harbor-task>` {
		t.Errorf("task block spelling drifted from the server's: %s", taskBlockHTML(fixtureTaskID))
	}
}

// A task with no title degrades to its id rather than to a blank bullet.
func TestTaskLossRefusalNamesEveryTask(t *testing.T) {
	msg := taskLossRefusal([]noteTask{{ID: fixtureTaskID, Title: "Buy milk"}, {ID: fixtureTaskID2}})

	for _, want := range []string{"2 tasks", "Buy milk", fixtureTaskID, fixtureTaskID2, "Nothing has been written"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal never mentions %q:\n%s", want, msg)
		}
	}
}

// Counts read as sentences, not as counters.
func TestCountTasks(t *testing.T) {
	if got := countTasks(1); got != "1 task" {
		t.Errorf("countTasks(1) = %q", got)
	}
	if got := countTasks(3); got != "3 tasks" {
		t.Errorf("countTasks(3) = %q", got)
	}
}

// Every refusal in this file used to offer `harbor notes append` as the safe way
// out. It is not one: append keeps the body, so it saves a task the body still
// REFERENCES — and releases one it has stopped referencing exactly like the update
// just refused (measured against a local server: the referenced task survived the
// append, the stranded one was gone). The stranded task is the whole reason the
// task-list read exists, so unqualified append advice at the moment of a refusal
// walks the user into the loss they were spared a line earlier. If a message names
// append, it has to name the limit too.
func TestNoRefusalOffersAppendAsUnconditionallySafe(t *testing.T) {
	lost := []noteTask{{ID: fixtureTaskID, Title: "Buy milk"}}
	for name, msg := range map[string]string{
		"taskLossRefusal":           taskLossRefusal(lost),
		"keepFailedRefusal/md":      keepFailedRefusal(lost, "markdown"),
		"keepFailedRefusal/html":    keepFailedRefusal(lost, "html"),
		"taskListIncompleteRefusal": taskListIncompleteRefusal(500),
	} {
		if !strings.Contains(msg, "notes append") {
			continue
		}
		if !strings.Contains(msg, "stopped referencing") {
			t.Errorf("%s offers 'notes append' with no mention of what it still deletes:\n%s", name, msg)
		}
	}
}

// The short-read refusal must not send the user to --keep-tasks: it can only carry
// the blocks it was told about, so on a list that would not read whole it reports
// success and deletes the rest.
func TestTaskListIncompleteRefusalDoesNotOfferKeepTasks(t *testing.T) {
	msg := taskListIncompleteRefusal(500)

	if strings.Contains(msg, "--keep-tasks") {
		t.Errorf("the short-read refusal offers --keep-tasks, which cannot cover the tasks it could not read:\n%s", msg)
	}
	if !strings.Contains(msg, "--allow-task-loss") {
		t.Errorf("the short-read refusal leaves the user with no way through:\n%s", msg)
	}
}

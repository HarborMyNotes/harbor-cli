// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractNote(t *testing.T) {
	// Mutation envelope.
	n, usn := extractNote([]byte(`{"note":{"id":"n1","title":"T"},"usn":88}`))
	if str(n, "id") != "n1" || usn != "88" {
		t.Errorf("mutation extract: id=%q usn=%q", str(n, "id"), usn)
	}
	// Bare note.
	n2, usn2 := extractNote([]byte(`{"id":"n2","title":"T2"}`))
	if str(n2, "id") != "n2" || usn2 != "" {
		t.Errorf("bare extract: id=%q usn=%q", str(n2, "id"), usn2)
	}
}

func TestDisplayNoteRendersBodyAndUSN(t *testing.T) {
	data := []byte(`{"note":{"id":"n1","title":"Plan","notebook_id":"nb1","is_encrypted":false,"word_count":3,"usn":88,"content":"<p>Hello <strong>world</strong></p>","updated_at":1750000000000},"usn":88}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "Plan") {
		t.Errorf("title missing:\n%s", out)
	}
	if !strings.Contains(out, "New USN") {
		t.Errorf("new USN missing:\n%s", out)
	}
	// HTML body should be stripped to readable text.
	if !strings.Contains(out, "Hello world") {
		t.Errorf("body not rendered:\n%s", out)
	}
}

func TestDisplayNoteSourceAndAuthor(t *testing.T) {
	// A clipped note carries source_url and author: both should print.
	clipped := []byte(`{"id":"n1","title":"Clip","source_url":"https://example.com/clip","author":"Jane Doe","content":"body"}`)
	out := captureStdout(t, func() { displayNote(clipped) })
	if !strings.Contains(out, "Source") || !strings.Contains(out, "https://example.com/clip") {
		t.Errorf("source line missing:\n%s", out)
	}
	if !strings.Contains(out, "Author") || !strings.Contains(out, "Jane Doe") {
		t.Errorf("author line missing:\n%s", out)
	}
	// A plain note has neither field: nothing extra should appear.
	plain := []byte(`{"id":"n2","title":"Plain","content":"body"}`)
	out = captureStdout(t, func() { displayNote(plain) })
	if strings.Contains(out, "Source") || strings.Contains(out, "Author") {
		t.Errorf("unclipped note should not print Source/Author:\n%s", out)
	}
}

func TestDisplayNoteEncrypted(t *testing.T) {
	data := []byte(`{"id":"n1","title":"sealed","is_encrypted":true,"content":"AAAA"}`)
	out := captureStdout(t, func() { displayNote(data) })
	if !strings.Contains(out, "[encrypted]") {
		t.Errorf("encrypted body should be hidden:\n%s", out)
	}
	if strings.Contains(out, "AAAA") {
		t.Errorf("ciphertext should not be printed:\n%s", out)
	}
}

func TestDisplayNotesTable(t *testing.T) {
	data := []byte(`{"data":[{"id":"n1","title":"Plan","notebook_id":"nbxxxxxxxx","is_encrypted":true,"word_count":3,"usn":88,"updated_at":1750000000000}],"paging":{"offset":0,"total":1}}`)
	out := captureStdout(t, func() { displayNotes(data) })
	if !strings.Contains(out, "Plan") || !strings.Contains(out, "🔒") {
		t.Errorf("notes table missing fields:\n%s", out)
	}
}

func TestMapNoteError(t *testing.T) {
	cases := map[string]string{
		"note_title_too_long":            "title is too long",
		"note_too_large":                 "too large",
		"append_not_supported_encrypted": "encrypted",
		// The base_usn precondition the task guard sends. The message has to say
		// "nothing was written" and "merge" — a user told only "stale" retries the
		// same body, which is exactly the clobber the server just refused.
		"note_usn_stale": "nothing was written",
	}
	for code, sub := range cases {
		if got := mapNoteError(apiErr(code)); !strings.Contains(got.Error(), sub) {
			t.Errorf("mapNoteError(%s) = %q", code, got.Error())
		}
	}
	if got := mapNoteError(apiErr("note_usn_stale")); !strings.Contains(got.Error(), "merge") {
		t.Errorf("the stale-usn message never says to merge: %q", got)
	}
}

// ===========================================================================
// `notes delete --permanent` — the second route to a permanent expunge
// ===========================================================================
//
// `notes delete` is the safe, everyday command; the one flag that makes it
// irreversible is easy to miss when skimming a script. It reaches the same
// expunge `trash expunge` does, so it asks the same question — and, like every
// other gate, the wrong-answer branch is pinned at the call site rather than
// only in the helper.

// TestNotesDeletePermanentRunEStopsOnAWrongAnswer proves nothing reaches the
// server when the user declines.
func TestNotesDeletePermanentRunEStopsOnAWrongAnswer(t *testing.T) {
	for _, answer := range []string{"no", "n", "", "y", "YES"} {
		t.Run(answer, func(t *testing.T) {
			answerPrompt(t, answer)
			m := newAPIMock(t, map[string]mockReply{})

			out, err := runCLI(t, m, "notes", "delete", "n1", "--permanent")
			if err == nil {
				t.Fatalf("answering %q permanently deleted the note", answer)
			}
			if len(m.calls()) != 0 {
				t.Fatalf("the note was deleted anyway: %v", m.calls())
			}
			if strings.Contains(out, "permanently deleted") {
				t.Errorf("stdout claimed success after an abort:\n%s", out)
			}
		})
	}
}

// TestNotesDeletePermanentRunERefusesUnattended covers scripts and agents: no
// terminal to ask at means refuse, not proceed.
func TestNotesDeletePermanentRunERefusesUnattended(t *testing.T) {
	pipedStdin(t)
	m := newAPIMock(t, map[string]mockReply{})

	_, err := runCLI(t, m, "notes", "delete", "n1", "--permanent")
	if err == nil {
		t.Fatal("an unattended --permanent delete without --yes must refuse")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %q, want it to name the flag that would have worked", err.Error())
	}
	if len(m.calls()) != 0 {
		t.Errorf("nothing should have been sent, got %v", m.calls())
	}
}

// TestNotesDeletePermanentRunEProceedsWhenConfirmed keeps both confirmed routes
// working, and pins that --permanent really reaches the wire as permanent — a
// gate that quietly downgraded the delete to a trash would also "pass" the
// aborts above.
func TestNotesDeletePermanentRunEProceedsWhenConfirmed(t *testing.T) {
	const route = "DELETE /api/v1/notes/n1"

	answerPrompt(t, "yes")
	m := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})
	out, err := runCLI(t, m, "notes", "delete", "n1", "--permanent")
	if err != nil {
		t.Fatalf("typed yes: %v", err)
	}
	if got := m.queryOf(t, route).Get("permanent"); got != "true" {
		t.Errorf("permanent=%q on the wire, want \"true\"", got)
	}
	if !strings.Contains(out, "permanently deleted") {
		t.Errorf("output = %q", out)
	}

	pipedStdin(t) // --yes must not prompt
	m2 := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})
	if _, err := runCLI(t, m2, "notes", "delete", "n1", "--permanent", "--yes"); err != nil {
		t.Fatalf("--yes: %v", err)
	}
	if len(m2.calls()) != 1 {
		t.Errorf("calls = %v", m2.calls())
	}
}

// TestNotesDeleteWithoutPermanentNeverAsks is the other half of the rule. The
// ordinary delete is recoverable, and making it prompt would train people to
// type "yes" without reading — which is how the prompt that matters stops being
// read at all.
func TestNotesDeleteWithoutPermanentNeverAsks(t *testing.T) {
	const route = "DELETE /api/v1/notes/n1"
	pipedStdin(t) // any prompt fails the test
	m := newAPIMock(t, map[string]mockReply{route: {Status: 204, Body: ""}})

	out, err := runCLI(t, m, "notes", "delete", "n1")
	if err != nil {
		t.Fatalf("trashing a note must not need confirmation: %v", err)
	}
	if !strings.Contains(out, "moved to trash") {
		t.Errorf("output = %q", out)
	}
	if got := m.queryOf(t, route).Get("permanent"); got == "true" {
		t.Errorf("a plain delete must not send permanent=true")
	}
}

// ===========================================================================
// notes export — one note to a file
// ===========================================================================

// noteExportMock serves the per-note export endpoint the way the real one does:
// the SAME url answers with two different content types, and only the
// Content-Disposition header says which.
func noteExportMock(t *testing.T, filename, contentType, body string) *apiMock {
	t.Helper()
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}
	return m
}

// TestNotesExportWritesTheFile is the ordinary case: a note with no
// attachments lands as one .md at the path asked for.
func TestNotesExportWritesTheFile(t *testing.T) {
	m := noteExportMock(t, "Plan.md", "text/markdown; charset=utf-8", "---\ntitle: \"Plan\"\n---\n\n# Plan\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")

	out, err := runCLI(t, m, "notes", "export", "n1", "--output", path)
	if err != nil {
		t.Fatalf("notes export: %v", err)
	}

	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("nothing was written: %v", rerr)
	}
	if !strings.Contains(string(got), "# Plan") {
		t.Errorf("the file does not hold the export: %q", got)
	}
	if !strings.Contains(out, "Wrote") || !strings.Contains(out, path) {
		t.Errorf("the command never said where it put the file:\n%s", out)
	}
}

// TestNotesExportIntoADirectoryTakesTheServersName is the reason the header is
// read before the body. The same command can produce a .md or a .zip, so the
// caller cannot name the file and the server's own name is the only correct one.
func TestNotesExportIntoADirectoryTakesTheServersName(t *testing.T) {
	m := noteExportMock(t, "Quarterly plan.zip", "application/zip", "PK\x03\x04")
	dir := t.TempDir()

	if _, err := runCLI(t, m, "notes", "export", "n1", "--output", dir); err != nil {
		t.Fatalf("notes export: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "Quarterly plan.zip")); err != nil {
		entries, _ := os.ReadDir(dir)
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the archive was not written under the server's name; the directory holds %v", names)
	}
}

// TestNotesExportToStdoutWritesOnlyTheDocument keeps -o - pipeable. A "Wrote …"
// line on stdout would land inside the file the user is redirecting.
func TestNotesExportToStdoutWritesOnlyTheDocument(t *testing.T) {
	const doc = "---\ntitle: \"Plan\"\n---\n\n# Plan\n"
	m := noteExportMock(t, "Plan.md", "text/markdown; charset=utf-8", doc)

	out, err := runCLI(t, m, "notes", "export", "n1", "--output", "-")
	if err != nil {
		t.Fatalf("notes export -o -: %v", err)
	}

	if out != doc {
		t.Errorf("stdout carried something other than the document verbatim:\n%q", out)
	}
}

// TestNotesExportRequiresAnOutput refuses rather than guessing. The two shapes
// this command can return have different extensions, so a default filename
// would be wrong half the time.
func TestNotesExportRequiresAnOutput(t *testing.T) {
	m := noteExportMock(t, "Plan.md", "text/markdown; charset=utf-8", "# Plan\n")

	_, err := runCLI(t, m, "notes", "export", "n1")

	if err == nil {
		t.Fatal("notes export ran with nowhere to put the result")
	}
	if !strings.Contains(err.Error(), "--output") {
		t.Errorf("the refusal never names the missing flag:\n%s", err)
	}
}

// TestNotesExportZipIsAskedForOnTheWire pins --zip to the query parameter, not
// to anything the CLI decides for itself.
func TestNotesExportZipIsAskedForOnTheWire(t *testing.T) {
	m := noteExportMock(t, "Plan.zip", "application/zip", "PK\x03\x04")
	dir := t.TempDir()

	if _, err := runCLI(t, m, "notes", "export", "n1", "--zip", "--output", dir); err != nil {
		t.Fatalf("notes export --zip: %v", err)
	}

	if got := m.queryOf(t, "GET /api/v1/notes/n1/export.md").Get("zip"); got != "1" {
		t.Errorf("zip = %q on the wire, want 1", got)
	}
}

// TestNotesExportFormatIsValidatedLocally spends no round trip on a value the
// endpoint does not have.
func TestNotesExportFormatIsValidatedLocally(t *testing.T) {
	m := noteExportMock(t, "Plan.md", "text/markdown; charset=utf-8", "# Plan\n")

	_, err := runCLI(t, m, "notes", "export", "n1", "--format", "pdf", "--output", "-")

	if err == nil {
		t.Fatal("an unsupported --format was sent to the server")
	}
	if !strings.Contains(err.Error(), "markdown") {
		t.Errorf("the refusal never says what is supported:\n%s", err)
	}
	for _, r := range m.requests {
		if r.Method == http.MethodGet {
			t.Errorf("a rejected --format still cost a request: %s %s", r.Method, r.Path)
		}
	}
}

// TestEncryptedNoteExportSaysWhy turns the API code into the sentence a person
// can act on, and names the way through.
func TestEncryptedNoteExportSaysWhy(t *testing.T) {
	err := mapNoteError(apiErr("encrypted_not_exportable"))

	if err == nil {
		t.Fatal("the encrypted refusal was passed through as a raw API code")
	}
	for _, want := range []string{"encrypted", "notes decrypt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message never mentions %q:\n%s", want, err)
		}
	}
}

// TestNotesExportChecksFlagsBeforeCredentials keeps a typo answering the typo. A
// logged-out user who mistypes --format should not be sent to log in first, only
// to find out afterwards that the value was never going to work.
func TestNotesExportChecksFlagsBeforeCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_TOKEN", "")
	t.Setenv("HARBOR_API_URL", "")
	resetCommandState(t)
	prepareCommandTree()

	rootCmd.SetArgs([]string{"notes", "export", "n1", "--format", "pdf", "--output", "-"})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("an unsupported --format was accepted")
	}
	if !strings.Contains(err.Error(), "markdown") {
		t.Errorf("a logged-out user was told about their credentials instead of their typo:\n%s", err)
	}
}

// TestNotesExportSaysWhenTheServerNamedNoFile keeps the blame in the right
// place. Falling through to os.Create on a directory reports "is a directory",
// which reads like the path was wrong when it was the response.
func TestNotesExportSaysWhenTheServerNamedNoFile(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		// No Content-Disposition at all.
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("# Plan\n"))
	}
	dir := t.TempDir()

	_, err := runCLI(t, m, "notes", "export", "n1", "--output", dir)

	if err == nil {
		t.Fatal("a nameless response wrote something anyway")
	}
	if !strings.Contains(err.Error(), "did not name the file") {
		t.Errorf("the error blames the wrong thing:\n%s", err)
	}
}

// TestNotesExportBlamesTheServerOnlyWhenItNamedNothing keeps the refusal above
// from misattributing. A directory that happens to share the note's exported
// name is the user's filesystem, not a server that failed to name the file, and
// saying otherwise sends them looking in the wrong place.
func TestNotesExportBlamesTheServerOnlyWhenItNamedNothing(t *testing.T) {
	m := noteExportMock(t, "Plan.md", "text/markdown; charset=utf-8", "# Plan\n")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("Plan.md", 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, m, "notes", "export", "n1", "--output", ".")

	if err == nil {
		t.Fatal("the export wrote over a directory")
	}
	if strings.Contains(err.Error(), "did not name the file") {
		t.Errorf("the server named the file; the directory in the way is the user's:\n%s", err)
	}
}

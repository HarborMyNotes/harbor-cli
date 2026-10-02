// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"net/http"
	"net/url"
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

// TestNotesExportDefaultsToTheCurrentDirectory saves under the server's own
// name in the working directory when --output is left off, as every other
// Harbor app saves a download under that name.
func TestNotesExportDefaultsToTheCurrentDirectory(t *testing.T) {
	m := noteExportMock(t, "Plan.md", "text/markdown; charset=utf-8", "# Plan\n")
	t.Chdir(t.TempDir())

	out, err := runCLI(t, m, "notes", "export", "n1")
	if err != nil {
		t.Fatalf("notes export with no --output: %v", err)
	}

	got, rerr := os.ReadFile("Plan.md")
	if rerr != nil {
		t.Fatalf("nothing was written to the current directory: %v", rerr)
	}
	if string(got) != "# Plan\n" {
		t.Errorf("the file does not hold the export: %q", got)
	}
	if !strings.Contains(out, "Plan.md") {
		t.Errorf("the command never said where it put the file:\n%s", out)
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

	_, err := runCLI(t, m, "notes", "export", "n1", "--format", "docx", "--output", "-")

	if err == nil {
		t.Fatal("an unsupported --format was sent to the server")
	}
	if !strings.Contains(err.Error(), "markdown|pdf|html|enex") {
		t.Errorf("the refusal never says what is supported:\n%s", err)
	}
	for _, r := range m.requests {
		if r.Method == http.MethodGet {
			t.Errorf("a rejected --format still cost a request: %s %s", r.Method, r.Path)
		}
	}
}

// TestEncryptedNoteExportSaysWhy turns the API code into the sentence every
// Harbor app shows, and names the way through.
func TestEncryptedNoteExportSaysWhy(t *testing.T) {
	err := mapNoteError(apiErr("encrypted_not_exportable"))

	if err == nil {
		t.Fatal("the encrypted refusal was passed through as a raw API code")
	}
	if first := strings.SplitN(err.Error(), "\n", 2)[0]; first != "Encrypted notes can't be exported." {
		t.Errorf("first line = %q, want the shared sentence", first)
	}
	if !strings.Contains(err.Error(), "notes decrypt") {
		t.Errorf("the message never names the way through:\n%s", err)
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

	rootCmd.SetArgs([]string{"notes", "export", "n1", "--format", "docx", "--output", "-"})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("an unsupported --format was accepted")
	}
	if !strings.Contains(err.Error(), "markdown") {
		t.Errorf("a logged-out user was told about their credentials instead of their typo:\n%s", err)
	}
}

// TestNotesExportWontWriteOverADirectory reports a directory in the way of the
// server's file name as the filesystem error it is, and writes nothing.
func TestNotesExportWontWriteOverADirectory(t *testing.T) {
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
	if !strings.Contains(err.Error(), "cannot create output file") {
		t.Errorf("err = %q, want the filesystem refusal", err)
	}
}

// exportFixtureNote is one note in exportFixtureMock: the bodies it exports
// to, and whether the server holds it only as ciphertext.
type exportFixtureNote struct {
	encrypted bool
	title     string // the exact name, as filename* carries it
	asciiName string // the ASCII stand-in in plain filename
	pdfName   string // the server's ASCII-only, dash-separated PDF name
}

// exportFixtureMock serves all four per-note exports the way the server does,
// for an encrypted note AND a normal one in the same account. PDF, Markdown and
// HTML refuse the encrypted note with 422; ENEX answers it 200 with an empty
// export and X-Skipped-Encrypted: 1. disposition picks the header shape:
// "both" (filename + filename*), "plain" (filename only) or "none".
func exportFixtureMock(t *testing.T, notes map[string]exportFixtureNote, disposition string) *apiMock {
	t.Helper()
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		var id, ext, body string
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/export/enex":
			var req struct {
				NoteIDs []string `json:"note_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.NoteIDs) != 1 {
				t.Errorf("ENEX asked for %v, want exactly one note", req.NoteIDs)
			}
			id, ext = req.NoteIDs[0], ".enex"
			if notes[id].encrypted {
				// The real server: 200, zero notes, the skip in a header.
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("Content-Disposition", `attachment; filename="note.enex"; filename*=UTF-8''note.enex`)
				w.Header().Set("X-Skipped-Encrypted", "1")
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`<?xml version="1.0"?><en-export></en-export>`))
				return
			}
			w.Header().Set("X-Skipped-Encrypted", "0")
			body = `<en-export><note><title>` + notes[id].title + `</title></note></en-export>`
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/notes/"):
			rest := strings.TrimPrefix(r.URL.Path, "/api/v1/notes/")
			dot := strings.Index(rest, "/export.")
			if dot < 0 {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(404)
				return
			}
			id, ext = rest[:dot], "."+strings.TrimPrefix(rest[dot:], "/export.")
			if notes[id].encrypted {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				_, _ = w.Write([]byte(apiErrorBody("encrypted_not_exportable", "this note is encrypted and cannot be exported")))
				return
			}
			body = ext + " export of " + id
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		n := notes[id]
		name, ascii := n.title+ext, n.asciiName+ext
		if ext == ".pdf" {
			// PDF names are ASCII only on the server, so both forms agree.
			name, ascii = n.pdfName+ext, n.pdfName+ext
		}
		switch disposition {
		case "both":
			w.Header().Set("Content-Disposition", `attachment; filename="`+ascii+`"; filename*=UTF-8''`+url.PathEscape(name))
		case "plain":
			w.Header().Set("Content-Disposition", `attachment; filename="`+ascii+`"`)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}
	return m
}

// exportFixtureNotes is one normal note with an emoji title and one
// encrypted note, side by side.
var exportFixtureNotes = map[string]exportFixtureNote{
	"plain1": {title: "Welcome to Harbor 👋", asciiName: "Welcome to Harbor _", pdfName: "Welcome-to-Harbor"},
	"sealed": {encrypted: true, title: "note", asciiName: "note", pdfName: "note"},
}

// dirNames lists what a directory holds, for asserting what a run left behind.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestNotesExportEachFormatAsksForTheRightThing pins every format to its own
// request: the three downloads on their own paths, and ENEX as a one-id
// selection with the attachments included.
func TestNotesExportEachFormatAsksForTheRightThing(t *testing.T) {
	cases := map[string]string{
		"markdown": "GET /api/v1/notes/plain1/export.md",
		"pdf":      "GET /api/v1/notes/plain1/export.pdf",
		"html":     "GET /api/v1/notes/plain1/export.html",
		"enex":     "POST /api/v1/export/enex",
	}
	for format, want := range cases {
		t.Run(format, func(t *testing.T) {
			m := exportFixtureMock(t, exportFixtureNotes, "both")
			t.Chdir(t.TempDir())

			if _, err := runCLI(t, m, "notes", "export", "plain1", "--format", format); err != nil {
				t.Fatalf("notes export --format %s: %v", format, err)
			}

			if calls := m.calls(); len(calls) != 1 || calls[0] != want {
				t.Fatalf("calls = %v, want [%s]", calls, want)
			}
			if format == "enex" {
				body := m.bodyOf(t, want)
				ids, _ := body["note_ids"].([]any)
				if len(ids) != 1 || ids[0] != "plain1" || body["include_resources"] != true {
					t.Errorf("ENEX body = %v, want note_ids [plain1] and include_resources true", body)
				}
				if _, ok := body["notebook_id"]; ok {
					t.Errorf("ENEX body carries a notebook_id: %v", body)
				}
			}
		})
	}
}

// TestNotesExportNamesTheFileLikeTheServerSays covers the three header shapes
// for every format: filename* wins when it is there (emoji intact), plain
// filename is next, and with no header the shared fallback name is used.
func TestNotesExportNamesTheFileLikeTheServerSays(t *testing.T) {
	want := map[string]map[string]string{
		"both": {
			"markdown": "Welcome to Harbor 👋.md",
			"pdf":      "Welcome-to-Harbor.pdf",
			"html":     "Welcome to Harbor 👋.html",
			"enex":     "Welcome to Harbor 👋.enex",
		},
		"plain": {
			"markdown": "Welcome to Harbor _.md",
			"pdf":      "Welcome-to-Harbor.pdf",
			"html":     "Welcome to Harbor _.html",
			"enex":     "Welcome to Harbor _.enex",
		},
		"none": {
			"markdown": "note.md",
			"pdf":      "note.pdf",
			"html":     "note.html",
			"enex":     "note.enex",
		},
	}
	for disposition, byFormat := range want {
		for format, name := range byFormat {
			t.Run(disposition+"/"+format, func(t *testing.T) {
				m := exportFixtureMock(t, exportFixtureNotes, disposition)
				dir := t.TempDir()
				t.Chdir(dir)

				out, err := runCLI(t, m, "notes", "export", "plain1", "--format", format)
				if err != nil {
					t.Fatalf("notes export: %v", err)
				}

				if got := dirNames(t, dir); len(got) != 1 || got[0] != name {
					t.Fatalf("the directory holds %q, want [%q]", got, name)
				}
				if !strings.Contains(out, name) {
					t.Errorf("the command never said where it put the file:\n%s", out)
				}
			})
		}
	}
}

// TestNotesExportMarkdownFallbackNamesAZipAsAZip keeps the one fallback that
// depends on the response: a Markdown export that came back as an archive.
func TestNotesExportMarkdownFallbackNamesAZipAsAZip(t *testing.T) {
	cases := map[string]string{
		"application/zip":              "note.zip",
		"text/markdown; charset=utf-8": "note.md",
		"":                             "note.md",
	}
	for contentType, want := range cases {
		if got := notesExportFallbackName("markdown", contentType); got != want {
			t.Errorf("notesExportFallbackName(markdown, %q) = %q, want %q", contentType, got, want)
		}
	}
	// The other formats do not look at the content type.
	if got := notesExportFallbackName("pdf", "application/zip"); got != "note.pdf" {
		t.Errorf("pdf fallback = %q", got)
	}
}

// TestNotesExportEncryptedNoteFailsCleanly runs every format against an
// encrypted note and a normal note in the same account. The encrypted one must
// fail with the shared sentence, a non-zero exit and no file — including ENEX,
// where the server answers 200 — while the normal one still exports.
func TestNotesExportEncryptedNoteFailsCleanly(t *testing.T) {
	for _, format := range notesExportFormats {
		t.Run(format, func(t *testing.T) {
			m := exportFixtureMock(t, exportFixtureNotes, "both")
			dir := t.TempDir()
			t.Chdir(dir)

			out, err := runCLI(t, m, "notes", "export", "sealed", "--format", format)
			if err == nil {
				t.Fatalf("an encrypted note exported without complaint:\n%s", out)
			}
			if first := strings.SplitN(err.Error(), "\n", 2)[0]; first != "Encrypted notes can't be exported." {
				t.Errorf("err = %q, want the shared sentence first", err)
			}
			if code := exitCodeFor(err); code == exitOK {
				t.Errorf("exit code = %d, want non-zero", code)
			}
			if got := dirNames(t, dir); len(got) != 0 {
				t.Errorf("a file was left behind for the encrypted note: %v", got)
			}
			if strings.Contains(out, "Wrote") {
				t.Errorf("stdout claimed success:\n%s", out)
			}

			// To stdout, nothing at all may be written.
			out, err = runCLI(t, m, "notes", "export", "sealed", "--format", format, "--output", "-")
			if err == nil || out != "" {
				t.Errorf("-o -: err = %v, stdout = %q; want an error and nothing written", err, out)
			}

			// The normal note in the same account is unaffected.
			if _, err := runCLI(t, m, "notes", "export", "plain1", "--format", format); err != nil {
				t.Fatalf("the normal note failed too: %v", err)
			}
			if got := dirNames(t, dir); len(got) != 1 {
				t.Errorf("the normal note left %v, want one file", got)
			}
		})
	}
}

// TestNotesExportPDFReportsSkippedAttachments warns on stderr when the server
// could not combine some attachments, keeps stdout clean for -o -, and says
// nothing when every attachment went in.
func TestNotesExportPDFReportsSkippedAttachments(t *testing.T) {
	serve := func(skipped string) *apiMock {
		m := newAPIMock(t, map[string]mockReply{})
		m.handler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename="Plan.pdf"`)
			if skipped != "" {
				w.Header().Set("X-Skipped-Attachments", skipped)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte("%PDF-1.3"))
		}
		return m
	}

	t.Chdir(t.TempDir())
	var out string
	stderr := captureStderr(t, func() {
		var err error
		out, err = runCLI(t, serve("2"), "notes", "export", "n1", "--format", "pdf", "--output", "-")
		if err != nil {
			t.Fatalf("notes export: %v", err)
		}
	})
	if out != "%PDF-1.3" {
		t.Errorf("stdout = %q, want only the PDF", out)
	}
	if !strings.Contains(stderr, "2 attachments could not be combined") {
		t.Errorf("stderr = %q, want the skipped count", stderr)
	}

	for _, header := range []string{"", "0"} {
		stderr = captureStderr(t, func() {
			if _, err := runCLI(t, serve(header), "notes", "export", "n1", "--format", "pdf"); err != nil {
				t.Fatalf("notes export: %v", err)
			}
		})
		if strings.Contains(stderr, "could not be combined") {
			t.Errorf("X-Skipped-Attachments %q still warned: %q", header, stderr)
		}
	}
}

// TestNotesExportZipIsMarkdownOnly refuses --zip with any other format before
// a request is spent, rather than silently ignoring it.
func TestNotesExportZipIsMarkdownOnly(t *testing.T) {
	m := exportFixtureMock(t, exportFixtureNotes, "both")
	for _, format := range []string{"pdf", "html", "enex"} {
		_, err := runCLI(t, m, "notes", "export", "plain1", "--format", format, "--zip", "--output", "-")
		if err == nil || !strings.Contains(err.Error(), "--zip only applies to --format markdown") {
			t.Errorf("--format %s --zip: err = %v", format, err)
		}
	}
	if len(m.calls()) != 0 {
		t.Errorf("a rejected flag combination still cost requests: %v", m.calls())
	}
}

// notebooksForCreate answers the default-notebook lookup every note create
// makes before it writes.
const notebooksForCreate = `{"data":[{"id":"nb1","is_default":true,"default_encrypt":false}],` +
	`"paging":{"limit":500,"offset":0,"total":1,"has_more":false}}`

// TestNotesCreateSendsMetadata covers --meta and --meta-json on create: one
// object in the create body, with --meta applied on top of the JSON.
func TestNotesCreateSendsMetadata(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks": {Status: 200, Body: notebooksForCreate},
		"POST /api/v1/notes":    {Status: 201, Body: `{"note":{"id":"n1","title":"Lead","metadata":{"a":true,"b":1}},"usn":2}`},
	})
	if _, err := runCLI(t, m, "notes", "create", "--title", "Lead", "--content", "x",
		"--meta-json", `{"b":1,"a":false}`, "--meta", "a=true"); err != nil {
		t.Fatalf("notes create: %v", err)
	}
	body := m.bodyOf(t, "POST /api/v1/notes")
	want := map[string]any{"a": true, "b": float64(1)}
	if got, _ := body["metadata"].(map[string]any); len(got) != 2 || got["a"] != want["a"] || got["b"] != want["b"] {
		t.Errorf("metadata = %#v", body["metadata"])
	}
}

// TestNotesUpdateMetadataOnly proves metadata alone touches only the metadata
// route, so the note itself is not edited.
func TestNotesUpdateMetadataOnly(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/notes/n1/metadata": {Status: 200, Body: `{"metadata":{"gallery":true}}`},
	})
	out, err := runCLI(t, m, "notes", "update", "n1", "--meta", "gallery=true", "--unset-meta", "draft")
	if err != nil {
		t.Fatalf("notes update: %v", err)
	}
	if calls := strings.Join(m.calls(), ", "); calls != "PATCH /api/v1/notes/n1/metadata" {
		t.Errorf("calls = %s", calls)
	}
	if got := m.rawBodyOf(t, "PATCH /api/v1/notes/n1/metadata"); got != `{"draft":null,"gallery":true}` {
		t.Errorf("body = %s", got)
	}
	if !strings.Contains(out, "gallery") {
		t.Errorf("output:\n%s", out)
	}
}

// TestNotesUpdateFieldsThenMetadata covers an update carrying both: the note's
// own PATCH goes first and never carries metadata, the metadata write follows,
// and the printed note shows the new metadata.
func TestNotesUpdateFieldsThenMetadata(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes/n1":            {Status: 200, Body: `{"id":"n1","title":"Old","is_encrypted":false}`},
		"PATCH /api/v1/notes/n1":          {Status: 200, Body: `{"note":{"id":"n1","title":"New","metadata":{"old":1}},"usn":5}`},
		"PATCH /api/v1/notes/n1/metadata": {Status: 200, Body: `{"metadata":{"gallery":true,"old":1}}`},
	})
	out, err := runCLI(t, m, "notes", "update", "n1", "--title", "New", "--meta", "gallery=true", "--json")
	if err != nil {
		t.Fatalf("notes update: %v", err)
	}
	calls := m.calls()
	if len(calls) != 3 || calls[1] != "PATCH /api/v1/notes/n1" || calls[2] != "PATCH /api/v1/notes/n1/metadata" {
		t.Errorf("calls = %v", calls)
	}
	if _, ok := m.bodyOf(t, "PATCH /api/v1/notes/n1")["metadata"]; ok {
		t.Error("the note's own PATCH carried metadata; it must go to the metadata route")
	}
	if !strings.Contains(out, `"gallery": true`) || !strings.Contains(out, `"title": "New"`) {
		t.Errorf("printed note lacks the new metadata:\n%s", out)
	}
}

// TestNotesUpdateReportsAHalfDoneUpdate covers a metadata refusal after the
// note's own fields were saved: the error must say which half landed.
func TestNotesUpdateReportsAHalfDoneUpdate(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes/n1":   {Status: 200, Body: `{"id":"n1","is_encrypted":false}`},
		"PATCH /api/v1/notes/n1": {Status: 200, Body: `{"note":{"id":"n1"},"usn":5}`},
		"PATCH /api/v1/notes/n1/metadata": {Status: 422, Body: `{"error":{"code":"validation_failed","message":"The request was invalid.",` +
			`"details":{"metadata":"has 65 top-level keys; the limit is 64"}}}`},
	})
	_, err := runCLI(t, m, "notes", "update", "n1", "--title", "New", "--meta", "a=1")
	if err == nil || !strings.Contains(err.Error(), "the note was updated, but its metadata was not") ||
		!strings.Contains(err.Error(), "65 top-level keys") {
		t.Errorf("err = %v", err)
	}
}

// TestNotesListForwardsMetaFilters pins the query, next to the existing
// --meta switch that leaves out bodies.
func TestNotesListForwardsMetaFilters(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notes": {Status: 200, Body: `{"data":[],"paging":{"total":0}}`},
	})
	if _, err := runCLI(t, m, "notes", "list", "--meta", "--meta-eq", "gallery=true", "--meta-has", "crm_id"); err != nil {
		t.Fatalf("notes list: %v", err)
	}
	q := m.queryOf(t, "GET /api/v1/notes")
	if q.Get("fields") != "meta" || q.Get("meta.gallery") != "true" || q.Get("meta_has") != "crm_id" {
		t.Errorf("query = %v", q)
	}
}

// TestNotesListPointsAtMetaEq covers the likeliest mistake: `--meta KEY=VALUE`
// on a command where --meta is a switch.
func TestNotesListPointsAtMetaEq(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{})
	_, err := runCLI(t, m, "notes", "list", "--meta", "gallery=true")
	if err == nil || !strings.Contains(err.Error(), "--meta-eq gallery=true") {
		t.Errorf("err = %v", err)
	}
	if len(m.calls()) != 0 {
		t.Errorf("requests made: %v", m.calls())
	}
	if _, err := runCLI(t, m, "notes", "list", "bogus"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("a plain stray argument: err = %v", err)
	}
}

// TestDisplayNoteShowsMetadata covers the detail view's Metadata row.
func TestDisplayNoteShowsMetadata(t *testing.T) {
	out := captureStdout(t, func() {
		displayNote([]byte(`{"note":{"id":"n1","title":"T","content":"hi","metadata":{"crm_id":12345678901234567890}},"usn":3}`))
	})
	if !strings.Contains(out, "Metadata") || !strings.Contains(out, "crm_id=12345678901234567890") {
		t.Errorf("metadata row missing or rounded:\n%s", out)
	}
	out = captureStdout(t, func() { displayNote([]byte(`{"id":"n1","title":"T","content":"hi","metadata":{}}`)) })
	if strings.Contains(out, "Metadata") {
		t.Errorf("empty metadata shown:\n%s", out)
	}
}

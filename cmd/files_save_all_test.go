// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-10-24

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/crypto"
)

// saveAllNoteID is the note every save-all test saves from.
const saveAllNoteID = "11111111-2222-4333-8444-555555555555"

// saveAllHash builds a distinct, well-formed sha256 hex for test file n.
func saveAllHash(n int) string { return fmt.Sprintf("%064x", n) }

// saveAllFile is one file in a saveAllMock: its list row and its stored bytes.
// A nil body makes its presigned download answer 404.
type saveAllFile struct {
	hash, filename, mime string
	encrypted            bool
	body                 []byte
}

// saveAllMock serves one note, its file list (honouring limit and offset, as the
// server does) and each file's bytes behind a presigned URL on the same server.
// An empty note makes GET /notes/:id answer 404.
func saveAllMock(t *testing.T, note string, files []saveAllFile) *apiMock {
	t.Helper()
	m := newAPIMock(t, nil)
	byHash := map[string]saveAllFile{}
	for _, f := range files {
		if _, ok := byHash[f.hash]; !ok {
			byHash[f.hash] = f
		}
	}
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch path := r.URL.Path; {
		case path == "/api/v1/notes/"+saveAllNoteID:
			if note == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(apiErrorBody("not_found", "The requested resource was not found.")))
				return
			}
			_, _ = w.Write([]byte(note))
		case path == "/api/v1/files":
			_, _ = w.Write([]byte(fileListPage(files, r)))
		case strings.HasPrefix(path, "/api/v1/files/") && strings.HasSuffix(path, "/raw"):
			f := byHash[strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/files/"), "/raw")]
			if f.body == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(apiErrorBody("blob_missing", "The file's bytes are missing.")))
				return
			}
			_, _ = w.Write(f.body)
		case strings.HasPrefix(path, "/api/v1/files/"):
			hash := strings.TrimPrefix(path, "/api/v1/files/")
			f := byHash[hash]
			out, _ := json.Marshal(map[string]any{
				"download_url": m.srv.URL + "/blob/" + hash, "filename": f.filename, "mime": f.mime,
			})
			_, _ = w.Write(out)
		case strings.HasPrefix(path, "/blob/"):
			f := byHash[strings.TrimPrefix(path, "/blob/")]
			if f.body == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(f.body)
		default:
			t.Errorf("saveAllMock: unrouted request %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return m
}

// fileListPage serves one page of the file list the way GET /files does: limit
// and offset from the query, and a paging block saying whether more follow.
func fileListPage(files []saveAllFile, r *http.Request) string {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	end := min(offset+limit, len(files))
	rows := []map[string]any{}
	for _, f := range files[min(offset, len(files)):end] {
		rows = append(rows, map[string]any{
			"hash": f.hash, "filename": f.filename, "mime": f.mime, "is_encrypted": f.encrypted,
			"notes": []map[string]any{{"note_id": saveAllNoteID, "role": "inline", "position": nil}},
		})
	}
	out, _ := json.Marshal(map[string]any{
		"data":   rows,
		"paging": map[string]any{"limit": limit, "offset": offset, "total": len(files), "has_more": end < len(files)},
	})
	return string(out)
}

// saveAllNote builds a plain note's JSON with the given HTML body.
func saveAllNote(body string) string {
	out, _ := json.Marshal(map[string]any{"id": saveAllNoteID, "title": "Trip", "content": body, "is_encrypted": false})
	return string(out)
}

// embedTag is the body markup that shows a file inline.
func embedTag(hash string) string {
	return `<harbor-embed resource="sha256:` + hash + `" type="image/png"></harbor-embed>`
}

// readSaved returns every file in dir by name, with its contents.
func readSaved(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

// The whole run: every linked file once, named by the rule, in body order and
// then by name, and a second run into the same folder overwrites nothing.
func TestFilesDownloadNoteSavesEveryFileUnderItsRealName(t *testing.T) {
	h1, h2, h3, h4, h5 := saveAllHash(1), saveAllHash(2), saveAllHash(3), saveAllHash(4), saveAllHash(5)
	files := []saveAllFile{
		{hash: h1, filename: "attachment.png", mime: "image/png", body: []byte("one")},
		{hash: h2, filename: "attachment.png", mime: "image/png", body: []byte("two")},
		{hash: h3, filename: "attachment.png", mime: "image/png", body: []byte("three")},
		{hash: h1, filename: "attachment.png", mime: "image/png", body: []byte("one")},
		{hash: h4, filename: "Report", mime: "application/pdf", body: []byte("four")},
		{hash: h5, filename: "zeta.txt", mime: "text/plain", body: []byte("five")},
	}
	// The body shows h3, then h1; its hashes are upper case, which still match.
	body := "<p>Trip</p>" + embedTag(strings.ToUpper(h3)) + embedTag(h1)
	m := saveAllMock(t, saveAllNote(body), files)
	dir := filepath.Join(t.TempDir(), "out")

	out, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", dir)
	if err != nil {
		t.Fatalf("files download --note: %v\n%s", err, out)
	}
	want := map[string]string{
		"attachment.png":     "three",
		"attachment (2).png": "one",
		"Report.pdf":         "four",
		"attachment (3).png": "two",
		"zeta.txt":           "five",
	}
	if got := readSaved(t, dir); !equalMaps(got, want) {
		t.Errorf("saved files = %v, want %v", got, want)
	}
	for _, line := range []string{
		"saved  " + filepath.Join(dir, "attachment.png"),
		"saved  " + filepath.Join(dir, "attachment (2).png"),
		"saved  " + filepath.Join(dir, "Report.pdf"),
		"5 of 5 attachments saved",
	} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("output is missing %q:\n%s", line, out)
		}
	}
	if n := countCalls(m, "GET /blob/"+h1); n != 1 {
		t.Errorf("the file linked twice was downloaded %d times, want once", n)
	}
	if q := m.queryOf(t, "GET /api/v1/files"); q.Get("note_id") != saveAllNoteID || q.Get("order") != "created_at" {
		t.Errorf("file list query = %v, want note_id and order=created_at", q)
	}

	// Again into the same folder: the files already there are taken.
	if _, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", dir); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for name, content := range map[string]string{
		"attachment.png": "three", "attachment (4).png": "three", "attachment (5).png": "one",
		"Report.pdf": "four", "Report (2).pdf": "four", "attachment (6).png": "two", "zeta (2).txt": "five",
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != content {
			t.Errorf("after the second run %s = %q (%v), want %q", name, b, err, content)
		}
	}
	if got := len(readSaved(t, dir)); got != 10 {
		t.Errorf("the folder holds %d files after two runs, want 10", got)
	}
}

// equalMaps reports whether two string maps hold the same pairs.
func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// A file that cannot be fetched, or cannot be decrypted, is skipped with its
// reason; the rest are saved, a skipped file takes no name, and the run fails.
func TestFilesDownloadNoteSkipsWhatItCannotSave(t *testing.T) {
	lockedSession(t)
	sealed, err := crypto.SealBytes(newMasterKey(t), []byte("secret bytes"))
	if err != nil {
		t.Fatal(err)
	}
	h1, h2, h3, h4 := saveAllHash(1), saveAllHash(2), saveAllHash(3), saveAllHash(4)
	files := []saveAllFile{
		{hash: h1, filename: "a.png", mime: "image/png", body: []byte("one")},
		{hash: h2, filename: "a.png", mime: "image/png"}, // its download fails
		{hash: h3, filename: "a.png", mime: "image/png", body: []byte("three")},
		{hash: h4, filename: "secret.pdf", mime: "application/pdf", encrypted: true, body: sealed},
	}
	m := saveAllMock(t, saveAllNote(""), files)
	dir := t.TempDir()

	out, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", dir)
	if err == nil {
		t.Fatal("a run that skipped files must fail (exit 1)")
	}
	if want := "2 of 4 attachments could not be saved — each one is listed above"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if code := exitCodeFor(err); code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	// The failed h2 takes no name, so h3 is "(2)", not "(3)".
	want := map[string]string{"a.png": "one", "a (2).png": "three"}
	if got := readSaved(t, dir); !equalMaps(got, want) {
		t.Errorf("saved files = %v, want %v", got, want)
	}
	for _, line := range []string{
		"skipped  a.png: download failed: HTTP 404",
		"skipped  secret.pdf: it is encrypted and HARBOR_PASSPHRASE is not set",
		"2 of 4 attachments saved",
	} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("output is missing %q:\n%s", line, out)
		}
	}
}

// With the passphrase, sealed files are decrypted, a plain file on an encrypted
// note passes through, and the order comes from the decrypted body.
func TestFilesDownloadNoteDecryptsWithThePassphrase(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	h1, h2 := saveAllHash(1), saveAllHash(2)
	sealed, err := crypto.SealBytes(key, []byte("sealed text"))
	if err != nil {
		t.Fatal(err)
	}
	// By name and hash h1 would come first; the body shows h2 first.
	content, err := crypto.SealField(key, saveAllNoteID, "content", embedTag(h2)+embedTag(h1))
	if err != nil {
		t.Fatal(err)
	}
	note, _ := json.Marshal(map[string]any{"id": saveAllNoteID, "title": "HRBC2.x", "content": content, "is_encrypted": true})
	m := saveAllMock(t, string(note), []saveAllFile{
		{hash: h1, filename: "b.txt", mime: "text/plain", body: []byte("plain text")},
		{hash: h2, filename: "b.txt", mime: "text/plain", encrypted: true, body: sealed},
	})
	dir := t.TempDir()

	if out, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", dir); err != nil {
		t.Fatalf("files download --note: %v\n%s", err, out)
	}
	want := map[string]string{"b.txt": "sealed text", "b (2).txt": "plain text"}
	if got := readSaved(t, dir); !equalMaps(got, want) {
		t.Errorf("saved files = %v, want %v", got, want)
	}
}

// A note with no files says so, and leaves no empty folder behind.
func TestFilesDownloadNoteWithNoAttachments(t *testing.T) {
	m := saveAllMock(t, saveAllNote("<p>hi</p>"), nil)
	dir := filepath.Join(t.TempDir(), "never-made")

	out, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", dir)
	if err != nil {
		t.Fatalf("files download --note: %v", err)
	}
	if out != "This note has no attachments.\n" {
		t.Errorf("output = %q", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the output folder was created for a note with no files (stat err = %v)", err)
	}
}

// An unknown or trashed note is named as the problem, before the file list is
// read.
func TestFilesDownloadNoteUnknownNote(t *testing.T) {
	m := saveAllMock(t, "", nil)

	_, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no note with that id") {
		t.Fatalf("error = %v, want it to name the note as missing", err)
	}
	if n := countCalls(m, "GET /api/v1/files"); n != 0 {
		t.Errorf("the file list was read %d times for a missing note", n)
	}
}

// --json prints the whole report, and --raw streams each file through the API.
func TestFilesDownloadNoteJSONAndRaw(t *testing.T) {
	lockedSession(t)
	h1, h2 := saveAllHash(1), saveAllHash(2)
	m := saveAllMock(t, saveAllNote(""), []saveAllFile{
		{hash: h1, filename: "Q3 Budget.xlsx", mime: "application/vnd.ms-excel", body: []byte("sheet")},
		{hash: h2, filename: "", mime: "image/png"},
	})
	dir := t.TempDir()

	out, err := runCLI(t, m, "files", "download", "--note", saveAllNoteID, "--output-dir", dir, "--raw", "--json")
	if err == nil {
		t.Fatal("a run that skipped a file must fail")
	}
	var report saveAllReport
	if jerr := json.Unmarshal([]byte(out), &report); jerr != nil {
		t.Fatalf("--json output is not one JSON report: %v\n%s", jerr, out)
	}
	if report.Total != 2 || len(report.Saved) != 1 || len(report.Skipped) != 1 {
		t.Fatalf("report = %+v, want 1 saved and 1 skipped of 2", report)
	}
	if s := report.Saved[0]; s.Hash != h1 || s.Path != filepath.Join(dir, "Q3 Budget.xlsx") || s.Size != 5 {
		t.Errorf("saved = %+v", s)
	}
	if s := report.Skipped[0]; s.Hash != h2 || s.Reason != "the blob bytes are not stored" {
		t.Errorf("skipped = %+v", s)
	}
	if n := countCalls(m, "GET /api/v1/files/"+h1+"/raw"); n != 1 {
		t.Errorf("--raw did not stream through the API: %v", m.calls())
	}
}

// The file list is read to the end, and a hash that turns up on two pages is
// saved once.
func TestListNoteAttachmentsReadsEveryPage(t *testing.T) {
	var files []saveAllFile
	for i := 1; i <= 600; i++ {
		files = append(files, saveAllFile{hash: saveAllHash(i), filename: fmt.Sprintf("f%d.txt", i), mime: "text/plain"})
	}
	files = append(files, saveAllFile{hash: saveAllHash(7), filename: "again.txt"})
	m := saveAllMock(t, saveAllNote(""), files)

	got, err := listNoteAttachments(client.NewClient(m.baseURL(), "hbp_test"), saveAllNoteID)
	if err != nil {
		t.Fatalf("listNoteAttachments: %v", err)
	}
	if len(got) != 600 {
		t.Fatalf("got %d files, want 600 (one per hash)", len(got))
	}
	if got[6].Filename != "f7.txt" {
		t.Errorf("a repeated hash kept %q, want the first row", got[6].Filename)
	}
	if n := countCalls(m, "GET /api/v1/files"); n != 2 {
		t.Errorf("read %d pages, want 2", n)
	}
}

// embeddedHashes reads <harbor-embed> references the way the server does.
func TestEmbeddedHashes(t *testing.T) {
	a, b := saveAllHash(10), saveAllHash(11)
	body := `<p>x</p>` +
		`<harbor-embed resource="sha256:` + strings.ToUpper(a) + `"></harbor-embed>` +
		`<HARBOR-EMBED type="image/png" resource='` + b + `'/>` +
		`<harbor-embed resource="sha256:` + a + `"></harbor-embed>` +
		`<harbor-embed resource="9e107d9d372bb6826bd81d3542a419d6"></harbor-embed>` +
		`<harbor-embed resource="https://example.com/x.png"></harbor-embed>` +
		`<harbor-embed type="image/png"></harbor-embed>` +
		`<img src="sha256:` + saveAllHash(12) + `">`
	got := embeddedHashes(body)
	if want := []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("embeddedHashes = %v, want %v", got, want)
	}
}

// Shown files come first in body order; the rest sort by name in UTF-16 order,
// then by hash.
func TestOrderAttachments(t *testing.T) {
	files := []noteAttachment{
		{Hash: "c", Filename: "b.txt"},
		{Hash: "b", Filename: "a.txt"},
		{Hash: "a", Filename: "a.txt"},
		// U+FF01 is above every UTF-16 surrogate, so the emoji sorts first by
		// code units even though it is the higher code point.
		{Hash: "d", Filename: "\uFF01.txt"},
		{Hash: "e", Filename: "\U0001F600.txt"},
		{Hash: saveAllHash(1), Filename: "z.txt"},
		{Hash: saveAllHash(2), Filename: "y.txt"},
	}
	body := embedTag(saveAllHash(2)) + embedTag(saveAllHash(1))
	var got []string
	for _, f := range orderAttachments(files, body) {
		got = append(got, f.Hash)
	}
	want := []string{saveAllHash(2), saveAllHash(1), "a", "b", "c", "e", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// compareUTF16 orders by code units, which is not Go's byte order above U+FFFF.
func TestCompareUTF16(t *testing.T) {
	words := []string{"\uFF01", "b", "\U0001F600", "", "a", "ab", "\u00E9"}
	sort.Slice(words, func(i, j int) bool { return compareUTF16(words[i], words[j]) < 0 })
	want := []string{"", "a", "ab", "b", "\u00E9", "\U0001F600", "\uFF01"}
	if strings.Join(words, "|") != strings.Join(want, "|") {
		t.Errorf("sorted = %q, want %q", words, want)
	}
	if compareUTF16("same", "same") != 0 {
		t.Error("equal strings must compare equal")
	}
}

// The usage rules: one hash, or --note alone, and the flags that only fit one
// of them. A mixed-up command is refused before anything is fetched.
func TestFilesDownloadArgs(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"no hash":                {[]string{"files", "download"}, "accepts 1 arg(s)"},
		"hash and --note":        {[]string{"files", "download", "abc", "--note", saveAllNoteID}, "not both"},
		"--note and --output":    {[]string{"files", "download", "--note", saveAllNoteID, "--output", "x"}, "--output-dir"},
		"--note and ciphertext":  {[]string{"files", "download", "--note", saveAllNoteID, "--ciphertext"}, "never writes encrypted bytes"},
		"--output-dir with hash": {[]string{"files", "download", "abc", "--output-dir", "x"}, "--output-dir goes with --note"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newAPIMock(t, map[string]mockReply{})
			_, err := runCLI(t, m, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if len(m.calls()) != 0 {
				t.Errorf("a usage error still hit the network: %v", m.calls())
			}
		})
	}
}

// A name already in the folder is never overwritten; the file gets "(2)".
func TestSaveUnderRealNameNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.PDF"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	used, err := takenNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	path, n, err := saveUnderRealName(dir, used, "Report.pdf", "application/pdf", strings.NewReader("new"))
	if err != nil || n != 3 || path != filepath.Join(dir, "Report (2).pdf") {
		t.Fatalf("saveUnderRealName = %q, %d, %v; want Report (2).pdf", path, n, err)
	}
	if got := readSaved(t, dir); !equalMaps(got, map[string]string{"report.PDF": "old", "Report (2).pdf": "new"}) {
		t.Errorf("folder = %v", got)
	}
}

// A file that appears after the folder was read is still not overwritten: the
// create fails on it and the next name is used.
func TestSaveUnderRealNameSkipsANameThatAppeared(t *testing.T) {
	dir := t.TempDir()
	used := map[string]bool{}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, _, err := saveUnderRealName(dir, used, "a.txt", "text/plain", strings.NewReader("ours"))
	if err != nil || path != filepath.Join(dir, "a (2).txt") {
		t.Fatalf("saveUnderRealName = %q, %v; want a (2).txt", path, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "theirs" {
		t.Errorf("a.txt was overwritten: %q", b)
	}
}

// A write that fails part way leaves no partial file and gives its name back, so
// the next file with that name gets it.
func TestSaveUnderRealNameGivesTheNameBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	used := map[string]bool{}
	broken := io.MultiReader(strings.NewReader("half"), iotestErrReader{})
	if _, _, err := saveUnderRealName(dir, used, "a.txt", "text/plain", broken); err == nil {
		t.Fatal("a failed stream was reported as saved")
	}
	if got := readSaved(t, dir); len(got) != 0 {
		t.Errorf("a partial file was left behind: %v", got)
	}
	path, _, err := saveUnderRealName(dir, used, "a.txt", "text/plain", strings.NewReader("whole"))
	if err != nil || path != filepath.Join(dir, "a.txt") {
		t.Errorf("next save = %q, %v; want the freed name a.txt", path, err)
	}
}

// openAttachmentBytes refuses sealed bytes it cannot open, rather than handing
// back ciphertext, and passes plain bytes through untouched.
func TestOpenAttachmentBytes(t *testing.T) {
	key := newMasterKey(t)
	sealed, err := crypto.SealBytes(key, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	unlockedSession(t, make([]byte, 32)) // the wrong key
	if r, reason := openAttachmentBytes(nil, nil, strings.NewReader(string(sealed))); r != nil || !strings.Contains(reason, "did not decrypt") {
		t.Errorf("wrong key: reader %v, reason %q", r, reason)
	}

	unlockedSession(t, key)
	r, reason := openAttachmentBytes(nil, nil, strings.NewReader(string(sealed)))
	if reason != "" {
		t.Fatalf("right key: reason %q", reason)
	}
	if got := string(readAll(t, r)); got != "secret" {
		t.Errorf("decrypted = %q", got)
	}

	lockedSession(t)
	r, reason = openAttachmentBytes(nil, nil, strings.NewReader("plain"))
	if reason != "" || string(readAll(t, r)) != "plain" {
		t.Errorf("plain bytes: reason %q", reason)
	}
}

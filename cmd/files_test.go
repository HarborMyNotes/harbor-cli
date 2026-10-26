// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/crypto"
)

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	h, n, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile error: %v", err)
	}
	// sha256("abc") is a known constant.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if h != want {
		t.Errorf("hash = %s, want %s", h, want)
	}
	if n != 3 {
		t.Errorf("size = %d, want 3", n)
	}
}

func TestWriteOutputToFileAndStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	n, err := writeOutput(path, bytes.NewReader([]byte("hello")))
	if err != nil || n != 5 {
		t.Fatalf("writeOutput file: n=%d err=%v", n, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "hello" {
		t.Errorf("file content = %q", b)
	}

	out := captureStdout(t, func() {
		_, _ = writeOutput("-", bytes.NewReader([]byte("piped")))
	})
	if out != "piped" {
		t.Errorf("stdout content = %q", out)
	}
}

// TestWriteOutputRemovesAPartialFile drops a download that broke part way, so
// a truncated file is never left looking like a finished one.
func TestWriteOutputRemovesAPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.bin")
	broken := io.MultiReader(strings.NewReader("half a file"), iotestErrReader{})

	if _, err := writeOutput(path, broken); err == nil {
		t.Fatal("a failed stream was reported as written")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the partial file is still there (stat err = %v)", err)
	}
}

// TestWriteOutputKeepsASymlinkOnFailure leaves a symlink at the output path in
// place when the download breaks. The user made that link; only a plain file
// the download itself produced is ours to clean up.
func TestWriteOutputKeepsASymlinkOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	broken := io.MultiReader(strings.NewReader("half a file"), iotestErrReader{})

	if _, err := writeOutput(link, broken); err == nil {
		t.Fatal("a failed stream was reported as written")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("the symlink was removed: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the path is no longer a symlink (mode %v)", info.Mode())
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the symlink's target was removed: %v", err)
	}
}

// iotestErrReader fails every read, standing in for a dropped connection.
type iotestErrReader struct{}

// Read always fails.
func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestFilenameFromContentDisposition covers the shapes the header actually
// arrives in.
//
// The semicolon rows are the ones that matter. A note title is free text, so
// semicolons in it are ordinary, and `notes export` names its file from the
// title — cutting the header at the first ";" turns "Q3 planning; Dana.md" into
// "Q3 planning": no extension, so nothing opens it, and two such notes exported
// into one directory overwrite each other.
func TestFilenameFromContentDisposition(t *testing.T) {
	cases := map[string]string{
		`attachment; filename="diagram.png"`:           "diagram.png",
		`attachment; filename=report.pdf`:              "report.pdf",
		`inline`:                                       "",
		`attachment; filename="Q3 planning; Dana.md"`:  "Q3 planning; Dana.md",
		`attachment; filename="Café ünïcode probe.md"`: "Café ünïcode probe.md",
		`attachment; filename*=UTF-8''Caf%C3%A9.md`:    "Café.md",
		`attachment; filename="Plan.zip"; size=42`:     "Plan.zip",
		`attachment`: "",
		// Headers a strict parser rejects outright. The scan behind it still
		// recovers a usable name, which is the whole reason it is kept.
		`attachment; filename="Plan.md"; badparam`:     "Plan.md",
		`attachment; filename="a.md"; filename="b.md"`: "a.md",
		// Go decodes filename* without checking the result and prefers it over
		// filename, so a broken one must not beat a good plain name.
		`attachment; filename="fallback.md"; filename*=UTF-8''%E2%82`: "fallback.md",
		// Legitimate non-ASCII must survive the validity check that rejects the
		// row above — note titles are whatever the user typed.
		`attachment; filename*=UTF-8''%E5%9B%9B%E5%8D%8A%E6%9C%9F.md`: "四半期.md",
		`attachment; filename="Plan 🚢.md"`:                            "Plan 🚢.md",
		// What the server sends today: an ASCII stand-in AND the exact name.
		// The exact one wins, emoji and all.
		`attachment; filename="Welcome to Harbor _.html"; filename*=UTF-8''Welcome%20to%20Harbor%20%F0%9F%91%8B.html`: "Welcome to Harbor 👋.html",
	}
	for in, want := range cases {
		if got := filenameFromContentDisposition(in); got != want {
			t.Errorf("filenameFromContentDisposition(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayFiles(t *testing.T) {
	data := []byte(`{"data":[{"hash":"e3b0c44298fc","mime":"image/png","size":1048576,"ocr_status":"done","thumb_status":"done","filename":"diagram.png","notes":[{"note_id":"a1"},{"note_id":"c3"}]}],"paging":{"offset":0,"total":1}}`)
	out := captureStdout(t, func() { displayFiles(data) })
	if !strings.Contains(out, "diagram.png") || !strings.Contains(out, "1.0 MB") {
		t.Errorf("files table missing fields:\n%s", out)
	}
	if !strings.Contains(out, "2") { // notes count
		t.Errorf("notes count missing:\n%s", out)
	}
	// The status column covers Office and text files too, so it is not "OCR".
	if !strings.Contains(out, "TEXT") || strings.Contains(out, "OCR") {
		t.Errorf("status column should be TEXT, not OCR:\n%s", out)
	}
}

// TestDisplayResourceSaysTextStatus checks a single file's details label its
// ocr_status as the text status, not OCR.
func TestDisplayResourceSaysTextStatus(t *testing.T) {
	data := []byte(`{"hash":"e3b0c44298fc","filename":"Budget.xlsx","mime":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","size":2048,"ocr_status":"done","thumb_status":"skipped_unsupported","usn":7,"created_at":1750000000000}`)
	out := captureStdout(t, func() { displayResource(data) })
	if !strings.Contains(out, "Text status") || !strings.Contains(out, "done") {
		t.Errorf("details should show the text status:\n%s", out)
	}
	if strings.Contains(out, "OCR") {
		t.Errorf("details should not say OCR:\n%s", out)
	}
}

// TestFilesListOCRStatusFlagHelp checks --ocr-status keeps its name but is
// described as the text status, with the values the server accepts.
func TestFilesListOCRStatusFlagHelp(t *testing.T) {
	f := filesListCmd.Flags().Lookup("ocr-status")
	if f == nil {
		t.Fatal("--ocr-status flag is missing")
	}
	if !strings.HasPrefix(f.Usage, "Filter by text status: ") {
		t.Errorf("--ocr-status usage = %q", f.Usage)
	}
	values := strings.Split(strings.TrimPrefix(f.Usage, "Filter by text status: "), "|")
	want := []string{"pending", "processing", "done", "failed", "skipped_encrypted", "skipped_unsupported"}
	if strings.Join(values, "|") != strings.Join(want, "|") {
		t.Errorf("--ocr-status values = %v, want %v", values, want)
	}
}

func TestMapFileError(t *testing.T) {
	cases := map[string]string{
		"file_too_large":   "maximum upload size",
		"unsupported_type": "not allowed",
		"blob_missing":     "not stored",
		"not_found":        "no file with that hash",
	}
	for code, sub := range cases {
		if got := mapFileError(apiErr(code)); !strings.Contains(got.Error(), sub) {
			t.Errorf("mapFileError(%s) = %q", code, got.Error())
		}
	}
}

// readAll drains a reader in tests, failing rather than returning an error.
func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	return b
}

// TestDecryptDownloadPassesPlaintextThrough proves an ordinary blob is handed
// back byte-identical, including files shorter than the sniff window — reading
// the first 33 bytes must never truncate a 5-byte file.
func TestDecryptDownloadPassesPlaintextThrough(t *testing.T) {
	lockedSession(t)
	for _, body := range []string{"", "x", "hello", "just a normal file, not encrypted at all", strings.Repeat("z", 5000)} {
		got, _, err := decryptDownload(nil, nil, strings.NewReader(body), false)
		if err != nil {
			t.Fatalf("decryptDownload(%d bytes): %v", len(body), err)
		}
		if out := string(readAll(t, got)); out != body {
			t.Fatalf("plaintext altered: got %d bytes, want %d", len(out), len(body))
		}
	}
}

// TestDecryptDownloadDecryptsEnvelope proves an encrypted blob is unwrapped when
// the session is unlocked, which is the whole point of the transparent path.
func TestDecryptDownloadDecryptsEnvelope(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)

	original := []byte("the real file bytes, secret")
	sealed, err := crypto.SealBytes(key, original)
	if err != nil {
		t.Fatalf("SealBytes: %v", err)
	}
	got, _, err := decryptDownload(nil, nil, bytes.NewReader(sealed), false)
	if err != nil {
		t.Fatalf("decryptDownload: %v", err)
	}
	if out := readAll(t, got); !bytes.Equal(out, original) {
		t.Fatalf("decrypted = %q, want %q", out, original)
	}
}

// TestDecryptDownloadRefusesWhenLocked proves the CLI does NOT write ciphertext
// into a file the user thinks is their document, and that the refusal names both
// the env var and the escape hatch.
func TestDecryptDownloadRefusesWhenLocked(t *testing.T) {
	key := newMasterKey(t)
	sealed, err := crypto.SealBytes(key, []byte("secret"))
	if err != nil {
		t.Fatalf("SealBytes: %v", err)
	}
	lockedSession(t)

	got, _, err := decryptDownload(nil, nil, bytes.NewReader(sealed), false)
	if err == nil {
		t.Fatalf("expected a refusal, got %q", readAll(t, got))
	}
	for _, want := range []string{"encrypted", "HARBOR_PASSPHRASE", "--ciphertext", "nothing was written"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
}

// TestDecryptDownloadCiphertextOptOut proves --ciphertext hands back the raw
// envelope untouched, for backups and moving bytes between machines.
func TestDecryptDownloadCiphertextOptOut(t *testing.T) {
	key := newMasterKey(t)
	sealed, err := crypto.SealBytes(key, []byte("secret"))
	if err != nil {
		t.Fatalf("SealBytes: %v", err)
	}
	lockedSession(t)

	got, kept, err := decryptDownload(nil, nil, bytes.NewReader(sealed), true)
	if err != nil {
		t.Fatalf("decryptDownload(--ciphertext): %v", err)
	}
	if out := readAll(t, got); !bytes.Equal(out, sealed) {
		t.Fatalf("--ciphertext altered the envelope: %d bytes, want %d", len(out), len(sealed))
	}
	if !kept {
		t.Error("the envelope was handed back without saying it is still sealed")
	}
}

// TestDecryptDownloadWrongKey proves a blob sealed under a different key fails
// closed with an explanation rather than writing garbage to disk.
func TestDecryptDownloadWrongKey(t *testing.T) {
	sealed, err := crypto.SealBytes(newMasterKey(t), []byte("secret"))
	if err != nil {
		t.Fatalf("SealBytes: %v", err)
	}
	unlockedSession(t, make([]byte, 32)) // a different, all-zero key

	if _, _, err := decryptDownload(nil, nil, bytes.NewReader(sealed), false); err == nil {
		t.Fatal("expected a decrypt failure")
	} else if !strings.Contains(err.Error(), "nothing was written") {
		t.Errorf("error should say nothing was written: %v", err)
	}
}

// TestFilesKeyRefusals proves the upload path refuses with actionable text
// rather than uploading a plaintext file stamped is_encrypted.
func TestFilesKeyRefusals(t *testing.T) {
	lockedSession(t)
	_, err := filesKey(nil, nil)
	if err == nil {
		t.Fatal("expected a refusal with no passphrase set")
	}
	for _, want := range []string{"HARBOR_PASSPHRASE", "nothing was uploaded", "in the clear"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
}

// TestUploadEncryptedSendsEnvelopeNotPlaintext is the regression guard for the
// bug this feature replaced: an upload that stamped the resource is_encrypted
// while putting the file on the server in the clear. It asserts against the
// actual multipart body on the wire, so reverting to a plaintext upload fails
// here even though every other test would still pass.
func TestUploadEncryptedSendsEnvelopeNotPlaintext(t *testing.T) {
	const marker = "ATTACHMENT-PLAINTEXT-MARKER-12345"

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		if r.URL.Path != "/files/upload" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"hash":"abc","filename":"secret.txt","is_encrypted":true}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte(marker), 0644); err != nil {
		t.Fatal(err)
	}
	key := newMasterKey(t)
	if _, err := uploadEncrypted(client.NewClient(srv.URL, "at_test"), key, path, "", ""); err != nil {
		t.Fatalf("uploadEncrypted: %v", err)
	}

	if bytes.Contains(body, []byte(marker)) {
		t.Fatal("the plaintext reached the server — the bytes were not encrypted before upload")
	}
	if !bytes.Contains(body, []byte("HRBC2")) {
		t.Fatal("no HRBC2 envelope in the multipart body")
	}
	// Filename and MIME come from the ORIGINAL file, not from the ciphertext.
	for _, want := range []string{"secret.txt", "text/plain", "is_encrypted"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("multipart body missing %q", want)
		}
	}

	// And what was sent must decrypt back to the original file.
	start := bytes.Index(body, []byte("HRBC2"))
	sealed := body[start : start+crypto.BinaryEnvelopeMinBytes+len(marker)]
	got, err := crypto.OpenBytes(key, sealed)
	if err != nil {
		t.Fatalf("the uploaded envelope does not decrypt: %v", err)
	}
	if string(got) != marker {
		t.Fatalf("decrypted = %q, want %q", got, marker)
	}
}

// TestFilesKeyNoKeystore covers the other refusal branch: a passphrase is set
// but the account has no keys yet.
func TestFilesKeyNoKeystore(t *testing.T) {
	resetSession()
	t.Cleanup(resetSession)
	t.Setenv("HARBOR_PASSPHRASE", "pw")
	t.Setenv("HOME", t.TempDir())

	sessionUnlockd, sessionKey, sessionErr = true, nil, errNoKeystore
	_, err := filesKey(nil, nil)
	if err == nil {
		t.Fatal("expected a refusal when no keystore exists")
	}
	for _, want := range []string{"crypto setup", "nothing was uploaded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
}

// TestFilesKeyHappyPath proves filesKey returns the unlocked key unchanged, so
// the refusal wrapper cannot break the working case.
func TestFilesKeyHappyPath(t *testing.T) {
	key := newMasterKey(t)
	unlockedSession(t, key)
	got, err := filesKey(nil, nil)
	if err != nil {
		t.Fatalf("filesKey: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("filesKey returned a different key")
	}
}

// TestRawDownloadKeepsTheServersNameInTheWorkingDirectory pins the other place a
// filename arrives over the network.
//
// With no --output, the server's name becomes the output path, so one holding
// ".." must not write outside the directory the command was run in. The rule
// turns each "/" into "_" and drops the leading dots.
func TestRawDownloadKeepsTheServersNameInTheWorkingDirectory(t *testing.T) {
	// Run two levels down inside the temp dir so the escape this asserts against
	// has somewhere to land that the test still owns. Pointing "../.." at the
	// real filesystem would litter it on the way to failing.
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="../../ESCAPED.txt"`)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("bytes"))
	}

	if _, err := runCLI(t, m, "files", "download", "abc123", "--raw"); err != nil {
		t.Fatalf("files download --raw: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "_.._ESCAPED.txt")); err != nil {
		t.Errorf("the download did not land in the working directory: %v", err)
	}
	for _, outside := range []string{filepath.Join(root, "ESCAPED.txt"), filepath.Join(root, "a", "ESCAPED.txt")} {
		if _, err := os.Stat(outside); err == nil {
			t.Errorf("the server's filename walked out of the working directory to %s", outside)
		}
	}
}

// downloadHash is a well-formed sha256 for the single-file download tests.
const downloadHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// downloadMock serves one file for `files download <hash>`: its metadata, and its
// bytes behind a presigned URL on the same server.
func downloadMock(t *testing.T, filename, mimeType, content string) *apiMock {
	t.Helper()
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/" + downloadHash:
			_, _ = fmt.Fprintf(w, `{"download_url":%q,"filename":%q,"mime":%q,"size":%d}`,
				m.srv.URL+"/blob", filename, mimeType, len(content))
		case "/blob":
			_, _ = w.Write([]byte(content))
		default:
			t.Errorf("unrouted request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return m
}

// With no --output, a download is saved under the real-name rule's name, not its
// hash, and a second download of the same file never overwrites the first.
func TestFilesDownloadSavesUnderTheRealName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := downloadMock(t, "Report", "application/pdf", "pdf bytes")

	out, err := runCLI(t, m, "files", "download", downloadHash)
	if err != nil {
		t.Fatalf("files download: %v", err)
	}
	if !strings.Contains(out, "to Report.pdf") {
		t.Errorf("output = %q, want it to name Report.pdf", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "Report.pdf"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, m, "files", "download", downloadHash); err != nil {
		t.Fatalf("second download: %v", err)
	}
	for name, want := range map[string]string{"Report.pdf": "edited", "Report (2).pdf": "pdf bytes"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != want {
			t.Errorf("%s = %q (%v), want %q", name, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, downloadHash)); err == nil {
		t.Error("the file was saved under its hash")
	}
}

// --output names the file yourself, and still writes over what is there.
func TestFilesDownloadOutputStillWins(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := downloadMock(t, "Report.pdf", "application/pdf", "new bytes")
	if err := os.WriteFile(filepath.Join(dir, "mine.pdf"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, m, "files", "download", downloadHash, "--output", "mine.pdf"); err != nil {
		t.Fatalf("files download --output: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "mine.pdf")); string(b) != "new bytes" {
		t.Errorf("mine.pdf = %q, want the download", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "Report.pdf")); err == nil {
		t.Error("--output was ignored: the stored name was used too")
	}
}

// ===========================================================================
// Upload size pre-check
// ===========================================================================

// uploadMock builds a stub API for the upload command: the root /client-flags
// document plus a successful upload route. flags is the raw body served for the
// policy probe, so a test can publish a cap, withhold one, or fail the probe.
func uploadMock(t *testing.T, flagsStatus int, flags string) *apiMock {
	t.Helper()
	return newAPIMock(t, map[string]mockReply{
		"GET /client-flags":         {Status: flagsStatus, Body: flags},
		"POST /api/v1/files/upload": {Status: 201, Body: `{"hash":"abc123","size":10,"mime":"text/plain"}`},
	})
}

// writeSized writes a file of exactly n bytes and returns its path.
func writeSized(t *testing.T, name string, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// An over-cap file must be refused before a single byte is streamed — the whole
// point of publishing the policy — so the test asserts the absence of the
// upload call as much as the message.
//
// The cap and the file size are chosen so that nearest and round-up rounding
// DISAGREE for both numbers (1050 B is 1 KB nearest but 1.1 KB rounded up;
// 1075 B likewise). Rounder values let either half of the rounding rule be
// flipped without any test noticing, which is how the self-contradicting
// "is 1 KB — the per-file limit is 1 KB" gets back in.
func TestFilesUploadRefusesOverCapBeforeStreaming(t *testing.T) {
	m := uploadMock(t, 200, `{"max_upload_bytes":1050,"allowed_mime":"*"}`)
	path := writeSized(t, "big.bin", 1075)

	_, err := runCLI(t, m, "files", "upload", path)
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}

	// Catching it early must be indistinguishable from catching it late, so the
	// refusal carries the server's own code and --json reports it.
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("refusal must carry the API error shape, got %T: %v", err, err)
	}
	if apiErr.Code != "file_too_large" {
		t.Errorf("code = %q, want file_too_large (--json would report cli_error)", apiErr.Code)
	}
	want := `"big.bin" is 1.1 KB — the per-file limit is 1 KB`
	if apiErr.Message != want {
		t.Errorf("message = %q, want %q", apiErr.Message, want)
	}

	for _, call := range m.calls() {
		if strings.HasPrefix(call, "POST") {
			t.Errorf("refused upload still sent %s (calls: %v)", call, m.calls())
		}
	}

	// The probe is built as its own tokenless client because /client-flags is
	// public. The session's token is set for this run, so an inherited client
	// would carry it here.
	if auth := m.authOf(t, "GET /client-flags"); auth != "" {
		t.Errorf("policy probe sent %q, want no Authorization on a public endpoint", auth)
	}
}

// Fail open means the cap is UNKNOWN, not a guess. Asserting the exact zero is
// what stops a fallback constant reappearing: a default of 100 MB sails past
// any test that merely checks an over-cap file still uploads, because the test
// file is smaller than the invented number.
func TestUploadSizeCapIsZeroWhenUnknown(t *testing.T) {
	serve := func(t *testing.T, status int, body string) *client.Client {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return client.NewClient(srv.URL+"/api/v1", "")
	}

	unknown := map[string]struct {
		status int
		body   string
	}{
		"probe fails":   {500, `{"error":{"code":"internal_error","message":"boom"}}`},
		"field missing": {200, `{"offline_priming":true}`},
		"unparseable":   {200, `not json`},
		"empty body":    {200, ``},
		"zero cap":      {200, `{"max_upload_bytes":0}`},
		"negative cap":  {200, `{"max_upload_bytes":-1}`},
	}
	for name, tc := range unknown {
		t.Run(name, func(t *testing.T) {
			if got := uploadSizeCap(serve(t, tc.status, tc.body)); got != 0 {
				t.Errorf("uploadSizeCap = %d, want 0 — a guessed cap refuses files the server would take", got)
			}
		})
	}

	// The positive control: a published cap really is read, so the zeros above
	// are proving fail-open rather than a probe that never works at all.
	if got := uploadSizeCap(serve(t, 200, `{"max_upload_bytes":104857600}`)); got != 104857600 {
		t.Errorf("uploadSizeCap = %d, want 104857600", got)
	}
}

// A directory can never be uploaded, so it is named as one instead of dying
// downstream inside the multipart copy.
//
// The exact message and the empty call log both matter. Without the guard the
// failure still mentions "is a directory" and still sends no POST, so a looser
// assertion passes either way; what actually changes is the wording, the
// pointless policy probe, and the exit code.
func TestFilesUploadRefusesADirectory(t *testing.T) {
	m := uploadMock(t, 200, `{"max_upload_bytes":104857600,"allowed_mime":"*"}`)
	dir := filepath.Join(t.TempDir(), "a-folder")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, m, "files", "upload", dir)
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if want := `"a-folder" is a directory`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if len(m.calls()) != 0 {
		t.Errorf("a directory is a purely local fact but still hit the network: %v", m.calls())
	}
}

// A path that does not exist is a purely local fact, so it must not cost a
// network round-trip before the user is told.
func TestFilesUploadDoesNotProbeForAMissingPath(t *testing.T) {
	m := uploadMock(t, 200, `{"max_upload_bytes":104857600,"allowed_mime":"*"}`)

	if _, err := runCLI(t, m, "files", "upload", filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Fatal("expected an error for a missing path")
	}
	if len(m.calls()) != 0 {
		t.Errorf("missing path still hit the network: %v", m.calls())
	}
}

// The server's own predicate accepts a file sized exactly at the cap, so a
// client that refused it would block an upload the server would take.
func TestFilesUploadAcceptsFileExactlyAtCap(t *testing.T) {
	m := uploadMock(t, 200, `{"max_upload_bytes":1024,"allowed_mime":"*"}`)
	path := writeSized(t, "exact.bin", 1024)

	if _, err := runCLI(t, m, "files", "upload", path); err != nil {
		t.Fatalf("file exactly at the cap must upload, got %v", err)
	}
	if !strings.Contains(strings.Join(m.calls(), " "), "POST /api/v1/files/upload") {
		t.Errorf("upload was not sent: %v", m.calls())
	}
}

// Fail open is absolute: an unreachable or older server, or a document without
// the field, means "pre-validate nothing and let the server decide" — never a
// guessed cap, which would refuse good files the day an operator raises it.
func TestFilesUploadFailsOpenWithoutAPublishedCap(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"probe fails":   {500, `{"error":{"code":"internal_error","message":"boom"}}`},
		"field missing": {200, `{"offline_priming":true}`},
		"unparseable":   {200, `not json`},
		"zero cap":      {200, `{"max_upload_bytes":0}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := uploadMock(t, tc.status, tc.body)
			path := writeSized(t, "big.bin", 2000)

			if _, err := runCLI(t, m, "files", "upload", path); err != nil {
				t.Fatalf("must fail open and upload, got %v", err)
			}
			if !strings.Contains(strings.Join(m.calls(), " "), "POST /api/v1/files/upload") {
				t.Errorf("upload was not sent: %v", m.calls())
			}
		})
	}
}

// The friendlier prose must not cost the user the diagnostics — the code line,
// the detail bullets and --verbose's http/request_id all come off the typed
// value, and --json reports its code.
func TestMapFileErrorKeepsTheAPIError(t *testing.T) {
	original := &client.APIError{
		Code:      "file_too_large",
		Message:   "file exceeds the configured maximum",
		Details:   map[string]any{"max_bytes": "104857600"},
		RequestID: "req_abc123",
		Status:    422,
	}

	var got *client.APIError
	if !errors.As(mapFileError(original), &got) {
		t.Fatal("mapFileError destroyed the *client.APIError")
	}
	if got.Message != "the file exceeds the maximum upload size" {
		t.Errorf("friendly prose lost: %q", got.Message)
	}
	if got.Code != "file_too_large" {
		t.Errorf("code = %q, want file_too_large (--json would report cli_error)", got.Code)
	}
	if got.RequestID != "req_abc123" || got.Status != 422 {
		t.Errorf("verbose diagnostics lost: request_id=%q status=%d", got.RequestID, got.Status)
	}
	if lines := got.DetailLines(); len(lines) != 1 || !strings.Contains(lines[0], "max_bytes") {
		t.Errorf("detail bullets lost: %v", lines)
	}
	if original.Message != "file exceeds the configured maximum" {
		t.Errorf("mapFileError mutated the caller's error: %q", original.Message)
	}
}

// ===========================================================================
// Rename
// ===========================================================================

// renameHash is a well-formed sha256 for the rename tests.
const renameHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// renameMock builds a stub API for the rename command: the lookup that reports
// the stored name, plus a rename route that answers with the given reply.
func renameMock(t *testing.T, stored string, rename mockReply) *apiMock {
	t.Helper()
	return newAPIMock(t, map[string]mockReply{
		"GET /api/v1/files/" + renameHash:   {Status: 200, Body: `{"download_url":"https://s3/x","mime":"application/pdf","size":10,"filename":"` + stored + `"}`},
		"PATCH /api/v1/files/" + renameHash: rename,
	})
}

// The split must match the server's, because the server judges a rename by it:
// a client that reads ".bashrc" as an extension would add it to every new name.
func TestFileExtension(t *testing.T) {
	cases := map[string]string{
		"invoice.pdf": ".pdf",
		"Scan.PDF":    ".PDF",
		"a.tar.gz":    ".gz",
		"clip.mp4":    ".mp4",
		".bashrc":     "",
		"README":      "",
		"notes.":      "",
		"Dr. Smith":   "",
		"v1.2-final":  "",
		"résumé.docx": ".docx",
		"photo.jpé":   "",
		"":            "",
	}
	for in, want := range cases {
		if got := fileExtension(in); got != want {
			t.Errorf("fileExtension(%q) = %q, want %q", in, got, want)
		}
	}
}

// A name typed without the extension gets the stored one; a name that already
// ends with it is sent as typed.
func TestWithStoredExtension(t *testing.T) {
	cases := []struct{ name, stored, want string }{
		{"Scan 2015-01-06", "scan0001.pdf", "Scan 2015-01-06.pdf"},
		{"Scan 2015-01-06.pdf", "scan0001.pdf", "Scan 2015-01-06.pdf"},
		// The case of a typed extension is the server's to settle, so it is not
		// doubled up here.
		{"invoice.pdf", "Scan.PDF", "invoice.pdf"},
		{"invoice", "Scan.PDF", "invoice.PDF"},
		// A dot in the typed name is not an extension of its own.
		{"Scan v1.2", "scan0001.pdf", "Scan v1.2.pdf"},
		{"report.docx", "scan0001.pdf", "report.docx.pdf"},
		{"pdf", "scan0001.pdf", "pdf.pdf"},
		{"  Scan  ", "scan0001.pdf", "Scan.pdf"},
		{"backup", "a.tar.gz", "backup.gz"},
		// A file with no extension takes the name as typed.
		{"notes.txt", "README", "notes.txt"},
		{"profile", ".bashrc", "profile"},
		{"Scan", "", "Scan"},
		// A multi-byte name shorter than the extension must not be sliced.
		{"é", "scan0001.jpeg", "é.jpeg"},
	}
	for _, tc := range cases {
		if got := withStoredExtension(tc.name, tc.stored); got != tc.want {
			t.Errorf("withStoredExtension(%q, %q) = %q, want %q", tc.name, tc.stored, got, tc.want)
		}
	}
}

// The command reads the stored name first, then sends the full new name.
func TestFilesRenameKeepsTheExtension(t *testing.T) {
	m := renameMock(t, "scan0001.pdf", mockReply{Status: 200, Body: `{"hash":"` + renameHash + `","filename":"Scan 2015-01-06.pdf","mime":"application/pdf","size":10}`})

	out, err := runCLI(t, m, "files", "rename", renameHash, "Scan 2015-01-06")
	if err != nil {
		t.Fatalf("files rename: %v", err)
	}
	if got := m.bodyOf(t, "PATCH /api/v1/files/"+renameHash)["filename"]; got != "Scan 2015-01-06.pdf" {
		t.Errorf("sent filename = %v, want the typed name plus the stored extension", got)
	}
	if !strings.Contains(out, "Scan 2015-01-06.pdf") {
		t.Errorf("the new name is missing from the output:\n%s", out)
	}
}

// A file whose bytes are missing has no readable stored name, but it can still
// be renamed: the name goes out as typed and the server judges it.
func TestFilesRenameSendsTheNameAsTypedWhenTheBytesAreMissing(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/files/" + renameHash:   {Status: 404, Body: apiErrorBody("blob_missing", "The file's bytes are missing.")},
		"PATCH /api/v1/files/" + renameHash: {Status: 200, Body: `{"hash":"` + renameHash + `","filename":"invoice.pdf"}`},
	})

	if _, err := runCLI(t, m, "files", "rename", renameHash, "invoice.pdf"); err != nil {
		t.Fatalf("files rename: %v", err)
	}
	if got := m.bodyOf(t, "PATCH /api/v1/files/"+renameHash)["filename"]; got != "invoice.pdf" {
		t.Errorf("sent filename = %v, want the name as typed", got)
	}
}

// An unknown hash stops at the lookup: nothing is renamed, and the message says
// what a hash has to look like.
func TestFilesRenameUnknownHash(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/files/abc123": {Status: 404, Body: apiErrorBody("not_found", "The requested resource was not found.")},
	})

	_, err := runCLI(t, m, "files", "rename", "abc123", "invoice")
	if err == nil {
		t.Fatal("expected an error for an unknown hash")
	}
	if !strings.Contains(err.Error(), "no file with that hash") {
		t.Errorf("error = %q, want it to name the hash as the problem", err.Error())
	}
	for _, call := range m.calls() {
		if strings.HasPrefix(call, "PATCH") {
			t.Errorf("an unknown hash still sent %s", call)
		}
	}
}

// The server's reason for refusing a name must reach the user: it lives in the
// error's details, which only the typed error carries to the renderer.
func TestFilesRenameShowsTheServersRefusal(t *testing.T) {
	refusal := `{"error":{"code":"validation_failed","message":"The request failed validation.","details":{"filename":"must not contain /, \\ or control characters"}}}`
	m := renameMock(t, "scan0001.pdf", mockReply{Status: 422, Body: refusal})

	_, err := runCLI(t, m, "files", "rename", renameHash, "a/b")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("refusal must keep the API error shape, got %T: %v", err, err)
	}
	want := `filename: must not contain /, \ or control characters`
	if lines := apiErr.DetailLines(); len(lines) != 1 || lines[0] != want {
		t.Errorf("detail lines = %v, want [%s]", lines, want)
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

// The real server's /raw stream names no file, so a --raw download looks up the
// stored name and type rather than saving every file as "attachment".
func TestFilesDownloadRawUsesTheStoredName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/" + downloadHash + "/raw":
			// What the server sends: a type and a length, no Content-Disposition.
			w.Header().Set("Content-Type", "application/vnd.ms-excel")
			_, _ = w.Write([]byte("sheet"))
		case "/api/v1/files/" + downloadHash:
			_, _ = fmt.Fprintf(w, `{"download_url":"https://s3.invalid/x","filename":"Q3 Budget.xlsx","mime":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}`)
		default:
			t.Errorf("unrouted request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}

	if _, err := runCLI(t, m, "files", "download", downloadHash, "--raw"); err != nil {
		t.Fatalf("files download --raw: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Q3 Budget.xlsx")); err != nil || string(b) != "sheet" {
		t.Errorf("Q3 Budget.xlsx = %q (%v), want the download under its stored name", b, err)
	}
}

// rawMock serves a --raw download the way the real server does: the bytes with a
// Content-Type and no name. meta answers GET /files/:hash; an empty one fails it.
func rawMock(t *testing.T, contentType, meta string) *apiMock {
	t.Helper()
	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/" + downloadHash + "/raw":
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write([]byte("bytes"))
		case "/api/v1/files/" + downloadHash:
			if meta == "" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(apiErrorBody("internal", "boom")))
				return
			}
			_, _ = w.Write([]byte(meta))
		default:
			t.Errorf("unrouted request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return m
}

// The stored type beats the stream's generic one, so a name with no extension
// still gets the right one.
func TestFilesDownloadRawUsesTheStoredType(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := rawMock(t, "application/octet-stream", `{"download_url":"https://s3.invalid/x","filename":"Report","mime":"application/pdf"}`)

	if _, err := runCLI(t, m, "files", "download", downloadHash, "--raw"); err != nil {
		t.Fatalf("files download --raw: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Report.pdf")); err != nil {
		t.Errorf("want Report.pdf: %v", err)
	}
}

// If the name cannot be looked up, the download is still saved, under the rule's
// fallback name and the stream's type.
func TestFilesDownloadRawWhenTheLookupFails(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := rawMock(t, "image/png", "")

	if _, err := runCLI(t, m, "files", "download", downloadHash, "--raw"); err != nil {
		t.Fatalf("files download --raw: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "attachment.png")); err != nil || string(b) != "bytes" {
		t.Errorf("attachment.png = %q (%v)", b, err)
	}
}

// Without --raw the presigned answer already names the file, so a file with no
// stored name is not looked up a second time.
func TestFilesDownloadLooksUpTheNameOnlyForRaw(t *testing.T) {
	t.Chdir(t.TempDir())
	m := downloadMock(t, "", "image/png", "png bytes")

	if _, err := runCLI(t, m, "files", "download", downloadHash); err != nil {
		t.Fatalf("files download: %v", err)
	}
	if n := countCalls(m, "GET /api/v1/files/"+downloadHash); n != 1 {
		t.Errorf("the file was looked up %d times, want once", n)
	}
}

// A file that is not encrypted keeps its own name under --ciphertext: there is
// no envelope to keep away from it.
func TestFilesDownloadCiphertextOnAPlainFileKeepsItsName(t *testing.T) {
	lockedSession(t)
	dir := t.TempDir()
	t.Chdir(dir)
	m := downloadMock(t, "Notes.txt", "text/plain", "plain words")

	if _, err := runCLI(t, m, "files", "download", downloadHash, "--ciphertext"); err != nil {
		t.Fatalf("files download --ciphertext: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Notes.txt")); err != nil || string(b) != "plain words" {
		t.Errorf("Notes.txt = %q (%v)", b, err)
	}
}

// --ciphertext without --output saves the sealed bytes under the file's hash, so
// an envelope never sits under a name like "secret.pdf" that promises the file.
func TestFilesDownloadCiphertextKeepsTheHashName(t *testing.T) {
	lockedSession(t)
	dir := t.TempDir()
	t.Chdir(dir)
	sealed, err := crypto.SealBytes(newMasterKey(t), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	m := downloadMock(t, "secret.pdf", "application/pdf", string(sealed))

	if _, err := runCLI(t, m, "files", "download", downloadHash, "--ciphertext"); err != nil {
		t.Fatalf("files download --ciphertext: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, downloadHash)); err != nil || string(b) != string(sealed) {
		t.Errorf("the envelope was not saved under the hash (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.pdf")); err == nil {
		t.Error("the sealed bytes took the real file's name")
	}
}

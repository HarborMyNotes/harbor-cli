// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"errors"
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
}

func TestMapFileError(t *testing.T) {
	cases := map[string]string{
		"file_too_large":   "maximum upload size",
		"unsupported_type": "not allowed",
		"blob_missing":     "not stored",
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
		got, err := decryptDownload(nil, nil, strings.NewReader(body), false)
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
	got, err := decryptDownload(nil, nil, bytes.NewReader(sealed), false)
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

	got, err := decryptDownload(nil, nil, bytes.NewReader(sealed), false)
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

	got, err := decryptDownload(nil, nil, bytes.NewReader(sealed), true)
	if err != nil {
		t.Fatalf("decryptDownload(--ciphertext): %v", err)
	}
	if out := readAll(t, got); !bytes.Equal(out, sealed) {
		t.Fatalf("--ciphertext altered the envelope: %d bytes, want %d", len(out), len(sealed))
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

	if _, err := decryptDownload(nil, nil, bytes.NewReader(sealed), false); err == nil {
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
// With no --output, the server's name IS the output path, so one holding ".."
// writes outside the directory the command was run in.
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

	if _, err := os.Stat(filepath.Join(dir, "ESCAPED.txt")); err != nil {
		t.Errorf("the download did not land in the working directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ESCAPED.txt")); err == nil {
		t.Error("the server's filename walked out of the working directory")
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

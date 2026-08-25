// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
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

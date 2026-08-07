// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestFilenameFromContentDisposition(t *testing.T) {
	cases := map[string]string{
		`attachment; filename="diagram.png"`: "diagram.png",
		`attachment; filename=report.pdf`:    "report.pdf",
		`inline`:                             "",
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

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListFiles(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[],"paging":{}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ListFiles(map[string]string{"mime": "image/", "ocr_status": "done"}); err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if rec.Path != "/files" {
		t.Errorf("path = %s", rec.Path)
	}
	if !containsAll(rec.Query, "mime=image", "ocr_status=done") {
		t.Errorf("query = %q", rec.Query)
	}
}

func TestCheckFileOmitsZeroSize(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"exists":false}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).CheckFile("abc123", 0); err != nil {
		t.Fatalf("CheckFile error: %v", err)
	}
	if rec.Path != "/files/check" {
		t.Errorf("path = %s", rec.Path)
	}
	if strings.Contains(string(rec.Body), "size") {
		t.Errorf("zero size should be omitted: %s", rec.Body)
	}
}

func TestUploadFileMultipart(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"hash":"abc","filename":"x.txt"}`)
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("file bytes here"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := testClient(srv.URL).UploadFile(path, "text/plain", "", false); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if rec.Path != "/files/upload" {
		t.Errorf("path = %s", rec.Path)
	}
	if !strings.HasPrefix(rec.ContentType, "multipart/form-data") {
		t.Errorf("content-type = %q", rec.ContentType)
	}
	body := string(rec.Body)
	if !strings.Contains(body, "file bytes here") || !strings.Contains(body, "x.txt") {
		t.Error("multipart body missing file content or filename")
	}
}

func TestUploadFileDetectsMIMEByExtension(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"hash":"abc","mime":"image/png"}`)
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, []byte("bytes; the .png extension drives detection"), 0644); err != nil {
		t.Fatal(err)
	}
	// No explicit mime → the client must detect image/png and send it as the
	// `mime` form field (otherwise the server stores application/octet-stream).
	if _, err := testClient(srv.URL).UploadFile(path, "", "", false); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	body := string(rec.Body)
	if !strings.Contains(body, "image/png") {
		t.Errorf("expected detected mime image/png in the multipart body:\n%s", body)
	}
}

func TestUploadFileExplicitMIMEWins(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"hash":"abc"}`)
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	_ = os.WriteFile(path, []byte("x"), 0644)
	// An explicit mime is sent verbatim (no detection override).
	if _, err := testClient(srv.URL).UploadFile(path, "application/zip", "", false); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if !strings.Contains(string(rec.Body), "application/zip") {
		t.Errorf("explicit mime not sent:\n%s", rec.Body)
	}
}

func TestGetFileDownloadAndRaw(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"download_url":"https://s3/x","mime":"image/png","size":10}`)
	defer srv.Close()
	c := testClient(srv.URL)
	if _, err := c.GetFileDownload("h1"); err != nil {
		t.Fatalf("GetFileDownload error: %v", err)
	}
	if rec.Path != "/files/h1" {
		t.Errorf("get path = %s", rec.Path)
	}
	resp, err := c.RawDownload("h1")
	if err != nil {
		t.Fatalf("RawDownload error: %v", err)
	}
	resp.Body.Close()
	if rec.Path != "/files/h1/raw" {
		t.Errorf("raw path = %s", rec.Path)
	}
}

func TestFetchURLUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("presigned fetch must not send a bearer token")
		}
		_, _ = w.Write([]byte("blob-bytes"))
	}))
	defer srv.Close()
	resp, err := testClient(srv.URL).FetchURL(srv.URL + "/x?sig=1")
	if err != nil {
		t.Fatalf("FetchURL error: %v", err)
	}
	resp.Body.Close()
}

// TestFetchURLSkipsHarborHeaders is the deliberate exception to "every request
// carries X-Harbor-Platform: cli". A presigned URL points at object storage, not
// at the Harbor API — nothing there reads the header, and S3-compatible backends
// can reject a request whose headers were not part of the signature. So this is
// the one outbound request the CLI makes that must stay bare, and it is the
// exemption TestEveryRequestBuilderSetsCommonHeaders allows by name.
func TestFetchURLSkipsHarborHeaders(t *testing.T) {
	var platform, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platform = r.Header.Get("X-Harbor-Platform")
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("blob-bytes"))
	}))
	defer srv.Close()

	resp, err := testClient(srv.URL).FetchURL(srv.URL + "/x?sig=1")
	if err != nil {
		t.Fatalf("FetchURL error: %v", err)
	}
	resp.Body.Close()

	if platform != "" {
		t.Errorf("X-Harbor-Platform = %q on a presigned URL, want it absent", platform)
	}
	if auth != "" {
		t.Errorf("Authorization = %q on a presigned URL, want it absent", auth)
	}
}

// TestFetchURLDownloadError pins that a non-2xx from a presigned URL comes back
// as a *DownloadError carrying the status, and that Gone() marks exactly the
// object-is-missing statuses. An account export reads that distinction to decide
// between "the download broke" and "re-check the export — it may be gone".
func TestFetchURLDownloadError(t *testing.T) {
	cases := []struct {
		status   int
		wantGone bool
	}{
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		}))

		_, err := testClient(srv.URL).FetchURL(srv.URL + "/x?sig=1")
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d: expected an error", tc.status)
		}
		var derr *DownloadError
		if !errors.As(err, &derr) {
			t.Fatalf("HTTP %d: error type = %T, want *DownloadError", tc.status, err)
		}
		if derr.Status != tc.status {
			t.Errorf("status = %d, want %d", derr.Status, tc.status)
		}
		if derr.Gone() != tc.wantGone {
			t.Errorf("HTTP %d: Gone() = %v, want %v", tc.status, derr.Gone(), tc.wantGone)
		}
		if !strings.Contains(derr.Error(), "download failed") {
			t.Errorf("message = %q, want the original wording", derr.Error())
		}
	}
}

// TestUploadBytesMultipart proves the in-memory upload path sends exactly the
// bytes it is given (no re-reading from disk), stamps is_encrypted, and keeps
// the caller's filename and MIME rather than deriving them from the ciphertext.
func TestUploadBytesMultipart(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"hash":"abc","filename":"secret.pdf","is_encrypted":true}`)
	defer srv.Close()

	sealed := []byte("HRBC2\x00\x01\x02ciphertext-not-plaintext")
	if _, err := testClient(srv.URL).UploadBytes(sealed, "application/pdf", "secret.pdf", true); err != nil {
		t.Fatalf("UploadBytes error: %v", err)
	}
	if rec.Path != "/files/upload" {
		t.Errorf("path = %s", rec.Path)
	}
	if !strings.HasPrefix(rec.ContentType, "multipart/form-data") {
		t.Errorf("content-type = %q", rec.ContentType)
	}
	body := string(rec.Body)
	for _, want := range []string{"ciphertext-not-plaintext", "secret.pdf", "application/pdf", "is_encrypted", "true"} {
		if !strings.Contains(body, want) {
			t.Errorf("multipart body missing %q: %s", want, body)
		}
	}
}

// TestUploadBytesOmitsEncryptedFlag proves is_encrypted is sent only when true,
// so an ordinary upload is not mislabelled.
func TestUploadBytesOmitsEncryptedFlag(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"hash":"abc"}`)
	defer srv.Close()

	if _, err := testClient(srv.URL).UploadBytes([]byte("plain"), "text/plain", "a.txt", false); err != nil {
		t.Fatalf("UploadBytes error: %v", err)
	}
	if strings.Contains(string(rec.Body), "is_encrypted") {
		t.Errorf("is_encrypted should be omitted when false: %s", rec.Body)
	}
}

// TestDetectMIME resolves the original file's type, which the encrypted upload
// path needs before it replaces the bytes with an opaque envelope.
func TestDetectMIME(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := DetectMIME(path); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("DetectMIME = %q, want text/plain…", got)
	}
	if got := DetectMIME(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("DetectMIME(missing) = %q, want \"\"", got)
	}
}

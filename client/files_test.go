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

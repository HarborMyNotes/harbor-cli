// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ListFiles returns the user's files, each with its linked notes (collection
// envelope). Accepts mime, note_id, ocr_status, is_encrypted, updated_since,
// and the standard list params.
func (c *Client) ListFiles(params map[string]string) ([]byte, error) {
	return c.doGet("/files", params)
}

// CheckFile reports whether a blob already exists for the given sha256 hash.
// A zero size is omitted.
func (c *Client) CheckFile(hash string, size int64) ([]byte, error) {
	body := map[string]any{"hash": hash}
	if size > 0 {
		body["size"] = size
	}
	return c.doPost("/files/check", body)
}

// UploadFile uploads a file via the direct multipart endpoint. The server
// computes the sha256 and returns the bare resource object. mimeType/filename
// are optional (filename defaults to the base name). The CLI intentionally does
// NOT use the internal presign-upload/commit endpoints.
//
// When the caller does not pin a mimeType, we detect one from the file and send
// it as the `mime` form field. This matters: the server records that MIME, and
// without it the upload would be stored as application/octet-stream — which
// skips the thumbnail and OCR pipelines (those run only for image/* and PDFs).
// The `mime` field is also necessary because Go's multipart CreateFormFile
// always stamps the file part's Content-Type as application/octet-stream.
func (c *Client) UploadFile(path, mimeType, filename string, isEncrypted bool) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	defer f.Close()

	if filename == "" {
		filename = filepath.Base(path)
	}
	if mimeType == "" {
		mimeType = detectMIME(path, f)
	}
	return c.uploadMultipart(f, mimeType, filename, isEncrypted)
}

// UploadBytes uploads content already held in memory. It exists for the
// client-side-encryption path: the bytes on the wire are the HRBC2 envelope, not
// what is on disk, so there is no file to stream and the caller has already
// resolved the MIME type and filename from the original.
//
// Those two stay PLAINTEXT on the resource record, matching every other Harbor
// client — an accepted metadata leak. The server hashes whatever it receives, so
// the resulting content address covers the ciphertext.
func (c *Client) UploadBytes(content []byte, mimeType, filename string, isEncrypted bool) ([]byte, error) {
	return c.uploadMultipart(bytes.NewReader(content), mimeType, filename, isEncrypted)
}

// uploadMultipart is the shared body of UploadFile and UploadBytes: build the
// form fields and POST the multipart request.
func (c *Client) uploadMultipart(content io.Reader, mimeType, filename string, isEncrypted bool) ([]byte, error) {
	fields := map[string]string{"filename": filename}
	if mimeType != "" {
		fields["mime"] = mimeType
	}
	if isEncrypted {
		fields["is_encrypted"] = "true"
	}
	return c.doMultipart("/files/upload", fields, "file", filename, content)
}

// DetectMIME resolves a path's MIME type exactly as UploadFile would, so a
// caller that transforms the bytes before upload (client-side encryption) can
// still record the ORIGINAL type rather than the ciphertext's. Returns "" when
// it cannot do better than application/octet-stream.
func DetectMIME(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return detectMIME(path, f)
}

// detectMIME guesses a file's MIME type: first by extension (covers png, jpg,
// pdf, gif, webp, svg, txt, …), then by sniffing the leading bytes. It returns
// "" when it cannot do better than application/octet-stream, leaving the choice
// to the server. The file offset is restored so a subsequent read starts at 0.
func detectMIME(path string, f *os.File) string {
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		if i := strings.IndexByte(ct, ';'); i >= 0 { // drop "; charset=utf-8"
			ct = strings.TrimSpace(ct[:i])
		}
		return ct
	}
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	if n == 0 {
		return ""
	}
	if ct := http.DetectContentType(buf[:n]); ct != "application/octet-stream" {
		return ct
	}
	return ""
}

// GetFileDownload returns the presigned download URL and basic metadata for a
// blob (no bytes).
func (c *Client) GetFileDownload(hash string) ([]byte, error) {
	return c.doGet("/files/"+hash, nil)
}

// RawDownload streams the blob bytes straight through the API (the caller must
// close the response body). Used when a presigned URL cannot be followed.
func (c *Client) RawDownload(hash string) (*http.Response, error) {
	return c.doGetRaw("/files/"+hash+"/raw", nil)
}

// DownloadError is a non-2xx response from a presigned storage URL. Those URLs
// answer in raw S3 XML rather than the Harbor error envelope, so there is no
// *APIError to branch on — the HTTP status is all the caller gets, and it is
// worth keeping: a 403/404 means "the object is gone", which for an account
// export is a cue to re-read the status endpoint rather than to report a broken
// download (the archive may have been deleted or expired from another device).
type DownloadError struct {
	Status int
}

// Error renders the status-only message this used to return as a flat error, so
// existing callers that only print it read exactly as before.
func (e *DownloadError) Error() string {
	return "download failed: HTTP " + strconv.Itoa(e.Status)
}

// Gone reports whether the failure means the object itself is missing (403 or
// 404) rather than the request being at fault.
func (e *DownloadError) Gone() bool {
	return e.Status == http.StatusForbidden || e.Status == http.StatusNotFound
}

// FetchURL performs an unauthenticated GET against a presigned URL (its
// credentials live in the query string), returning the live response for
// streaming. The caller must close the body. A non-2xx reply comes back as a
// *DownloadError carrying the status.
func (c *Client) FetchURL(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid download URL: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, &DownloadError{Status: resp.StatusCode}
	}
	return resp, nil
}

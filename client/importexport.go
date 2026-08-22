// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ImportUploadPart pairs a 1-based part number with the ETag object storage
// returned when that chunk was PUT. The complete call reassembles the staged
// object from this list, so an ETag is passed back exactly as received —
// surrounding quotes included, because that is what the store signed.
type ImportUploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// importPartAttempts is how many times a single chunk's PUT is tried before the
// upload gives up. There is no resume: a chunk that fails takes the whole import
// with it, so a momentary network blip a hundred parts into a multi-gigabyte
// export is worth a retry rather than a restart.
const importPartAttempts = 3

// importPartRetryBackoff is the pause before re-PUTting a chunk that failed
// transiently; it doubles on each further attempt. A var so tests can prove the
// retry happens without spending real seconds on it.
var importPartRetryBackoff = time.Second

// importUploadPath builds a path on the format-parameterised import route,
// /api/v1/import/:kind/uploads... Every import format the server registers
// mounts the same four upload calls under its own kind segment, so the kind
// stays an argument here: an obsidian_zip or standardnotes command reuses this
// transport unchanged rather than needing a second copy of it.
func importUploadPath(kind, suffix string) string {
	return "/import/" + kind + "/uploads" + suffix
}

// CreateImportUpload opens a direct-to-storage upload and returns the server's
// chunking plan (data-wrapped: import_job_id, status, part_size, part_count).
// It declares only the file's SIZE — the bytes go straight to object storage
// from here on, never through the API — so this is where the size cap, the
// target notebook and the encrypted-notebook refusal are all decided, before
// anything is transferred. filename and targetNotebookID are optional; empty
// values are omitted so the server's own defaults apply.
func (c *Client) CreateImportUpload(kind string, totalBytes int64, filename, targetNotebookID string) ([]byte, error) {
	body := map[string]any{"total_bytes": totalBytes}
	if filename != "" {
		body["filename"] = filename
	}
	if targetNotebookID != "" {
		body["target_notebook_id"] = targetNotebookID
	}
	return c.doPost(importUploadPath(kind, ""), body)
}

// PresignImportParts asks for a batch of presigned part URLs (data-wrapped:
// parts[{part_number,url}], expires_in_seconds). Part numbers are 1-based and
// must fall inside the plan's part_count; the server caps a single batch at
// 1000, which is why a large upload requests them incrementally.
func (c *Client) PresignImportParts(kind, jobID string, partNumbers []int) ([]byte, error) {
	return c.doPost(importUploadPath(kind, "/"+jobID+"/parts"),
		map[string]any{"part_numbers": partNumbers})
}

// CompleteImportUpload assembles the staged object from its uploaded parts and
// enqueues the import, returning 202 with the job to poll. The server verifies
// the assembled size against the size declared at create and refuses a short
// object, so a missing part fails here rather than importing a truncated file.
func (c *Client) CompleteImportUpload(kind, jobID string, parts []ImportUploadPart) ([]byte, error) {
	return c.doPost(importUploadPath(kind, "/"+jobID+"/complete"),
		map[string]any{"parts": parts})
}

// AbortImportUpload cancels an upload that is still awaiting its bytes: the
// multipart upload is aborted so storage keeps no orphaned parts, and the job is
// marked aborted. It is only valid while the job is awaiting an upload — once
// complete has run the import belongs to the server.
func (c *Client) AbortImportUpload(kind, jobID string) ([]byte, error) {
	return c.doPost(importUploadPath(kind, "/"+jobID+"/abort"), map[string]any{})
}

// UploadImportPart PUTs one chunk straight to object storage and returns the
// ETag the store answered with.
//
// The presigned URL carries its own credentials in the query string, so this
// request must NOT be signed again — sending an Authorization header alongside
// a presigned URL is rejected by S3-compatible stores as two auth mechanisms.
// chunk is re-seeked before every attempt so a retry sends the same bytes, and
// Content-Length is set explicitly: the store requires a length up front, and
// net/http cannot infer one from a section reader.
func (c *Client) UploadImportPart(ctx context.Context, url string, chunk io.ReadSeeker, size int64) (string, error) {
	backoff := importPartRetryBackoff
	var lastErr error
	for attempt := 1; attempt <= importPartAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		etag, retryable, err := c.putImportPart(ctx, url, chunk, size)
		if err == nil {
			return etag, nil
		}
		if !retryable || ctx.Err() != nil {
			return "", err
		}
		lastErr = err
	}
	return "", lastErr
}

// putImportPart performs a single PUT of a chunk, reporting whether a failure is
// worth retrying. A transport error or a 5xx/408/429 is transient; anything else
// (a 403 from an expired signature, a 400 from a malformed request) would fail
// identically however many times it is sent.
func (c *Client) putImportPart(ctx context.Context, url string, chunk io.ReadSeeker, size int64) (etag string, retryable bool, err error) {
	if _, err := chunk.Seek(0, io.SeekStart); err != nil {
		return "", false, fmt.Errorf("cannot re-read the file: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, chunk)
	if err != nil {
		return "", false, fmt.Errorf("failed to create upload request: %w", err)
	}
	req.ContentLength = size

	resp, err := c.storageClient().Do(req)
	if err != nil {
		return "", ctx.Err() == nil, fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()
	// The body of a successful part PUT is empty; on failure it is the store's
	// own XML, which is not a Harbor error envelope. Drain either way so the
	// connection can be reused, and keep a short excerpt for the message.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		transient := resp.StatusCode >= 500 ||
			resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests
		return "", transient, fmt.Errorf("storage rejected the upload: HTTP %d %s",
			resp.StatusCode, bytes.TrimSpace(body))
	}

	etag = resp.Header.Get("ETag")
	if etag == "" {
		// Without an ETag the complete call cannot name this part, so the upload
		// is already lost — say so here rather than failing opaquely later.
		return "", false, fmt.Errorf("storage returned no ETag for the uploaded chunk (HTTP %d)", resp.StatusCode)
	}
	return etag, false, nil
}

// ImportStatus polls an import job by id and returns the data-wrapped status
// document (live counters plus the per-note error list). A 404 surfaces as an
// APIError with code not_found when the job is unknown or not the caller's.
// The route names a kind, but the server looks a job up by id alone, so any
// kind's path answers for any of the caller's jobs.
func (c *Client) ImportStatus(kind, jobID string) ([]byte, error) {
	return c.doGet("/import/"+kind+"/"+jobID, nil)
}

// ExportENEX exports a notebook or an explicit note selection to a raw ENEX
// document. The response body is the .enex file bytes (not a JSON envelope), so
// it returns the live *http.Response for streaming — the caller MUST close the
// body, and reads the X-Skipped-Encrypted header off the response before
// draining it. Targeting is XOR: pass a non-empty notebookID OR a non-empty
// noteIDs slice (the server rejects both or neither with validation_failed).
// includeResources inlines each linked attachment as a base64 <resource> block.
func (c *Client) ExportENEX(notebookID string, noteIDs []string, includeResources bool) (*http.Response, error) {
	body := map[string]any{}
	if notebookID != "" {
		body["notebook_id"] = notebookID
	}
	if len(noteIDs) > 0 {
		body["note_ids"] = noteIDs
	}
	if includeResources {
		body["include_resources"] = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request body: %w", err)
	}
	full, err := c.buildURL("/export/enex", nil)
	if err != nil {
		return nil, err
	}
	return c.rawPost(full, raw, "application/json")
}

// rawPost performs a POST whose successful response is streamed back as a live
// *http.Response (the caller closes the body) — the streaming sibling of doPost,
// used by ENEX export which returns a raw file plus a header to read. On a
// non-2xx response it drains and closes the body and returns a decoded APIError,
// retrying once after a transparent token refresh on a 401 invalid_token.
func (c *Client) rawPost(fullURL string, body []byte, contentType string) (*http.Response, error) {
	return c.rawPostWithRefresh(fullURL, body, contentType, true)
}

// rawPostWithRefresh is rawPost's implementation; allowRefresh is set false on
// the single retry so a failed refresh can never loop.
func (c *Client) rawPostWithRefresh(fullURL string, body []byte, contentType string, allowRefresh bool) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, fullURL, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.setCommonHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.LastRequestID = resp.Header.Get("X-Request-Id")
		return resp, nil
	}

	errBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.LastRequestID = resp.Header.Get("X-Request-Id")
	apiErr := parseAPIError(errBody, resp.StatusCode, c.LastRequestID)
	if allowRefresh && resp.StatusCode == http.StatusUnauthorized && apiErr.Code == "invalid_token" && c.OnUnauthorized != nil {
		if newTok, ok := c.refresh(); ok {
			c.AccessToken = newTok
			return c.rawPostWithRefresh(fullURL, body, contentType, false)
		}
	}
	return nil, apiErr
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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

// defaultUploadMIME is the type a file is declared as when its own cannot be
// told. A presigned link is signed for one Content-Type, so the upload must name
// one; this is what the server records for an unknown file anyway.
const defaultUploadMIME = "application/octet-stream"

// fileUploadAttempts is how many times each step of an upload is tried: the
// check, the presign, the PUT to storage and the commit. A dropped connection
// starts the PUT again from the first byte.
const fileUploadAttempts = 3

// fileUploadRetryBackoff is the pause before the first retry. It doubles for
// each one after that. A variable so tests can make it instant.
var fileUploadRetryBackoff = time.Second

// errFileChanged is a file whose bytes changed between hashing and sending.
var errFileChanged = errors.New("the file changed while it was being uploaded, so it was not saved; upload it again")

// fileUpload is one file on its way to storage: its bytes and what the server
// records about them.
type fileUpload struct {
	// Body holds the bytes. Each PUT reads it through its own section reader,
	// so the transport can still be reading a failed attempt's body while the
	// retry starts. It is never read into memory as a whole.
	Body        io.ReaderAt
	Size        int64
	Hash        string
	MIME        string
	Filename    string
	IsEncrypted bool
}

// PresignedUpload is the server's permit for one PUT straight to storage. The
// link is signed for the exact size and Content-Type that were declared, so the
// headers must be sent exactly as given.
type PresignedUpload struct {
	URL     string            `json:"upload_url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
}

// UploadFile uploads the file at path and returns the bare resource object.
//
// The bytes go straight to storage with a presigned link, read from the open
// file piece by piece, so the file's size sets neither the memory used nor a
// time limit. The file is read twice: once to hash it, once to send it.
//
// mimeType and filename are optional. When mimeType is empty the type is
// detected from the file. This matters: the server picks thumbnails and OCR by
// the recorded type, so a PDF declared as application/octet-stream gets
// neither.
func (c *Client) UploadFile(ctx context.Context, path, mimeType, filename string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot read file: %w", err)
	}
	if !info.Mode().IsRegular() {
		// A pipe can be read only once, and the upload reads twice. A private
		// copy on disk can be read again.
		copied, err := copyToTempFile(f)
		if err != nil {
			return nil, err
		}
		defer os.Remove(copied.Name())
		defer copied.Close()
		f = copied
	}

	if filename == "" {
		filename = filepath.Base(path)
	}
	if mimeType == "" {
		mimeType = detectMIME(path, f)
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return nil, fmt.Errorf("cannot read file: %w", err)
	}
	if size == 0 {
		return c.uploadEmpty(mimeType, filename, false)
	}
	return c.upload(ctx, fileUpload{
		Body:     f,
		Size:     size,
		Hash:     hex.EncodeToString(h.Sum(nil)),
		MIME:     mimeType,
		Filename: filename,
	})
}

// copyToTempFile copies a stream into a new temporary file (mode 0600) and
// returns it rewound. The caller closes and removes it.
func copyToTempFile(r io.Reader) (*os.File, error) {
	tmp, err := os.CreateTemp("", "harbor-upload-*")
	if err != nil {
		return nil, fmt.Errorf("cannot make a temporary copy of the input: %w", err)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("cannot read the input: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("cannot read the temporary copy: %w", err)
	}
	return tmp, nil
}

// UploadBytes uploads content already held in memory, with the same presigned
// flow as UploadFile. It exists for client-side encryption: the bytes on the
// wire are the HRBC2 envelope, not what is on disk, and the caller has already
// resolved the MIME type and filename from the original.
//
// Those two stay PLAINTEXT on the resource record, matching every other Harbor
// client. The hash covers the ciphertext.
func (c *Client) UploadBytes(ctx context.Context, content []byte, mimeType, filename string, isEncrypted bool) ([]byte, error) {
	if len(content) == 0 {
		return c.uploadEmpty(mimeType, filename, isEncrypted)
	}
	sum := sha256.Sum256(content)
	return c.upload(ctx, fileUpload{
		Body:        bytes.NewReader(content),
		Size:        int64(len(content)),
		Hash:        hex.EncodeToString(sum[:]),
		MIME:        mimeType,
		Filename:    filename,
		IsEncrypted: isEncrypted,
	})
}

// upload runs the hash-first flow every Harbor app uses: check whether the
// bytes are already stored, PUT them to a presigned link when they are not, and
// commit the resource row.
func (c *Client) upload(ctx context.Context, up fileUpload) ([]byte, error) {
	if up.MIME == "" {
		up.MIME = defaultUploadMIME
	}
	stored, err := c.blobStored(ctx, up.Hash, up.Size)
	if err != nil {
		return nil, err
	}
	sent := false
	if !stored {
		if sent, err = c.sendBlob(ctx, up); err != nil {
			return nil, err
		}
	}
	return c.commitUpload(ctx, up, sent)
}

// blobStored asks the server whether it already holds these bytes, so a file
// uploaded before is not sent again.
func (c *Client) blobStored(ctx context.Context, hash string, size int64) (bool, error) {
	data, err := retryStep(ctx, func() ([]byte, error) { return c.CheckFile(hash, size) }, nil)
	if err != nil {
		return false, err
	}
	var r struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(UnwrapData(data), &r); err != nil {
		return false, fmt.Errorf("unexpected response from the file check: %w", err)
	}
	return r.Exists, nil
}

// sendBlob gets a presigned link and PUTs the bytes to it, retrying a failure
// that may not happen again. It reports whether it sent anything: presign can
// answer that a live row already holds these bytes, and then there is nothing
// to send.
//
// A 403 from storage on a retry gets a fresh link and one more PUT. Links
// expire, and the attempts before it may have outlived this one. On the first
// attempt a 403 is final: the link is new, so the store refused the request
// itself.
func (c *Client) sendBlob(ctx context.Context, up fileUpload) (bool, error) {
	target, err := c.presignFor(ctx, up)
	if err != nil || target == nil {
		return false, err
	}

	backoff := fileUploadRetryBackoff
	var lastErr error
	for attempt := 1; attempt <= fileUploadAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, backoff); err != nil {
				return false, err
			}
			backoff *= 2
		}
		status, retryable, err := c.putPresigned(ctx, target, up)
		if err != nil && status == http.StatusForbidden && attempt > 1 {
			fresh, perr := c.presignFor(ctx, up)
			if perr != nil || fresh == nil {
				return false, perr
			}
			target = fresh
			_, retryable, err = c.putPresigned(ctx, target, up)
		}
		if err == nil {
			return true, nil
		}
		if !retryable || ctx.Err() != nil {
			return false, err
		}
		lastErr = err
	}
	return false, lastErr
}

// presignFor asks for a link to PUT these bytes. A nil link with no error means
// a live row already holds them (409 already_exists): another upload of the
// same file got there first, so the commit can go ahead without a PUT.
func (c *Client) presignFor(ctx context.Context, up fileUpload) (*PresignedUpload, error) {
	target, err := retryStep(ctx, func() (*PresignedUpload, error) {
		return c.PresignUpload(up.Hash, up.Size, up.MIME, up.IsEncrypted)
	}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == "already_exists" {
		return nil, nil
	}
	return target, err
}

// commitUpload registers the stored bytes as a resource. Right after a PUT, a
// blob_missing answer can mean the store has not caught up yet, so that answer
// is tried again too. Without a PUT it is final.
func (c *Client) commitUpload(ctx context.Context, up fileUpload, justSent bool) ([]byte, error) {
	storeLag := func(err error) bool {
		var apiErr *APIError
		return justSent && errors.As(err, &apiErr) && apiErr.Code == "blob_missing"
	}
	return retryStep(ctx, func() ([]byte, error) {
		return c.CommitFile(up.Hash, up.Size, up.MIME, up.Filename, up.IsEncrypted)
	}, storeLag)
}

// retryStep runs one API call of an upload and tries it again after a failure
// that can go better next time: no answer at all, a 5xx or a 429, or whatever
// also says. Every step is safe to repeat. One blip after a long PUT would
// otherwise throw the whole upload away.
func retryStep[T any](ctx context.Context, step func() (T, error), also func(error) bool) (T, error) {
	backoff := fileUploadRetryBackoff
	for attempt := 1; ; attempt++ {
		out, err := step()
		if err == nil || attempt == fileUploadAttempts || ctx.Err() != nil ||
			!(transientAPIError(err) || (also != nil && also(err))) {
			return out, err
		}
		if serr := sleepCtx(ctx, backoff); serr != nil {
			return out, err
		}
		backoff *= 2
	}
}

// transientAPIError reports whether an API call failed in a way a retry can
// fix: the request got no answer, or the server answered 5xx or 429.
func transientAPIError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// PresignUpload asks for a presigned link to PUT one new blob straight to
// storage. size must be positive: the server cannot sign a link for zero bytes.
func (c *Client) PresignUpload(hash string, size int64, mimeType string, isEncrypted bool) (*PresignedUpload, error) {
	data, err := c.doPost("/files/presign-upload", map[string]any{
		"hash": hash, "size": size, "mime": mimeType, "is_encrypted": isEncrypted,
	})
	if err != nil {
		return nil, err
	}
	var target PresignedUpload
	if err := json.Unmarshal(UnwrapData(data), &target); err != nil {
		return nil, fmt.Errorf("unexpected response from presign-upload: %w", err)
	}
	if target.URL == "" {
		return nil, errors.New("the server returned no upload link")
	}
	// A server without real storage hands out links like fake://…, which no
	// HTTP client can PUT to. Say so once instead of retrying a dead end.
	if u, err := url.Parse(target.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("the server's upload link is not an http(s) link, so this CLI cannot send the file to it: %s",
			strings.SplitN(target.URL, "?", 2)[0])
	}
	return &target, nil
}

// CommitFile registers bytes already in storage as a resource and returns the
// bare resource object. It is safe to repeat: a second commit of the same hash
// returns the existing row.
func (c *Client) CommitFile(hash string, size int64, mimeType, filename string, isEncrypted bool) ([]byte, error) {
	return c.doPost("/files/commit", map[string]any{
		"hash": hash, "size": size, "mime": mimeType, "filename": filename, "is_encrypted": isEncrypted,
	})
}

// putPresigned sends the upload's bytes to a presigned storage link, and
// reports the storage status (0 when no answer came) and whether a failure is
// worth another try.
//
// The link carries its own credentials, so the request must NOT carry the
// bearer token: S3-compatible stores refuse two kinds of auth on one request.
// The body streams from up.Body as the network takes it, and Content-Length is
// set up front because the link is signed for that length. There is no total
// time limit; sendUpload gives up when nothing moves for the stall timeout.
//
// The bytes are hashed as they go. Storage does not check them against the
// hash in the link, so a file edited in place after it was hashed would
// otherwise be stored under a hash that does not match it.
//
// A dropped connection, a 5xx, a 408, a 429, or S3's 400 RequestTimeout (sent
// when the connection went idle) can go better next time. A stall cannot be
// hurried by trying again at once, so it fails the upload.
func (c *Client) putPresigned(ctx context.Context, target *PresignedUpload, up fileUpload) (status int, retryable bool, err error) {
	method := target.Method
	if method == "" {
		method = http.MethodPut
	}
	body := &hashingReader{r: io.NewSectionReader(up.Body, 0, up.Size), h: sha256.New()}
	req, err := http.NewRequestWithContext(ctx, method, target.URL, body)
	if err != nil {
		return 0, false, fmt.Errorf("failed to create upload request: %w", err)
	}
	req.ContentLength = up.Size
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.sendUpload(c.storageClient(), req)
	if err != nil {
		var stall *StallError
		return 0, !errors.As(err, &stall) && ctx.Err() == nil, fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()
	// On failure the body is the store's own XML, not a Harbor error envelope.
	// A short excerpt names the problem.
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if sum, n := body.sum(); n != up.Size || sum != up.Hash {
			return resp.StatusCode, false, errFileChanged
		}
		return resp.StatusCode, false, nil
	}
	transient := resp.StatusCode >= 500 ||
		resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusBadRequest && bytes.Contains(excerpt, []byte("<Code>RequestTimeout</Code>")))
	return resp.StatusCode, transient, fmt.Errorf("storage refused the upload: HTTP %d %s",
		resp.StatusCode, bytes.TrimSpace(excerpt))
}

// hashingReader hashes what passes through it. The transport reads it on its
// own goroutine, so the hash is behind a lock.
type hashingReader struct {
	r io.Reader

	mu sync.Mutex
	h  hash.Hash
	n  int64
}

// Read passes the read through and adds what it read to the hash.
func (r *hashingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.mu.Lock()
	r.h.Write(p[:n])
	r.n += int64(n)
	r.mu.Unlock()
	return n, err
}

// sum returns the hex sha256 of what was read so far, and how many bytes.
func (r *hashingReader) sum() (string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return hex.EncodeToString(r.h.Sum(nil)), r.n
}

// uploadEmpty sends a zero-byte file through the API's multipart route. A
// presigned link must be signed for a positive size, so this is the one upload
// that cannot go straight to storage. With no file bytes, the buffered
// multipart body is only a few hundred bytes.
func (c *Client) uploadEmpty(mimeType, filename string, isEncrypted bool) ([]byte, error) {
	fields := map[string]string{"filename": filename}
	if mimeType != "" {
		fields["mime"] = mimeType
	}
	if isEncrypted {
		fields["is_encrypted"] = "true"
	}
	return c.doMultipart("/files/upload", fields, "file", filename, http.NoBody)
}

// sleepCtx waits for d, or returns early with the context's error.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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

// RenameFile gives a file a new name and returns the bare resource object.
// filename is the full name, extension included: the server refuses one that
// does not end with the file's current extension.
func (c *Client) RenameFile(hash, filename string) ([]byte, error) {
	return c.doPatch("/files/"+hash, map[string]any{"filename": filename})
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
//
// It goes through the storage client, which has no overall timeout, because a
// large file can take longer to arrive than any fixed limit. A download that
// stops receiving bytes is given up after the stall timeout instead.
func (c *Client) FetchURL(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid download URL: %w", err)
	}
	resp, err := c.sendStreaming(c.storageClient(), req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, &DownloadError{Status: resp.StatusCode}
	}
	return resp, nil
}

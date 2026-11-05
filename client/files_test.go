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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// A rename is a PATCH on the file's own path that carries only the new name.
func TestRenameFile(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"hash":"h1","filename":"Scan 2015-01-06.pdf"}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).RenameFile("h1", "Scan 2015-01-06.pdf"); err != nil {
		t.Fatalf("RenameFile error: %v", err)
	}
	if rec.Method != "PATCH" || rec.Path != "/files/h1" {
		t.Errorf("request = %s %s, want PATCH /files/h1", rec.Method, rec.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body)
	}
	if len(body) != 1 || body["filename"] != "Scan 2015-01-06.pdf" {
		t.Errorf("body = %s, want only filename", rec.Body)
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

// ===========================================================================
// Presigned upload
// ===========================================================================

// storedPut is one PUT the fake object store received.
type storedPut struct {
	Query         string
	ContentLength int64
	Header        http.Header
	Body          []byte
}

// uploadStub is a fake Harbor API and a fake object store on one test server,
// so a test can follow a whole upload: check, presign, PUT, commit.
//
// Each presign hands out a link with a new signature (sig=1, sig=2, …), so a
// test can tell which link a PUT used.
type uploadStub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	calls    []string
	bodies   map[string][]map[string]any
	puts     []storedPut
	presigns int
	commits  int

	// exists is what /files/check answers.
	exists bool
	// presignReply, when set, answers presign instead of a link.
	presignReply *mockReply
	// putStatus gives the status for the nth PUT (1-based). Nil means 200.
	putStatus func(n int) int
	// commitReply gives the reply for the nth commit. Nil means 201.
	commitReply func(n int) mockReply
	// onPut, when set, handles the PUT itself (after it is counted).
	onPut func(w http.ResponseWriter, r *http.Request)
}

// mockReply is a canned status and body.
type mockReply struct {
	Status int
	Body   string
}

// newUploadStub starts the fake API and store.
func newUploadStub(t *testing.T) *uploadStub {
	t.Helper()
	s := &uploadStub{t: t, bodies: map[string][]map[string]any{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// serve answers one request and records it.
func (s *uploadStub) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.calls = append(s.calls, key)
	s.mu.Unlock()

	if r.Method == http.MethodPut && r.URL.Path == "/store/blob" {
		s.mu.Lock()
		n := len(s.puts) + 1
		s.puts = append(s.puts, storedPut{Query: r.URL.RawQuery, ContentLength: r.ContentLength, Header: r.Header.Clone()})
		onPut, putStatus := s.onPut, s.putStatus
		s.mu.Unlock()
		if onPut != nil {
			onPut(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.puts[n-1].Body = body
		s.mu.Unlock()
		status := http.StatusOK
		if putStatus != nil {
			status = putStatus(n)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte("<Error><Code>Refused</Code></Error>"))
		}
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.bodies[key] = append(s.bodies[key], body)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch key {
	case "POST /files/check":
		_, _ = fmt.Fprintf(w, `{"exists":%t,"hash":%q}`, s.exists, body["hash"])
	case "POST /files/presign-upload":
		s.mu.Lock()
		s.presigns++
		n := s.presigns
		s.mu.Unlock()
		if s.presignReply != nil {
			w.WriteHeader(s.presignReply.Status)
			_, _ = w.Write([]byte(s.presignReply.Body))
			return
		}
		_, _ = fmt.Fprintf(w, `{"upload_url":"%s/store/blob?sig=%d","method":"PUT",`+
			`"headers":{"Content-Type":%q,"X-Amz-Meta-Origin":"kept"},"expires_at":1,"key":"blobs/u/x"}`,
			s.srv.URL, n, body["mime"])
	case "POST /files/commit":
		s.mu.Lock()
		s.commits++
		n := s.commits
		s.mu.Unlock()
		reply := mockReply{Status: http.StatusCreated, Body: fmt.Sprintf(`{"hash":%q,"filename":%q}`, body["hash"], body["filename"])}
		if s.commitReply != nil {
			reply = s.commitReply(n)
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write([]byte(reply.Body))
	case "POST /files/upload":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"hash":"e3b0","size":0}`))
	default:
		s.t.Errorf("uploadStub: unrouted request %s", key)
		w.WriteHeader(http.StatusNotFound)
	}
}

// client returns a client pointed at the stub, with instant retries.
func (s *uploadStub) client(t *testing.T) *Client {
	t.Helper()
	restore := fileUploadRetryBackoff
	fileUploadRetryBackoff = time.Millisecond
	t.Cleanup(func() { fileUploadRetryBackoff = restore })
	return testClient(s.srv.URL)
}

// callList returns the requests so far as "METHOD /path".
func (s *uploadStub) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// body returns the nth JSON body sent to "METHOD /path" (0-based).
func (s *uploadStub) body(t *testing.T, key string, n int) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if n >= len(s.bodies[key]) {
		t.Fatalf("no request %d to %s in %v", n, key, s.calls)
	}
	return s.bodies[key][n]
}

// presignCount returns how many presign calls arrived.
func (s *uploadStub) presignCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.presigns
}

// commitCount returns how many commit calls arrived.
func (s *uploadStub) commitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

// putList returns the PUTs the store received.
func (s *uploadStub) putList() []storedPut {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storedPut(nil), s.puts...)
}

// sha256Hex is the content address of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeFile writes content to a new file called name and returns its path.
func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The whole flow, in order: check, presign, PUT the file to the link, commit.
// Each step must carry what the server checks: the hash and size of the bytes,
// the type the link is signed for, the stored name. The PUT must send exactly
// the file, with its exact length, the headers the link came with, and no
// Harbor credentials (S3-compatible stores refuse a second kind of auth).
func TestUploadFileUsesThePresignedFlow(t *testing.T) {
	stub := newUploadStub(t)
	content := []byte("%PDF-1.7 a small but real-looking report")
	path := writeFile(t, "report.pdf", content)

	data, err := stub.client(t).UploadFile(context.Background(), path, "", "")
	if err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if !strings.Contains(string(data), sha256Hex(content)) {
		t.Errorf("UploadFile returned %s, want the commit's resource", data)
	}

	want := []string{"POST /files/check", "POST /files/presign-upload", "PUT /store/blob", "POST /files/commit"}
	if got := stub.callList(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	hash := sha256Hex(content)
	check := stub.body(t, "POST /files/check", 0)
	if check["hash"] != hash || check["size"] != float64(len(content)) {
		t.Errorf("check body = %v, want hash %s and size %d", check, hash, len(content))
	}
	presign := stub.body(t, "POST /files/presign-upload", 0)
	if presign["hash"] != hash || presign["size"] != float64(len(content)) ||
		presign["mime"] != "application/pdf" || presign["is_encrypted"] != false {
		t.Errorf("presign body = %v", presign)
	}

	puts := stub.putList()
	put := puts[0]
	if put.Query != "sig=1" {
		t.Errorf("PUT went to ?%s, want the presigned link", put.Query)
	}
	if put.ContentLength != int64(len(content)) || !bytes.Equal(put.Body, content) {
		t.Errorf("PUT sent %d bytes (Content-Length %d), want exactly the %d-byte file", len(put.Body), put.ContentLength, len(content))
	}
	if got := put.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("PUT Content-Type = %q, want the signed application/pdf", got)
	}
	if got := put.Header.Get("X-Amz-Meta-Origin"); got != "kept" {
		t.Errorf("PUT dropped a header the link came with: X-Amz-Meta-Origin = %q", got)
	}
	if got := put.Header.Get("Authorization"); got != "" {
		t.Errorf("PUT carried Authorization %q, want none on a presigned link", got)
	}
	if got := put.Header.Get("X-Harbor-Platform"); got != "" {
		t.Errorf("PUT carried X-Harbor-Platform %q, want none on a presigned link", got)
	}

	commit := stub.body(t, "POST /files/commit", 0)
	if commit["hash"] != hash || commit["size"] != float64(len(content)) || commit["mime"] != "application/pdf" ||
		commit["filename"] != "report.pdf" || commit["is_encrypted"] != false {
		t.Errorf("commit body = %v", commit)
	}
}

// A file the server already holds is not sent again: check says it exists, so
// the upload goes straight to commit.
func TestUploadFileSkipsThePutWhenAlreadyStored(t *testing.T) {
	stub := newUploadStub(t)
	stub.exists = true
	path := writeFile(t, "again.txt", []byte("bytes the server already has"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	want := []string{"POST /files/check", "POST /files/commit"}
	if got := stub.callList(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

// Presign's 409 already_exists means another upload of the same bytes got there
// between check and presign. That is not a failure: commit, without a PUT.
func TestUploadFileCommitsWhenPresignSaysAlreadyExists(t *testing.T) {
	stub := newUploadStub(t)
	stub.presignReply = &mockReply{Status: http.StatusConflict,
		Body: `{"error":{"code":"already_exists","message":"call commit instead"}}`}
	path := writeFile(t, "race.txt", []byte("two uploads of one file"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	want := []string{"POST /files/check", "POST /files/presign-upload", "POST /files/commit"}
	if got := stub.callList(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

// A 403 from storage on a retry means the link may have expired while the
// earlier attempts ran. The upload gets a fresh link and PUTs to it, rather
// than failing.
func TestUploadPresignsAgainAfterA403OnARetry(t *testing.T) {
	stub := newUploadStub(t)
	stub.putStatus = func(n int) int {
		switch n {
		case 1:
			return http.StatusServiceUnavailable
		case 2:
			return http.StatusForbidden
		}
		return http.StatusOK
	}
	content := []byte("a file whose link ran out")
	path := writeFile(t, "slow.bin", content)

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if stub.presignCount() != 2 {
		t.Errorf("presigned %d times, want 2 (the 403 on the retry must get a fresh link)", stub.presignCount())
	}
	puts := stub.putList()
	if len(puts) != 3 {
		t.Fatalf("store got %d PUTs, want 3", len(puts))
	}
	if puts[1].Query != "sig=1" || puts[2].Query != "sig=2" {
		t.Errorf("PUT links = %s, %s; want the retry on sig=1 and the last PUT on the fresh sig=2", puts[1].Query, puts[2].Query)
	}
	for i, put := range puts {
		if !bytes.Equal(put.Body, content) {
			t.Errorf("PUT %d sent %q, want the whole file again", i+1, put.Body)
		}
	}
	if stub.commitCount() != 1 {
		t.Errorf("committed %d times, want 1", stub.commitCount())
	}
}

// On the first attempt the link is brand new, so a 403 is the store refusing
// the request itself. Asking again would only repeat it.
func TestUploadFirst403IsFinal(t *testing.T) {
	stub := newUploadStub(t)
	stub.putStatus = func(int) int { return http.StatusForbidden }
	path := writeFile(t, "refused.bin", []byte("refused"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error = %v, want the store's 403", err)
	}
	if stub.presignCount() != 1 || len(stub.putList()) != 1 || stub.commitCount() != 0 {
		t.Errorf("presigns=%d puts=%d commits=%d, want 1, 1, 0", stub.presignCount(), len(stub.putList()), stub.commitCount())
	}
}

// A store that keeps failing is tried a fixed number of times, then the error
// is reported and nothing is committed.
func TestUploadGivesUpAfterTheLastAttempt(t *testing.T) {
	stub := newUploadStub(t)
	stub.putStatus = func(int) int { return http.StatusInternalServerError }
	path := writeFile(t, "flaky.bin", []byte("flaky"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %v, want the store's 500", err)
	}
	if got := len(stub.putList()); got != fileUploadAttempts {
		t.Errorf("store got %d PUTs, want %d", got, fileUploadAttempts)
	}
	if stub.commitCount() != 0 {
		t.Errorf("committed %d times after a failed upload, want 0", stub.commitCount())
	}
}

// lazyReader makes size bytes on demand, so a test can send a big body without
// holding it. It counts what was read, and it refuses to be rewound once read:
// the client may only seek to the start before it sends.
type lazyReader struct {
	size int64
	pos  int64
	read atomic.Int64
}

// Read fills p with a repeating pattern.
func (r *lazyReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if left := r.size - r.pos; n > left {
		n = left
	}
	for i := range p[:n] {
		p[i] = byte('a' + (r.pos+int64(i))%26)
	}
	r.pos += n
	r.read.Add(n)
	return int(n), nil
}

// Seek allows only a rewind to the start, and only before anything was read.
func (r *lazyReader) Seek(offset int64, whence int) (int64, error) {
	if offset != 0 || whence != io.SeekStart {
		return 0, errors.New("lazyReader: only Seek(0, SeekStart)")
	}
	if r.pos != 0 {
		return 0, errors.New("lazyReader: read twice")
	}
	return 0, nil
}

// The body streams: the store sees the first bytes while most of the body has
// not even been read yet. A client that read the whole body into memory first
// would have read all of it before sending a byte. The body is far bigger than
// the socket buffers, so what is in flight cannot explain the gap.
func TestUploadStreamsTheBodyInsteadOfBufferingIt(t *testing.T) {
	const size = 64 << 20
	body := &lazyReader{size: size}
	var readWhenFirstByteArrived, received atomic.Int64

	stub := newUploadStub(t)
	stub.onPut = func(w http.ResponseWriter, r *http.Request) {
		first := make([]byte, 1)
		if _, err := io.ReadFull(r.Body, first); err != nil {
			t.Errorf("reading the first byte: %v", err)
		}
		readWhenFirstByteArrived.Store(body.read.Load())
		n, _ := io.Copy(io.Discard, r.Body)
		received.Store(n + 1)
	}

	up := fileUpload{Body: body, Size: size, Hash: strings.Repeat("a", 64), MIME: "video/mp4", Filename: "big.mp4"}
	if _, err := stub.client(t).upload(context.Background(), up); err != nil {
		t.Fatalf("upload error: %v", err)
	}
	if got := readWhenFirstByteArrived.Load(); got >= size/2 {
		t.Errorf("%d of %d bytes were read before the store saw the first one — the body was buffered, not streamed",
			got, size)
	}
	if received.Load() != size || body.read.Load() != size {
		t.Errorf("store received %d bytes and the client read %d, want %d each", received.Load(), body.read.Load(), size)
	}
	if got := stub.putList()[0].ContentLength; got != size {
		t.Errorf("Content-Length = %d, want %d", got, size)
	}
}

// slowReader gives out its bytes a chunk at a time with a pause before each,
// like a slow but steady connection.
type slowReader struct {
	chunks int
	delay  time.Duration
	sent   int
}

// Read waits, then returns one 1 KiB chunk.
func (r *slowReader) Read(p []byte) (int, error) {
	if r.sent >= r.chunks {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.sent++
	return copy(p, bytes.Repeat([]byte("s"), min(len(p), 1024))), nil
}

// Seek rewinds to the start.
func (r *slowReader) Seek(int64, int) (int64, error) { r.sent = 0; return 0, nil }

// An upload that keeps moving has no time limit: it outlives both the API
// client's request timeout and the stall timeout. The body takes about 360ms,
// past both 150ms limits, with no gap longer than 30ms.
func TestUploadOutlivesTheRequestTimeoutWhileBytesMove(t *testing.T) {
	stub := newUploadStub(t)
	c := stub.client(t)
	c.HTTPClient.Timeout = 150 * time.Millisecond
	c.StallTimeout = 150 * time.Millisecond

	up := fileUpload{Body: &slowReader{chunks: 12, delay: 30 * time.Millisecond}, Size: 12 * 1024,
		Hash: strings.Repeat("b", 64), MIME: "text/plain", Filename: "slow.txt"}
	if _, err := c.upload(context.Background(), up); err != nil {
		t.Fatalf("a slow but steady upload failed: %v", err)
	}
	if got := len(stub.putList()[0].Body); got != 12*1024 {
		t.Errorf("store received %d bytes, want all %d", got, 12*1024)
	}
}

// An upload that stops moving is given up after the stall timeout, with an
// error that says so and counts as a network timeout. It is not tried again: a
// connection that moved nothing for the whole stall timeout will not be
// better a second later.
func TestUploadGivesUpOnAStall(t *testing.T) {
	release := make(chan struct{})
	stub := newUploadStub(t)
	t.Cleanup(func() { close(release) }) // runs before the server closes
	stub.onPut = func(w http.ResponseWriter, r *http.Request) {
		<-release // never read the body: the client's socket fills and stops
	}
	c := stub.client(t)
	c.StallTimeout = 100 * time.Millisecond

	up := fileUpload{Body: &lazyReader{size: 64 << 20}, Size: 64 << 20,
		Hash: strings.Repeat("c", 64), MIME: "video/mp4", Filename: "stuck.mp4"}
	start := time.Now()
	_, err := c.upload(context.Background(), up)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the stall was noticed after %v, want about 100ms", elapsed)
	}
	var stall *StallError
	if !errors.As(err, &stall) {
		t.Fatalf("error = %v (%T), want a *StallError", err, err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("a stall must count as a network timeout, got %v", err)
	}
	if want := "upload failed: no data moved for 100ms"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if got := len(stub.putList()); got != 1 {
		t.Errorf("store got %d PUTs, want 1 (a stall is not retried)", got)
	}
	if stub.commitCount() != 0 {
		t.Errorf("committed after a stalled upload")
	}
}

// Right after a PUT, blob_missing can mean the store has not caught up yet, so
// commit is tried again.
func TestUploadRetriesCommitOnBlobMissingAfterAPut(t *testing.T) {
	stub := newUploadStub(t)
	stub.commitReply = func(n int) mockReply {
		if n == 1 {
			return mockReply{Status: http.StatusNotFound, Body: `{"error":{"code":"blob_missing","message":"no bytes"}}`}
		}
		return mockReply{Status: http.StatusCreated, Body: `{"hash":"h"}`}
	}
	path := writeFile(t, "lag.txt", []byte("store lag"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if stub.commitCount() != 2 {
		t.Errorf("committed %d times, want 2", stub.commitCount())
	}
}

// Without a PUT there is nothing for the store to catch up with: check said
// the bytes were there, so blob_missing is the real answer and is reported.
func TestUploadBlobMissingIsFinalWithoutAPut(t *testing.T) {
	stub := newUploadStub(t)
	stub.exists = true
	stub.commitReply = func(int) mockReply {
		return mockReply{Status: http.StatusNotFound, Body: `{"error":{"code":"blob_missing","message":"no bytes"}}`}
	}
	path := writeFile(t, "gone.txt", []byte("gone"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "blob_missing" {
		t.Fatalf("error = %v, want blob_missing", err)
	}
	if stub.commitCount() != 1 {
		t.Errorf("committed %d times, want 1", stub.commitCount())
	}
}

// A zero-byte file cannot have a presigned link (the server signs only a
// positive size), so it goes through the API's multipart route instead.
func TestUploadFileSendsAnEmptyFileThroughTheAPI(t *testing.T) {
	stub := newUploadStub(t)
	path := writeFile(t, "empty.txt", nil)

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if got := stub.callList(); len(got) != 1 || got[0] != "POST /files/upload" {
		t.Errorf("calls = %v, want only POST /files/upload", got)
	}
}

// When no type is given, the file's own is detected and declared, so the link
// is signed for it and the server records it.
func TestUploadFileDetectsMIMEByExtension(t *testing.T) {
	stub := newUploadStub(t)
	path := writeFile(t, "pic.png", []byte("bytes; the .png extension drives detection"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if got := stub.body(t, "POST /files/presign-upload", 0)["mime"]; got != "image/png" {
		t.Errorf("presign mime = %v, want image/png", got)
	}
	if got := stub.putList()[0].Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("PUT Content-Type = %q, want image/png", got)
	}
}

// An explicit type is sent as given, with no detection over it.
func TestUploadFileExplicitMIMEWins(t *testing.T) {
	stub := newUploadStub(t)
	path := writeFile(t, "data.bin", []byte("x"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "application/zip", "renamed.zip"); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if got := stub.body(t, "POST /files/presign-upload", 0)["mime"]; got != "application/zip" {
		t.Errorf("presign mime = %v, want application/zip", got)
	}
	if got := stub.body(t, "POST /files/commit", 0)["filename"]; got != "renamed.zip" {
		t.Errorf("commit filename = %v, want renamed.zip", got)
	}
}

// A file whose type cannot be told is declared application/octet-stream: the
// link must be signed for some type, and presign refuses an empty one.
func TestUploadFileUnknownTypeIsOctetStream(t *testing.T) {
	stub := newUploadStub(t)
	path := writeFile(t, "blob", []byte{0x00, 0x01, 0x02, 0xff, 0xfe})

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", ""); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if got := stub.body(t, "POST /files/presign-upload", 0)["mime"]; got != "application/octet-stream" {
		t.Errorf("presign mime = %v, want application/octet-stream", got)
	}
}

// The in-memory path (client-side encryption) sends exactly the bytes it is
// given, hashed as given, stamped is_encrypted, under the caller's name and
// type rather than ones derived from the ciphertext.
func TestUploadBytesSendsTheGivenBytes(t *testing.T) {
	stub := newUploadStub(t)
	sealed := []byte("HRBC2\x00\x01\x02ciphertext-not-plaintext")

	if _, err := stub.client(t).UploadBytes(context.Background(), sealed, "application/pdf", "secret.pdf", true); err != nil {
		t.Fatalf("UploadBytes error: %v", err)
	}
	presign := stub.body(t, "POST /files/presign-upload", 0)
	if presign["hash"] != sha256Hex(sealed) || presign["size"] != float64(len(sealed)) || presign["is_encrypted"] != true {
		t.Errorf("presign body = %v, want the sealed bytes' hash and size, is_encrypted true", presign)
	}
	if got := stub.putList()[0].Body; !bytes.Equal(got, sealed) {
		t.Errorf("PUT body = %q, want the sealed bytes", got)
	}
	commit := stub.body(t, "POST /files/commit", 0)
	if commit["filename"] != "secret.pdf" || commit["mime"] != "application/pdf" || commit["is_encrypted"] != true {
		t.Errorf("commit body = %v", commit)
	}
}

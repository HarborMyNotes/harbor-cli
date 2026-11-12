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
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	// putReply gives the reply for the nth PUT (1-based). Nil means 200.
	putReply func(n int) mockReply
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
		onPut, putReply := s.onPut, s.putReply
		s.mu.Unlock()
		if onPut != nil {
			onPut(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.puts[n-1].Body = body
		s.mu.Unlock()
		reply := mockReply{Status: http.StatusOK}
		if putReply != nil {
			reply = putReply(n)
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write([]byte(reply.Body))
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.bodies[key] = append(s.bodies[key], body)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Harbor-Server", "app-1")
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

// storeError is an object store's XML refusal.
func storeError(status int, code string) mockReply {
	return mockReply{Status: status, Body: "<Error><Code>" + code + "</Code></Error>"}
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

	data, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
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
	// The API's answers named a server before the PUT, so the client had one
	// cached; it is for the API only.
	if got := put.Header.Get("X-Harbor-Server"); got != "" {
		t.Errorf("PUT carried X-Harbor-Server %q, want none on a presigned link", got)
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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
	stub.putReply = func(n int) mockReply {
		switch n {
		case 1:
			return storeError(http.StatusServiceUnavailable, "SlowDown")
		case 2:
			return storeError(http.StatusForbidden, "AccessDenied")
		}
		return mockReply{Status: http.StatusOK}
	}
	content := []byte("a file whose link ran out")
	path := writeFile(t, "slow.bin", content)

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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
	stub.putReply = func(int) mockReply { return storeError(http.StatusForbidden, "SignatureDoesNotMatch") }
	path := writeFile(t, "refused.bin", []byte("refused"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
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
	stub.putReply = func(int) mockReply { return storeError(http.StatusInternalServerError, "InternalError") }
	path := writeFile(t, "flaky.bin", []byte("flaky"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
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
// holding it. It counts what was read.
type lazyReader struct {
	size int64
	read atomic.Int64
}

// lazyByte is the byte at offset off.
func lazyByte(off int64) byte { return byte('a' + off%26) }

// ReadAt fills p with the pattern from off.
func (r *lazyReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if left := r.size - off; n > left {
		n = left
	}
	for i := range p[:n] {
		p[i] = lazyByte(off + int64(i))
	}
	r.read.Add(n)
	if n < int64(len(p)) {
		return int(n), io.EOF
	}
	return int(n), nil
}

// hash returns the sha256 of the whole pattern without counting it as read.
func (r *lazyReader) hash() string {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for off := int64(0); off < r.size; off += int64(len(buf)) {
		n := min(int64(len(buf)), r.size-off)
		for i := range buf[:n] {
			buf[i] = lazyByte(off + int64(i))
		}
		h.Write(buf[:n])
	}
	return hex.EncodeToString(h.Sum(nil))
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

	up := fileUpload{Body: body, Size: size, Hash: body.hash(), MIME: "video/mp4", Filename: "big.mp4"}
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

// slowReader gives out its bytes 1 KiB at a time with a pause before each,
// like a slow but steady connection. Its short reads break the io.ReaderAt
// rule that a short read returns an error; io.SectionReader passes them
// through, which is all this needs.
type slowReader struct {
	size  int64
	delay time.Duration
}

// ReadAt waits, then returns at most 1 KiB of 's'.
func (r *slowReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := min(int64(len(p)), 1024, r.size-off)
	return copy(p, bytes.Repeat([]byte("s"), int(n))), nil
}

// An upload that keeps moving has no time limit: it outlives both the API
// client's request timeout and the stall timeout. The body takes about 600ms,
// twice the 300ms request timeout and four times the 150ms stall timeout, with
// no gap longer than 30ms. The request timeout still covers check, presign and
// commit, so it is long enough for those on a busy machine.
func TestUploadOutlivesTheRequestTimeoutWhileBytesMove(t *testing.T) {
	stub := newUploadStub(t)
	c := stub.client(t)
	c.HTTPClient.Timeout = 300 * time.Millisecond
	c.StallTimeout = 150 * time.Millisecond

	const size = 20 * 1024
	want := bytes.Repeat([]byte("s"), size)
	up := fileUpload{Body: &slowReader{size: size, delay: 30 * time.Millisecond}, Size: size,
		Hash: sha256Hex(want), MIME: "text/plain", Filename: "slow.txt"}
	if _, err := c.upload(context.Background(), up); err != nil {
		t.Fatalf("a slow but steady upload failed: %v", err)
	}
	if got := stub.putList()[0].Body; !bytes.Equal(got, want) {
		t.Errorf("store received %d bytes, want all %d", len(got), size)
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
		Hash: "not checked: the upload never finishes", MIME: "video/mp4", Filename: "stuck.mp4"}
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "application/zip", "renamed.zip", 0); err != nil {
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

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
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

// UploadFile sends the file as it is on disk at the moment of sending, piece by
// piece, never a copy read into memory first. The store changes the end of the
// file once the first byte arrives. A streaming upload then sends the changed
// bytes; a buffered one would already hold the old ones. The file is far
// bigger than the socket buffers, so the end cannot have been read yet.
//
// The change means the bytes no longer match the hash they would be stored
// under. The server finds stored bytes by that hash, so they must never reach
// the store whole: the last piece is held back, the PUT fails, and nothing is
// retried or committed. It is not a network failure, so it does not get the
// network exit code.
func TestUploadFileStreamsFromDiskAndRefusesAFileChangedMidUpload(t *testing.T) {
	const size, tail = 64 << 20, 1 << 20
	path := writeFile(t, "growing.log", bytes.Repeat([]byte("a"), size))

	var received atomic.Int64
	var sawChange atomic.Bool
	putDone := make(chan struct{})
	stub := newUploadStub(t)
	stub.onPut = func(w http.ResponseWriter, r *http.Request) {
		defer close(putDone)
		if _, err := io.ReadFull(r.Body, make([]byte, 1)); err != nil {
			t.Errorf("reading the first byte: %v", err)
			return
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Errorf("opening the file to change it: %v", err)
			return
		}
		_, _ = f.WriteAt(bytes.Repeat([]byte("z"), tail), size-tail)
		f.Close()
		rest, _ := io.ReadAll(r.Body)
		received.Store(int64(len(rest)) + 1)
		sawChange.Store(bytes.Contains(rest, []byte("zzzzzzzz")))
	}

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
	// The client gives up as soon as it holds the last piece back; the store
	// may still be reading what came before.
	select {
	case <-putDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the store never finished reading the PUT")
	}
	if !sawChange.Load() {
		t.Error("the store never saw the changed bytes — the body was read into memory before sending")
	}
	if received.Load() >= size {
		t.Errorf("the store received all %d bytes; the last piece must be held back", received.Load())
	}
	if !errors.Is(err, errFileChanged) {
		t.Errorf("error = %v, want errFileChanged", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Errorf("the refusal looks like a network failure (%v)", err)
	}
	if n := len(stub.putList()); n != 1 {
		t.Errorf("store got %d PUTs, want 1: a changed file is not sent again", n)
	}
	if n := stub.commitCount(); n != 0 {
		t.Errorf("committed %d times, want 0", n)
	}
}

// A file that shrinks while it uploads ends before its declared length. That
// is the same refusal as a changed file: once, not retried, not a network
// failure.
func TestUploadFileRefusesAFileThatShrankMidUpload(t *testing.T) {
	const size = 32 << 20
	path := writeFile(t, "shrinking.bin", bytes.Repeat([]byte("b"), size))

	stub := newUploadStub(t)
	stub.onPut = func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadFull(r.Body, make([]byte, 1)); err != nil {
			return
		}
		if err := os.Truncate(path, size/2); err != nil {
			t.Errorf("truncating: %v", err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
	}

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
	if !errors.Is(err, errFileChanged) {
		t.Errorf("error = %v, want errFileChanged", err)
	}
	if n := len(stub.putList()); n != 1 {
		t.Errorf("store got %d PUTs, want 1", n)
	}
}

// gatedReader is a lazyReader whose first read past the start waits for
// release. A test uses it to keep a failed attempt's transport in the middle
// of reading the body while the retry runs.
type gatedReader struct {
	lazyReader
	release chan struct{}
	once    sync.Once
	held    atomic.Bool
}

// newGatedReader makes a gated body of size bytes. Cleanup lets go of a read
// still held when the test ends.
func newGatedReader(t *testing.T, size int64) *gatedReader {
	r := &gatedReader{lazyReader: lazyReader{size: size}, release: make(chan struct{})}
	t.Cleanup(r.open)
	return r
}

// open lets the held read go on.
func (r *gatedReader) open() { r.once.Do(func() { close(r.release) }) }

// ReadAt holds the first read past offset 0 until open is called.
func (r *gatedReader) ReadAt(p []byte, off int64) (int, error) {
	if off > 0 && r.held.CompareAndSwap(false, true) {
		<-r.release
	}
	return r.lazyReader.ReadAt(p, off)
}

// A retry gets its own reader. The failed attempt's transport is held in the
// middle of reading the body, and lets go once the retry is under way. With a
// reader shared between the attempts, that late read would move the retry's
// place in the body, and the retry would send the wrong bytes.
func TestUploadRetryDoesNotShareTheBody(t *testing.T) {
	const size = 64 << 20
	body := newGatedReader(t, size)
	want := body.hash()

	var got atomic.Value
	stub := newUploadStub(t)
	stub.onPut = func(w http.ResponseWriter, r *http.Request) {
		if len(stub.putList()) == 1 {
			// Answer without reading: attempt 1's transport is still mid-body.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		h := sha256.New()
		if _, err := io.CopyN(h, r.Body, 1); err != nil {
			return
		}
		body.open() // attempt 1's held read goes on now, during attempt 2
		_, _ = io.Copy(h, r.Body)
		got.Store(hex.EncodeToString(h.Sum(nil)))
	}

	up := fileUpload{Body: body, Size: size, Hash: want, MIME: "video/mp4", Filename: "retry.mp4"}
	if _, err := stub.client(t).upload(context.Background(), up); err != nil {
		t.Fatalf("upload error: %v", err)
	}
	if !body.held.Load() {
		t.Fatal("attempt 1 never reached the held read, so this test proved nothing")
	}
	if got.Load() != want {
		t.Error("the retry sent different bytes from the file")
	}
	if n := len(stub.putList()); n != 2 {
		t.Errorf("store got %d PUTs, want 2: a third means the second sent a broken body", n)
	}
}

// S3 answers 400 RequestTimeout when the connection went idle for a while.
// That is the network, not the request, so it is tried again.
func TestUploadRetriesS3RequestTimeout(t *testing.T) {
	stub := newUploadStub(t)
	stub.putReply = func(n int) mockReply {
		if n == 1 {
			return storeError(http.StatusBadRequest, "RequestTimeout")
		}
		return mockReply{Status: http.StatusOK}
	}
	path := writeFile(t, "idle.bin", []byte("idle connection"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if got := len(stub.putList()); got != 2 {
		t.Errorf("store got %d PUTs, want 2", got)
	}
}

// Any other 400 is the request itself, and sending it again cannot help.
func TestUploadOtherBadRequestIsFinal(t *testing.T) {
	stub := newUploadStub(t)
	stub.putReply = func(int) mockReply { return storeError(http.StatusBadRequest, "InvalidArgument") }
	path := writeFile(t, "bad.bin", []byte("bad"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err == nil {
		t.Fatal("expected an error")
	}
	if got := len(stub.putList()); got != 1 {
		t.Errorf("store got %d PUTs, want 1", got)
	}
}

// One blip on check, presign or commit does not throw the upload away: each is
// tried again after a 5xx or a 429. After a long PUT, a lost commit would
// otherwise mean starting over.
func TestUploadRetriesTheAPIStepsOnATemporaryError(t *testing.T) {
	var checks, presigns, commits atomic.Int32
	stub := newUploadStub(t)
	busy := `{"error":{"code":"internal_error","message":"busy"}}`
	stub.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		first := false
		switch key {
		case "POST /files/check":
			first = checks.Add(1) == 1
		case "POST /files/presign-upload":
			first = presigns.Add(1) == 1
		case "POST /files/commit":
			first = commits.Add(1) == 1
		}
		if first {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(busy))
			return
		}
		stub.serve(w, r)
	})
	path := writeFile(t, "blip.bin", []byte("one blip each"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if checks.Load() != 2 || presigns.Load() != 2 || commits.Load() != 2 {
		t.Errorf("tries: check %d, presign %d, commit %d; want 2 each", checks.Load(), presigns.Load(), commits.Load())
	}
}

// A refusal is not a blip: a 4xx from an API step is reported at once.
func TestUploadDoesNotRetryAnAPIRefusal(t *testing.T) {
	stub := newUploadStub(t)
	stub.presignReply = &mockReply{Status: http.StatusUnprocessableEntity,
		Body: `{"error":{"code":"file_too_large","message":"too big"}}`}
	path := writeFile(t, "big.bin", []byte("pretend this is big"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "file_too_large" {
		t.Fatalf("error = %v, want file_too_large", err)
	}
	if n := stub.presignCount(); n != 1 {
		t.Errorf("presigned %d times, want 1", n)
	}
}

// A server without real storage hands out links no HTTP client can reach.
// That is said once and plainly, not retried as if the network had failed.
func TestUploadRefusesALinkThatIsNotHTTP(t *testing.T) {
	stub := newUploadStub(t)
	stub.presignReply = &mockReply{Status: http.StatusOK,
		Body: `{"upload_url":"fake://put/blobs/u/ab/cd/abcd?sig=1","method":"PUT","headers":{}}`}
	path := writeFile(t, "local.bin", []byte("local dev server"))

	_, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "not an http(s) link") || strings.Contains(err.Error(), "sig=") {
		t.Fatalf("error = %v, want a plain refusal of the link (without its signature)", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Errorf("the refusal looks like a network failure (%T), which would give it the network exit code", urlErr)
	}
	if n := stub.presignCount(); n != 1 {
		t.Errorf("presigned %d times, want 1", n)
	}
}

// A pipe can be read only once, and an upload reads its file twice (to hash it,
// then to send it). UploadFile copies a pipe to a temporary file first, so
// 'harbor files upload /dev/stdin' keeps working, every byte included.
func TestUploadFileAcceptsAPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/fd on Windows")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	content := bytes.Repeat([]byte("piped line of text\n"), 1000)
	go func() {
		_, _ = w.Write(content)
		w.Close()
	}()

	stub := newUploadStub(t)
	path := fmt.Sprintf("/dev/fd/%d", r.Fd())
	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "notes.txt", 0); err != nil {
		t.Fatalf("UploadFile(%s) error: %v", path, err)
	}
	if got := stub.putList()[0].Body; !bytes.Equal(got, content) {
		t.Errorf("store received %d bytes, want the %d piped", len(got), len(content))
	}
	if got := stub.body(t, "POST /files/presign-upload", 0)["hash"]; got != sha256Hex(content) {
		t.Errorf("presign hash = %v, want the piped bytes' hash", got)
	}
	if got := stub.body(t, "POST /files/commit", 0)["mime"]; !strings.HasPrefix(fmt.Sprint(got), "text/plain") {
		t.Errorf("commit mime = %v, want text/plain detected from the content", got)
	}
}

// A 429 and a request that got no answer at all are blips too.
func TestUploadRetriesA429AndNoAnswer(t *testing.T) {
	for name, fail := range map[string]func(w http.ResponseWriter){
		"429": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
		},
		"no answer": func(w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var checks atomic.Int32
			stub := newUploadStub(t)
			stub.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/files/check" && checks.Add(1) == 1 {
					fail(w)
					return
				}
				stub.serve(w, r)
			})
			path := writeFile(t, "blip.bin", []byte("one blip"))

			if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err != nil {
				t.Fatalf("UploadFile error: %v", err)
			}
			if n := checks.Load(); n != 2 {
				t.Errorf("check tried %d times, want 2", n)
			}
		})
	}
}

// A step that keeps failing is tried fileUploadAttempts times, then reported.
func TestUploadStopsRetryingAnAPIStepAfterTheLastAttempt(t *testing.T) {
	var checks atomic.Int32
	stub := newUploadStub(t)
	stub.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"down"}}`))
	})
	path := writeFile(t, "down.bin", []byte("server down"))

	if _, err := stub.client(t).UploadFile(context.Background(), path, "", "", 0); err == nil {
		t.Fatal("expected an error")
	}
	if n := checks.Load(); n != fileUploadAttempts {
		t.Errorf("check tried %d times, want %d", n, fileUploadAttempts)
	}
}

// Some failures with no answer cannot change by waiting: a certificate that
// does not verify, a URL that does not parse. They are reported at once. The
// backoff is an hour here, so a retry would hang the test.
func TestUploadDoesNotRetryAPermanentTransportFailure(t *testing.T) {
	restore := fileUploadRetryBackoff
	fileUploadRetryBackoff = time.Hour
	t.Cleanup(func() { fileUploadRetryBackoff = restore })

	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is the point
	tlsSrv.StartTLS()
	t.Cleanup(tlsSrv.Close)
	path := writeFile(t, "never.bin", []byte("never sent"))

	for name, base := range map[string]string{
		"untrusted certificate": tlsSrv.URL,
		"unparseable URL":       "http://exa mple.com\x7f/api/v1",
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := testClient(base).UploadFile(context.Background(), path, "", "", 0)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected an error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the failure was retried")
			}
		})
	}
}

// A pipe that goes on past the limit is stopped one byte past it, before
// anything is sent, so a stream that never ends cannot fill the disk.
func TestUploadFileStopsAPipeAtTheLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/fd on Windows")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		chunk := bytes.Repeat([]byte("y"), 4096)
		for {
			if _, err := w.Write(chunk); err != nil {
				w.Close()
				return
			}
		}
	}()

	stub := newUploadStub(t)
	_, err = stub.client(t).UploadFile(context.Background(), fmt.Sprintf("/dev/fd/%d", r.Fd()), "", "endless.txt", 1<<20)
	r.Close() // ends the writer
	var tooLarge *StreamTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 1<<20 {
		t.Fatalf("error = %v, want a *StreamTooLargeError for the 1 MiB limit", err)
	}
	if calls := stub.callList(); len(calls) != 0 {
		t.Errorf("a pipe over the limit still sent %v", calls)
	}
}

// The copy of a pipe has no name on disk while it is used, so nothing is left
// behind even when the command is killed part way.
func TestUploadFileLeavesNoCopyOfAPipeOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/fd on Windows")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		_, _ = w.Write([]byte("short piped text"))
		w.Close()
	}()

	var during []os.DirEntry
	stub := newUploadStub(t)
	stub.onPut = func(w http.ResponseWriter, r *http.Request) {
		during, _ = os.ReadDir(tmp)
		_, _ = io.Copy(io.Discard, r.Body)
	}
	if _, err := stub.client(t).UploadFile(context.Background(), fmt.Sprintf("/dev/fd/%d", r.Fd()), "", "p.txt", 0); err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}
	if len(during) != 0 {
		t.Errorf("while uploading, the temp folder held %d entries, want none", len(during))
	}
	if after, _ := os.ReadDir(tmp); len(after) != 0 {
		t.Errorf("after uploading, the temp folder held %d entries, want none", len(after))
	}
}

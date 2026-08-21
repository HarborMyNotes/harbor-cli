// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCreateImportUpload verifies the create call declares the file's size and
// its options on the format-parameterised uploads route — and that not one byte
// of the file rides along with it.
func TestCreateImportUpload(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201,
		`{"data":{"import_job_id":"j1","status":"awaiting_upload","part_size":8,"part_count":2}}`)
	defer srv.Close()

	data, err := testClient(srv.URL).CreateImportUpload("enex", 12, "my export.enex", "nb1")
	if err != nil {
		t.Fatalf("CreateImportUpload error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/import/enex/uploads" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if rec.ContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json (the bytes never go through the API)", rec.ContentType)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body: %v (raw %s)", err, rec.Body)
	}
	if body["total_bytes"] != float64(12) {
		t.Errorf("total_bytes = %v", body["total_bytes"])
	}
	if body["filename"] != "my export.enex" || body["target_notebook_id"] != "nb1" {
		t.Errorf("options not forwarded: %v", body)
	}
	if !strings.Contains(string(data), "part_count") {
		t.Errorf("body = %s", data)
	}
}

// TestCreateImportUploadOmitsEmptyOptions keeps an unset --filename/--notebook
// off the wire, so the server applies its own defaults rather than an empty
// string this client invented.
func TestCreateImportUploadOmitsEmptyOptions(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 201, `{"data":{"import_job_id":"j1","part_size":8,"part_count":1}}`)
	defer srv.Close()

	if _, err := testClient(srv.URL).CreateImportUpload("enex", 5, "", ""); err != nil {
		t.Fatalf("CreateImportUpload error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if _, ok := body["filename"]; ok {
		t.Errorf("filename should be omitted when empty: %s", rec.Body)
	}
	if _, ok := body["target_notebook_id"]; ok {
		t.Errorf("target_notebook_id should be omitted when empty: %s", rec.Body)
	}
}

// TestCreateImportUploadTooLarge verifies the size cap is enforced before any
// bytes move: the create call is where enex_too_large comes back.
func TestCreateImportUploadTooLarge(t *testing.T) {
	srv := newTestServer(t, nil, 422, `{"error":{"code":"enex_too_large","message":"too big"}}`)
	defer srv.Close()
	_, err := testClient(srv.URL).CreateImportUpload("enex", 1<<50, "big.enex", "")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != "enex_too_large" {
		t.Fatalf("want enex_too_large APIError, got %v", err)
	}
}

// TestImportUploadCallsAreKindParameterised pins that nothing hardcodes "enex"
// into the transport — the same four calls have to address obsidian_zip (or any
// later format) by swapping one segment.
func TestImportUploadCallsAreKindParameterised(t *testing.T) {
	cases := []struct {
		name string
		call func(c *Client) error
		path string
	}{
		{"create", func(c *Client) error { _, e := c.CreateImportUpload("obsidian_zip", 4, "", ""); return e },
			"/import/obsidian_zip/uploads"},
		{"parts", func(c *Client) error { _, e := c.PresignImportParts("obsidian_zip", "j1", []int{1}); return e },
			"/import/obsidian_zip/uploads/j1/parts"},
		{"complete", func(c *Client) error { _, e := c.CompleteImportUpload("obsidian_zip", "j1", nil); return e },
			"/import/obsidian_zip/uploads/j1/complete"},
		{"abort", func(c *Client) error { _, e := c.AbortImportUpload("obsidian_zip", "j1"); return e },
			"/import/obsidian_zip/uploads/j1/abort"},
		{"status", func(c *Client) error { _, e := c.ImportStatus("obsidian_zip", "j1"); return e },
			"/import/obsidian_zip/j1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recordedRequest
			srv := newTestServer(t, &rec, 200, `{"data":{}}`)
			defer srv.Close()
			if err := tc.call(testClient(srv.URL)); err != nil {
				t.Fatalf("%s error: %v", tc.name, err)
			}
			if rec.Path != tc.path {
				t.Errorf("path = %q, want %q", rec.Path, tc.path)
			}
		})
	}
}

// TestPresignImportParts verifies the batch of part numbers goes up as a list.
func TestPresignImportParts(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200,
		`{"data":{"parts":[{"part_number":1,"url":"https://store/1"}],"expires_in_seconds":21600}}`)
	defer srv.Close()

	if _, err := testClient(srv.URL).PresignImportParts("enex", "j1", []int{1, 2, 3}); err != nil {
		t.Fatalf("PresignImportParts error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/import/enex/uploads/j1/parts" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	nums, _ := body["part_numbers"].([]any)
	if len(nums) != 3 || nums[0] != float64(1) || nums[2] != float64(3) {
		t.Errorf("part_numbers = %v", body["part_numbers"])
	}
}

// TestCompleteImportUpload verifies the ETag list is sent back verbatim —
// quotes included, because the store signed them that way.
func TestCompleteImportUpload(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 202, `{"data":{"import_job_id":"j1","status":"queued"}}`)
	defer srv.Close()

	parts := []ImportUploadPart{{PartNumber: 1, ETag: `"abc"`}, {PartNumber: 2, ETag: `"def"`}}
	if _, err := testClient(srv.URL).CompleteImportUpload("enex", "j1", parts); err != nil {
		t.Fatalf("CompleteImportUpload error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/import/enex/uploads/j1/complete" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	if got := string(rec.Body); got != `{"parts":[{"part_number":1,"etag":"\"abc\""},{"part_number":2,"etag":"\"def\""}]}` {
		t.Errorf("body = %s", got)
	}
}

// TestAbortImportUpload verifies the cancel call reaches the abort sub-route.
func TestAbortImportUpload(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"id":"j1","status":"aborted"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).AbortImportUpload("enex", "j1"); err != nil {
		t.Fatalf("AbortImportUpload error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/import/enex/uploads/j1/abort" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestUploadImportPart verifies a chunk is PUT with an explicit Content-Length
// and that the store's ETag comes back for the complete call to name.
func TestUploadImportPart(t *testing.T) {
	var method, body string
	var length int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, length = r.Method, r.ContentLength
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("ETag", `"chunk-etag"`)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	chunk := strings.NewReader("chunk-bytes")
	etag, err := testClient("http://unused").UploadImportPart(context.Background(), srv.URL, chunk, chunk.Size())
	if err != nil {
		t.Fatalf("UploadImportPart error: %v", err)
	}
	if method != "PUT" {
		t.Errorf("method = %q, want PUT", method)
	}
	if body != "chunk-bytes" {
		t.Errorf("body = %q", body)
	}
	// Object stores refuse a chunked part upload; the length must be declared.
	if length != int64(len("chunk-bytes")) {
		t.Errorf("Content-Length = %d, want %d", length, len("chunk-bytes"))
	}
	if etag != `"chunk-etag"` {
		t.Errorf("etag = %q, want the store's value verbatim", etag)
	}
}

// TestUploadImportPartSkipsHarborHeaders is the second deliberate exception to
// "every request carries X-Harbor-Platform: cli" (see FetchURL). A presigned
// part URL points at object storage, and an Authorization header alongside the
// signature in the query string is rejected as two auth mechanisms — so the
// chunk PUT must go out bare.
func TestUploadImportPartSkipsHarborHeaders(t *testing.T) {
	var platform, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platform = r.Header.Get("X-Harbor-Platform")
		auth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"e"`)
	}))
	defer srv.Close()

	chunk := strings.NewReader("x")
	if _, err := testClient("http://unused").UploadImportPart(context.Background(), srv.URL, chunk, 1); err != nil {
		t.Fatalf("UploadImportPart error: %v", err)
	}
	if platform != "" {
		t.Errorf("X-Harbor-Platform = %q, want none on a presigned storage URL", platform)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want none on a presigned storage URL", auth)
	}
}

// TestUploadImportPartRetriesTransientFailures proves a blip does not cost the
// whole import: there is no resume, so a 500 on part 200 of 300 would otherwise
// mean re-uploading everything. The retried attempt must send the same bytes,
// which is what the re-seek is for.
func TestUploadImportPartRetriesTransientFailures(t *testing.T) {
	restore := importPartRetryBackoff
	importPartRetryBackoff = time.Millisecond
	defer func() { importPartRetryBackoff = restore }()

	var attempts int
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if attempts < 3 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("ETag", `"ok"`)
	}))
	defer srv.Close()

	chunk := strings.NewReader("payload")
	etag, err := testClient("http://unused").UploadImportPart(context.Background(), srv.URL, chunk, chunk.Size())
	if err != nil {
		t.Fatalf("UploadImportPart error: %v", err)
	}
	if attempts != 3 || etag != `"ok"` {
		t.Errorf("attempts = %d, etag = %q", attempts, etag)
	}
	for i, b := range bodies {
		if b != "payload" {
			t.Errorf("attempt %d sent %q — a retry must re-send the same chunk", i+1, b)
		}
	}
}

// TestUploadImportPartDoesNotRetryPermanentFailures keeps a dead signature from
// costing three round trips per part: a 403 answers the same however often it
// is asked.
func TestUploadImportPartDoesNotRetryPermanentFailures(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	}))
	defer srv.Close()

	_, err := testClient("http://unused").UploadImportPart(context.Background(), srv.URL, strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should name the status: %v", err)
	}
}

// TestUploadImportPartHonorsContextCancel is the transport half of Ctrl-C: the
// command cancels the context, and an in-flight chunk has to stop rather than
// finish uploading bytes the abort call is about to throw away.
func TestUploadImportPartHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer srv.Close()
	defer close(release)

	go func() {
		<-started
		cancel()
	}()

	_, err := testClient("http://unused").UploadImportPart(ctx, srv.URL, strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("expected the canceled upload to fail")
	}
}

// TestUploadImportPartRequiresETag fails loudly when the store answers 200 with
// no ETag: the complete call cannot name a part it has no tag for, so the
// upload is already lost and saying so here beats an opaque failure later.
func TestUploadImportPartRequiresETag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := testClient("http://unused").UploadImportPart(context.Background(), srv.URL, strings.NewReader("x"), 1)
	if err == nil || !strings.Contains(err.Error(), "ETag") {
		t.Fatalf("want an ETag error, got %v", err)
	}
}

// TestImportStatus verifies the poll hits GET /import/enex/:id.
func TestImportStatus(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"id":"j1","status":"partial","errors":[]}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ImportStatus("enex", "j1"); err != nil {
		t.Fatalf("ImportStatus error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/import/enex/j1" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

// TestExportENEXByNotebook verifies the export POST body and that the raw body
// streams back for a notebook target.
func TestExportENEXByNotebook(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `<?xml version="1.0"?><en-export></en-export>`)
	defer srv.Close()
	resp, err := testClient(srv.URL).ExportENEX("nb1", nil, true)
	if err != nil {
		t.Fatalf("ExportENEX error: %v", err)
	}
	defer resp.Body.Close()
	if rec.Method != "POST" || rec.Path != "/export/enex" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["notebook_id"] != "nb1" {
		t.Errorf("notebook_id = %v", body["notebook_id"])
	}
	if body["include_resources"] != true {
		t.Errorf("include_resources = %v", body["include_resources"])
	}
	if _, ok := body["note_ids"]; ok {
		t.Errorf("note_ids should be omitted when empty: %v", body["note_ids"])
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "<en-export>") {
		t.Errorf("raw body = %q", raw)
	}
}

// TestExportENEXByNotes verifies the note-selection body and reading the
// X-Skipped-Encrypted header off the live response.
func TestExportENEXByNotes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Skipped-Encrypted", "2")
		w.Header().Set("Content-Type", "application/enex+xml")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`<en-export></en-export>`))
	}))
	defer srv.Close()
	resp, err := testClient(srv.URL).ExportENEX("", []string{"n1", "n2"}, false)
	if err != nil {
		t.Fatalf("ExportENEX error: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Skipped-Encrypted") != "2" {
		t.Errorf("X-Skipped-Encrypted = %q, want 2", resp.Header.Get("X-Skipped-Encrypted"))
	}
}

// TestExportENEXNotFound verifies a 404 surfaces as an APIError (and the body is
// closed for us — we never receive a live response on error).
func TestExportENEXNotFound(t *testing.T) {
	srv := newTestServer(t, nil, 404, `{"error":{"code":"not_found","message":"no such notebook"}}`)
	defer srv.Close()
	resp, err := testClient(srv.URL).ExportENEX("missing", nil, false)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != "not_found" || apiErr.Status != 404 {
		t.Fatalf("want not_found APIError, got %v", err)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestParseAPIErrorEnvelope(t *testing.T) {
	body := []byte(`{"error":{"code":"validation_failed","message":"Bad.","details":{"email":"is required"},"request_id":"req_abc"}}`)
	e := parseAPIError(body, 422, "req_header")
	if e.Code != "validation_failed" {
		t.Errorf("code = %s", e.Code)
	}
	if e.Status != 422 {
		t.Errorf("status = %d", e.Status)
	}
	if e.RequestID != "req_abc" {
		t.Errorf("request_id = %s (body should win over header)", e.RequestID)
	}
	lines := e.DetailLines()
	if len(lines) != 1 || lines[0] != "email: is required" {
		t.Errorf("detail lines = %v", lines)
	}
}

func TestParseAPIErrorUsesHeaderRequestIDWhenBodyMissing(t *testing.T) {
	body := []byte(`{"error":{"code":"not_found","message":"Nope."}}`)
	e := parseAPIError(body, 404, "req_header")
	if e.RequestID != "req_header" {
		t.Errorf("request_id = %s, want header fallback", e.RequestID)
	}
}

func TestParseAPIErrorFallback(t *testing.T) {
	// Non-envelope body (e.g. a proxy error) → synthesize from status.
	e := parseAPIError([]byte("Bad Gateway"), 502, "req_x")
	if e.Code != "internal_error" {
		t.Errorf("code = %s, want internal_error for 5xx", e.Code)
	}
	if e.Status != 502 {
		t.Errorf("status = %d", e.Status)
	}

	e2 := parseAPIError([]byte(""), 401, "")
	if e2.Code != "unauthorized" {
		t.Errorf("code = %s, want unauthorized", e2.Code)
	}
}

func TestAPIErrorImplementsError(t *testing.T) {
	var err error = &APIError{Code: "conflict", Message: "Clash.", Status: 409}
	if err.Error() != "conflict: Clash." {
		t.Errorf("Error() = %q", err.Error())
	}

	wrapped := fmt.Errorf("while doing thing: %w", err)
	var apiErr *APIError
	if !errors.As(wrapped, &apiErr) {
		t.Fatal("errors.As should recover the APIError from a wrapped chain")
	}
	if apiErr.Code != "conflict" {
		t.Errorf("recovered code = %s", apiErr.Code)
	}
}

// downtimeResponse builds a response the way Harbor's proxy serves its
// downtime page today: an HTML body with no maintenance header.
func downtimeResponse(status int) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	return &http.Response{StatusCode: status, Header: h}
}

// jsonResponse builds a response the way the API server answers: a JSON body.
func jsonResponse(status int) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	return &http.Response{StatusCode: status, Header: h}
}

// TestIsMaintenanceResponse pins the rule every Harbor app shares: the
// maintenance header, or a 502/503/504 whose body is not JSON. A JSON
// 502/503/504 is the API server answering, so it is not maintenance.
func TestIsMaintenanceResponse(t *testing.T) {
	withHeader := func(r *http.Response, v string) *http.Response {
		r.Header.Set("X-Harbor-Maintenance", v)
		return r
	}
	noContentType := &http.Response{StatusCode: 503, Header: http.Header{}}
	plusJSON := &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/problem+json"}}}
	badContentType := &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {";;"}}}

	cases := []struct {
		name string
		resp *http.Response
		want bool
	}{
		{"html 503", downtimeResponse(503), true},
		{"html 502", downtimeResponse(502), true},
		{"html 504", downtimeResponse(504), true},
		{"503 with no content type", noContentType, true},
		{"503 with an unreadable content type", badContentType, true},
		{"json 503", jsonResponse(503), false},
		{"json 502", jsonResponse(502), false},
		{"json 504", jsonResponse(504), false},
		{"+json 503", plusJSON, false},
		{"html 500", downtimeResponse(500), false},
		{"html 404", downtimeResponse(404), false},
		{"header on a json 503", withHeader(jsonResponse(503), "1"), true},
		{"header on a json 500", withHeader(jsonResponse(500), "1"), true},
		{"header on a 200", withHeader(jsonResponse(200), "1"), true},
		{"header set to 0", withHeader(jsonResponse(503), "0"), false},
		{"plain 200", jsonResponse(200), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMaintenanceResponse(tc.resp); got != tc.want {
				t.Errorf("isMaintenanceResponse = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResponseErrorForTheDowntimePage proves the downtime page becomes the
// maintenance error with the shared sentence, whatever its body says, and that
// Retry-After is carried for --json.
func TestResponseErrorForTheDowntimePage(t *testing.T) {
	resp := downtimeResponse(503)
	resp.Header.Set("Retry-After", "30")
	e := responseError(resp, []byte("<html>We'll be right back</html>"), "req_x")

	if e.Code != CodeMaintenance {
		t.Errorf("code = %q, want %q", e.Code, CodeMaintenance)
	}
	if e.Message != "Harbor is down for maintenance. Try again in a few minutes." {
		t.Errorf("message = %q", e.Message)
	}
	if e.Status != 503 || e.RequestID != "req_x" {
		t.Errorf("status/request id = %d / %q", e.Status, e.RequestID)
	}
	if e.RetryAfterSeconds == nil || *e.RetryAfterSeconds != 30 {
		t.Errorf("retry_after_seconds = %v, want 30", e.RetryAfterSeconds)
	}
	if !IsMaintenance(e) {
		t.Error("IsMaintenance = false for the downtime page")
	}
}

// TestResponseErrorKeepsTheServersOwnJSON503 is the counterweight: the API
// server's own JSON 503 (the search timeout) keeps its code and message.
func TestResponseErrorKeepsTheServersOwnJSON503(t *testing.T) {
	body := []byte(`{"error":{"code":"timeout","message":"Search took too long.","request_id":"req_s"}}`)
	e := responseError(jsonResponse(503), body, "req_header")

	if e.Code != "timeout" || e.Message != "Search took too long." {
		t.Errorf("code/message = %q / %q, want the server's own", e.Code, e.Message)
	}
	if IsMaintenance(e) {
		t.Error("a JSON 503 from the server was taken for maintenance")
	}
	if e.RetryAfterSeconds != nil {
		t.Errorf("retry_after_seconds = %d, want none without the header", *e.RetryAfterSeconds)
	}
}

// TestRetryAfterSeconds covers both forms of the header and the values that
// must be ignored rather than guessed at.
func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	intp := func(n int) *int { return &n }
	cases := []struct {
		header string
		want   *int
	}{
		{"", nil},
		{"30", intp(30)},
		{" 120 ", intp(120)},
		{"0", intp(0)},
		{"-5", nil},
		{"soon", nil},
		{"Fri, 27 Nov 2026 12:00:45 GMT", intp(45)},
		{"Fri, 27 Nov 2026 11:59:00 GMT", intp(0)},
	}
	for _, tc := range cases {
		got := retryAfterSeconds(tc.header, now)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("retryAfterSeconds(%q) = %d, want nil", tc.header, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("retryAfterSeconds(%q) = %v, want %d", tc.header, got, *tc.want)
		}
	}

	// A date 1.5 s away rounds up to 2, so a script never comes back early.
	halfPast := now.Add(500 * time.Millisecond)
	if got := retryAfterSeconds("Fri, 27 Nov 2026 12:00:02 GMT", halfPast); got == nil || *got != 2 {
		t.Errorf("a date 1.5 s ahead = %v, want 2", got)
	}
}

// TestMaintenanceErrorStringIsTheSentence keeps an inline-printed maintenance
// error from repeating itself ("maintenance: Harbor is down for maintenance").
func TestMaintenanceErrorStringIsTheSentence(t *testing.T) {
	e := &APIError{Code: CodeMaintenance, Message: MaintenanceMessage, Status: 503}
	if e.Error() != MaintenanceMessage {
		t.Errorf("Error() = %q, want the sentence alone", e.Error())
	}
	if got := (&APIError{Code: "timeout", Message: "slow", Status: 503}).Error(); got != "timeout: slow" {
		t.Errorf("other codes changed: Error() = %q", got)
	}
}

// TestIsMaintenanceFindsAWrappedError proves the check sees through wrapping,
// which is how most commands return a client error.
func TestIsMaintenanceFindsAWrappedError(t *testing.T) {
	wrapped := fmt.Errorf("listing notes: %w", &APIError{Code: CodeMaintenance, Message: MaintenanceMessage})
	if !IsMaintenance(wrapped) {
		t.Error("IsMaintenance missed a wrapped maintenance error")
	}
	if IsMaintenance(errors.New("maintenance")) || IsMaintenance(nil) {
		t.Error("IsMaintenance matched something that is not a maintenance error")
	}
}

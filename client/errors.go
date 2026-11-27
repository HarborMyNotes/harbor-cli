// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CodeMaintenance is the error code for a response that came from Harbor's
// downtime page rather than from the API server.
const CodeMaintenance = "maintenance"

// MaintenanceMessage is the sentence every Harbor app shows when its servers
// are down for maintenance. The words are shared across apps, so keep them
// exactly as they are.
const MaintenanceMessage = "Harbor is down for maintenance. Try again in a few minutes."

// maintenanceHeader marks every response the downtime page sends.
const maintenanceHeader = "X-Harbor-Maintenance"

// APIError is the typed form of the Harbor error envelope:
//
//	{ "error": { "code", "message", "details", "request_id" } }
//
// Commands branch on Code (e.g. invalid_token triggers refresh; sync_conflict;
// validation_failed shows Details). It implements error so errors.As recovers
// the typed value from a wrapped error chain.
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	RequestID string         `json:"request_id"`
	Status    int            `json:"-"`

	// RetryAfterSeconds is the response's Retry-After header in seconds, so a
	// script reading --json can wait that long before it tries again. Nil when
	// the response had no usable Retry-After.
	RetryAfterSeconds *int `json:"retry_after_seconds,omitempty"`
}

// errorEnvelope is the wire shape carrying an APIError.
type errorEnvelope struct {
	Error *APIError `json:"error"`
}

// Error renders a concise single-line description. Richer rendering (bulleted
// details, dim request id) lives in the cmd display layer. A maintenance error
// is the message alone, because "maintenance: Harbor is down for maintenance"
// says the same thing twice wherever an error is printed inline.
func (e *APIError) Error() string {
	if e == nil {
		return "unknown API error"
	}
	if e.Code == CodeMaintenance && e.Message != "" {
		return e.Message
	}
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, msg)
	}
	return msg
}

// DetailLines returns the validation details as sorted "field: message" lines,
// for friendly multi-line rendering. Non-string detail values are stringified.
func (e *APIError) DetailLines() []string {
	if e == nil || len(e.Details) == 0 {
		return nil
	}
	lines := make([]string, 0, len(e.Details))
	for k, v := range e.Details {
		lines = append(lines, fmt.Sprintf("%s: %v", k, v))
	}
	sort.Strings(lines)
	return lines
}

// IsMaintenance reports whether err is Harbor's servers being down for
// maintenance. Nothing was decided by the server, so the same request can work
// once Harbor is back.
func IsMaintenance(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == CodeMaintenance
}

// isMaintenanceResponse reports whether resp came from Harbor's downtime page
// rather than from the API server. That is the case when the response carries
// the maintenance header, or when it is a 502, 503 or 504 whose body is not
// JSON. The API server's own 502/503/504 answers (a search timeout, a draining
// sync stream) are always JSON, so they are never mistaken for downtime.
func isMaintenanceResponse(resp *http.Response) bool {
	if strings.TrimSpace(resp.Header.Get(maintenanceHeader)) == "1" {
		return true
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return !isJSONContentType(resp.Header.Get("Content-Type"))
	}
	return false
}

// isJSONContentType reports whether a Content-Type header is application/json,
// with or without parameters such as charset.
func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

// responseError builds the error for a failed response. A response from the
// downtime page becomes the maintenance error whatever its body says; anything
// else is decoded from the body. Retry-After is kept on both, so a script can
// see how long to wait.
func responseError(resp *http.Response, body []byte, requestID string) *APIError {
	var apiErr *APIError
	if isMaintenanceResponse(resp) {
		apiErr = &APIError{
			Code:      CodeMaintenance,
			Message:   MaintenanceMessage,
			Status:    resp.StatusCode,
			RequestID: requestID,
		}
	} else {
		apiErr = parseAPIError(body, resp.StatusCode, requestID)
	}
	apiErr.RetryAfterSeconds = retryAfterSeconds(resp.Header.Get("Retry-After"), time.Now())
	return apiErr
}

// retryAfterSeconds reads a Retry-After header, which is either a number of
// seconds or an HTTP date. A date in the past means "now", so it is 0. It
// returns nil for a missing or unreadable header.
func retryAfterSeconds(header string, now time.Time) *int {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs < 0 {
			return nil
		}
		return &secs
	}
	when, err := http.ParseTime(header)
	if err != nil {
		return nil
	}
	secs := 0
	if wait := when.Sub(now); wait > 0 {
		// Round up, so a script never comes back a moment too early.
		secs = int((wait + time.Second - 1) / time.Second)
	}
	return &secs
}

// parseAPIError decodes a non-2xx response body into an APIError. When the body
// is not a well-formed error envelope (e.g. a proxy 502, or an empty body), it
// synthesizes an APIError from the HTTP status so callers always get a typed,
// non-nil error.
func parseAPIError(body []byte, status int, requestID string) *APIError {
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && (env.Error.Code != "" || env.Error.Message != "") {
		env.Error.Status = status
		if env.Error.RequestID == "" {
			env.Error.RequestID = requestID
		}
		return env.Error
	}

	// Fallback: not a recognizable envelope. Surface what we can.
	msg := strings.TrimSpace(string(body))
	if msg == "" || len(msg) > 500 {
		msg = http.StatusText(status)
	}
	return &APIError{
		Code:      fallbackCode(status),
		Message:   msg,
		Status:    status,
		RequestID: requestID,
	}
}

// fallbackCode maps an HTTP status to a stable code when the body carries none,
// mirroring the canonical code table for the common statuses.
func fallbackCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media"
	case http.StatusUnprocessableEntity:
		return "validation_failed"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusNotImplemented:
		return "not_implemented"
	case http.StatusServiceUnavailable:
		return "timeout"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "error"
	}
}

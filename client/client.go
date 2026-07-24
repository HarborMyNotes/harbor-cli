// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

// Package client is a stateless HTTP client for the Harbor API. It speaks the
// Harbor wire contract: JSON request/response bodies, the standard response
// envelopes, and the typed error model. See docs/conventions.md in the API
// repo for the authoritative contract.
package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to a single Harbor API base URL using a bearer access token.
// The zero value is not usable; construct one with NewClient.
type Client struct {
	// BaseURL includes the /api/v1 prefix (e.g. https://app.harbor.my/api/v1).
	// Domain method paths are relative to it.
	BaseURL string

	// AccessToken is the bearer token sent on protected requests. It may be
	// empty for public endpoints (login, password reset, public share, ops).
	AccessToken string

	// HTTPClient performs the requests. Defaults to a 30s-timeout client.
	HTTPClient *http.Client

	// Verbose, when true, makes callers surface request ids / HTTP status.
	Verbose bool

	// OnUnauthorized, when set, is invoked once when a protected request fails
	// with 401 invalid_token. It should refresh the token, persist the rotated
	// pair, and return the new access token. ok=false aborts the retry. This is
	// how transparent token refresh is wired without a config import cycle.
	OnUnauthorized func() (newAccessToken string, ok bool)

	// LastRequestID holds the X-Request-Id of the most recent response, for
	// verbose error reporting.
	LastRequestID string

	// refreshMu serializes refresh attempts so two requests can never rotate
	// the same refresh token concurrently (which would revoke the family).
	refreshMu sync.Mutex
}

// clientPlatform identifies this client to the API via the X-Harbor-Platform
// header on every request. The backend uses it to attribute in-app Contact
// Support requests (and analytics) to the platform they originated from.
const clientPlatform = "cli"

// NewClient creates a Harbor API client for the given base URL and access
// token. The base URL is trimmed of any trailing slash.
func NewClient(baseURL, accessToken string) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		AccessToken: accessToken,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Origin returns the scheme+host root of the API by stripping the trailing
// /api/v1 suffix from the base URL. The root-level operational probes
// (/health, /ready, /version) live there rather than under /api/v1.
func (c *Client) Origin() string {
	base := strings.TrimRight(c.BaseURL, "/")
	return strings.TrimSuffix(base, "/api/v1")
}

// buildURL joins the base URL with a path and encodes non-empty query params.
func (c *Client) buildURL(path string, params map[string]string) (string, error) {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			if v != "" {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// doGet performs a GET request with optional query parameters and returns the
// response body.
func (c *Client) doGet(path string, params map[string]string) ([]byte, error) {
	full, err := c.buildURL(path, params)
	if err != nil {
		return nil, err
	}
	return c.request(http.MethodGet, full, nil, "", true)
}

// doGetQuery is like doGet but takes a pre-built url.Values, so callers can
// send an explicitly-empty parameter (e.g. parent_id= to mean "top-level
// only") — buildURL's map form intentionally drops empty values.
func (c *Client) doGetQuery(path string, q url.Values) ([]byte, error) {
	full := c.BaseURL + path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	return c.request(http.MethodGet, full, nil, "", true)
}

// doPost performs a POST request with a JSON-encoded body.
func (c *Client) doPost(path string, body any) ([]byte, error) {
	return c.doJSON(http.MethodPost, path, body)
}

// doPatch performs a PATCH request with a JSON-encoded body.
func (c *Client) doPatch(path string, body any) ([]byte, error) {
	return c.doJSON(http.MethodPatch, path, body)
}

// doPut performs a PUT request with a JSON-encoded body.
func (c *Client) doPut(path string, body any) ([]byte, error) {
	return c.doJSON(http.MethodPut, path, body)
}

// doDelete performs a DELETE request with optional query parameters.
func (c *Client) doDelete(path string, params map[string]string) ([]byte, error) {
	full, err := c.buildURL(path, params)
	if err != nil {
		return nil, err
	}
	return c.request(http.MethodDelete, full, nil, "", true)
}

// doJSON marshals body (when non-nil) and performs a request with the given
// method and a JSON content type.
func (c *Client) doJSON(method, path string, body any) ([]byte, error) {
	full, err := c.buildURL(path, nil)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request body: %w", err)
		}
	}
	return c.request(method, full, raw, "application/json", true)
}

// doMultipart performs a multipart/form-data POST: extra text fields plus one
// file part. The whole body is buffered so the request can be safely retried
// after a transparent token refresh. Used by file upload and ENEX import.
func (c *Client) doMultipart(path string, fields map[string]string, fileField, filename string, fileContent io.Reader) ([]byte, error) {
	full, err := c.buildURL(path, nil)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("failed to write form field %q: %w", k, err)
		}
	}
	if fileContent != nil {
		fw, err := w.CreateFormFile(fileField, filename)
		if err != nil {
			return nil, fmt.Errorf("failed to create file part: %w", err)
		}
		if _, err := io.Copy(fw, fileContent); err != nil {
			return nil, fmt.Errorf("failed to copy file content: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart body: %w", err)
	}

	return c.request(http.MethodPost, full, buf.Bytes(), w.FormDataContentType(), true)
}

// doGetRaw performs a GET and returns the raw *http.Response for streaming
// (downloads, ENEX export). The caller MUST close the response body. It honors
// a single transparent refresh on 401. Non-2xx responses are decoded into an
// APIError and the body is closed before returning.
func (c *Client) doGetRaw(path string, params map[string]string) (*http.Response, error) {
	full, err := c.buildURL(path, params)
	if err != nil {
		return nil, err
	}
	return c.rawRequest(http.MethodGet, full, true)
}

// request sends a request and returns just the body, discarding the status.
// Most callers want this; use requestWithStatus when the 2xx code matters
// (e.g. attach-tag's 200-existing vs 201-created).
func (c *Client) request(method, fullURL string, body []byte, contentType string, allowRefresh bool) ([]byte, error) {
	data, _, err := c.requestWithStatus(method, fullURL, body, contentType, allowRefresh)
	return data, err
}

// requestWithStatus sends a request with a buffered body so it can be rebuilt
// for a retry, and returns the response body and HTTP status. On a 401
// invalid_token with a refresh hook set, it refreshes once and retries the
// original request exactly once.
func (c *Client) requestWithStatus(method, fullURL string, body []byte, contentType string, allowRefresh bool) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fullURL, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.setCommonHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	c.LastRequestID = resp.Header.Get("X-Request-Id")

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, resp.StatusCode, nil
	}

	apiErr := parseAPIError(respBody, resp.StatusCode, c.LastRequestID)
	if allowRefresh && resp.StatusCode == http.StatusUnauthorized && apiErr.Code == "invalid_token" && c.OnUnauthorized != nil {
		if newTok, ok := c.refresh(); ok {
			c.AccessToken = newTok
			return c.requestWithStatus(method, fullURL, body, contentType, false)
		}
	}
	return nil, resp.StatusCode, apiErr
}

// rawRequest is request's streaming sibling for downloads. On success it
// returns the live response (caller closes the body); on error it drains and
// closes the body and returns an APIError, optionally retrying once after a
// refresh.
func (c *Client) rawRequest(method, fullURL string, allowRefresh bool) (*http.Response, error) {
	req, err := http.NewRequest(method, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
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

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.LastRequestID = resp.Header.Get("X-Request-Id")
	apiErr := parseAPIError(body, resp.StatusCode, c.LastRequestID)
	if allowRefresh && resp.StatusCode == http.StatusUnauthorized && apiErr.Code == "invalid_token" && c.OnUnauthorized != nil {
		if newTok, ok := c.refresh(); ok {
			c.AccessToken = newTok
			return c.rawRequest(method, fullURL, false)
		}
	}
	return nil, apiErr
}

// setCommonHeaders applies Accept, Authorization (when a token is present), and
// a generated X-Request-Id to a request.
func (c *Client) setCommonHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	req.Header.Set("X-Harbor-Platform", clientPlatform)
	req.Header.Set("X-Request-Id", generateRequestID())
}

// refresh invokes the OnUnauthorized hook under a lock so concurrent callers
// never rotate the same single-use refresh token twice.
func (c *Client) refresh() (string, bool) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if c.OnUnauthorized == nil {
		return "", false
	}
	return c.OnUnauthorized()
}

// generateRequestID returns a client-supplied correlation id of the form
// req_<hex>. It uses crypto/rand but falls back to a static suffix if the
// system RNG is unavailable (the server will replace an invalid id anyway).
func generateRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "req_cli"
	}
	return "req_" + hex.EncodeToString(b)
}

// PrettyJSON pretty-prints raw JSON bytes with two-space indentation. Used for
// the --json output mode on every command.
func PrettyJSON(data []byte) (string, error) {
	var obj any
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return "", err
	}
	return string(pretty), nil
}

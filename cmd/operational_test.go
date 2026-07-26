// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// TestOperationalStatusEquals checks the bare-status comparison used to read
// liveness/readiness from the {"status":...} bodies.
func TestOperationalStatusEquals(t *testing.T) {
	if !operationalStatusEquals([]byte(`{"status":"ok"}`), "ok") {
		t.Error("expected ok body to match")
	}
	if operationalStatusEquals([]byte(`{"status":"not_ready"}`), "ready") {
		t.Error("not_ready should not match ready")
	}
	if operationalStatusEquals([]byte(`not json`), "ok") {
		t.Error("non-JSON body should not match")
	}
}

// TestOperationalReadyBodyPassthrough verifies a successful Ready() body is
// returned unchanged.
func TestOperationalReadyBodyPassthrough(t *testing.T) {
	body := []byte(`{"status":"ready","checks":{}}`)
	got := operationalReadyBody(body, nil)
	if string(got) != string(body) {
		t.Errorf("passthrough = %s, want %s", got, body)
	}
}

// TestOperationalReadyBodyRecoversFrom503 verifies the readiness JSON is
// recovered from the *APIError the client returns on a 503, so the degraded
// table can still be rendered.
func TestOperationalReadyBodyRecoversFrom503(t *testing.T) {
	readyJSON := `{"status":"not_ready","checks":{"s3":{"ok":false,"latency_ms":2003,"error":"timeout"}}}`
	apiErr := &client.APIError{Code: "timeout", Message: readyJSON, Status: 503}

	got := operationalReadyBody(nil, apiErr)
	if !strings.Contains(string(got), "not_ready") {
		t.Errorf("recovered body = %s, want the readiness JSON", got)
	}
	if operationalStatusEquals(got, "ready") {
		t.Error("recovered body should report not ready")
	}
}

// TestOperationalReadyBodyIgnoresNonJSONError verifies that a non-JSON error
// (e.g. a connection failure) does not get misread as a readiness body.
func TestOperationalReadyBodyIgnoresNonJSONError(t *testing.T) {
	apiErr := &client.APIError{Code: "internal_error", Message: "Service Unavailable", Status: 503}
	got := operationalReadyBody(nil, apiErr)
	if got != nil {
		t.Errorf("expected nil body for non-JSON error, got %s", got)
	}
}

// TestOperationalCombine verifies the combined object embeds liveness, readiness
// checks, and version, with the right live/ready flags.
func TestOperationalCombine(t *testing.T) {
	health := []byte(`{"status":"ok"}`)
	ready := []byte(`{"status":"ready","checks":{"system_db":{"ok":true,"latency_ms":1}}}`)
	version := []byte(`{"version":"2.0.0-m2","commit":"29ac12e"}`)

	combined := operationalCombine(health, nil, ready, version, true, true)
	root := parseJSON(combined)
	if !boolean(root, "live") || !boolean(root, "ready") {
		t.Errorf("live/ready flags wrong: %s", combined)
	}
	if nested(root, "version") == nil || str(nested(root, "version"), "version") != "2.0.0-m2" {
		t.Errorf("version not embedded: %s", combined)
	}
	checks := nested(root, "readiness")["checks"]
	if checks == nil {
		t.Errorf("readiness checks not embedded: %s", combined)
	}
}

// TestOperationalCombineRecordsLivenessError verifies a liveness failure is
// captured in the combined object when /health could not be reached.
func TestOperationalCombineRecordsLivenessError(t *testing.T) {
	combined := operationalCombine(nil, errString("connection refused"), nil, nil, false, false)
	root := parseJSON(combined)
	if boolean(root, "live") {
		t.Error("live should be false")
	}
	h := nested(root, "health")
	if h == nil || str(h, "error") == "" {
		t.Errorf("health error not recorded: %s", combined)
	}
}

// TestDisplayStatusHealthy verifies the healthy view shows each check, the
// version, and the "all systems operational" summary.
func TestDisplayStatusHealthy(t *testing.T) {
	combined := operationalCombine(
		[]byte(`{"status":"ok"}`),
		nil,
		[]byte(`{"status":"ready","checks":{"system_db":{"ok":true,"latency_ms":1},"s3":{"ok":true,"latency_ms":0}}}`),
		[]byte(`{"version":"2.0.0-m2"}`),
		true, true,
	)
	out := captureStdout(t, func() { displayStatus(combined) })
	for _, want := range []string{"system_db", "s3", "2.0.0-m2", "all systems operational"} {
		if !strings.Contains(out, want) {
			t.Errorf("healthy status output missing %q:\n%s", want, out)
		}
	}
	// Latency should be rendered with units.
	if !strings.Contains(out, "1 ms") {
		t.Errorf("latency missing units:\n%s", out)
	}
}

// TestDisplayStatusDegraded verifies the degraded view surfaces the failing
// check, its error, and the DEGRADED summary.
func TestDisplayStatusDegraded(t *testing.T) {
	combined := operationalCombine(
		[]byte(`{"status":"ok"}`),
		nil,
		[]byte(`{"status":"not_ready","checks":{"s3":{"ok":false,"latency_ms":2003,"error":"timeout"}}}`),
		[]byte(`{"version":"2.0.0-m2"}`),
		true, false,
	)
	out := captureStdout(t, func() { displayStatus(combined) })
	for _, want := range []string{"s3", "timeout", "DEGRADED", "not_ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("degraded status output missing %q:\n%s", want, out)
		}
	}
}

// TestDisplayAPIVersion verifies the version detail view renders every field.
func TestDisplayAPIVersion(t *testing.T) {
	data := []byte(`{"version":"2.0.0-m2","commit":"29ac12e","build_time":"2026-06-21T06:41:20Z","go_version":"go1.25.0"}`)
	out := captureStdout(t, func() { displayAPIVersion(data) })
	for _, want := range []string{"2.0.0-m2", "29ac12e", "2026-06-21T06:41:20Z", "go1.25.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output missing %q:\n%s", want, out)
		}
	}
}

// TestBoolWord checks the small word-picker helper.
func TestBoolWord(t *testing.T) {
	if got := boolWord(true, "yes", "no"); got != "yes" {
		t.Errorf("boolWord(true) = %q", got)
	}
	if got := boolWord(false, "yes", "no"); got != "no" {
		t.Errorf("boolWord(false) = %q", got)
	}
}

// errString is a trivial error type for exercising the liveness-error path
// without a real network failure.
type errString string

// Error returns the underlying message, satisfying the error interface.
func (e errString) Error() string { return string(e) }

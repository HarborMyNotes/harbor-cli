// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-23

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newSupportTestCmd builds an isolated command carrying the real support flag
// set, so tests can drive resolution without touching the global command tree.
func newSupportTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "support"}
	addSupportFlags(cmd)
	return cmd
}

// setFlag sets a flag value on the test command, failing the test on error.
func setFlag(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
}

// TestBuildSupportBodyShape asserts the wire body carries category, subject,
// message, and a metadata object built from what the CLI knows.
func TestBuildSupportBodyShape(t *testing.T) {
	cmd := newSupportTestCmd(t)
	setFlag(t, cmd, "category", "bug")
	setFlag(t, cmd, "subject", "Sync stuck")
	setFlag(t, cmd, "content", "It has been spinning for an hour.")

	body, err := buildSupportBody(cmd, false)
	if err != nil {
		t.Fatalf("buildSupportBody error: %v", err)
	}
	if body["category"] != "bug" {
		t.Errorf("category = %v", body["category"])
	}
	if body["subject"] != "Sync stuck" {
		t.Errorf("subject = %v", body["subject"])
	}
	if body["message"] != "It has been spinning for an hour." {
		t.Errorf("message = %v", body["message"])
	}
	meta, ok := body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing/not a map: %v", body["metadata"])
	}
	if meta["os"] == nil || meta["app_version"] == nil {
		t.Errorf("metadata missing os/app_version: %v", meta)
	}
	// name/email/account must NOT be sent — the server derives them.
	for _, forbidden := range []string{"name", "email", "account", "account_id"} {
		if _, present := body[forbidden]; present {
			t.Errorf("body must not include %q (derived server-side)", forbidden)
		}
	}
	// No attachments were provided, so the key is omitted entirely.
	if _, present := body["attachments"]; present {
		t.Errorf("attachments should be absent when none supplied")
	}
}

// TestBuildSupportBodyRejectsUnknownCategory covers unknown-category rejection.
func TestBuildSupportBodyRejectsUnknownCategory(t *testing.T) {
	cmd := newSupportTestCmd(t)
	setFlag(t, cmd, "category", "not_a_category")
	setFlag(t, cmd, "subject", "Hi")
	setFlag(t, cmd, "content", "Body")

	_, err := buildSupportBody(cmd, false)
	if err == nil || !strings.Contains(err.Error(), "invalid --category") {
		t.Fatalf("err = %v, want invalid --category", err)
	}
}

// TestBuildSupportBodyNonInteractiveGuards verifies each required field errors
// (rather than hanging on a prompt) when missing in non-interactive mode.
func TestBuildSupportBodyNonInteractiveGuards(t *testing.T) {
	cases := []struct {
		name    string
		set     map[string]string
		wantErr string
	}{
		{"missing category", map[string]string{"subject": "s", "content": "m"}, "--category is required"},
		{"missing subject", map[string]string{"category": "bug", "content": "m"}, "--subject is required"},
		{"missing message", map[string]string{"category": "bug", "subject": "s"}, "a message is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newSupportTestCmd(t)
			for k, v := range tc.set {
				setFlag(t, cmd, k, v)
			}
			_, err := buildSupportBody(cmd, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// TestBuildSupportBodyWithAttachment confirms a supplied file rides along as a
// base64 attachment object with filename + content_type.
func TestBuildSupportBodyWithAttachment(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(png, []byte("fake-png-bytes"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	cmd := newSupportTestCmd(t)
	setFlag(t, cmd, "category", "bug")
	setFlag(t, cmd, "subject", "Broken")
	setFlag(t, cmd, "content", "See attached")
	setFlag(t, cmd, "attach", png)

	body, err := buildSupportBody(cmd, false)
	if err != nil {
		t.Fatalf("buildSupportBody error: %v", err)
	}
	atts, ok := body["attachments"].([]map[string]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %v", body["attachments"])
	}
	if atts[0]["filename"] != "shot.png" {
		t.Errorf("filename = %v", atts[0]["filename"])
	}
	if ct, _ := atts[0]["content_type"].(string); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("content_type = %v, want image/png", atts[0]["content_type"])
	}
	if data, _ := atts[0]["data"].(string); data == "" {
		t.Error("attachment data (base64) is empty")
	}
}

// TestBuildSupportAttachmentsCaps exercises the file-count and per-file size
// caps (both fail before reading bytes) and the happy path.
func TestBuildSupportAttachmentsCaps(t *testing.T) {
	dir := t.TempDir()

	// Count cap: six paths, over the five-file limit.
	six := make([]string, 6)
	for i := range six {
		six[i] = filepath.Join(dir, "f")
	}
	if _, err := buildSupportAttachments(six); err == nil || !strings.Contains(err.Error(), "too many attachments") {
		t.Fatalf("count cap err = %v", err)
	}

	// Per-file cap: a (sparse) file just over 5 MB.
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatalf("create big: %v", err)
	}
	if err := f.Truncate(supportMaxAttachmentBytes + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_ = f.Close()
	if _, err := buildSupportAttachments([]string{big}); err == nil || !strings.Contains(err.Error(), "per-file limit") {
		t.Fatalf("per-file cap err = %v", err)
	}

	// Nil/empty input yields no attachments and no error.
	if got, err := buildSupportAttachments(nil); err != nil || got != nil {
		t.Fatalf("empty input = (%v, %v)", got, err)
	}
}

// TestBuildSupportAttachmentsTotalCap covers the 15 MB aggregate limit.
func TestBuildSupportAttachmentsTotalCap(t *testing.T) {
	dir := t.TempDir()
	// Four sparse 4 MB files = 16 MB total, over the 15 MB cap (each under 5 MB).
	paths := make([]string, 4)
	for i := range paths {
		p := filepath.Join(dir, string(rune('a'+i))+".bin")
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := f.Truncate(4 * 1024 * 1024); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		_ = f.Close()
		paths[i] = p
	}
	if _, err := buildSupportAttachments(paths); err == nil || !strings.Contains(err.Error(), "15 MB total") {
		t.Fatalf("total cap err = %v", err)
	}
}

// TestSupportCategoryHelpers checks the fixed slug set validation.
func TestSupportCategoryHelpers(t *testing.T) {
	for _, slug := range []string{"feature_request", "how_to", "access_login", "bug", "billing", "emergency", "other"} {
		if !isValidCategory(slug) {
			t.Errorf("isValidCategory(%q) = false", slug)
		}
	}
	if isValidCategory("nope") {
		t.Error("isValidCategory(nope) = true")
	}
	if len(categorySlugs()) != 7 {
		t.Errorf("categorySlugs len = %d, want 7", len(categorySlugs()))
	}
}

// TestSupportMetadata confirms the metadata map carries the fields the CLI knows.
func TestSupportMetadata(t *testing.T) {
	meta := supportMetadata()
	for _, key := range []string{"app_version", "os", "device_model"} {
		if meta[key] == nil || meta[key] == "" {
			t.Errorf("metadata[%q] missing", key)
		}
	}
}

// TestDisplaySupportConfirmation renders the success state: the 24-hour promise,
// the reference id, and the direct-email fallback.
func TestDisplaySupportConfirmation(t *testing.T) {
	data := []byte(`{"data":{"received":true,"reference":"SUP-4F2A"}}`)
	out := captureStdout(t, func() { displaySupportConfirmation(data) })
	for _, want := range []string{"24 hours", "SUP-4F2A", "help@harbor.my"} {
		if !strings.Contains(out, want) {
			t.Errorf("confirmation missing %q:\n%s", want, out)
		}
	}
}

// TestDisplaySupportConfirmationNoReference tolerates a missing reference id.
func TestDisplaySupportConfirmationNoReference(t *testing.T) {
	data := []byte(`{"data":{"received":true}}`)
	out := captureStdout(t, func() { displaySupportConfirmation(data) })
	if !strings.Contains(out, "24 hours") || !strings.Contains(out, "help@harbor.my") {
		t.Errorf("confirmation body missing:\n%s", out)
	}
}

// TestMapSupportError maps the domain-relevant codes to friendly guidance and
// lets validation_failed fall through (renderError prints its per-field details).
func TestMapSupportError(t *testing.T) {
	if got := mapSupportError(apiErr("payload_too_large")); !strings.Contains(got.Error(), "too large") {
		t.Errorf("payload_too_large = %q", got.Error())
	}
	if got := mapSupportError(apiErr("unsupported_media")); !strings.Contains(got.Error(), "unsupported file type") {
		t.Errorf("unsupported_media = %q", got.Error())
	}
	// validation_failed is returned unchanged so the renderer shows the details.
	ve := apiErr("validation_failed")
	if got := mapSupportError(ve); got != ve {
		t.Errorf("validation_failed should pass through unchanged, got %v", got)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"
)

func TestDisplayProfile(t *testing.T) {
	data := []byte(`{"data":{"id":"u1","name":"Jane","email":"jane@example.com","email_verified":true,"pending_email":"new@example.com","locale":"en","timezone":"UTC","created_at":1750000000000,"updated_at":1750000000000}}`)
	out := captureStdout(t, func() { displayProfile(data) })
	if !strings.Contains(out, "jane@example.com") {
		t.Errorf("email missing:\n%s", out)
	}
	if !strings.Contains(out, "Pending email") || !strings.Contains(out, "new@example.com") {
		t.Errorf("pending email missing:\n%s", out)
	}
}

func TestDisplayProfileShowsInboundEmail(t *testing.T) {
	data := []byte(`{"data":{"id":"u1","name":"Jane","email":"jane@example.com","inbound_email_address":"jane.k3f9qzab@m.example.test","inbound_email_enabled":true,"created_at":1750000000000,"updated_at":1750000000000}}`)
	out := captureStdout(t, func() { displayProfile(data) })
	if !strings.Contains(out, "jane.k3f9qzab@m.example.test") {
		t.Errorf("inbound address missing:\n%s", out)
	}
	if !strings.Contains(out, "Inbound email enabled") {
		t.Errorf("enabled row missing:\n%s", out)
	}
}

// A client that may not see the address gets NO keys back. The rows must then be
// omitted entirely — rendering them would state "off", which is not what an
// absent key means.
func TestDisplayProfileOmitsInboundEmailWhenUndisclosed(t *testing.T) {
	data := []byte(`{"data":{"id":"u1","name":"Jane","email":"jane@example.com","created_at":1750000000000,"updated_at":1750000000000}}`)
	out := captureStdout(t, func() { displayProfile(data) })
	if strings.Contains(out, "Inbound email") {
		t.Errorf("inbound rows should be absent:\n%s", out)
	}
}

func TestDisplayInboundEmailEnabled(t *testing.T) {
	data := []byte(`{"data":{"inbound_email_address":"jane.k3f9qzab@m.example.test","inbound_email_enabled":true}}`)
	out := captureStdout(t, func() { displayInboundEmail(data) })
	if !strings.Contains(out, "jane.k3f9qzab@m.example.test") {
		t.Errorf("address missing:\n%s", out)
	}
	if !strings.Contains(out, "On") {
		t.Errorf("on state missing:\n%s", out)
	}
	if !strings.Contains(out, "@Notebook") || !strings.Contains(out, "#tag") {
		t.Errorf("routing hint missing:\n%s", out)
	}
}

// Disabled does not mean gone: the address is still shown, with the state
// spelled out in words (the strikethrough alone would vanish with --no-color).
func TestDisplayInboundEmailDisabledStillShowsAddress(t *testing.T) {
	data := []byte(`{"data":{"inbound_email_address":"jane.k3f9qzab@m.example.test","inbound_email_enabled":false}}`)
	out := captureStdout(t, func() { displayInboundEmail(data) })
	if !strings.Contains(out, "jane.k3f9qzab@m.example.test") {
		t.Errorf("address must stay visible when off:\n%s", out)
	}
	if !strings.Contains(out, "Off") || !strings.Contains(out, "(off)") {
		t.Errorf("off state missing:\n%s", out)
	}
	if !strings.Contains(out, "dropped") {
		t.Errorf("dropped-mail explanation missing:\n%s", out)
	}
}

func TestDisplayInboundEmailUndisclosed(t *testing.T) {
	out := captureStdout(t, func() { displayInboundEmail([]byte(`{"data":{"id":"u1"}}`)) })
	if !strings.Contains(out, "harbor login") {
		t.Errorf("undisclosed explanation missing:\n%s", out)
	}
}

func TestDisplayInboundEmailReset(t *testing.T) {
	data := []byte(`{"data":{"inbound_email_address":"jane.9xq2mvbr@m.example.test"}}`)
	out := captureStdout(t, func() { displayInboundEmailReset(data) })
	if !strings.Contains(out, "jane.9xq2mvbr@m.example.test") {
		t.Errorf("new address missing:\n%s", out)
	}
	if !strings.Contains(out, "immediately") {
		t.Errorf("old-address warning missing:\n%s", out)
	}
	// A reset must not imply the switch moved.
	if !strings.Contains(out, "unchanged") {
		t.Errorf("unchanged-setting note missing:\n%s", out)
	}
}

func TestInboundEmailAddressDistinguishesAbsentFromEmpty(t *testing.T) {
	if _, ok := inboundEmailAddress(map[string]any{}); ok {
		t.Error("absent key reported as present")
	}
	addr, ok := inboundEmailAddress(map[string]any{"inbound_email_address": ""})
	if !ok || addr != "" {
		t.Errorf("empty-but-present = (%q, %v), want (\"\", true)", addr, ok)
	}
	if _, ok := inboundEmailAddress(nil); ok {
		t.Error("nil profile reported as present")
	}
}

// The rotation is destructive and irreversible, so it must refuse to run
// unattended (--json or a piped stdin) unless --yes was passed.
func TestInboundEmailConfirmReset(t *testing.T) {
	if err := inboundEmailConfirmReset(true); err != nil {
		t.Errorf("--yes should proceed, got %v", err)
	}

	jsonOutput = true
	defer func() { jsonOutput = false }()
	err := inboundEmailConfirmReset(false)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("--json without --yes = %v, want a --yes refusal", err)
	}
}

func TestMapProfileError(t *testing.T) {
	cases := map[string]string{
		"reauth_required":         "current password",
		"email_taken":             "already in use",
		"password_reused":         "differ",
		"inbound_email_forbidden": "harbor login",
	}
	for code, sub := range cases {
		if got := mapProfileError(apiErr(code)); !strings.Contains(got.Error(), sub) {
			t.Errorf("mapProfileError(%s) = %q", code, got.Error())
		}
	}
}

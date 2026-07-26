// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"io"
	"reflect"
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

// A disabled address must still be shown in the profile table — the primary
// acceptance criterion of the issue is that 'profile get' surfaces the address,
// and turning the switch off does not take the address away.
func TestDisplayProfileShowsDisabledInboundAddress(t *testing.T) {
	data := []byte(`{"data":{"id":"u1","name":"Jane","email":"jane@example.com","inbound_email_address":"jane.k3f9qzab@m.example.test","inbound_email_enabled":false,"created_at":1750000000000,"updated_at":1750000000000}}`)
	out := captureStdout(t, func() { displayProfile(data) })
	if !strings.Contains(out, "jane.k3f9qzab@m.example.test") {
		t.Errorf("address must stay visible in the profile table when off:\n%s", out)
	}
	if !strings.Contains(out, "(off)") {
		t.Errorf("off badge missing from the profile table:\n%s", out)
	}
}

func TestRenderInboundEmailCardEnabled(t *testing.T) {
	p := map[string]any{"inbound_email_address": "jane.k3f9qzab@m.example.test", "inbound_email_enabled": true}
	out := captureStdout(t, func() { renderInboundEmailCard(p) })
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
func TestRenderInboundEmailCardDisabledStillShowsAddress(t *testing.T) {
	p := map[string]any{"inbound_email_address": "jane.k3f9qzab@m.example.test", "inbound_email_enabled": false}
	out := captureStdout(t, func() { renderInboundEmailCard(p) })
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

// The gate — not a display function — is what decides that a response without
// the address is a failure, so nothing can print a card for one.
func TestInboundEmailProfileGate(t *testing.T) {
	if _, err := inboundEmailProfile([]byte(`{"data":{"id":"u1"}}`)); err == nil {
		t.Error("omitted keys must be an error, not a renderable card")
	} else if !strings.Contains(err.Error(), "harbor login") {
		t.Errorf("gate error = %q, want the sign-in explanation", err.Error())
	}
	p, err := inboundEmailProfile([]byte(`{"data":{"inbound_email_address":"jane.k3f9qzab@m.example.test","inbound_email_enabled":true}}`))
	if err != nil {
		t.Fatalf("disclosed profile rejected: %v", err)
	}
	// Data-wrapped, and unwrapped once — not the raw envelope.
	if got, _ := inboundEmailAddress(p); got != "jane.k3f9qzab@m.example.test" {
		t.Errorf("address = %q", got)
	}
}

func TestRenderInboundEmailReset(t *testing.T) {
	out := captureStdout(t, func() { renderInboundEmailReset("jane.9xq2mvbr@m.example.test") })
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

// The strikethrough is the web-parity affordance for "present but inactive".
// captureStdout forces color off (no TTY under `go test`), so it is only
// reachable with color explicitly forced on.
func TestStrikeAndOnOffEmitColor(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	noColorFlag = false
	colorReady = false
	t.Cleanup(func() { colorReady = false })

	if got := strike("jane.k3f9qzab@m.example.test"); !strings.Contains(got, "\x1b[9m") {
		t.Errorf("strike() emitted no crossed-out sequence: %q", got)
	}
	if got := onOff(true); !strings.Contains(got, "\x1b[32m") || !strings.Contains(got, "On") {
		t.Errorf("onOff(true) = %q, want green On", got)
	}
	if got := onOff(false); !strings.Contains(got, "\x1b[33m") || !strings.Contains(got, "Off") {
		t.Errorf("onOff(false) = %q, want yellow Off", got)
	}
	// A disabled address is struck AND badged, so the state survives either way.
	got := inboundAddressDisplay("jane.k3f9qzab@m.example.test", false)
	if !strings.Contains(got, "\x1b[9m") || !strings.Contains(got, "(off)") {
		t.Errorf("disabled address = %q, want struck through plus an (off) badge", got)
	}
	if plain := inboundAddressDisplay("jane.k3f9qzab@m.example.test", true); strings.Contains(plain, "\x1b[9m") {
		t.Errorf("an enabled address must not be struck through: %q", plain)
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

// The rotation is destructive and irreversible, so every branch of the gate is
// pinned — including the typed-wrong-answer one, which is only reachable because
// the prompt is injected.
func TestInboundEmailResetGuard(t *testing.T) {
	answers := func(reply string) func(string) (string, error) {
		return func(string) (string, error) { return reply, nil }
	}
	refuse := func(string) (string, error) {
		t.Error("the user must not be prompted in this case")
		return "", nil
	}

	cases := []struct {
		name        string
		jsonMode    bool
		interactive bool
		yes         bool
		ask         func(string) (string, error)
		wantErr     string // "" = must succeed
	}{
		{name: "--yes proceeds without asking", yes: true, interactive: true, ask: refuse},
		{name: "--yes proceeds unattended too", yes: true, ask: refuse},
		{name: "typed yes proceeds", interactive: true, ask: answers("yes")},
		{name: "typed no aborts", interactive: true, ask: answers("no"), wantErr: "aborted"},
		{name: "empty answer aborts", interactive: true, ask: answers(""), wantErr: "aborted"},
		{name: "anything but yes aborts", interactive: true, ask: answers("y"), wantErr: "aborted"},
		{name: "YES is not yes", interactive: true, ask: answers("YES"), wantErr: "aborted"},
		{name: "piped stdin refuses", ask: refuse, wantErr: "--yes"},
		{name: "--json refuses", jsonMode: true, interactive: true, ask: refuse, wantErr: "--yes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			// The gate prints its warning, so keep it off the test output.
			captureStdout(t, func() {
				err = inboundEmailResetGuard(tc.jsonMode, tc.interactive, tc.yes, tc.ask)
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want proceed, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got nil (the rotation would have run)", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}

	// A read failure at the prompt must abort, never fall through to rotating.
	captureStdout(t, func() {
		err := inboundEmailResetGuard(false, true, false, func(string) (string, error) {
			return "", io.ErrUnexpectedEOF
		})
		if err == nil {
			t.Error("a prompt read error must abort the rotation")
		}
	})
}

// ===========================================================================
// RunE wiring (the actual cobra commands, against a mock API)
// ===========================================================================

// inboundProfileBody is a GET/PUT /profile response for a client entitled to see
// the address.
func inboundProfileBody(addr string, enabled bool) string {
	return fmt.Sprintf(`{"data":{"id":"u1","name":"Jane","email":"jane@example.com","locale":"en","timezone":"UTC","inbound_email_address":%q,"inbound_email_enabled":%t,"created_at":1750000000000,"updated_at":1750000000000}}`, addr, enabled)
}

// undisclosedProfileBody is the same 200 response as seen by a client that may
// not see the address: the keys are absent, not empty or false.
const undisclosedProfileBody = `{"data":{"id":"u1","name":"Jane","email":"jane@example.com","locale":"en","timezone":"UTC","created_at":1750000000000,"updated_at":1750000000000}}`

func TestInboundEmailShowRunE(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/profile": {Status: 200, Body: inboundProfileBody("jane.k3f9qzab@m.example.test", true)},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "jane.k3f9qzab@m.example.test") || !strings.Contains(out, "On") {
		t.Errorf("card missing address/state:\n%s", out)
	}
	if want := []string{"GET /api/v1/profile"}; !reflect.DeepEqual(m.calls(), want) {
		t.Errorf("calls = %v, want %v", m.calls(), want)
	}
}

// A response the API withheld the address from is a failure, and it must be
// reported as one: non-zero exit, nothing on stdout claiming otherwise.
func TestInboundEmailShowRunEErrorsWhenUndisclosed(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/profile": {Status: 200, Body: undisclosedProfileBody},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "show")
	if err == nil {
		t.Fatal("show must fail when the address was not disclosed")
	}
	if !errors.Is(err, errInboundEmailUndisclosed) {
		t.Errorf("err = %v, want errInboundEmailUndisclosed", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("nothing should reach stdout on failure:\n%s", out)
	}
}

func TestInboundEmailDisableRunESendsOnlyTheSwitch(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PUT /api/v1/profile": {Status: 200, Body: inboundProfileBody("jane.k3f9qzab@m.example.test", false)},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "disable")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	body := m.bodyOf(t, "PUT /api/v1/profile")
	if len(body) != 1 || body["inbound_email_enabled"] != false {
		t.Errorf("body = %v, want only inbound_email_enabled=false", body)
	}
	if !strings.Contains(out, "Off") || !strings.Contains(out, "jane.k3f9qzab@m.example.test") {
		t.Errorf("disabled card missing state/address:\n%s", out)
	}
}

func TestInboundEmailEnableRunESendsOnlyTheSwitch(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PUT /api/v1/profile": {Status: 200, Body: inboundProfileBody("jane.k3f9qzab@m.example.test", true)},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "enable")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	body := m.bodyOf(t, "PUT /api/v1/profile")
	if len(body) != 1 || body["inbound_email_enabled"] != true {
		t.Errorf("body = %v, want only inbound_email_enabled=true", body)
	}
	if !strings.Contains(out, "On") {
		t.Errorf("enabled card missing state:\n%s", out)
	}
}

// A mutating command that did not mutate must not exit 0. Before this gate the
// notice went to stdout and the exit code said success, so
// `harbor profile inbound-email disable >/dev/null 2>&1 && echo off` printed
// "off" while the address was still live.
func TestInboundEmailSetRunEErrorsWhenUndisclosed(t *testing.T) {
	for _, sub := range []string{"enable", "disable"} {
		t.Run(sub, func(t *testing.T) {
			m := newAPIMock(t, map[string]mockReply{
				"PUT /api/v1/profile": {Status: 200, Body: undisclosedProfileBody},
			})
			out, err := runCLI(t, m, "profile", "inbound-email", sub)
			if err == nil {
				t.Fatalf("%s must fail when the response carries no address", sub)
			}
			if !errors.Is(err, errInboundEmailUndisclosed) {
				t.Errorf("err = %v, want errInboundEmailUndisclosed", err)
			}
			if strings.TrimSpace(out) != "" {
				t.Errorf("the failure must not be printed to stdout:\n%s", out)
			}
		})
	}
}

// The confirmation gate must be wired into the command, not merely exist: with
// no --yes and no terminal, the rotation must never reach the server.
func TestInboundEmailResetRunERequiresConfirmation(t *testing.T) {
	for _, args := range [][]string{
		{"profile", "inbound-email", "reset"},
		{"profile", "inbound-email", "reset", "--json"},
	} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			m := newAPIMock(t, map[string]mockReply{
				"POST /api/v1/profile/inbound-email/reset": {Status: 200, Body: `{"data":{"inbound_email_address":"jane.SHOULD-NOT-HAPPEN@m.example.test"}}`},
			})
			_, err := runCLI(t, m, args...)
			if err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("err = %v, want a --yes refusal", err)
			}
			if len(m.calls()) != 0 {
				t.Errorf("the rotation must not be attempted: %v", m.calls())
			}
		})
	}
}

// Contract rule: rotating does not re-enable a disabled address. The only
// request may be the rotation itself — no PUT tagging along.
func TestInboundEmailResetRunEDoesNotReEnable(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/profile/inbound-email/reset": {Status: 200, Body: `{"data":{"inbound_email_address":"jane.9xq2mvbr@m.example.test"}}`},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "reset", "--yes")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	want := []string{"POST /api/v1/profile/inbound-email/reset"}
	if !reflect.DeepEqual(m.calls(), want) {
		t.Errorf("calls = %v, want exactly %v (a PUT would flip the switch back on)", m.calls(), want)
	}
	if m.requests[0].Body != "" {
		t.Errorf("reset body = %q, want empty", m.requests[0].Body)
	}
	if !strings.Contains(out, "jane.9xq2mvbr@m.example.test") || !strings.Contains(out, "unchanged") {
		t.Errorf("reset output missing address/unchanged note:\n%s", out)
	}
}

// The worst failure for a non-undoable command: claiming success while
// withholding the replacement. The old address is already dead here, so this
// must exit non-zero and point at 'show'.
func TestInboundEmailResetRunEErrorsWhenAddressMissing(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/profile/inbound-email/reset": {Status: 200, Body: `{"data":{}}`},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "reset", "--yes")
	if err == nil {
		t.Fatal("a reset that returns no address must fail")
	}
	if !errors.Is(err, errInboundEmailResetUnreadable) {
		t.Errorf("err = %v, want errInboundEmailResetUnreadable", err)
	}
	if !strings.Contains(err.Error(), "show") {
		t.Errorf("err = %q, want it to point at 'inbound-email show'", err.Error())
	}
	if strings.Contains(out, "rotated") {
		t.Errorf("must not print a success line it cannot substantiate:\n%s", out)
	}
}

// --json still echoes the raw response so a caller keeps whatever came back,
// while the command itself fails.
func TestInboundEmailResetRunEJSONEchoesResponseAndStillFails(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"POST /api/v1/profile/inbound-email/reset": {Status: 200, Body: `{"data":{"rotated":true}}`},
	})
	out, err := runCLI(t, m, "profile", "inbound-email", "reset", "--yes", "--json")
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(out, "rotated") {
		t.Errorf("--json should still emit the raw response:\n%s", out)
	}
}

// Every inbound-email command maps the domain's 403 to the friendly explanation
// rather than surfacing the API's wording.
func TestInboundEmailRunEsMapForbidden(t *testing.T) {
	forbidden := mockReply{Status: 403, Body: apiErrorBody("inbound_email_forbidden", "This client may not read the inbound email address.")}
	cases := []struct {
		name   string
		args   []string
		routes map[string]mockReply
	}{
		{"show", []string{"profile", "inbound-email", "show"}, map[string]mockReply{"GET /api/v1/profile": forbidden}},
		{"enable", []string{"profile", "inbound-email", "enable"}, map[string]mockReply{"PUT /api/v1/profile": forbidden}},
		{"disable", []string{"profile", "inbound-email", "disable"}, map[string]mockReply{"PUT /api/v1/profile": forbidden}},
		{"reset", []string{"profile", "inbound-email", "reset", "--yes"}, map[string]mockReply{"POST /api/v1/profile/inbound-email/reset": forbidden}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newAPIMock(t, tc.routes)
			_, err := runCLI(t, m, tc.args...)
			if err == nil {
				t.Fatal("a 403 must fail")
			}
			if err.Error() != errInboundEmailUndisclosed.Error() {
				t.Errorf("err = %q, want the mapped explanation %q", err.Error(), errInboundEmailUndisclosed.Error())
			}
		})
	}
}

// 'profile get' is where the issue's primary acceptance criterion lives, so pin
// it end to end in both states.
func TestProfileGetRunEShowsInboundRows(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			m := newAPIMock(t, map[string]mockReply{
				"GET /api/v1/profile": {Status: 200, Body: inboundProfileBody("jane.k3f9qzab@m.example.test", enabled)},
			})
			out, err := runCLI(t, m, "profile", "get")
			if err != nil {
				t.Fatalf("profile get: %v", err)
			}
			if !strings.Contains(out, "Inbound email") || !strings.Contains(out, "jane.k3f9qzab@m.example.test") {
				t.Errorf("inbound rows missing:\n%s", out)
			}
			if !enabled && !strings.Contains(out, "(off)") {
				t.Errorf("disabled address should carry the (off) badge:\n%s", out)
			}
		})
	}
}

// A client that may not see the address gets a profile table with no inbound
// rows at all — 'profile get' is not gated, it just shows what it was given.
func TestProfileGetRunEOmitsInboundRowsWhenUndisclosed(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/profile": {Status: 200, Body: undisclosedProfileBody},
	})
	out, err := runCLI(t, m, "profile", "get")
	if err != nil {
		t.Fatalf("profile get: %v", err)
	}
	if strings.Contains(out, "Inbound email") {
		t.Errorf("inbound rows must be absent, never rendered as off:\n%s", out)
	}
	if !strings.Contains(out, "jane@example.com") {
		t.Errorf("the rest of the profile should still render:\n%s", out)
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

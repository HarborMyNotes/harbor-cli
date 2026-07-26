// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"testing"
)

func TestGetProfile(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"id":"u1"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetProfile(); err != nil {
		t.Fatalf("GetProfile error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/profile" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
}

func TestUpdateProfileUsesPUT(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"id":"u1"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).UpdateProfile(map[string]any{"name": "Jane"}); err != nil {
		t.Fatalf("UpdateProfile error: %v", err)
	}
	if rec.Method != "PUT" || rec.Path != "/profile" {
		t.Errorf("%s %s, want PUT /profile", rec.Method, rec.Path)
	}
}

func TestChangePassword(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"changed":true}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ChangePassword("old", "new"); err != nil {
		t.Fatalf("ChangePassword error: %v", err)
	}
	if rec.Path != "/profile/change-password" {
		t.Errorf("path = %s", rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["current_password"] != "old" || body["new_password"] != "new" {
		t.Errorf("body = %v", body)
	}
}

func TestSetInboundEmailEnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var rec recordedRequest
		srv := newTestServer(t, &rec, 200, `{"data":{"id":"u1","inbound_email_address":"jane.k3f9qzab@m.example.test","inbound_email_enabled":true}}`)
		if _, err := testClient(srv.URL).SetInboundEmailEnabled(enabled); err != nil {
			t.Fatalf("SetInboundEmailEnabled(%v) error: %v", enabled, err)
		}
		srv.Close()
		if rec.Method != "PUT" || rec.Path != "/profile" {
			t.Errorf("%s %s, want PUT /profile", rec.Method, rec.Path)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body, &body)
		// Only the switch is sent — a toggle must never restate other fields.
		if len(body) != 1 || body["inbound_email_enabled"] != enabled {
			t.Errorf("body = %v, want only inbound_email_enabled=%v", body, enabled)
		}
	}
}

func TestResetInboundEmail(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"inbound_email_address":"jane.9xq2mvbr@m.example.test"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).ResetInboundEmail(); err != nil {
		t.Fatalf("ResetInboundEmail error: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/profile/inbound-email/reset" {
		t.Errorf("%s %s, want POST /profile/inbound-email/reset", rec.Method, rec.Path)
	}
	// The endpoint takes no body at all.
	if len(rec.Body) != 0 {
		t.Errorf("body = %q, want empty", string(rec.Body))
	}
}

func TestAvatarAndConfirmEmail(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{}}`)
	defer srv.Close()
	c := testClient(srv.URL)

	if _, err := c.SetAvatar("abc123"); err != nil {
		t.Fatalf("SetAvatar: %v", err)
	}
	if rec.Path != "/profile/avatar" {
		t.Errorf("set-avatar path = %s", rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["hash"] != "abc123" {
		t.Errorf("avatar body = %v (want hash)", body)
	}

	if _, err := c.RemoveAvatar(); err != nil {
		t.Fatalf("RemoveAvatar: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/profile/avatar" {
		t.Errorf("remove-avatar = %s %s", rec.Method, rec.Path)
	}

	if _, err := c.ConfirmEmailChange("ec_1"); err != nil {
		t.Fatalf("ConfirmEmailChange: %v", err)
	}
	if rec.Path != "/profile/email/confirm" {
		t.Errorf("confirm-email path = %s", rec.Path)
	}
}

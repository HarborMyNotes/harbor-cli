// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// settingsTestCmd builds a fresh command carrying the same flag set as
// 'settings set', so the partial-body builder can be exercised in isolation
// (without mutating the shared global command between tests).
func settingsTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "set"}
	c.Flags().String("theme", "", "")
	c.Flags().String("default-notebook", "", "")
	c.Flags().Bool("clear-default-notebook", false, "")
	c.Flags().String("default-sort", "", "")
	c.Flags().String("locale", "", "")
	c.Flags().String("timezone", "", "")
	c.Flags().Int("editor-font-size", 0, "")
	c.Flags().String("editor-font-family", "", "")
	c.Flags().Bool("editor-spellcheck", false, "")
	c.Flags().Int("editor-autosave", 0, "")
	c.Flags().Bool("editor-show-word-count", false, "")
	c.Flags().Bool("editor-show-list-guides", false, "")
	c.Flags().Bool("email-reminders", false, "")
	c.Flags().Bool("email-product-news", false, "")
	c.Flags().Bool("push-reminders", false, "")
	return c
}

// TestSettingsBuildSetBodyPartial verifies that 'set' sends ONLY the flags the
// user actually changed, and that the nested objects carry ONLY their changed
// sub-fields (so the server deep-merge cannot clobber untouched preferences).
func TestSettingsBuildSetBodyPartial(t *testing.T) {
	cmd := settingsTestCmd()
	// Change one top-level scalar and one editor sub-field only.
	if err := cmd.Flags().Set("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("editor-font-size", "18"); err != nil {
		t.Fatal(err)
	}

	body := settingsBuildSetBody(cmd)

	// Exactly two top-level keys: theme and the editor_prefs object.
	if len(body) != 2 {
		t.Fatalf("body has %d top-level keys, want 2: %v", len(body), body)
	}
	if body["theme"] != "dark" {
		t.Errorf("theme = %v, want dark", body["theme"])
	}
	// notification_prefs must be entirely absent (no notification flag changed).
	if _, ok := body["notification_prefs"]; ok {
		t.Errorf("notification_prefs should be absent, body = %v", body)
	}
	// Untouched scalars must be absent too.
	for _, k := range []string{"locale", "timezone", "default_sort", "default_notebook_id"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s should be absent, body = %v", k, body)
		}
	}
	// editor_prefs must contain ONLY font_size — not the other four sub-fields.
	editor, ok := body["editor_prefs"].(map[string]any)
	if !ok {
		t.Fatalf("editor_prefs missing/not an object: %v", body["editor_prefs"])
	}
	if len(editor) != 1 || editor["font_size"] != 16+2 {
		t.Errorf("editor_prefs = %v, want only {font_size:18}", editor)
	}
	for _, k := range []string{"font_family", "spellcheck", "autosave_seconds", "show_word_count", "show_list_guides"} {
		if _, ok := editor[k]; ok {
			t.Errorf("editor_prefs.%s should be absent, got %v", k, editor)
		}
	}
}

// TestSettingsBuildSetBodyNotificationSubset verifies a single notification flag
// builds a notification_prefs object holding only that one sub-field.
func TestSettingsBuildSetBodyNotificationSubset(t *testing.T) {
	cmd := settingsTestCmd()
	if err := cmd.Flags().Set("email-product-news", "true"); err != nil {
		t.Fatal(err)
	}
	body := settingsBuildSetBody(cmd)

	notif, ok := body["notification_prefs"].(map[string]any)
	if !ok {
		t.Fatalf("notification_prefs missing: %v", body)
	}
	if len(notif) != 1 || notif["email_product_news"] != true {
		t.Errorf("notification_prefs = %v, want only {email_product_news:true}", notif)
	}
	if _, ok := body["editor_prefs"]; ok {
		t.Errorf("editor_prefs should be absent, body = %v", body)
	}
}

// TestSettingsBuildSetBodyClearDefaultNotebook verifies --clear-default-notebook
// puts an explicit nil (JSON null) under default_notebook_id.
func TestSettingsBuildSetBodyClearDefaultNotebook(t *testing.T) {
	cmd := settingsTestCmd()
	if err := cmd.Flags().Set("clear-default-notebook", "true"); err != nil {
		t.Fatal(err)
	}
	body := settingsBuildSetBody(cmd)

	val, present := body["default_notebook_id"]
	if !present {
		t.Fatalf("default_notebook_id should be present (as null), body = %v", body)
	}
	if val != nil {
		t.Errorf("default_notebook_id = %v, want explicit nil", val)
	}
}

// TestSettingsBuildSetBodyAdoptDefaultNotebook verifies --default-notebook sets
// the id string.
func TestSettingsBuildSetBodyAdoptDefaultNotebook(t *testing.T) {
	cmd := settingsTestCmd()
	if err := cmd.Flags().Set("default-notebook", "nb-1"); err != nil {
		t.Fatal(err)
	}
	body := settingsBuildSetBody(cmd)
	if body["default_notebook_id"] != "nb-1" {
		t.Errorf("default_notebook_id = %v, want nb-1", body["default_notebook_id"])
	}
}

// TestSettingsBuildSetBodyEmpty verifies that passing no flags yields an empty
// body (the command turns this into a friendly "nothing to update" error).
func TestSettingsBuildSetBodyEmpty(t *testing.T) {
	if body := settingsBuildSetBody(settingsTestCmd()); len(body) != 0 {
		t.Errorf("empty flags should yield empty body, got %v", body)
	}
}

// TestDisplaySettings verifies the detail view renders top-level scalars, the
// grouped nested preferences, and a placeholder when no default notebook is set.
func TestDisplaySettings(t *testing.T) {
	data := []byte(`{"data":{
		"theme":"dark",
		"default_notebook_id":null,
		"default_sort":"-updated_at",
		"locale":"en-US",
		"timezone":"UTC",
		"notification_prefs":{"email_reminders":true,"email_product_news":false,"email_security":true,"push_reminders":true},
		"editor_prefs":{"font_size":18,"font_family":"mono","spellcheck":true,"autosave_seconds":5,"show_word_count":false},
		"updated_at":1750000000000
	}}`)
	out := captureStdout(t, func() { displaySettings(data) })

	for _, want := range []string{"dark", "(none)", "Notifications", "Editor", "mono", "18"} {
		if !strings.Contains(out, want) {
			t.Errorf("display missing %q:\n%s", want, out)
		}
	}
}

// TestSettingsSetListGuidesWireBody runs the real 'settings set' against a mock
// API and pins the exact PUT body, so the flag name, the wire key, and the
// partial-update shape are all checked together.
func TestSettingsSetListGuidesWireBody(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bare flag turns it on", []string{"--editor-show-list-guides"}, `{"editor_prefs":{"show_list_guides":true}}`},
		{"explicit true", []string{"--editor-show-list-guides=true"}, `{"editor_prefs":{"show_list_guides":true}}`},
		{"explicit false sends false", []string{"--editor-show-list-guides=false"}, `{"editor_prefs":{"show_list_guides":false}}`},
		{"not passed sends nothing for it", []string{"--theme", "dark"}, `{"theme":"dark"}`},
		{"other editor pref leaves it out", []string{"--editor-show-word-count"}, `{"editor_prefs":{"show_word_count":true}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newAPIMock(t, map[string]mockReply{
				"PUT /api/v1/settings": {Status: 200, Body: `{"data":{"editor_prefs":{}}}`},
			})
			args := append([]string{"settings", "set"}, tc.args...)
			if _, err := runCLI(t, m, args...); err != nil {
				t.Fatalf("settings set: %v", err)
			}
			if got := m.rawBodyOf(t, "PUT /api/v1/settings"); got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestDisplaySettingsListGuides verifies the "Show list guide lines" row follows
// the stored value: a check mark when on, the dim dot when off.
func TestDisplaySettingsListGuides(t *testing.T) {
	cases := []struct {
		name   string
		editor string
		mark   string
	}{
		{"on", `{"show_list_guides":true}`, "✓"},
		{"off", `{"show_list_guides":false}`, "·"},
		{"missing reads as off", `{}`, "·"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`{"data":{"theme":"system","editor_prefs":` + tc.editor + `}}`)
			out := captureStdout(t, func() { displaySettings(data) })
			line := ""
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "Show list guide lines") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("no \"Show list guide lines\" row:\n%s", out)
			}
			if !strings.Contains(line, tc.mark) {
				t.Errorf("row = %q, want mark %q", line, tc.mark)
			}
		})
	}
}

// TestDisplaySettingsWithDefaultNotebook verifies the default notebook id shows
// when one is set.
func TestDisplaySettingsWithDefaultNotebook(t *testing.T) {
	data := []byte(`{"data":{"theme":"system","default_notebook_id":"nb-42","notification_prefs":{},"editor_prefs":{},"updated_at":null}}`)
	out := captureStdout(t, func() { displaySettings(data) })
	if !strings.Contains(out, "nb-42") {
		t.Errorf("default notebook id missing:\n%s", out)
	}
	// updated_at null should render as the em dash, not "0".
	if strings.Contains(out, "Updated") && !strings.Contains(out, "—") {
		t.Errorf("null updated_at should render as em dash:\n%s", out)
	}
}

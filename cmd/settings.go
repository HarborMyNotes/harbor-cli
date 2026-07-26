// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// settingsCmd is the parent for account-level user preferences (theme, default
// notebook, sort, locale, timezone, notification and editor preferences).
var settingsCmd = &cobra.Command{
	Use:     "settings",
	Aliases: []string{"prefs", "preferences"},
	Short:   "View and update your account preferences",
	GroupID: groupAccount,
	Long: `View and update your account-level preferences.

Settings cover your theme, default notebook, default note sort, locale, timezone,
notification preferences, and editor preferences. They are account metadata (not
note data) and are NOT part of the sync stream — last write wins across devices.

'set' is a partial update: only the flags you actually pass are sent, and the
server deep-merges the nested notification/editor preferences, so you can change
a single field without disturbing the rest.`,
}

// settingsGetCmd shows the current effective settings.
var settingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show your current settings",
	Long:  "Show the effective settings for your account (stored values overlaid on the built-in defaults, so the view is always complete even before your first change).",
	Example: `  harbor settings get
  harbor settings get --json | jq .data.theme`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetSettings()
		if err != nil {
			return err
		}
		printResult(data, displaySettings)
		return nil
	},
}

// settingsSetCmd applies a partial update. Only the flags the user explicitly
// set are sent; the nested notification_prefs / editor_prefs objects carry ONLY
// their changed sub-fields and the server deep-merges them.
var settingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Update settings (only the flags you pass are changed)",
	Long: `Update your account preferences. Only the flags you pass are modified.

The nested notification and editor preferences are deep-merged on the server, so
changing one (for example --editor-font-size) leaves every other preference
untouched.

The default notebook is handled three ways:
  --default-notebook <id>     adopt that notebook (it must exist)
  --clear-default-notebook    clear it (no default notebook)
  (neither)                   leave it unchanged

The security-email preference cannot be disabled, so there is no flag for it.`,
	Example: `  harbor settings set --theme dark
  harbor settings set --editor-font-size 18 --editor-show-word-count
  harbor settings set --default-notebook 5b1f2c9a-... --default-sort title
  harbor settings set --clear-default-notebook --email-product-news=false`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		body := settingsBuildSetBody(cmd)
		if len(body) == 0 {
			return errors.New("nothing to update — pass at least one flag (see 'harbor settings set --help')")
		}
		data, err := c.UpdateSettings(body)
		if err != nil {
			return err
		}
		printResult(data, displaySettings)
		return nil
	},
}

// settingsBuildSetBody assembles the partial PUT body for 'settings set' from
// the flags the user explicitly changed. Top-level scalars are copied straight
// through; the nested notification_prefs / editor_prefs objects are included
// only when at least one of their sub-fields changed (and then carry ONLY those
// changed sub-fields, so the server deep-merge never clobbers untouched prefs).
func settingsBuildSetBody(cmd *cobra.Command) map[string]any {
	body := map[string]any{}

	// Top-level scalars — each is sent only when explicitly set.
	addStringIfChanged(cmd, body, "theme", "theme")
	addStringIfChanged(cmd, body, "default-sort", "default_sort")
	addStringIfChanged(cmd, body, "locale", "locale")
	addStringIfChanged(cmd, body, "timezone", "timezone")

	// default_notebook_id is nullable: --default-notebook adopts an id while
	// --clear-default-notebook sends an explicit JSON null. They are mutually
	// exclusive (enforced via MarkFlagsMutuallyExclusive in init).
	if cmd.Flags().Changed("default-notebook") {
		body["default_notebook_id"] = stringFlag(cmd, "default-notebook")
	}
	if boolFlag(cmd, "clear-default-notebook") {
		body["default_notebook_id"] = nil
	}

	// notification_prefs — build a nested object containing only the changed
	// sub-fields. email_security is intentionally unsupported (the server coerces
	// it back to true), so it has no flag.
	notif := map[string]any{}
	addBoolIfChanged(cmd, notif, "email-reminders", "email_reminders")
	addBoolIfChanged(cmd, notif, "email-product-news", "email_product_news")
	addBoolIfChanged(cmd, notif, "push-reminders", "push_reminders")
	if len(notif) > 0 {
		body["notification_prefs"] = notif
	}

	// editor_prefs — same partial-nested pattern.
	editor := map[string]any{}
	addIntIfChanged(cmd, editor, "editor-font-size", "font_size")
	addStringIfChanged(cmd, editor, "editor-font-family", "font_family")
	addBoolIfChanged(cmd, editor, "editor-spellcheck", "spellcheck")
	addIntIfChanged(cmd, editor, "editor-autosave", "autosave_seconds")
	addBoolIfChanged(cmd, editor, "editor-show-word-count", "show_word_count")
	if len(editor) > 0 {
		body["editor_prefs"] = editor
	}

	return body
}

// ===========================================================================
// Display
// ===========================================================================

// displaySettings renders the full effective settings as a grouped key/value
// detail view (top-level scalars, then notification and editor preferences).
func displaySettings(data []byte) {
	s := parseJSON(client.UnwrapData(data))
	if s == nil {
		fmt.Println(string(data))
		return
	}

	pairs := [][2]string{
		{"Theme", str(s, "theme")},
		{"Default notebook", settingsDefaultNotebook(s)},
		{"Default sort", str(s, "default_sort")},
		{"Locale", str(s, "locale")},
		{"Timezone", str(s, "timezone")},
	}

	// Notification preferences (nested).
	if n := nested(s, "notification_prefs"); n != nil {
		pairs = append(pairs,
			[2]string{dim("Notifications"), ""},
			[2]string{"  Email reminders", boolMark(boolean(n, "email_reminders"))},
			[2]string{"  Email product news", boolMark(boolean(n, "email_product_news"))},
			[2]string{"  Email security", boolMark(boolean(n, "email_security"))},
			[2]string{"  Push reminders", boolMark(boolean(n, "push_reminders"))},
		)
	}

	// Editor preferences (nested).
	if e := nested(s, "editor_prefs"); e != nil {
		pairs = append(pairs,
			[2]string{dim("Editor"), ""},
			[2]string{"  Font size", str(e, "font_size")},
			[2]string{"  Font family", str(e, "font_family")},
			[2]string{"  Spellcheck", boolMark(boolean(e, "spellcheck"))},
			[2]string{"  Autosave (seconds)", str(e, "autosave_seconds")},
			[2]string{"  Show word count", boolMark(boolean(e, "show_word_count"))},
		)
	}

	pairs = append(pairs, [2]string{"Updated", epochMS(num(s, "updated_at"))})
	printKV(pairs)
}

// settingsDefaultNotebook renders the default notebook id, or a dim placeholder
// when none is set (the JSON value is null).
func settingsDefaultNotebook(s map[string]any) string {
	if id := str(s, "default_notebook_id"); id != "" {
		return bold(id)
	}
	return dim("(none)")
}

func init() {
	// set — top-level scalar flags.
	settingsSetCmd.Flags().String("theme", "", "Color theme: system, light, or dark")
	settingsSetCmd.Flags().String("default-notebook", "", "Set the default notebook by id")
	settingsSetCmd.Flags().Bool("clear-default-notebook", false, "Clear the default notebook (no default)")
	settingsSetCmd.Flags().String("default-sort", "", "Default note sort: -updated_at, updated_at, -created_at, created_at, title, or -title")
	settingsSetCmd.Flags().String("locale", "", "Locale as a BCP-47 tag (e.g. en-US)")
	settingsSetCmd.Flags().String("timezone", "", "IANA timezone (e.g. America/New_York)")

	// set — editor preferences.
	settingsSetCmd.Flags().Int("editor-font-size", 0, "Editor font size (10–32)")
	settingsSetCmd.Flags().String("editor-font-family", "", "Editor font family: sans, serif, or mono")
	settingsSetCmd.Flags().Bool("editor-spellcheck", false, "Enable editor spellcheck")
	settingsSetCmd.Flags().Int("editor-autosave", 0, "Editor autosave interval in seconds (1–60)")
	settingsSetCmd.Flags().Bool("editor-show-word-count", false, "Show the editor word count")

	// set — notification preferences (email_security is omitted on purpose).
	settingsSetCmd.Flags().Bool("email-reminders", false, "Email me about reminders")
	settingsSetCmd.Flags().Bool("email-product-news", false, "Email me product news (opt-in)")
	settingsSetCmd.Flags().Bool("push-reminders", false, "Push-notify me about reminders")

	// Adopting and clearing the default notebook are contradictory.
	settingsSetCmd.MarkFlagsMutuallyExclusive("default-notebook", "clear-default-notebook")

	settingsCmd.AddCommand(settingsGetCmd, settingsSetCmd)
	rootCmd.AddCommand(settingsCmd)
}

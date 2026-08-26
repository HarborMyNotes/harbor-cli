// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// templatesCmd is the parent for note-template management. Templates are
// reusable note "starting points"; the apply subcommand materializes a fresh
// note from one.
var templatesCmd = &cobra.Command{
	Use:     "templates",
	Aliases: []string{"template", "tpl"},
	Short:   "Manage note templates (list, get, create, update, delete, apply)",
	GroupID: groupContent,
	Long: `Note templates are reusable starting points for notes. Bodies accept
Markdown (default) or HTML via --format, supplied with --content, --file, or
piped via --stdin. Use 'apply' to instantiate a fresh note from a template.

Built-in (system) templates are read-only: they cannot be updated or deleted.`,
}

// templatesListCmd lists templates.
var templatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List note templates",
	Example: `  harbor templates list
  harbor templates list --include-system=false --order -usn
  harbor templates list --json | jq '.data[] | {id, name}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		// include_system defaults to true on the server; only send it when the
		// user explicitly flipped it so server defaults otherwise apply.
		if cmd.Flags().Changed("include-system") {
			params["include_system"] = boolStr(boolFlag(cmd, "include-system"))
		}
		if boolFlag(cmd, "include-deleted") {
			params["include_deleted"] = "true"
		}
		data, err := c.ListTemplates(params)
		if err != nil {
			return err
		}
		printResult(data, displayTemplates)
		return nil
	},
}

// templatesGetCmd fetches a single template (including its content).
var templatesGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Get a template by id",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor templates get 3c4d5e6f-...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetTemplate(args[0], boolFlag(cmd, "include-deleted"))
		if err != nil {
			return err
		}
		printResult(data, displayTemplate)
		return nil
	},
}

// templatesCreateCmd creates a user template.
var templatesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a note template",
	Long: `Create a template. Provide the body with --content, --file, or --stdin
(Markdown by default; --format html for HTML).

--notebook sets the template's DEFAULT notebook — where a note made from it is
filed when the apply call names no notebook of its own. It must be a live
notebook that is not encrypt-by-default, since a plaintext template can never be
materialized into an encrypted notebook. --tags are the tags a note made from
this template gets.

The body may contain {{date}}-style variables; the server expands them when the
template is applied (see 'harbor templates apply').`,
	Example: `  harbor templates create --name "Meeting notes" --content "# Meeting\n\nAttendees:"
  echo "# Standup" | harbor templates create --name Standup --stdin
  harbor templates create --name Recipe --file recipe.md --format markdown
  harbor templates create --name Standup --content "# {{date}}" --notebook 5b1f... --tags 7e1d...,9a2c...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		name := stringFlag(cmd, "name")
		if name == "" {
			return errors.New("--name is required")
		}
		content, format, hasContent, err := readContent(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{"name": name}
		if hasContent {
			body["content"] = content
			body["content_format"] = format
		}
		addStringIfChanged(cmd, body, "notebook", "notebook_id")
		addCSVIfChanged(cmd, body, "tags", "tag_ids")
		data, err := c.CreateTemplate(body)
		if err != nil {
			return mapTemplateError(err)
		}
		printResult(data, displayTemplate)
		return nil
	},
}

// templatesUpdateCmd partially updates a user template.
var templatesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a template (only the flags you pass are changed)",
	Args:  cobra.ExactArgs(1),
	Long: `Update a template. Only the fields you pass are modified. Built-in
(system) templates are read-only and cannot be updated.

--notebook and --tags follow that rule too: leave one off and the stored value
is preserved, or pass an empty value to clear it.`,
	Example: `  harbor templates update 3c4d... --name "Meeting notes (v2)"
  harbor templates update 3c4d... --file updated.md
  harbor templates update 3c4d... --notebook 5b1f... --tags 7e1d...,9a2c...
  harbor templates update 3c4d... --notebook "" --tags ""   # clear both`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		content, format, hasContent, err := readContent(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "name", "name")
		if hasContent {
			body["content"] = content
			body["content_format"] = format
		}
		addStringIfChanged(cmd, body, "notebook", "notebook_id")
		addCSVIfChanged(cmd, body, "tags", "tag_ids")
		if len(body) == 0 {
			return errors.New("nothing to update — pass --name, --notebook, --tags, or content")
		}
		data, err := c.UpdateTemplate(args[0], body)
		if err != nil {
			return mapTemplateError(err)
		}
		printResult(data, displayTemplate)
		return nil
	},
}

// templatesDeleteCmd deletes (tombstones) a user template.
var templatesDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a template",
	Args:    cobra.ExactArgs(1),
	Long:    "Tombstone a user template so it syncs as a deletion. Built-in (system) templates are read-only and cannot be deleted.",
	Example: "  harbor templates delete 3c4d...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.DeleteTemplate(args[0]); err != nil {
			return mapTemplateError(err)
		}
		fmt.Println("Template deleted.")
		return nil
	},
}

// templatesApplyCmd instantiates a new note from a template.
var templatesApplyCmd = &cobra.Command{
	Use:   "apply <id>",
	Short: "Create a new note from a template",
	Args:  cobra.ExactArgs(1),
	Long: `Instantiate a new note from a template. The server expands the template's
{{date}}-style variables into the title and body as it goes, so what comes back
is already filled in. The title defaults to the template name, and the notebook
to the template's default and then to yours, unless overridden.

--tags REPLACES the template's tags rather than adding to them: leave it off and
the note inherits whatever the template carries, pass a list and the note gets
exactly that list, or pass an empty value for no tags at all.

A notebook YOU name with --notebook is rejected if it is encrypt-by-default —
fetch the template, encrypt locally, and create the note via 'harbor notes
create'. A notebook the TEMPLATE remembers is tolerated instead: if it has since
been deleted or turned encrypt-by-default, the note goes to your default
notebook and the server prints a line saying so.`,
	Example: `  harbor templates apply 3c4d...
  harbor templates apply 3c4d... --title "Standup 2026-06-22" --notebook 5b1f...
  harbor templates apply 3c4d... --tags 7e1d...,9a2c...
  harbor templates apply 3c4d... --tags ""   # drop the template's tags`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "notebook", "notebook_id")
		addStringIfChanged(cmd, body, "title", "title")
		// Sent wins, omitted inherits: only a --tags the user actually typed is
		// forwarded, so leaving it off still inherits the template's tags while
		// an empty value clears them.
		addCSVIfChanged(cmd, body, "tags", "tags")
		data, err := c.ApplyTemplate(args[0], body)
		if err != nil {
			return mapTemplateError(err)
		}
		printResult(data, displayAppliedNote)
		return nil
	},
}

// displayAppliedNote renders the note an apply produced, then the server's
// advisory line.
//
// The notice explains a silent substitution — the template's notebook was gone
// or encrypt-by-default, so the note was filed in the account default instead.
// Without it the user simply finds their note somewhere unexpected. It is
// server-owned wording and is printed verbatim, never parsed or reworded.
func displayAppliedNote(data []byte) {
	displayNote(data)
	if notice := str(parseJSON(client.UnwrapData(data)), "notice"); notice != "" {
		fmt.Println()
		fmt.Println(notice)
	}
}

// valueOrDash renders an empty field as an em dash, so an unset default
// notebook reads as "nothing set" rather than a blank the eye skips.
func valueOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// countOrDash renders a zero count as an em dash, matching valueOrDash so an
// empty column never looks like a rendering fault.
func countOrDash(n int) string {
	if n == 0 {
		return "—"
	}
	return strconv.Itoa(n)
}

// mapTemplateError gives friendly messages for the template-specific codes while
// keeping the typed *client.APIError, so the display layer can still render the
// code line, the detail bullets and --verbose's http/request_id, and --json
// still reports the code the server sent rather than a generic cli_error.
//
// Only the message is swapped; every other field is carried through, and a code
// this does not recognise passes through untouched — which is what keeps the
// plan-limit walkthrough and its exit code working here.
func mapTemplateError(err error) error {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	var friendly string
	switch apiErr.Code {
	case "system_template_readonly":
		friendly = "this is a built-in (system) template — it cannot be edited or deleted"
	case "validation_failed":
		// A notebook that is missing, foreign, tombstoned or encrypt-by-default
		// surfaces here on create, update AND apply, so the wording stays
		// neutral about which one the user ran; the server explains the rest
		// via details.
		// The detail is inlined even though it also prints as a bullet below:
		// "that notebook cannot be used" alone does not say WHY, and the why
		// differs — missing, someone else's, tombstoned, or encrypt-by-default.
		// One duplicated line is worth the first line being the whole answer.
		if nb, ok := apiErr.Details["notebook_id"]; ok {
			friendly = fmt.Sprintf("that notebook cannot be used: %v", nb)
			break
		}
		// Two spellings on purpose: create and update validate the template's
		// own list and report "tag_ids", while apply validates the list sent
		// for the new note and reports "tags".
		for _, key := range []string{"tag_ids", "tags"} {
			if tags, ok := apiErr.Details[key]; ok {
				friendly = fmt.Sprintf("those tags cannot be used: %v", tags)
				break
			}
		}
	}
	if friendly == "" {
		return err
	}
	rewritten := *apiErr
	rewritten.Message = friendly
	return &rewritten
}

// ===========================================================================
// Display
// ===========================================================================

// displayTemplates renders a template collection as a table.
func displayTemplates(data []byte) {
	items := client.CollectionItems(data)
	// Only a COUNT for tags, and no notebook column at all: both are ids, and a
	// pair of UUIDs per row would push everything else off an ordinary terminal.
	// The detail view carries the actual values.
	headers := []string{"ID", "NAME", "SYSTEM", "TAGS", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		t := parseJSON(raw)
		rows = append(rows, []string{
			str(t, "id"),
			truncate(str(t, "name"), 40),
			boolMark(boolean(t, "is_system")),
			countOrDash(len(toStringSlice(t["tag_ids"]))),
			dim(str(t, "usn")),
			epochMS(num(t, "updated_at")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayTemplate renders one template as a key/value detail view plus its
// (plain-text) body.
func displayTemplate(data []byte) {
	t := parseJSON(client.UnwrapData(data))
	if t == nil {
		fmt.Println(string(data))
		return
	}
	// Ids, not names. Resolving them would mean a notebook fetch and a tag list
	// on every single get, and --json carries the raw values for anything that
	// wants to do the lookup itself.
	printKV([][2]string{
		{"ID", bold(str(t, "id"))},
		{"Name", str(t, "name")},
		{"System", boolMark(boolean(t, "is_system"))},
		{"Notebook", valueOrDash(str(t, "notebook_id"))},
		{"Tags", valueOrDash(strings.Join(toStringSlice(t["tag_ids"]), ", "))},
		{"USN", str(t, "usn")},
		{"Deleted", boolMark(boolean(t, "deleted"))},
		{"Updated", epochMS(num(t, "updated_at"))},
		{"Created", epochMS(num(t, "created_at"))},
	})

	fmt.Println()
	body := str(t, "content")
	// Template content is sanitized HTML; render it readably.
	if strings.Contains(body, "<") && strings.Contains(body, ">") {
		body = stripHTML(body)
	}
	if body != "" {
		fmt.Println(body)
	}
}

func init() {
	addPagingFlags(templatesListCmd)
	templatesListCmd.Flags().Bool("include-system", true, "Include built-in (system) templates")
	templatesListCmd.Flags().Bool("include-deleted", false, "Include tombstoned templates")

	templatesGetCmd.Flags().Bool("include-deleted", false, "Return the template even if tombstoned")

	templatesCreateCmd.Flags().String("name", "", "Template name (required)")
	templatesCreateCmd.Flags().String("notebook", "", "Default notebook id for notes made from this template")
	templatesCreateCmd.Flags().String("tags", "", "Comma-separated tag ids for notes made from this template")
	addContentFlags(templatesCreateCmd)

	templatesUpdateCmd.Flags().String("name", "", "New name")
	templatesUpdateCmd.Flags().String("notebook", "", "Default notebook id (empty string clears it)")
	templatesUpdateCmd.Flags().String("tags", "", "Comma-separated tag ids (empty string clears them)")
	addContentFlags(templatesUpdateCmd)

	templatesApplyCmd.Flags().String("notebook", "", "Notebook id for the new note (defaults to the template's, then yours)")
	templatesApplyCmd.Flags().String("title", "", "Title for the new note (defaults to the template name)")
	templatesApplyCmd.Flags().String("tags", "", "Comma-separated tag ids — REPLACES the template's tags (omit to inherit them, empty string for none)")

	templatesCmd.AddCommand(templatesListCmd, templatesGetCmd, templatesCreateCmd, templatesUpdateCmd, templatesDeleteCmd, templatesApplyCmd)
	rootCmd.AddCommand(templatesCmd)
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
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
	Long:  "Create a template. Provide the body with --content, --file, or --stdin (Markdown by default; --format html for HTML).",
	Example: `  harbor templates create --name "Meeting notes" --content "# Meeting\n\nAttendees:"
  echo "# Standup" | harbor templates create --name Standup --stdin
  harbor templates create --name Recipe --file recipe.md --format markdown`,
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
	Long:  "Update a template. Only the fields you pass are modified. Built-in (system) templates are read-only and cannot be updated.",
	Example: `  harbor templates update 3c4d... --name "Meeting notes (v2)"
  harbor templates update 3c4d... --file updated.md`,
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
		if len(body) == 0 {
			return errors.New("nothing to update — pass --name, content, or another field")
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
	Long: `Instantiate a new note from a template. The template's content is copied
verbatim into a fresh note (no token expansion in v1). The title defaults to the
template name, the notebook to your default, unless overridden.

Applying into an encrypt-by-default notebook is rejected by the server — fetch
the template, encrypt locally, and create the note via 'harbor notes create'.`,
	Example: `  harbor templates apply 3c4d...
  harbor templates apply 3c4d... --title "Standup 2026-06-22" --notebook 5b1f...
  harbor templates apply 3c4d... --tags 7e1d...,9a2c...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "notebook", "notebook_id")
		addStringIfChanged(cmd, body, "title", "title")
		if s := stringFlag(cmd, "tags"); s != "" {
			body["tags"] = splitCSV(s)
		}
		data, err := c.ApplyTemplate(args[0], body)
		if err != nil {
			return mapTemplateError(err)
		}
		printResult(data, displayNote)
		return nil
	},
}

// mapTemplateError gives friendly messages for the template-specific codes.
func mapTemplateError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "system_template_readonly":
			return errors.New("this is a built-in (system) template — it cannot be edited or deleted")
		case "validation_failed":
			// apply into an encrypt-by-default notebook surfaces here; the
			// server explains via details.notebook_id.
			if nb, ok := apiErr.Details["notebook_id"]; ok {
				return fmt.Errorf("cannot apply this template: %v", nb)
			}
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayTemplates renders a template collection as a table.
func displayTemplates(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "NAME", "SYSTEM", "USN", "UPDATED"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		t := parseJSON(raw)
		rows = append(rows, []string{
			str(t, "id"),
			truncate(str(t, "name"), 40),
			boolMark(boolean(t, "is_system")),
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
	printKV([][2]string{
		{"ID", bold(str(t, "id"))},
		{"Name", str(t, "name")},
		{"System", boolMark(boolean(t, "is_system"))},
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
	addContentFlags(templatesCreateCmd)

	templatesUpdateCmd.Flags().String("name", "", "New name")
	addContentFlags(templatesUpdateCmd)

	templatesApplyCmd.Flags().String("notebook", "", "Notebook id for the new note (defaults to your default notebook)")
	templatesApplyCmd.Flags().String("title", "", "Title for the new note (defaults to the template name)")
	templatesApplyCmd.Flags().String("tags", "", "Comma-separated tag ids to attach to the new note")

	templatesCmd.AddCommand(templatesListCmd, templatesGetCmd, templatesCreateCmd, templatesUpdateCmd, templatesDeleteCmd, templatesApplyCmd)
	rootCmd.AddCommand(templatesCmd)
}

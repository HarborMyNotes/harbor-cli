// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// stacksCmd is the parent for stack commands. A stack is a named group of
// notebooks; a notebook joins one through its stack label, which is set with
// `harbor notebooks update <id> --stack NAME`.
var stacksCmd = &cobra.Command{
	Use:     "stacks",
	Aliases: []string{"stack"},
	Short:   "List stacks and manage their color and metadata",
	GroupID: groupOrg,
	Long: `A stack is a named group of notebooks. A notebook joins a stack through its
stack label: 'harbor notebooks update <id> --stack NAME'.`,
}

// stacksListCmd lists every stack with its notebook count.
var stacksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stacks",
	Example: `  harbor stacks list
  harbor stacks list --order -notebook_count
  harbor stacks list --meta-eq client=acme
  harbor stacks list --meta-has billable --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		q, err := metaFilterQuery(cmd, pagingParams(cmd))
		if err != nil {
			return err
		}
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListStacks(q)
		if err != nil {
			return err
		}
		printResult(data, displayStacks)
		return nil
	},
}

// stacksMetaCmd reads or changes one stack's metadata.
var stacksMetaCmd = &cobra.Command{
	Use:   "meta <name>",
	Short: "Read or change a stack's metadata",
	Args:  cobra.ExactArgs(1),
	Long: metaCommandLong("stack") + `

A stack is named exactly, case included. Renaming a stack keeps its metadata;
deleting it discards it.`,
	Example: `  harbor stacks meta Projects
  harbor stacks meta "Client Work" --set client=acme --set billable=true
  harbor stacks meta Projects --unset billable
  harbor stacks meta Projects --clear`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMetaCommand(cmd, client.StackMetadataPath(args[0]))
	},
}

// stacksUpdateCmd changes a stack's color.
var stacksUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Change a stack's color",
	Args:  cobra.ExactArgs(1),
	Long: `Change a stack's color. A stack is named exactly, case included.

The color is the stack's own; its notebooks keep theirs. A stack that exists only
through its notebooks' labels is saved as a stack once it has a color, so it
stays in 'harbor stacks list' after its last notebook leaves.`,
	Example: `  harbor stacks update Projects --color green
  harbor stacks update "Client Work" --color none`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The flags are checked before the credentials so a typo answers the typo.
		body := map[string]any{}
		if err := addColorIfChanged(cmd, body); err != nil {
			return err
		}
		if len(body) == 0 {
			return errors.New("nothing to update — pass --color")
		}
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.UpdateStack(args[0], body)
		if err != nil {
			return mapStackError(err)
		}
		printResult(data, displayStack)
		return nil
	},
}

// stackNotFoundMessage is the friendly answer to stack_not_found, shared by
// every command that addresses a stack by name.
const stackNotFoundMessage = "no stack has that name — see 'harbor stacks list'"

// mapStackError gives a friendly message for an unknown stack name. A plan
// limit is left alone: the renderer has its own treatment and exit code for it.
func mapStackError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "stack_not_found" {
		return errors.New(stackNotFoundMessage)
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displayStacks renders a stack collection as a table.
func displayStacks(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"NAME", "COLOR", "NOTEBOOKS"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		s := parseJSON(raw)
		rows = append(rows, []string{
			str(s, "name"),
			colorName(str(s, "color")),
			str(s, "notebook_count"),
		})
	}
	headers, rows = withMetaColumn(items, headers, rows)
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayStack renders one stack's list object as a detail view.
func displayStack(data []byte) {
	s := parseJSON(client.UnwrapData(data))
	if s == nil {
		fmt.Println(string(data))
		return
	}
	pairs := [][2]string{
		{"Name", bold(str(s, "name"))},
		{"Color", colorName(str(s, "color"))},
		{"Notebooks", str(s, "notebook_count")},
	}
	if meta, ok := metadataPair(data); ok {
		pairs = append(pairs, meta)
	}
	printKV(pairs)
}

// init registers the stack commands. The paging flags are declared here
// rather than with addPagingFlags because this list's default is every stack,
// not a page of 100.
func init() {
	stacksListCmd.Flags().Int("limit", 0, "Maximum results to return (default: all stacks, cap 500)")
	stacksListCmd.Flags().Int("offset", 0, "Number of results to skip")
	stacksListCmd.Flags().String("order", "", "Sort order: name (default) or notebook_count; - for descending")
	addMetaFilterFlags(stacksListCmd)

	addMetaWriteFlags(stacksMetaCmd, metaCmdFlags)

	addColorFlag(stacksUpdateCmd)

	stacksCmd.AddCommand(stacksListCmd, stacksUpdateCmd, stacksMetaCmd)
	rootCmd.AddCommand(stacksCmd)
}

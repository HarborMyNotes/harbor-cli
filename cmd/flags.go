// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// addPagingFlags registers the standard list flags (--limit/--offset/--order)
// on a command, so pagination is identical across every list command.
func addPagingFlags(cmd *cobra.Command) {
	cmd.Flags().Int("limit", 0, "Maximum results to return (default 100, cap 500)")
	cmd.Flags().Int("offset", 0, "Number of results to skip")
	cmd.Flags().String("order", "", "Sort order, e.g. -updated_at,name (- = descending)")
}

// pagingParams extracts the standard list flags into an API query map, omitting
// any that were not explicitly set so server defaults apply.
func pagingParams(cmd *cobra.Command) map[string]string {
	params := map[string]string{}
	if cmd.Flags().Changed("limit") {
		v, _ := cmd.Flags().GetInt("limit")
		params["limit"] = strconv.Itoa(v)
	}
	if cmd.Flags().Changed("offset") {
		v, _ := cmd.Flags().GetInt("offset")
		params["offset"] = strconv.Itoa(v)
	}
	if cmd.Flags().Changed("order") {
		v, _ := cmd.Flags().GetString("order")
		params["order"] = v
	}
	return params
}

// stringFlag returns a string flag value (empty if unset).
func stringFlag(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// boolFlag returns a bool flag value.
func boolFlag(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// intFlag returns an int flag value.
func intFlag(cmd *cobra.Command, name string) int {
	v, _ := cmd.Flags().GetInt(name)
	return v
}

// durationFlag returns a duration flag value (zero if unset or not a duration
// flag), for the polling intervals and timeouts on long-running commands.
func durationFlag(cmd *cobra.Command, name string) time.Duration {
	v, _ := cmd.Flags().GetDuration(name)
	return v
}

// addStringIfChanged copies a string flag into body[key] only when the user
// explicitly set it — the basis for partial (PATCH) updates that touch only the
// provided fields.
func addStringIfChanged(cmd *cobra.Command, body map[string]any, flag, key string) {
	if cmd.Flags().Changed(flag) {
		body[key], _ = cmd.Flags().GetString(flag)
	}
}

// addCSVIfChanged copies a comma-separated flag into body[key] as a JSON array,
// but only when the user explicitly set it.
//
// An empty value becomes an empty array rather than being skipped, because on a
// partial update the server reads an omitted list as "leave it alone" and an
// empty one as "clear it" — collapsing the two would make a list impossible to
// empty.
func addCSVIfChanged(cmd *cobra.Command, body map[string]any, flag, key string) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		body[key] = splitCSV(v)
	}
}

// addBoolIfChanged copies a bool flag into body[key] only when explicitly set.
func addBoolIfChanged(cmd *cobra.Command, body map[string]any, flag, key string) {
	if cmd.Flags().Changed(flag) {
		body[key], _ = cmd.Flags().GetBool(flag)
	}
}

// addIntIfChanged copies an int flag into body[key] only when explicitly set.
func addIntIfChanged(cmd *cobra.Command, body map[string]any, flag, key string) {
	if cmd.Flags().Changed(flag) {
		body[key], _ = cmd.Flags().GetInt(flag)
	}
}

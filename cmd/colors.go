// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-11-28

package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// harborColor is one entry in Harbor's display palette: the name the CLI
// accepts and prints, and the hex the API stores.
type harborColor struct {
	Name string
	Hex  string
}

// harborColors is the closed palette tags, notebooks and stacks share, in the
// web picker's order. The API accepts only these hexes (or "" for none) and
// refuses anything else with 422 invalid_color.
var harborColors = []harborColor{
	{Name: "red", Hex: "#ef4444"},
	{Name: "orange", Hex: "#f97316"},
	{Name: "amber", Hex: "#f59e0b"},
	{Name: "green", Hex: "#22c55e"},
	{Name: "teal", Hex: "#14b8a6"},
	{Name: "harbor-blue", Hex: "#1184f0"},
	{Name: "indigo", Hex: "#6366f1"},
	{Name: "purple", Hex: "#a855f7"},
	{Name: "pink", Hex: "#ec4899"},
	{Name: "slate", Hex: "#64748b"},
}

// colorNames lists the palette's names in order, comma-separated, for help
// text and error messages.
func colorNames() string {
	names := make([]string, len(harborColors))
	for i, c := range harborColors {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// colorFlagUsage is the --color help line, so every command lists the same ten
// names.
func colorFlagUsage() string {
	return "Color: " + colorNames() + ", its hex (e.g. #ef4444), or none to clear"
}

// parseColor turns a --color value into the hex the API stores, or "" for
// none. It takes a palette name in any case ("Harbor Blue" and "harbor_blue"
// mean harbor-blue), a palette hex with or without its "#" (an unquoted "#" can
// start a shell comment), or "none" / "" to clear.
func parseColor(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || v == "none" {
		return "", nil
	}
	name := strings.NewReplacer(" ", "-", "_", "-").Replace(v)
	hex := v
	if !strings.HasPrefix(hex, "#") {
		hex = "#" + hex
	}
	for _, c := range harborColors {
		if name == c.Name || hex == c.Hex {
			return c.Hex, nil
		}
	}
	return "", fmt.Errorf("invalid --color %q — use one of %s, or none to clear", raw, colorNames())
}

// colorName is how a stored color reads in table output: its palette name,
// blank for none. A value outside the palette is printed as stored, so a color
// the server adds later still shows up rather than vanishing.
func colorName(stored string) string {
	v := strings.ToLower(strings.TrimSpace(stored))
	for _, c := range harborColors {
		if v == c.Hex {
			return c.Name
		}
	}
	return stored
}

// addColorFlag registers --color on a create or update command.
func addColorFlag(cmd *cobra.Command) {
	cmd.Flags().String("color", "", colorFlagUsage())
}

// addColorIfChanged puts the parsed --color into body["color"], but only when
// the user passed the flag. Leaving the key out is what keeps a stored color on
// an update, so a rename must never send it.
func addColorIfChanged(cmd *cobra.Command, body map[string]any) error {
	if !cmd.Flags().Changed("color") {
		return nil
	}
	hex, err := parseColor(stringFlag(cmd, "color"))
	if err != nil {
		return err
	}
	body["color"] = hex
	return nil
}

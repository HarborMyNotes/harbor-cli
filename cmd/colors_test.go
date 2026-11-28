// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-11-28

package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// TestHarborColorsMatchTheAPIPalette pins the ten names and hexes. The API
// refuses any other hex, so a typo here would make one name unusable.
func TestHarborColorsMatchTheAPIPalette(t *testing.T) {
	want := []harborColor{
		{"red", "#ef4444"}, {"orange", "#f97316"}, {"amber", "#f59e0b"},
		{"green", "#22c55e"}, {"teal", "#14b8a6"}, {"harbor-blue", "#1184f0"},
		{"indigo", "#6366f1"}, {"purple", "#a855f7"}, {"pink", "#ec4899"},
		{"slate", "#64748b"},
	}
	if len(harborColors) != len(want) {
		t.Fatalf("palette has %d colors, want %d", len(harborColors), len(want))
	}
	for i, c := range want {
		if harborColors[i] != c {
			t.Errorf("palette[%d] = %+v, want %+v", i, harborColors[i], c)
		}
	}
}

// TestParseColorAccepts covers every accepted spelling: a name in any case, a
// name with a space or underscore, a hex in any case with or without its "#",
// and none to clear.
func TestParseColorAccepts(t *testing.T) {
	cases := map[string]string{
		"red":         "#ef4444",
		"RED":         "#ef4444",
		" Teal ":      "#14b8a6",
		"harbor-blue": "#1184f0",
		"Harbor Blue": "#1184f0",
		"harbor_blue": "#1184f0",
		"#EC4899":     "#ec4899",
		"#6366f1":     "#6366f1",
		"64748b":      "#64748b",
		"F59E0B":      "#f59e0b",
		"none":        "",
		"NONE":        "",
		" None ":      "",
	}
	for in, want := range cases {
		got, err := parseColor(in)
		if err != nil {
			t.Errorf("parseColor(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseColor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseColorRejects covers values outside the palette, and an empty value,
// which must not clear a color the way none does. The error must name every
// allowed color so the user can pick one without opening the help.
func TestParseColorRejects(t *testing.T) {
	for _, in := range []string{"", "   ", "blue", "harborblue", "#000000", "#ef444", "ef44444", "##ef4444", "redd", "null"} {
		_, err := parseColor(in)
		if err == nil {
			t.Errorf("parseColor(%q) accepted a value outside the palette", in)
			continue
		}
		for _, c := range harborColors {
			if !strings.Contains(err.Error(), c.Name) {
				t.Errorf("parseColor(%q) error does not list %q: %v", in, c.Name, err)
			}
		}
		if !strings.Contains(err.Error(), "none") {
			t.Errorf("parseColor(%q) error does not mention none: %v", in, err)
		}
	}
}

// TestColorName covers the table spelling: the palette name, and blank for
// none or for a value outside the palette.
func TestColorName(t *testing.T) {
	cases := map[string]string{
		"#ef4444": "red",
		"#1184F0": "harbor-blue",
		"":        "",
		"#123456": "",
	}
	for in, want := range cases {
		if got := colorName(in); got != want {
			t.Errorf("colorName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestColorFlagHelpListsEveryName proves each command's --color help names all
// ten colors and the way to clear one.
func TestColorFlagHelpListsEveryName(t *testing.T) {
	for _, c := range []struct {
		name string
		flag *pflag.Flag
	}{
		{"tags create", tagsCreateCmd.Flags().Lookup("color")},
		{"tags update", tagsUpdateCmd.Flags().Lookup("color")},
		{"notebooks create", notebooksCreateCmd.Flags().Lookup("color")},
		{"notebooks update", notebooksUpdateCmd.Flags().Lookup("color")},
		{"stacks update", stacksUpdateCmd.Flags().Lookup("color")},
	} {
		if c.flag == nil {
			t.Errorf("%s has no --color flag", c.name)
			continue
		}
		for _, hc := range harborColors {
			if !strings.Contains(c.flag.Usage, hc.Name) {
				t.Errorf("%s --color help is missing %q: %s", c.name, hc.Name, c.flag.Usage)
			}
		}
		if !strings.Contains(c.flag.Usage, "none") {
			t.Errorf("%s --color help does not say how to clear: %s", c.name, c.flag.Usage)
		}
	}
}

// TestColorFlagIsCheckedBeforeLogin proves a bad --color, or nothing to update,
// is reported even with no saved session, rather than "not logged in". The
// HOME is empty and there is no HARBOR_TOKEN, so reaching the credentials check
// would fail with a different error.
func TestColorFlagIsCheckedBeforeLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_TOKEN", "")
	t.Setenv("HARBOR_API_URL", "http://127.0.0.1:1/api/v1")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tags", "create", "--name", "Urgent", "--color", "blue"}, "harbor-blue"},
		{[]string{"tags", "update", "t1", "--color", "blue"}, "harbor-blue"},
		{[]string{"tags", "update", "t1"}, "nothing to update"},
		{[]string{"notebooks", "create", "--name", "Ideas", "--color", "blue"}, "harbor-blue"},
		{[]string{"notebooks", "update", "nb1", "--color", "blue"}, "harbor-blue"},
		{[]string{"notebooks", "update", "nb1"}, "nothing to update"},
		{[]string{"stacks", "update", "Projects", "--color", "blue"}, "harbor-blue"},
		{[]string{"stacks", "update", "Projects"}, "nothing to update"},
	} {
		resetCommandState(t)
		prepareCommandTree()
		rootCmd.SetArgs(tc.args)
		var err error
		captureStdout(t, func() { err = rootCmd.Execute() })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
	}
}

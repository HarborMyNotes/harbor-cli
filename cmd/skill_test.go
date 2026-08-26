// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// fixedNow is a deterministic timestamp so backup directory names are stable in
// tests.
var fixedNow = time.Date(2026, 6, 22, 20, 27, 44, 0, time.UTC)

// embeddedSkillMD returns the bundled SKILL.md bytes, failing the test if the
// embed is somehow broken.
func embeddedSkillMD(t *testing.T) []byte {
	t.Helper()
	data, err := skillFS.ReadFile(skillSourceDir + "/SKILL.md")
	if err != nil {
		t.Fatalf("embedded SKILL.md unreadable: %v", err)
	}
	return data
}

// TestSkillEmbedHasFrontmatter verifies the embed is wired and the skill's
// frontmatter name matches the install directory.
func TestSkillEmbedHasFrontmatter(t *testing.T) {
	data := embeddedSkillMD(t)
	head := string(data)
	if !strings.HasPrefix(head, "---\n") {
		t.Errorf("SKILL.md should start with YAML frontmatter, got:\n%.40s", head)
	}
	if !strings.Contains(head, "name: "+skillName) {
		t.Errorf("SKILL.md frontmatter name should be %q", skillName)
	}
}

// TestBundledSkillFiles checks all three skill files ship in the embed.
func TestBundledSkillFiles(t *testing.T) {
	got := bundledSkillFiles()
	for _, want := range []string{"SKILL.md", "formatting.md", "reference.md"} {
		found := false
		for _, name := range got {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bundled skill missing %q (have %v)", want, got)
		}
	}
}

// TestInstallSkillIntoFresh installs into an empty root and asserts every file
// landed, SKILL.md matches the embed, and no backup was made.
func TestInstallSkillIntoFresh(t *testing.T) {
	root := t.TempDir()
	res, err := installSkillInto(root, false, fixedNow)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if res.UpToDate {
		t.Error("a fresh install should not report up-to-date")
	}
	if res.Backup != "" {
		t.Errorf("a fresh install should not create a backup, got %q", res.Backup)
	}

	dest := filepath.Join(root, skillName)
	for _, name := range []string{"SKILL.md", "formatting.md", "reference.md"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("expected %s to be installed: %v", name, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("reading installed SKILL.md: %v", err)
	}
	if string(got) != string(embeddedSkillMD(t)) {
		t.Error("installed SKILL.md does not match the embedded bundle")
	}
}

// TestInstallSkillIntoUpToDate installs twice and asserts the second run is a
// no-op (no rewrite, no backup, no extra directory).
func TestInstallSkillIntoUpToDate(t *testing.T) {
	root := t.TempDir()
	if _, err := installSkillInto(root, false, fixedNow); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	res, err := installSkillInto(root, false, fixedNow)
	if err != nil {
		t.Fatalf("second install failed: %v", err)
	}
	if !res.UpToDate {
		t.Error("re-installing an unchanged skill should report up-to-date")
	}
	if res.Backup != "" {
		t.Errorf("an up-to-date no-op should not back anything up, got %q", res.Backup)
	}

	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Errorf("expected only the skill directory, got %d entries", len(entries))
	}
}

// TestInstallSkillIntoBacksUp simulates a user edit, reinstalls, and asserts the
// edited copy is preserved in a timestamped backup while the fresh bundle is
// written.
func TestInstallSkillIntoBacksUp(t *testing.T) {
	root := t.TempDir()
	if _, err := installSkillInto(root, false, fixedNow); err != nil {
		t.Fatalf("first install failed: %v", err)
	}

	dest := filepath.Join(root, skillName)
	edited := filepath.Join(dest, "SKILL.md")
	if err := os.WriteFile(edited, []byte("MY LOCAL EDIT"), 0o644); err != nil {
		t.Fatalf("simulating user edit: %v", err)
	}

	later := fixedNow.Add(time.Minute)
	res, err := installSkillInto(root, false, later)
	if err != nil {
		t.Fatalf("reinstall failed: %v", err)
	}
	if res.UpToDate {
		t.Error("reinstalling over an edited skill should not report up-to-date")
	}
	if res.Backup == "" {
		t.Fatal("expected a backup to be created")
	}

	// The backup keeps the user's edit…
	backupMD, err := os.ReadFile(filepath.Join(res.Backup, "SKILL.md"))
	if err != nil {
		t.Fatalf("reading backup SKILL.md: %v", err)
	}
	if string(backupMD) != "MY LOCAL EDIT" {
		t.Errorf("backup should preserve the user edit, got %q", string(backupMD))
	}

	// …and the live directory is the fresh bundle again.
	freshMD, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("reading reinstalled SKILL.md: %v", err)
	}
	if string(freshMD) != string(embeddedSkillMD(t)) {
		t.Error("reinstalled SKILL.md should match the embedded bundle")
	}
}

// TestInstallSkillForceReinstalls asserts --force rewrites (and backs up) even
// when the installed skill already matches the bundle.
func TestInstallSkillForceReinstalls(t *testing.T) {
	root := t.TempDir()
	if _, err := installSkillInto(root, false, fixedNow); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	res, err := installSkillInto(root, true, fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("forced reinstall failed: %v", err)
	}
	if res.UpToDate {
		t.Error("--force should not short-circuit as up-to-date")
	}
	if res.Backup == "" {
		t.Error("--force over an existing skill should back it up")
	}
}

// TestUniqueBackupPath checks the collision suffix when a same-second backup
// already exists.
func TestUniqueBackupPath(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "harbor.backup-20260622-202744")
	if got := uniqueBackupPath(base); got != base {
		t.Errorf("with no collision want %q, got %q", base, got)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("seeding collision: %v", err)
	}
	if got, want := uniqueBackupPath(base), base+"-2"; got != want {
		t.Errorf("with a collision want %q, got %q", want, got)
	}
}

// newSkillTargetCmd builds a throwaway command carrying the target flags
// resolveSkillsRoot reads, so root resolution can be tested without the real
// install command.
func newSkillTargetCmd(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "x"}
	c.Flags().String("agent", "claude", "")
	c.Flags().String("dir", "", "")
	c.Flags().Bool("project", false, "")
	return c
}

// TestResolveSkillTarget covers each agent's default destination, a
// project/dir override, and the unsupported-agent error.
func TestResolveSkillTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// claude (default) → a skill directory under ~/.claude/skills/harbor.
	c := newSkillTargetCmd(t)
	tgt, err := resolveSkillTarget(c)
	if err != nil {
		t.Fatalf("claude resolve failed: %v", err)
	}
	if tgt.kind != kindSkillDir {
		t.Errorf("claude should be a skill dir, got kind %v", tgt.kind)
	}
	if want := filepath.Join(home, ".claude", "skills", skillName); tgt.path != want {
		t.Errorf("claude path: want %q, got %q", want, tgt.path)
	}

	// codex → a managed AGENTS.md under ~/.codex.
	c = newSkillTargetCmd(t)
	_ = c.Flags().Set("agent", "codex")
	tgt, err = resolveSkillTarget(c)
	if err != nil {
		t.Fatalf("codex resolve failed: %v", err)
	}
	if tgt.kind != kindManagedDoc {
		t.Errorf("codex should be a managed doc, got kind %v", tgt.kind)
	}
	if want := filepath.Join(home, ".codex", "AGENTS.md"); tgt.path != want {
		t.Errorf("codex path: want %q, got %q", want, tgt.path)
	}

	// cursor → an absolute project rule file (.cursor/rules/harbor.mdc).
	c = newSkillTargetCmd(t)
	_ = c.Flags().Set("agent", "cursor")
	tgt, err = resolveSkillTarget(c)
	if err != nil {
		t.Fatalf("cursor resolve failed: %v", err)
	}
	if tgt.kind != kindSingleDoc {
		t.Errorf("cursor should be a single doc, got kind %v", tgt.kind)
	}
	if !filepath.IsAbs(tgt.path) || !strings.HasSuffix(tgt.path, filepath.Join(".cursor", "rules", "harbor.mdc")) {
		t.Errorf("cursor path unexpected: %q", tgt.path)
	}

	// codex --project → ./AGENTS.md (absolute).
	c = newSkillTargetCmd(t)
	_ = c.Flags().Set("agent", "codex")
	_ = c.Flags().Set("project", "true")
	tgt, err = resolveSkillTarget(c)
	if err != nil {
		t.Fatalf("codex project resolve failed: %v", err)
	}
	if !filepath.IsAbs(tgt.path) || !strings.HasSuffix(tgt.path, "AGENTS.md") {
		t.Errorf("codex project path unexpected: %q", tgt.path)
	}

	// --dir wins for claude → <dir>/harbor.
	c = newSkillTargetCmd(t)
	_ = c.Flags().Set("dir", home)
	tgt, err = resolveSkillTarget(c)
	if err != nil {
		t.Fatalf("claude dir resolve failed: %v", err)
	}
	if want := filepath.Join(home, skillName); tgt.path != want {
		t.Errorf("claude --dir path: want %q, got %q", want, tgt.path)
	}

	// An unknown agent is a friendly error.
	c = newSkillTargetCmd(t)
	_ = c.Flags().Set("agent", "aider")
	if _, err := resolveSkillTarget(c); err == nil {
		t.Error("an unsupported --agent should error")
	}
}

// TestSkillBodyForAgents checks the frontmatter is stripped and the on-demand
// pointers to the companion docs are present.
func TestSkillBodyForAgents(t *testing.T) {
	body, err := skillBodyForAgents()
	if err != nil {
		t.Fatalf("skillBodyForAgents: %v", err)
	}
	if strings.HasPrefix(body, "---") {
		t.Error("agent body should have the YAML frontmatter stripped")
	}
	if !strings.Contains(body, "# Harbor CLI") {
		t.Error("agent body should keep the skill heading")
	}
	if !strings.Contains(body, "harbor skill show formatting.md") {
		t.Error("agent body should point to the companion docs via 'skill show'")
	}
}

// TestRenderCursorRule checks the MDC frontmatter wrapper.
func TestRenderCursorRule(t *testing.T) {
	rule, err := renderCursorRule()
	if err != nil {
		t.Fatalf("renderCursorRule: %v", err)
	}
	if !strings.HasPrefix(rule, "---\n") || !strings.Contains(rule, "alwaysApply: false") {
		t.Errorf("cursor rule should open with MDC frontmatter:\n%.80s", rule)
	}
	if !strings.Contains(rule, "# Harbor CLI") {
		t.Error("cursor rule should contain the skill body")
	}
}

// TestSpliceManagedBlock checks insertion, idempotence, user-content
// preservation, and block replacement.
func TestSpliceManagedBlock(t *testing.T) {
	block, err := renderCodexBlock()
	if err != nil {
		t.Fatalf("renderCodexBlock: %v", err)
	}

	// Empty file → just the block, exactly one trailing newline.
	got := spliceManagedBlock(nil, block)
	if !strings.Contains(got, codexBlockBegin) || !strings.Contains(got, codexBlockEnd) {
		t.Error("spliced output should contain both markers")
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Error("spliced output should end with exactly one newline")
	}
	if again := spliceManagedBlock([]byte(got), block); again != got {
		t.Error("splice should be idempotent on its own output")
	}

	// User content is preserved alongside the block.
	user := "# My rules\n\nBe concise.\n"
	merged := spliceManagedBlock([]byte(user), block)
	if !strings.Contains(merged, "Be concise.") || !strings.Contains(merged, codexBlockBegin) {
		t.Error("user content and the block should both be present")
	}
	if again := spliceManagedBlock([]byte(merged), block); again != merged {
		t.Error("splice over user content should be idempotent")
	}

	// A stale block is replaced; surrounding user content survives.
	stale := strings.Replace(merged, "# Harbor CLI", "# OLD HARBOR", 1)
	replaced := spliceManagedBlock([]byte(stale), block)
	if strings.Contains(replaced, "# OLD HARBOR") {
		t.Error("stale block content should be replaced")
	}
	if !strings.Contains(replaced, "Be concise.") {
		t.Error("user content should survive a block replace")
	}
}

// TestInstallSingleDoc covers a Cursor rule: fresh write, no-op, and
// edit→backup→rewrite.
func TestInstallSingleDoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor", "rules", "harbor.mdc")

	res, err := installSingleDoc(path, false, fixedNow)
	if err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if res.UpToDate || res.Backup != "" {
		t.Error("fresh cursor install should write without a backup")
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "---\n") {
		t.Error("cursor rule should have MDC frontmatter")
	}

	res, _ = installSingleDoc(path, false, fixedNow)
	if !res.UpToDate {
		t.Error("re-installing an unchanged cursor rule should be up to date")
	}

	if err := os.WriteFile(path, []byte("EDITED"), 0o644); err != nil {
		t.Fatalf("simulating edit: %v", err)
	}
	res, err = installSingleDoc(path, false, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if res.Backup == "" {
		t.Error("an edited cursor rule should be backed up")
	}
	if bak, _ := os.ReadFile(res.Backup); string(bak) != "EDITED" {
		t.Error("backup should preserve the user edit")
	}
	if fresh, _ := os.ReadFile(path); !strings.HasPrefix(string(fresh), "---\n") {
		t.Error("cursor rule should be rewritten fresh")
	}
}

// TestInstallManagedDoc covers a Codex AGENTS.md: fresh write, no-op, and a
// stale-block refresh that preserves user content and backs up the prior file.
func TestInstallManagedDoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	res, err := installManagedDoc(path, false, fixedNow)
	if err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if res.UpToDate || res.Backup != "" {
		t.Error("fresh managed install should write without a backup")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), codexBlockBegin) {
		t.Error("AGENTS.md should contain the managed block")
	}

	res, _ = installManagedDoc(path, false, fixedNow)
	if !res.UpToDate {
		t.Error("re-installing an unchanged managed doc should be up to date")
	}

	// User content + a stale block → backup + refresh, user content kept.
	stale := "# Personal rules\n\nBe nice.\n\n" + strings.Replace(string(data), "# Harbor CLI", "# OLD HARBOR", 1)
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatalf("seeding stale doc: %v", err)
	}
	res, err = installManagedDoc(path, false, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Backup == "" {
		t.Error("a changed managed doc should be backed up")
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "# OLD HARBOR") {
		t.Error("the stale block should be refreshed")
	}
	if !strings.Contains(string(got), "Be nice.") {
		t.Error("user content should be preserved")
	}
	if bak, _ := os.ReadFile(res.Backup); !strings.Contains(string(bak), "# OLD HARBOR") {
		t.Error("the backup should hold the prior (stale) content")
	}
}

// TestSkillShow prints the default and a named bundled file.
func TestSkillShow(t *testing.T) {
	out := captureStdout(t, func() {
		if err := skillShowCmd.RunE(skillShowCmd, nil); err != nil {
			t.Fatalf("show default failed: %v", err)
		}
	})
	if !strings.Contains(out, "name: "+skillName) {
		t.Errorf("default show should print SKILL.md frontmatter:\n%.80s", out)
	}

	out = captureStdout(t, func() {
		if err := skillShowCmd.RunE(skillShowCmd, []string{"formatting.md"}); err != nil {
			t.Fatalf("show formatting.md failed: %v", err)
		}
	})
	if !strings.Contains(out, "rich-content cookbook") {
		t.Errorf("show formatting.md should print the cookbook:\n%.80s", out)
	}

	// An unknown file is a friendly error, not a panic.
	if err := skillShowCmd.RunE(skillShowCmd, []string{"missing.md"}); err == nil {
		t.Error("showing an unknown file should error")
	}
}

// TestSkillVersionMatchesTheShippedMarker keeps the two hand-maintained version
// numbers from drifting apart.
//
// skillVersion is what an install reports; the marker at the top of SKILL.md is
// what a reader of the installed file sees. They have already drifted three
// releases once, which makes an install claim to be current while the file it
// wrote says otherwise — and nothing but this test notices.
func TestSkillVersionMatchesTheShippedMarker(t *testing.T) {
	body, err := skillFS.ReadFile(skillSourceDir + "/SKILL.md")
	if err != nil {
		t.Fatalf("cannot read the embedded SKILL.md: %v", err)
	}
	marker := regexp.MustCompile(`(?m)^<!-- Harbor agent skill • v(\S+) -->$`).FindStringSubmatch(string(body))
	if marker == nil {
		t.Fatal("SKILL.md has no `<!-- Harbor agent skill • vX.Y.Z -->` marker")
	}
	if marker[1] != skillVersion {
		t.Errorf("SKILL.md's marker says v%s but skillVersion is %s — bump both together", marker[1], skillVersion)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// skillFS holds the bundled Harbor agent skill — SKILL.md plus its companion
// reference docs — embedded into the binary at build time. Embedding (rather
// than shipping loose files) means `harbor skill install` works no matter how
// harbor was installed: Homebrew, `go install`, or a release tarball.
//
//go:embed assets/skill
var skillFS embed.FS

const (
	// skillSourceDir is the path, inside skillFS, that roots the skill's files.
	skillSourceDir = "assets/skill"

	// skillName is the on-disk directory the Claude skill installs as (and must
	// match the `name:` in SKILL.md's frontmatter): <skills-root>/harbor/. It is
	// also the base name for the Cursor rule file (harbor.mdc).
	skillName = "harbor"

	// skillVersion is the content version of the bundled skill. Bump it whenever
	// the skill files change; it is surfaced to the user on install and helps
	// distinguish "already current" from "needs updating".
	skillVersion = "1.3.0"

	// codexBlockBegin / codexBlockEnd delimit the managed section the installer
	// splices into a Codex AGENTS.md (a file the user may share with their own
	// instructions). Re-installing replaces only the bytes between these markers.
	codexBlockBegin = "<!-- BEGIN harbor skill (managed by `harbor skill install`) -->"
	codexBlockEnd   = "<!-- END harbor skill -->"
)

// agentKind is how a given agent expects its instructions on disk.
type agentKind int

const (
	// kindSkillDir is a dedicated directory of files (Claude Code skills).
	kindSkillDir agentKind = iota
	// kindSingleDoc is a dedicated single file we fully own (Cursor .mdc rule).
	kindSingleDoc
	// kindManagedDoc is a shared file into which we splice a delimited block,
	// leaving the user's own content intact (Codex AGENTS.md).
	kindManagedDoc
)

// skillTarget describes where and how the skill installs for a chosen agent.
type skillTarget struct {
	// agent is the canonical id (claude / codex / cursor).
	agent string
	// label is a human description of the destination (shown to the user).
	label string
	// kind selects the install mechanic.
	kind agentKind
	// path is the exact destination: a directory for kindSkillDir, otherwise a
	// file.
	path string
}

// skillInstallResult captures what an install did, for rendering as text or JSON.
type skillInstallResult struct {
	// Path is the directory or file that was (or would be) written.
	Path string
	// Backup is where an existing copy was preserved (empty if none was made).
	Backup string
	// UpToDate is true when the destination already matched and nothing changed.
	UpToDate bool
	// SkillVersion is the bundled skill's content version.
	SkillVersion string
	// CLIVersion is the harbor CLI version that produced this skill.
	CLIVersion string
}

// ===========================================================================
// Commands
// ===========================================================================

// skillCmd is the parent for managing the Harbor agent skill.
var skillCmd = &cobra.Command{
	Use:     "skill",
	Aliases: []string{"skills"},
	Short:   "Install the Harbor agent skill for Claude Code, Codex, or Cursor",
	GroupID: groupSystem,
	Long: `Install the bundled Harbor "skill": a verbose, self-contained guide that
teaches an AI coding agent how to drive this CLI — creating, editing, and richly
formatting notes, organizing notebooks and tags, searching, reminders, sharing,
and more.

Supported agents (--agent): claude (Claude Code), codex (OpenAI Codex), cursor.
Each gets the skill in its native form:

  claude  →  ~/.claude/skills/harbor/        (a multi-file skill directory)
  codex   →  ~/.codex/AGENTS.md              (a managed block, your content kept)
  cursor  →  .cursor/rules/harbor.mdc        (a project rule file)

The skill is embedded in the harbor binary, so 'harbor skill install' works
anywhere harbor is installed. Re-installing after a CLI upgrade refreshes it; an
existing copy is backed up first, so your own edits are never lost.`,
}

// skillInstallCmd writes the bundled skill into the target agent's location,
// backing up any existing copy first.
var skillInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install or update the Harbor skill (backs up any existing copy)",
	Long: `Write the bundled Harbor skill into your agent's conventional location:

  --agent claude  →  ~/.claude/skills/harbor/   (multi-file skill; --project for ./.claude)
  --agent codex   →  ~/.codex/AGENTS.md         (managed block; --project for ./AGENTS.md)
  --agent cursor  →  .cursor/rules/harbor.mdc   (project rule file)

For a dedicated destination (Claude dir, Cursor rule), an existing copy is renamed
to a timestamped .backup-* before the new files are written. For Codex's shared
AGENTS.md, only the delimited Harbor block is replaced; the rest of the file is
left untouched (and the prior file is copied to a .backup-* first). If nothing
would change, install is a no-op unless --force. Use --dir to target any other
directory.`,
	Example: `  harbor skill install                       # Claude Code (global)
  harbor skill install --agent codex         # Codex (~/.codex/AGENTS.md)
  harbor skill install --agent cursor        # Cursor (.cursor/rules/harbor.mdc)
  harbor skill install --force
  harbor skill install --project             # project-scoped variant
  harbor skill install --dir ~/somewhere/skills`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve which agent and destination to install to.
		tgt, err := resolveSkillTarget(cmd)
		if err != nil {
			return err
		}

		// Do the install (compares, backs up, and writes as needed).
		res, err := installSkillTarget(tgt, boolFlag(cmd, "force"), time.Now())
		if err != nil {
			return err
		}

		// JSON mode: emit a machine-readable summary and stop.
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{
				"agent":         tgt.agent,
				"target":        tgt.label,
				"path":          res.Path,
				"backup":        res.Backup,
				"up_to_date":    res.UpToDate,
				"skill_version": res.SkillVersion,
				"cli_version":   res.CLIVersion,
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		// Nothing changed: tell the user how to force a reinstall.
		if res.UpToDate {
			fmt.Printf("Harbor skill is already up to date (v%s) for %s:\n  %s\n", res.SkillVersion, tgt.label, res.Path)
			fmt.Println(dim("Use --force to reinstall, or 'harbor skill show' to view it."))
			return nil
		}

		// Success: report where it landed and any backup we made.
		fmt.Printf("✓ Installed the Harbor skill v%s for %s:\n  %s\n", res.SkillVersion, tgt.label, res.Path)
		if res.Backup != "" {
			fmt.Printf("  backed up your previous copy → %s\n", res.Backup)
		}
		fmt.Println()
		fmt.Println(dim("Start a new agent session to pick it up. View it with 'harbor skill show'."))
		return nil
	},
}

// skillShowCmd prints a bundled skill file to stdout without installing.
var skillShowCmd = &cobra.Command{
	Use:   "show [file]",
	Short: "Print a bundled skill file (default SKILL.md)",
	Long: `Print one of the bundled skill files to stdout without installing
anything. Defaults to SKILL.md; pass a name like 'formatting.md' or
'reference.md' to view the companions. Agents on Codex/Cursor use this to read
the deep-dive guides on demand.`,
	Example: `  harbor skill show
  harbor skill show formatting.md
  harbor skill show reference.md`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default to the main skill file; allow a base name for the companions.
		name := "SKILL.md"
		if len(args) == 1 {
			name = filepath.Base(args[0])
		}
		data, err := skillFS.ReadFile(skillSourceDir + "/" + name)
		if err != nil {
			return fmt.Errorf("no bundled skill file %q (available: %s)", name, strings.Join(bundledSkillFiles(), ", "))
		}
		fmt.Print(string(data))
		return nil
	},
}

// skillPathCmd prints the destination the skill installs into, honoring the same
// target flags as install. Handy for scripting and for other agents.
var skillPathCmd = &cobra.Command{
	Use:     "path",
	Short:   "Print where the skill installs (honors --agent/--dir/--project)",
	Args:    cobra.NoArgs,
	Example: "  harbor skill path\n  harbor skill path --agent codex\n  harbor skill path --agent cursor",
	RunE: func(cmd *cobra.Command, args []string) error {
		tgt, err := resolveSkillTarget(cmd)
		if err != nil {
			return err
		}
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]string{
				"agent":  tgt.agent,
				"target": tgt.label,
				"path":   tgt.path,
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		fmt.Println(tgt.path)
		return nil
	},
}

// ===========================================================================
// Target resolution
// ===========================================================================

// resolveSkillTarget decides which agent and destination to install to from the
// --agent / --dir / --project flags. --dir overrides the directory for any
// agent; otherwise each agent resolves to its conventional global location, or a
// project-scoped one with --project.
func resolveSkillTarget(cmd *cobra.Command) (skillTarget, error) {
	agent := strings.ToLower(strings.TrimSpace(stringFlag(cmd, "agent")))
	if agent == "" {
		agent = "claude"
	}
	dir := strings.TrimSpace(stringFlag(cmd, "dir"))
	project := boolFlag(cmd, "project")

	switch agent {
	case "claude":
		// A dedicated skill directory: <root>/harbor/.
		if dir != "" {
			p, err := filepath.Abs(filepath.Join(dir, skillName))
			return skillTarget{agent, "Claude Code (" + p + ")", kindSkillDir, p}, absErr(err)
		}
		if project {
			p, err := filepath.Abs(filepath.Join(".claude", "skills", skillName))
			return skillTarget{agent, "Claude Code (this project)", kindSkillDir, p}, absErr(err)
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return skillTarget{}, homeErr(err)
		}
		p := filepath.Join(home, ".claude", "skills", skillName)
		return skillTarget{agent, "Claude Code (global)", kindSkillDir, p}, nil

	case "codex":
		// A shared AGENTS.md we splice a managed block into.
		if dir != "" {
			p, err := filepath.Abs(filepath.Join(dir, "AGENTS.md"))
			return skillTarget{agent, "Codex (managed block in " + p + ")", kindManagedDoc, p}, absErr(err)
		}
		if project {
			p, err := filepath.Abs("AGENTS.md")
			return skillTarget{agent, "Codex (managed block in ./AGENTS.md)", kindManagedDoc, p}, absErr(err)
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return skillTarget{}, homeErr(err)
		}
		p := filepath.Join(home, ".codex", "AGENTS.md")
		return skillTarget{agent, "Codex (managed block in ~/.codex/AGENTS.md)", kindManagedDoc, p}, nil

	case "cursor":
		// Cursor rules are project-scoped .mdc files; there is no documented
		// file-based global location, so cursor always writes a rule file.
		if dir != "" {
			p, err := filepath.Abs(filepath.Join(dir, skillName+".mdc"))
			return skillTarget{agent, "Cursor (rule " + p + ")", kindSingleDoc, p}, absErr(err)
		}
		p, err := filepath.Abs(filepath.Join(".cursor", "rules", skillName+".mdc"))
		return skillTarget{agent, "Cursor (project rule .cursor/rules/harbor.mdc)", kindSingleDoc, p}, absErr(err)

	default:
		return skillTarget{}, fmt.Errorf("unsupported --agent %q — supported: claude, codex, cursor (or use --dir)", agent)
	}
}

// absErr forwards a filepath.Abs error (kept tiny so resolution stays readable).
func absErr(err error) error { return err }

// homeErr wraps a home-dir lookup failure with a consistent message.
func homeErr(err error) error { return fmt.Errorf("unable to determine home directory: %w", err) }

// ===========================================================================
// Install mechanics
// ===========================================================================

// installSkillTarget dispatches to the right install mechanic for the target's
// kind. now is injected so backup names are deterministic in tests.
func installSkillTarget(tgt skillTarget, force bool, now time.Time) (skillInstallResult, error) {
	switch tgt.kind {
	case kindSkillDir:
		return installSkillDir(tgt.path, force, now)
	case kindSingleDoc:
		return installSingleDoc(tgt.path, force, now)
	case kindManagedDoc:
		return installManagedDoc(tgt.path, force, now)
	default:
		return skillInstallResult{}, fmt.Errorf("unknown install kind")
	}
}

// installSkillDir installs the multi-file Claude skill at dest. When dest already
// holds the bundled skill (matched by SKILL.md) and force is false it is a no-op;
// otherwise any existing directory is renamed to a timestamped backup before the
// fresh files are written.
func installSkillDir(dest string, force bool, now time.Time) (skillInstallResult, error) {
	res := skillInstallResult{Path: dest, SkillVersion: skillVersion, CLIVersion: resolveVersion()}

	current, err := skillDirUpToDate(dest)
	if err != nil {
		return res, err
	}
	if current && !force {
		res.UpToDate = true
		return res, nil
	}

	// Preserve any existing skill (including user edits) by moving it aside.
	if _, statErr := os.Stat(dest); statErr == nil {
		backup := uniqueBackupPath(dest + ".backup-" + now.Format("20060102-150405"))
		if rerr := os.Rename(dest, backup); rerr != nil {
			return res, fmt.Errorf("unable to back up the existing skill: %w", rerr)
		}
		res.Backup = backup
	} else if !os.IsNotExist(statErr) {
		return res, statErr
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return res, fmt.Errorf("unable to create the skill directory: %w", err)
	}
	if err := writeBundledSkill(dest); err != nil {
		return res, fmt.Errorf("unable to write the skill files: %w", err)
	}
	return res, nil
}

// installSkillInto installs the Claude-style skill directory under root/harbor.
// It is the convenience entry used by tests and mirrors the default --agent
// claude destination.
func installSkillInto(root string, force bool, now time.Time) (skillInstallResult, error) {
	return installSkillDir(filepath.Join(root, skillName), force, now)
}

// installSingleDoc installs a dedicated single-file rule (Cursor .mdc) we fully
// own. An existing file is renamed to a backup before the fresh content is
// written; identical content is a no-op unless force.
func installSingleDoc(path string, force bool, now time.Time) (skillInstallResult, error) {
	res := skillInstallResult{Path: path, SkillVersion: skillVersion, CLIVersion: resolveVersion()}

	content, err := renderCursorRule()
	if err != nil {
		return res, err
	}

	existing, rerr := os.ReadFile(path)
	if rerr != nil && !os.IsNotExist(rerr) {
		return res, rerr
	}
	if rerr == nil && string(existing) == content && !force {
		res.UpToDate = true
		return res, nil
	}

	// Move any existing file aside (preserves user edits).
	if rerr == nil {
		backup := uniqueBackupPath(path + ".backup-" + now.Format("20060102-150405"))
		if mverr := os.Rename(path, backup); mverr != nil {
			return res, fmt.Errorf("unable to back up %s: %w", path, mverr)
		}
		res.Backup = backup
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("unable to create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return res, fmt.Errorf("unable to write %s: %w", path, err)
	}
	return res, nil
}

// installManagedDoc splices the Harbor block into a shared doc (Codex AGENTS.md),
// preserving the rest of the file. The prior file (if any) is copied to a backup
// before the new content is written; an unchanged result is a no-op unless force.
func installManagedDoc(path string, force bool, now time.Time) (skillInstallResult, error) {
	res := skillInstallResult{Path: path, SkillVersion: skillVersion, CLIVersion: resolveVersion()}

	block, err := renderCodexBlock()
	if err != nil {
		return res, err
	}

	existing, rerr := os.ReadFile(path)
	if rerr != nil && !os.IsNotExist(rerr) {
		return res, rerr
	}
	newContent := spliceManagedBlock(existing, block)
	if string(existing) == newContent && !force {
		res.UpToDate = true
		return res, nil
	}

	// Back up the prior file by copying it (we keep writing to the same path).
	if len(existing) > 0 {
		backup := uniqueBackupPath(path + ".backup-" + now.Format("20060102-150405"))
		if werr := os.WriteFile(backup, existing, 0o644); werr != nil {
			return res, fmt.Errorf("unable to back up %s: %w", path, werr)
		}
		res.Backup = backup
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("unable to create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return res, fmt.Errorf("unable to write %s: %w", path, err)
	}
	return res, nil
}

// ===========================================================================
// Rendering (single-doc / managed-doc agents)
// ===========================================================================

// agentDocPreamble is prepended to the single-file/managed renderings so the
// agent knows the companion guides live in the binary, fetched on demand.
const agentDocPreamble = "> **Harbor CLI skill.** You can manage the user's Harbor notes with the `harbor`\n" +
	"> command-line tool. Two companion guides are bundled in the binary — read them\n" +
	"> on demand by running these shell commands when a task needs them:\n" +
	">\n" +
	"> - `harbor skill show formatting.md` — rich-formatting cookbook (Markdown/HTML, colors, tables, embeds, note links)\n" +
	"> - `harbor skill show reference.md` — full command + flag reference\n"

// skillBodyForAgents returns the SKILL.md body (frontmatter stripped) prefixed by
// the on-demand preamble — the shared core for non-Claude agents.
func skillBodyForAgents() (string, error) {
	data, err := skillFS.ReadFile(skillSourceDir + "/SKILL.md")
	if err != nil {
		return "", err
	}
	body := stripFrontmatter(string(data))
	return agentDocPreamble + "\n" + body, nil
}

// stripFrontmatter removes a leading YAML frontmatter block (--- … ---) from a
// Markdown string, returning the remaining body.
func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	if idx := strings.Index(s[4:], "\n---\n"); idx >= 0 {
		s = s[4+idx+len("\n---\n"):]
	}
	return strings.TrimLeft(s, "\n")
}

// renderCursorRule renders the bundled skill as a Cursor MDC rule file: MDC
// frontmatter (description + alwaysApply) followed by the skill body.
func renderCursorRule() (string, error) {
	body, err := skillBodyForAgents()
	if err != nil {
		return "", err
	}
	frontmatter := "---\n" +
		"description: Harbor notes — create, edit, format, organize, and search notes via the `harbor` CLI\n" +
		"alwaysApply: false\n" +
		"---\n\n"
	return frontmatter + body, nil
}

// renderCodexBlock renders the bundled skill as a delimited managed block for a
// Codex AGENTS.md (no surrounding newlines; splice normalizes spacing).
func renderCodexBlock() (string, error) {
	body, err := skillBodyForAgents()
	if err != nil {
		return "", err
	}
	return codexBlockBegin + "\n\n" + strings.TrimRight(body, "\n") + "\n\n" + codexBlockEnd, nil
}

// spliceManagedBlock returns existing with the Harbor managed block inserted: it
// replaces the bytes between the markers when present, otherwise appends the
// block (separated by a blank line). The result always ends in a single newline,
// so re-splicing the same block is idempotent.
func spliceManagedBlock(existing []byte, block string) string {
	s := string(existing)
	var out string
	if bi := strings.Index(s, codexBlockBegin); bi >= 0 {
		if ei := strings.Index(s, codexBlockEnd); ei > bi {
			end := ei + len(codexBlockEnd)
			out = s[:bi] + block + s[end:]
		}
	}
	if out == "" {
		if trimmed := strings.TrimRight(s, "\n"); trimmed != "" {
			out = trimmed + "\n\n" + block
		} else {
			out = block
		}
	}
	return strings.TrimRight(out, "\n") + "\n"
}

// ===========================================================================
// Shared helpers
// ===========================================================================

// skillDirUpToDate reports whether dest already holds the bundled skill, by
// comparing every embedded file to its installed counterpart. Any missing or
// differing file means "not up to date" (so it gets rewritten). Extra files the
// user may have added are ignored — they survive an up-to-date no-op, and a real
// update backs up the whole directory anyway.
func skillDirUpToDate(dest string) (bool, error) {
	upToDate := true
	err := fs.WalkDir(skillFS, skillSourceDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		want, rerr := skillFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel := strings.TrimPrefix(p, skillSourceDir+"/")
		got, rerr := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				upToDate = false
				return nil
			}
			return rerr
		}
		if !bytes.Equal(want, got) {
			upToDate = false
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return upToDate, nil
}

// writeBundledSkill copies every embedded skill file into dest, recreating the
// directory tree so the skill can grow to include subfolders later.
func writeBundledSkill(dest string) error {
	return fs.WalkDir(skillFS, skillSourceDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// The embed root maps to dest itself (already created by the caller).
		if p == skillSourceDir {
			return nil
		}
		// Embedded paths use forward slashes; translate to the OS separator.
		rel := strings.TrimPrefix(p, skillSourceDir+"/")
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := skillFS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// uniqueBackupPath returns base, or base-2/base-3/… if base already exists, so a
// second install within the same second never overwrites the first backup.
func uniqueBackupPath(base string) string {
	candidate := base
	for i := 2; ; i++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

// bundledSkillFiles lists the top-level file names in the embedded skill, for a
// friendly "available: …" hint when `skill show` is given an unknown name.
func bundledSkillFiles() []string {
	entries, err := skillFS.ReadDir(skillSourceDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// init wires the skill command flags and registers the tree under rootCmd.
func init() {
	skillInstallCmd.Flags().String("agent", "claude", "Target agent: claude, codex, or cursor")
	skillInstallCmd.Flags().String("dir", "", "Install into this directory instead of the agent default")
	skillInstallCmd.Flags().Bool("project", false, "Install the project-scoped variant instead of the global one")
	skillInstallCmd.Flags().Bool("force", false, "Reinstall even when the bundled skill is already current")

	skillPathCmd.Flags().String("agent", "claude", "Target agent: claude, codex, or cursor")
	skillPathCmd.Flags().String("dir", "", "Directory override")
	skillPathCmd.Flags().Bool("project", false, "Resolve the project-scoped path")

	skillCmd.AddCommand(skillInstallCmd, skillShowCmd, skillPathCmd)
	rootCmd.AddCommand(skillCmd)
}

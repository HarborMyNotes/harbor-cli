// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-23

package cmd

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// supportEmail is the direct-email fallback we point people at when they'd
// rather not use the command.
const supportEmail = "help@harbor.my"

// Attachment caps, mirroring the backend's limits so we fail fast client-side
// with a friendly message instead of round-tripping a 413.
const (
	supportMaxAttachments     = 5
	supportMaxAttachmentBytes = 5 * 1024 * 1024  // 5 MB per file
	supportMaxTotalBytes      = 15 * 1024 * 1024 // 15 MB across all files
)

// supportCategory is one selectable Contact Support category: a fixed slug sent
// to the API, paired with the human label shown in the picker and help.
type supportCategory struct {
	Slug  string
	Label string
}

// supportCategories is the fixed, ordered set of categories the backend accepts.
// The slugs are a closed enum — the command validates --category against them.
var supportCategories = []supportCategory{
	{"feature_request", "I have got a feature request"},
	{"how_to", "I am not sure how something works"},
	{"access_login", "I have got an access / login problem"},
	{"bug", "I think something is broken"},
	{"billing", "I have a billing question"},
	{"emergency", "I have got an emergency"},
	{"other", "Other"},
}

// supportCmd files a Contact Support request from the terminal. Because the user
// is signed in, the server derives their name/email/account from the session —
// the command only collects the category, subject, message, and any attachments,
// and attaches the technical metadata the CLI knows (version, OS, locale, …).
var supportCmd = &cobra.Command{
	Use:     "support",
	Short:   "Contact Harbor support",
	GroupID: groupAccount,
	Long: `File a support request without leaving the terminal.

You're signed in, so Harbor already knows who you are — your name and email are
taken from your account and are not sent by the CLI. The request includes some
technical details (CLI version, OS) so we can help faster.

Pick a category with --category (one of the slugs below), give it a --subject,
and provide the message with --content, --file, or --stdin. On an interactive
terminal any missing field is prompted for; when piped, the flags are required.

Categories:
  feature_request   I have got a feature request
  how_to            I am not sure how something works
  access_login      I have got an access / login problem
  bug               I think something is broken
  billing           I have a billing question
  emergency         I have got an emergency
  other             Other

Prefer email? Write us directly at ` + supportEmail + `.`,
	Example: `  harbor support
  harbor support --category bug --subject "Sync stuck" --content "It has been spinning for an hour."
  harbor support --category how_to --subject "Tags" --file question.txt
  git log | harbor support --category bug --subject "Crash log" --stdin
  harbor support --category bug --subject "Broken export" --content "See screenshot" --attach shot.png`,
	RunE: runSupport,
}

// runSupport resolves the category/subject/message (from flags or interactive
// prompts), reads any attachments within the caps, and POSTs the request.
func runSupport(cmd *cobra.Command, args []string) error {
	c, creds, err := loadClientFromConfig()
	if err != nil {
		return err
	}

	// Prompting is only safe on a real terminal. When stdin is piped (scripts,
	// CI, agents, or --stdin), every field must come from a flag rather than
	// hanging on a prompt that will never be answered.
	interactive := accountIsInteractive()

	body, err := buildSupportBody(cmd, interactive)
	if err != nil {
		return err
	}

	// Reassure the user which account the request is filed under — name/email
	// are derived server-side, never sent from here.
	if !jsonOutput && creds != nil && creds.Email != "" {
		fmt.Println(dim("Filing as " + creds.Email + " — Harbor fills in your name and email from your account."))
	}

	data, err := c.SubmitSupportRequest(body)
	if err != nil {
		return mapSupportError(err)
	}
	printResult(data, displaySupportConfirmation)
	return nil
}

// buildSupportBody assembles the POST /support request body from the command's
// flags (prompting for missing category/subject/message when interactive). It
// returns the wire body: category, subject, message, a metadata object, and an
// attachments array when any were supplied. Kept separate from runSupport so
// the body shape is unit-testable without a client or network.
func buildSupportBody(cmd *cobra.Command, interactive bool) (map[string]any, error) {
	// Category: validated against the fixed slug set; picker when interactive.
	category, err := resolveCategory(cmd, interactive)
	if err != nil {
		return nil, err
	}

	// Subject: a one-line summary; prompted when interactive and missing.
	subject, err := resolveSubject(cmd, interactive)
	if err != nil {
		return nil, err
	}

	// Message: the shared --content/--file/--stdin block, else an interactive
	// prompt. Empty messages are rejected either way.
	message, err := resolveMessage(cmd, interactive)
	if err != nil {
		return nil, err
	}

	// Attachments: 0..N files, base64-encoded within the size caps.
	attachments, err := buildSupportAttachments(supportAttachPaths(cmd))
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"category": category,
		"subject":  subject,
		"message":  message,
		"metadata": supportMetadata(),
	}
	if len(attachments) > 0 {
		body["attachments"] = attachments
	}
	return body, nil
}

// ===========================================================================
// Field resolution
// ===========================================================================

// resolveCategory returns a valid category slug from --category, or prompts for
// one interactively. It errors when the slug is unknown, or when it is missing
// and we cannot prompt (non-interactive).
func resolveCategory(cmd *cobra.Command, interactive bool) (string, error) {
	category := strings.TrimSpace(stringFlag(cmd, "category"))
	if category == "" {
		if !interactive {
			return "", fmt.Errorf("--category is required (one of: %s)", strings.Join(categorySlugs(), ", "))
		}
		var err error
		if category, err = promptCategory(); err != nil {
			return "", err
		}
	}
	if !isValidCategory(category) {
		return "", fmt.Errorf("invalid --category %q (want one of: %s)", category, strings.Join(categorySlugs(), ", "))
	}
	return category, nil
}

// resolveSubject returns the --subject, or prompts for it interactively. An
// empty subject is rejected.
func resolveSubject(cmd *cobra.Command, interactive bool) (string, error) {
	subject := strings.TrimSpace(stringFlag(cmd, "subject"))
	if subject == "" {
		if !interactive {
			return "", errors.New("--subject is required")
		}
		line, err := promptLine("Subject: ")
		if err != nil {
			return "", err
		}
		subject = strings.TrimSpace(line)
	}
	if subject == "" {
		return "", errors.New("subject cannot be empty")
	}
	return subject, nil
}

// resolveMessage returns the message body from the shared --content/--file/
// --stdin flags, or an interactive prompt when none was provided. An empty
// message is rejected.
func resolveMessage(cmd *cobra.Command, interactive bool) (string, error) {
	message, _, hasContent, err := readContent(cmd)
	if err != nil {
		return "", err
	}
	if !hasContent {
		if !interactive {
			return "", errors.New("a message is required — pass --content, --file, or --stdin")
		}
		if message, err = promptLine("Message (use --file for a longer message): "); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("the message cannot be empty")
	}
	return message, nil
}

// promptCategory renders the numbered category menu and reads a choice, accepting
// either the 1-based number or the slug itself.
func promptCategory() (string, error) {
	fmt.Println("What can we help you with?")
	for i, cat := range supportCategories {
		fmt.Printf("  %d) %s\n", i+1, cat.Label)
	}
	line, err := promptLine(fmt.Sprintf("Choose 1-%d (or type a slug): ", len(supportCategories)))
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("no category chosen")
	}
	// A bare number picks by position.
	if n, err := strconv.Atoi(line); err == nil {
		if n < 1 || n > len(supportCategories) {
			return "", fmt.Errorf("choice %d is out of range (1-%d)", n, len(supportCategories))
		}
		return supportCategories[n-1].Slug, nil
	}
	// Otherwise it must be a slug.
	if isValidCategory(line) {
		return line, nil
	}
	return "", fmt.Errorf("unknown category %q (want a number 1-%d or one of: %s)", line, len(supportCategories), strings.Join(categorySlugs(), ", "))
}

// isValidCategory reports whether slug is one of the fixed category slugs.
func isValidCategory(slug string) bool {
	for _, cat := range supportCategories {
		if cat.Slug == slug {
			return true
		}
	}
	return false
}

// categorySlugs returns the category slugs in order, for help and error text.
func categorySlugs() []string {
	slugs := make([]string, len(supportCategories))
	for i, cat := range supportCategories {
		slugs[i] = cat.Slug
	}
	return slugs
}

// ===========================================================================
// Attachments
// ===========================================================================

// supportAttachPaths returns the repeatable --attach paths.
func supportAttachPaths(cmd *cobra.Command) []string {
	paths, _ := cmd.Flags().GetStringArray("attach")
	return paths
}

// buildSupportAttachments reads each attachment path into a base64 payload,
// enforcing the file-count, per-file, and total-size caps up front so we fail
// with a clear message rather than a server 413. Each entry is
// { filename, content_type, data }.
func buildSupportAttachments(paths []string) ([]map[string]any, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > supportMaxAttachments {
		return nil, fmt.Errorf("too many attachments: %d (the limit is %d files)", len(paths), supportMaxAttachments)
	}

	var total int64
	out := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read attachment %q: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("attachment %q is a directory", path)
		}
		if info.Size() > supportMaxAttachmentBytes {
			return nil, fmt.Errorf("attachment %q is %s — the per-file limit is 5 MB", path, bytesHuman(float64(info.Size())))
		}
		total += info.Size()
		if total > supportMaxTotalBytes {
			return nil, errors.New("attachments exceed the 15 MB total limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read attachment %q: %w", path, err)
		}
		out = append(out, map[string]any{
			"filename":     filepath.Base(path),
			"content_type": attachmentMIME(path, data),
			"data":         base64.StdEncoding.EncodeToString(data),
		})
	}
	return out, nil
}

// attachmentMIME guesses an attachment's MIME type: first by file extension,
// then by sniffing the leading bytes. It mirrors client/files.go's detectMIME
// but always returns a concrete type (http.DetectContentType falls back to
// application/octet-stream), which the server can then honor.
func attachmentMIME(path string, data []byte) string {
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		if i := strings.IndexByte(ct, ';'); i >= 0 { // drop "; charset=utf-8"
			ct = strings.TrimSpace(ct[:i])
		}
		return ct
	}
	return http.DetectContentType(data)
}

// ===========================================================================
// Metadata
// ===========================================================================

// supportMetadata gathers the technical context the CLI knows about this
// install, so support can triage without a back-and-forth. Empty values are
// omitted. Field names follow the shared Contact Support metadata contract.
func supportMetadata() map[string]any {
	meta := map[string]any{
		"app_version":  version,           // CLI version (ldflags), "dev" locally
		"app_build":    runtime.Version(), // Go toolchain the binary was built with
		"os":           runtime.GOOS,      // darwin / linux / windows
		"device_model": runtime.GOARCH,    // amd64 / arm64 — the CLI's "device"
	}
	if v := osVersion(); v != "" {
		meta["os_version"] = v
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		meta["device_name"] = host
	}
	if v := detectLocale(); v != "" {
		meta["locale"] = v
	}
	if v := detectTimezone(); v != "" {
		meta["timezone"] = v
	}
	return meta
}

// osVersion returns a best-effort OS/kernel version via `uname -r` on unix-like
// systems, or "" when it cannot be determined (e.g. Windows, or uname absent).
func osVersion() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectLocale reads the POSIX locale from the environment (LC_ALL, then
// LC_MESSAGES, then LANG), stripping any encoding/modifier suffix so
// "en_US.UTF-8" becomes "en_US". Returns "" when unset.
func detectLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			if i := strings.IndexAny(v, ".@"); i >= 0 {
				v = v[:i]
			}
			return v
		}
	}
	return ""
}

// detectTimezone reports the local timezone: the TZ env var when set, else the
// system location name (an IANA name like America/New_York on most systems),
// falling back to the zone abbreviation.
func detectTimezone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	if name := time.Now().Location().String(); name != "" && name != "Local" {
		return name
	}
	abbr, _ := time.Now().Zone()
	return abbr
}

// ===========================================================================
// Error mapping & display
// ===========================================================================

// mapSupportError turns the standard error envelope into friendly guidance.
// validation_failed falls through so renderError prints the per-field details.
func mapSupportError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "payload_too_large":
			return errors.New("your attachments are too large — keep each file under 5 MB and the total under 15 MB")
		case "unsupported_media":
			return errors.New("one of your attachments has an unsupported file type")
		}
	}
	return err
}

// displaySupportConfirmation renders the success confirmation, including the
// reference id when the server returns one, and the direct-email fallback.
func displaySupportConfirmation(data []byte) {
	d := parseJSON(client.UnwrapData(data))
	fmt.Println(boolMark(true) + " " + bold("Thanks — your request is in."))
	fmt.Println("Someone from our team will get back to you, typically within 24 hours.")
	if d != nil {
		if ref := str(d, "reference"); ref != "" {
			fmt.Println()
			printKV([][2]string{{"Reference", bold(ref)}})
		}
	}
	fmt.Println(dim("Prefer email? Write us at " + supportEmail + "."))
}

// addSupportFlags registers the support command's flags. Factored out so tests
// can build an isolated command with the same flag set.
func addSupportFlags(cmd *cobra.Command) {
	cmd.Flags().String("category", "", "Support category slug ("+strings.Join(categorySlugs(), ", ")+")")
	cmd.Flags().String("subject", "", "One-line subject / summary")
	// The shared --content/--file/--stdin body block (as used by 'notes create').
	// --format is irrelevant for a plain support message, so it is hidden.
	addContentFlags(cmd)
	_ = cmd.Flags().MarkHidden("format")
	cmd.Flags().StringArray("attach", nil, "Attach a file (repeatable; max 5 files, 5 MB each, 15 MB total)")
}

// init wires the support flags and registers the command under the root.
func init() {
	addSupportFlags(supportCmd)
	rootCmd.AddCommand(supportCmd)
}

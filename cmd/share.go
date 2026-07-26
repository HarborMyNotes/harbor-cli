// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// shareCmd is the parent for public note sharing: publish a note as a public,
// read-only page, revoke that link, and open (resolve) a shared note by its
// public token without authentication.
var shareCmd = &cobra.Command{
	Use:     "share",
	Short:   "Publish notes as public, read-only links",
	GroupID: groupContent,
	Long: `Share a note as a public, read-only web page reachable by an unguessable
token, revoke that link, or open a shared note by its token.

Publishing returns a public URL anyone can open without a Harbor account.
A note is either fully private or public read-only — there is no fine-grained
sharing. Encrypted notes can never be shared (the server only holds ciphertext).`,
}

// sharePublishCmd publishes a note as a public page and prints the public URL.
var sharePublishCmd = &cobra.Command{
	Use:   "publish <note-id>",
	Short: "Publish a note as a public, read-only page",
	Args:  cobra.ExactArgs(1),
	Long: `Publish a note as a public, read-only page and print its public URL.

Idempotent: re-publishing an already-public note returns the existing link
unchanged. Use --slug to request a friendly, URL-safe label (the unguessable
token, not the slug, is what actually resolves the page).`,
	Example: `  harbor share publish 9c2e...
  harbor share publish 9c2e... --slug quarterly-plan
  harbor share publish 9c2e... --json | jq -r '.data.public_url'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		var body map[string]any
		if slug := stringFlag(cmd, "slug"); slug != "" {
			body = map[string]any{"slug": slug}
		}
		data, fresh, err := c.PublishShare(args[0], body)
		if err != nil {
			return mapShareError(err)
		}
		printResult(data, sharePublishDisplay(fresh))
		return nil
	},
}

// shareUnpublishCmd revokes a note's public link.
var shareUnpublishCmd = &cobra.Command{
	Use:   "unpublish <note-id>",
	Short: "Revoke a note's public link",
	Args:  cobra.ExactArgs(1),
	Long: `Revoke a note's public link, making it private again.

Idempotent: a note that is already private, was never shared, or does not
exist still succeeds — there is nothing to reveal and nothing to undo.`,
	Example: "  harbor share unpublish 9c2e...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.UnpublishShare(args[0]); err != nil {
			return err
		}
		fmt.Println("Note unpublished — the public link no longer works.")
		return nil
	},
}

// shareOpenCmd resolves and renders a shared note by its public token. It runs
// without authentication, so it works even when logged out.
var shareOpenCmd = &cobra.Command{
	Use:   "open <token>",
	Short: "Open a shared note by its public token (no login required)",
	Args:  cobra.ExactArgs(1),
	Long: `Resolve and render a shared note by its public token.

This is a public read: it uses no credentials and works when logged out.
For anti-enumeration, every failure mode — unknown, revoked, deleted, or
no-longer-public — returns the same generic "not found".`,
	Example: `  harbor share open Xa9Kd...q2
  harbor share open Xa9Kd...q2 --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// PUBLIC endpoint: build a client with no credentials so it works when
		// logged out and never leaks a bearer token to the public route.
		c := newAnonymousClient()
		data, err := c.PublicNote(args[0])
		if err != nil {
			return mapShareError(err)
		}
		printResult(data, displayPublicNote)
		return nil
	},
}

// mapShareError gives friendly messages for share-specific codes. The public
// read's generic not_found is left untranslated so the honest, enumeration-safe
// server message is shown as-is.
func mapShareError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "encrypted_cannot_share":
			return errors.New("this note is encrypted, so it can never be shared as a public page")
		case "slug_taken":
			return errors.New("that slug is already in use — choose a different --slug")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// sharePublishDisplay returns a display function for a publish response. The
// public URL is the key output, so it is printed prominently. fresh controls
// the headline (newly published vs already public).
func sharePublishDisplay(fresh bool) func([]byte) {
	return func(data []byte) {
		s := parseJSON(client.UnwrapData(data))
		if s == nil {
			fmt.Println(string(data))
			return
		}
		if fresh {
			fmt.Println("Published.")
		} else {
			fmt.Println("Already public — returning the existing link.")
		}
		fmt.Println()
		// The public URL is the headline; show it bold and on its own line.
		fmt.Println(bold(str(s, "public_url")))
		fmt.Println()
		printKV([][2]string{
			{"Slug", str(s, "slug")},
			{"Token", str(s, "share_token")},
			{"Public", boolMark(boolean(s, "is_public"))},
			{"Views", str(s, "view_count")},
			{"Created", epochMS(num(s, "created_at"))},
		})
	}
}

// displayPublicNote renders a resolved public note: a metadata header, the
// body (HTML stripped to readable text), and any attachment download URLs.
func displayPublicNote(data []byte) {
	n := parseJSON(client.UnwrapData(data))
	if n == nil {
		fmt.Println(string(data))
		return
	}
	pairs := [][2]string{
		{"Title", bold(str(n, "title"))},
	}
	if a := str(n, "author"); a != "" {
		pairs = append(pairs, [2]string{"Author", a})
	}
	if u := str(n, "source_url"); u != "" {
		pairs = append(pairs, [2]string{"Source", u})
	}
	pairs = append(pairs,
		[2]string{"Created", epochMS(num(n, "created_at"))},
		[2]string{"Updated", epochMS(num(n, "updated_at"))},
		[2]string{"Views", str(n, "view_count")},
	)
	printKV(pairs)

	fmt.Println()
	body := stripHTML(str(n, "content_html"))
	if body != "" {
		fmt.Println(body)
	}

	attachments := toSlice(n["attachments"])
	if len(attachments) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(dim("Attachments:"))
	headers := []string{"FILENAME", "MIME", "SIZE", "URL"}
	rows := make([][]string, 0, len(attachments))
	for _, a := range attachments {
		rows = append(rows, []string{
			truncate(str(a, "filename"), 30),
			str(a, "mime"),
			bytesHuman(num(a, "size")),
			str(a, "url"),
		})
	}
	printTable(headers, rows)
}

func init() {
	sharePublishCmd.Flags().String("slug", "", "Custom URL-safe slug (sanitized; a slug is generated when omitted)")

	shareCmd.AddCommand(sharePublishCmd, shareUnpublishCmd, shareOpenCmd)
	rootCmd.AddCommand(shareCmd)
}

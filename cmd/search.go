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

// searchCmd runs a full-text query.
var searchCmd = &cobra.Command{
	Use:     "search <query>",
	Short:   "Full-text search across notes and attachments",
	GroupID: groupContent,
	Args:    cobra.MinimumNArgs(1),
	Long: `Search notes and OCR'd attachments with an Evernote-style query grammar.

Query operators (combine freely; multiple bare words AND together):
  tag:VALUE          note carries this tag (tag:"two words" for spaces)
  notebook:VALUE     note in this notebook (by id or name)
  intitle:VALUE      term appears in the note title
  resource:RTYPE     note owns an attachment of: image|pdf|audio|application|any
  created:RANGE      created date: YYYYMMDD | YYYYMMDD..YYYYMMDD | day-N
  updated:RANGE      last-updated date (same forms)
  "exact phrase"     consecutive, in-order words
  term*              prefix match (recei* → receipt, receive…)
  -token             negate any token (subtracts from the result set)`,
	Example: `  harbor search budget
  harbor search 'tag:finance resource:pdf "q3 plan"'
  harbor search 'report -draft' --order -updated_at
  harbor search invoice --json | jq '.data[] | select(.type=="attachment")'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		params["q"] = strings.Join(args, " ")
		if s := stringFlag(cmd, "types"); s != "" {
			params["types"] = s
		}
		if s := stringFlag(cmd, "notebook"); s != "" {
			params["notebook_id"] = s
		}
		if boolFlag(cmd, "no-snippet") {
			params["snippet"] = "false"
		}
		data, err := c.Search(params)
		if err != nil {
			return mapSearchError(err)
		}
		printResult(data, displaySearch)
		return nil
	},
}

// searchCoordinatesCmd returns OCR highlight boxes for an attachment.
var searchCoordinatesCmd = &cobra.Command{
	Use:   "coordinates",
	Short: "OCR highlight coordinates for an attachment (best paired with --json)",
	Long:  "Return per-page bounding boxes of matched OCR words on one attachment, for drawing highlight overlays. Provide either --query or --terms.",
	Example: `  harbor search coordinates --resource-id 0f9c... --query budget --json
  harbor search coordinates --resource-id 0f9c... --terms budget,q3 --page 0`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		resourceID := stringFlag(cmd, "resource-id")
		if resourceID == "" {
			return errors.New("--resource-id is required")
		}
		query := stringFlag(cmd, "query")
		terms := stringFlag(cmd, "terms")
		if query == "" && terms == "" {
			return errors.New("pass --query or --terms")
		}
		params := map[string]string{"resource_id": resourceID}
		if query != "" {
			params["q"] = query
		}
		if terms != "" {
			params["terms"] = terms
		}
		if cmd.Flags().Changed("page") {
			params["page"] = fmt.Sprintf("%d", intFlag(cmd, "page"))
		}
		if cmd.Flags().Changed("max-boxes") {
			params["max_boxes"] = fmt.Sprintf("%d", intFlag(cmd, "max-boxes"))
		}
		data, err := c.SearchCoordinates(params)
		if err != nil {
			return mapSearchError(err)
		}
		printResult(data, displayCoordinates)
		return nil
	},
}

// mapSearchError gives friendly messages for search-specific codes.
func mapSearchError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "encrypted_not_searchable":
			return errors.New("encrypted attachments (or attachments on encrypted notes) are never searchable")
		case "ocr_not_ready":
			return errors.New("OCR has not finished for this attachment yet — try again shortly")
		}
	}
	return err
}

// ===========================================================================
// Display
// ===========================================================================

// displaySearch renders mixed note/attachment hits as a unified table.
func displaySearch(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"TYPE", "ID", "TITLE / FILENAME", "SNIPPET", "SCORE", "COORD"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		h := parseJSON(raw)
		typ := str(h, "type")
		var id, label, coord string
		if typ == "attachment" {
			id = str(h, "resource_id")
			label = str(h, "filename")
			coord = boolMark(boolean(h, "has_coordinates"))
		} else {
			id = str(h, "note_id")
			label = str(h, "title")
			coord = dim("—")
		}
		rows = append(rows, []string{
			typ,
			id,
			truncate(label, 28),
			truncate(stripHTML(str(h, "snippet")), 44),
			fmt.Sprintf("%.2f", num(h, "score")),
			coord,
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayCoordinates renders OCR highlight boxes per page.
func displayCoordinates(data []byte) {
	d := parseJSON(client.UnwrapData(data))
	if d == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"Resource", str(d, "resource_id")},
		{"MIME", str(d, "mime")},
		{"Pages", str(d, "page_count")},
		{"Terms", strings.Join(toStringSlice(d["terms"]), ", ")},
		{"Truncated", boolMark(boolean(d, "truncated"))},
	})
	for _, page := range toSlice(d["pages"]) {
		fmt.Printf("\n%s %s\n", dim("page"), str(page, "page"))
		matches := toSlice(page["matches"])
		rows := make([][]string, 0, len(matches))
		for _, m := range matches {
			box := nested(m, "box")
			boxStr := "—"
			if box != nil {
				boxStr = fmt.Sprintf("%.0f,%.0f %.0fx%.0f", num(box, "x"), num(box, "y"), num(box, "w"), num(box, "h"))
			}
			rows = append(rows, []string{
				str(m, "term"),
				str(m, "word"),
				str(m, "word_index"),
				boxStr,
				fmt.Sprintf("%.2f", num(m, "confidence")),
			})
		}
		printTable([]string{"TERM", "WORD", "INDEX", "BOX (px)", "CONF"}, rows)
		if phrases := toSlice(page["phrases"]); len(phrases) > 0 {
			fmt.Println(dim(fmt.Sprintf("  %d phrase span(s) matched", len(phrases))))
		}
	}
}

func init() {
	addPagingFlags(searchCmd)
	searchCmd.Flags().String("types", "", "Hit types to return: note,attachment")
	searchCmd.Flags().String("notebook", "", "Restrict to this notebook id")
	searchCmd.Flags().Bool("no-snippet", false, "Skip snippet/highlight generation")

	searchCoordinatesCmd.Flags().String("resource-id", "", "Attachment resource id (required)")
	searchCoordinatesCmd.Flags().String("query", "", "Query string whose terms are highlighted")
	searchCoordinatesCmd.Flags().String("terms", "", "Comma-separated literal terms to highlight")
	searchCoordinatesCmd.Flags().Int("page", 0, "Restrict to a single 0-based page index")
	searchCoordinatesCmd.Flags().Int("max-boxes", 0, "Max boxes to return (default 1000, cap 2000)")

	searchCmd.AddCommand(searchCoordinatesCmd)
	rootCmd.AddCommand(searchCmd)
}

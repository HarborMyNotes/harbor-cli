// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// importCmd is the parent for importing external data into Harbor. Today it
// holds the Evernote ENEX importer and its job-status poller.
var importCmd = &cobra.Command{
	Use:     "import",
	Short:   "Import external data into Harbor (Evernote ENEX)",
	GroupID: groupAccount,
	Long: `Bring data into Harbor from other tools. The Evernote importer reads an
.enex export — each <note> becomes a note, its tags and attachments are
recreated, and every imported row flows through sync and search.`,
}

// importEnexCmd uploads an .enex file and starts an import.
var importEnexCmd = &cobra.Command{
	Use:   "enex <file.enex>",
	Short: "Import an Evernote .enex export",
	Args:  cobra.ExactArgs(1),
	Long: `Upload an Evernote .enex file. Small imports run inline and return their
counts immediately; larger ones are queued and return a job id you can poll with
'harbor import status'. By default the notes land in a new notebook named after
the file — use --notebook to force them into an existing (non-encrypted) one.`,
	Example: `  harbor import enex evernote.enex
  harbor import enex backup.enex --notebook 5b1f2c9a --filename "My Export.enex"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}

		// Open the .enex file; its base name doubles as the multipart filename
		// (and the name of the auto-created notebook) unless --filename overrides.
		path := args[0]
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("cannot open file: %w", err)
		}
		defer f.Close()

		filename := stringFlag(cmd, "filename")
		if filename == "" {
			filename = filepathBase(path)
		}

		data, status, err := c.ImportENEX(filename, stringFlag(cmd, "notebook"), f)
		if err != nil {
			return mapImportExportError(err)
		}
		// Stash whether the import ran inline (201) or was enqueued (202) so the
		// table renderer can print the right follow-up hint. printResult passes
		// only the body, so the status rides along in a package-level var.
		importEnexAsync = status == http.StatusAccepted
		printResult(data, displayImportJob)
		return nil
	},
}

// importStatusCmd polls an import job's counters and per-note errors.
var importStatusCmd = &cobra.Command{
	Use:     "status <job-id>",
	Short:   "Poll an ENEX import job",
	Args:    cobra.ExactArgs(1),
	Long:    "Fetch the live counters for an import job plus the list of per-note failures (up to 100). A job-level failure appears as a single error with note index -1.",
	Example: "  harbor import status 0f9c2b1e-1a2b-3c4d-5e6f-7a8b9c0d1e2f",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ImportStatus(args[0])
		if err != nil {
			return mapImportExportError(err)
		}
		printResult(data, displayImportStatus)
		return nil
	},
}

// exportCmd is the parent for exporting Harbor data out to portable formats.
var exportCmd = &cobra.Command{
	Use:     "export",
	Short:   "Export Harbor data to a portable format (Evernote ENEX)",
	GroupID: groupAccount,
	Long: `Export your notes back out of Harbor. The ENEX exporter writes a valid,
round-trippable Evernote <en-export> document for a whole notebook or an explicit
note selection.`,
}

// exportEnexCmd streams a notebook or note selection out as a raw .enex file.
//
// --notebook is DEPRECATED here (issue #1108): a notebook export belongs on the
// job path, 'harbor account export --format enex --notebook <id>', which gives
// it progress, the ready email, 72-hour retention, a delete action and the
// account's export slot — none of which a synchronous response can. It also
// cannot safely export a LARGE notebook: it marshals the whole document into
// memory. It still works and is not going away while clients depend on it, so
// this command keeps working byte-for-byte and merely points at the successor.
// --notes is NOT deprecated: per-note scoping has no successor to move to.
var exportEnexCmd = &cobra.Command{
	Use:   "enex",
	Short: "Export notes to an Evernote .enex file",
	Long: `Export a notebook or an explicit set of notes to a raw .enex document.
Provide exactly one of --notebook or --notes. With --include-resources each
linked attachment's bytes are inlined as base64. Encrypted notes hold only
ciphertext, so they are skipped and the count is reported. The document is
written to --output (use - for stdout).

DEPRECATED: --notebook. Use 'harbor account export --format enex --notebook
<id>' instead — that runs a real export job, so a big notebook gets progress,
the "your export is ready" email, 72-hour retention and a delete action, and it
streams rather than building the whole document in memory. This command is
unchanged and is not going away; it is simply the wrong tool for a whole
notebook. Note the two do not produce the same thing: this one writes a single
.enex of a notebook's LIVE notes, while the job path writes a ZIP and, being the
GDPR archive, also includes notes sitting in the TRASH.

--notes is not deprecated — a note selection has no successor and belongs here.`,
	Example: `  harbor export enex --notes n1,n2,n3 --include-resources --output sel.enex
  harbor export enex --notebook 5b1f2c9a --output backup.enex   # deprecated
  harbor account export --format enex --notebook 5b1f2c9a       # the successor`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}

		// Targeting is XOR: exactly one of --notebook / --notes must be set.
		notebook := stringFlag(cmd, "notebook")
		notes := splitCSV(stringFlag(cmd, "notes"))
		if (notebook == "") == (len(notes) == 0) {
			return errors.New("provide exactly one of --notebook or --notes")
		}

		out := stringFlag(cmd, "output")
		if out == "" {
			return errors.New("--output is required (use - for stdout)")
		}

		resp, err := c.ExportENEX(notebook, notes, boolFlag(cmd, "include-resources"))
		if err != nil {
			return mapImportExportError(err)
		}
		defer resp.Body.Close()

		// The skipped-encrypted count rides in a header (the body is raw XML, so
		// it cannot carry a JSON field). Read it before streaming the body out.
		skipped := resp.Header.Get("X-Skipped-Encrypted")

		// The server marks a notebook-scoped request deprecated in its headers.
		// Surface that once, on stderr, so it cannot corrupt a `--output -` pipe.
		if notice := exportEnexDeprecationNotice(resp.Header.Get("Deprecation"), notebook); notice != "" {
			fmt.Fprintln(os.Stderr, notice)
		}

		n, err := writeOutput(out, resp.Body)
		if err != nil {
			return err
		}

		// Keep stdout pristine when piping the file there; otherwise report the
		// byte count and any skipped (encrypted) notes to the user.
		if out != "-" {
			fmt.Printf("Wrote %s to %s\n", bytesHuman(float64(n)), out)
			if skip := importExportSkipCount(skipped); skip > 0 {
				fmt.Printf("%s %d encrypted %s skipped (no key on the server)\n",
					dim("note:"), skip, importExportPluralize(skip, "note", "notes"))
			}
		}
		return nil
	},
}

// mapImportExportError gives friendly messages for the import/export codes.
func mapImportExportError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "invalid_enex":
			return errors.New("that file is not a well-formed Evernote .enex (<en-export>) document")
		case "enex_too_large":
			return errors.New("the .enex file exceeds the maximum import size")
		case "cannot_import_into_encrypted":
			return errors.New("cannot import into an encrypted notebook (the server holds no key) — choose a different --notebook")
		}
	}
	return err
}

// ===========================================================================
// Helpers
// ===========================================================================

// importEnexAsync records whether the most recent import enqueued a background
// job (202) versus ran inline (201), so displayImportJob can print the right
// follow-up hint. printResult only forwards the response body, so the status is
// threaded through this package-level var.
var importEnexAsync bool

// filepathBase returns the final element of a path. It is a thin wrapper kept
// local to this domain so the file does not depend on importing path/filepath
// just for one call.
func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

// exportEnexDeprecationNotice returns the warning to print for a notebook-scoped
// ENEX export, or "" when none applies.
//
// The server flags this mode with `Deprecation: true` and a successor-version
// Link (issue #1108), and the flag itself is the other half of the same signal:
// either is enough, so a server that has not shipped the header yet still gets
// the notice, and a future deprecation of a mode this CLI does not know about
// still surfaces. The trashed-notes difference is called out because the
// successor is deliberately NOT a drop-in — the job path is the GDPR archive and
// includes notes in the trash, which this endpoint excludes.
func exportEnexDeprecationNotice(deprecationHeader, notebook string) string {
	deprecated := strings.EqualFold(strings.TrimSpace(deprecationHeader), "true")
	if notebook == "" {
		// A note selection is fully supported and carries no such header. If the
		// server ever says otherwise, relay it rather than swallow it.
		if deprecated {
			return dim("note: the server marked this export mode deprecated — see 'harbor account export'.")
		}
		return ""
	}
	return dim("note: --notebook here is deprecated. Use 'harbor account export --format enex --notebook " + notebook +
		"' — a real export job with progress, an email, retention and a delete action.\n" +
		"      It is not a drop-in: it writes a ZIP, and includes notes in the trash that this command leaves out.")
}

// importExportSkipCount parses the X-Skipped-Encrypted header into a count,
// treating a missing or unparseable value as zero.
func importExportSkipCount(header string) int {
	if header == "" {
		return 0
	}
	n, err := strconv.Atoi(header)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// importExportPluralize returns singular when n == 1, otherwise plural.
func importExportPluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// ===========================================================================
// Display
// ===========================================================================

// displayImportJob renders the summary returned when an import starts: the
// status, the four counters, and a follow-up hint (poll the job when it was
// enqueued, or the job id when it ran inline).
func displayImportJob(data []byte) {
	j := parseJSON(client.UnwrapData(data))
	if j == nil {
		fmt.Println(string(data))
		return
	}
	jobID := str(j, "import_job_id")
	printKV([][2]string{
		{"Job ID", bold(jobID)},
		{"Status", colorizeStatus(str(j, "status"))},
		{"Total notes", trimFloat(num(j, "total_notes"))},
		{"Imported", trimFloat(num(j, "imported_notes"))},
		{"Skipped", trimFloat(num(j, "skipped_notes"))},
		{"Failed", trimFloat(num(j, "failed_notes"))},
	})
	if importEnexAsync {
		fmt.Printf("%s import queued — poll it with: harbor import status %s\n", dim("→"), jobID)
	}
}

// displayImportStatus renders a polled import job: its counters as a detail
// view followed by a table of per-note errors (when any).
func displayImportStatus(data []byte) {
	j := parseJSON(client.UnwrapData(data))
	if j == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"Job ID", bold(str(j, "id"))},
		{"Status", colorizeStatus(str(j, "status"))},
		{"Total notes", trimFloat(num(j, "total_notes"))},
		{"Imported", trimFloat(num(j, "imported_notes"))},
		{"Skipped", trimFloat(num(j, "skipped_notes"))},
		{"Failed", trimFloat(num(j, "failed_notes"))},
		{"Updated", epochMS(num(j, "updated_at"))},
	})

	// errors is always present (never null); render the per-note failures, if
	// any, under the counters. A job-level failure carries note_index -1.
	errs := toSlice(j["errors"])
	if len(errs) == 0 {
		return
	}
	rows := make([][]string, 0, len(errs))
	for _, e := range errs {
		idx := int(num(e, "note_index"))
		idxStr := strconv.Itoa(idx)
		if idx < 0 {
			idxStr = dim("job")
		}
		rows = append(rows, []string{
			idxStr,
			truncate(str(e, "title"), 30),
			truncate(str(e, "reason"), 60),
		})
	}
	fmt.Println(dim("Errors:"))
	printTable([]string{"NOTE", "TITLE", "REASON"}, rows)
}

func init() {
	importEnexCmd.Flags().String("notebook", "", "Force all notes into this existing (non-encrypted) notebook id")
	importEnexCmd.Flags().String("filename", "", "Original file name (names the auto-created notebook; defaults to the file's base name)")
	importCmd.AddCommand(importEnexCmd, importStatusCmd)
	rootCmd.AddCommand(importCmd)

	exportEnexCmd.Flags().String("notebook", "", "DEPRECATED — use 'harbor account export --format enex --notebook <id>'. Exports this notebook's live notes (the successor also includes trashed ones)")
	exportEnexCmd.Flags().String("notes", "", "Export exactly these note ids (comma-separated)")
	exportEnexCmd.Flags().Bool("include-resources", false, "Inline each linked attachment's bytes as base64")
	exportEnexCmd.Flags().String("output", "", "Output path, or - for stdout (required)")
	exportCmd.AddCommand(exportEnexCmd)
	rootCmd.AddCommand(exportCmd)
}

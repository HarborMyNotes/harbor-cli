// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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

// importEnexKind is the import format this command drives. Harbor's whole import
// surface is one format-parameterised pipeline (/api/v1/import/:kind/...), so the
// transport takes the kind as an argument and this is the only place the CLI
// names it — a future Obsidian or Standard Notes command supplies its own.
const importEnexKind = "enex"

// importPresignBatch is how many part URLs are requested per presign call. The
// server caps a batch at 1000, but the URLs expire on a shared clock, so a big
// upload asks for a modest run at a time and signs the next batch only when it
// gets there — that way an hour-long upload never reaches a URL that went stale
// while earlier parts were still in flight.
const importPresignBatch = 50

// importTerminalStates are the statuses an import job never moves out of, i.e.
// the point at which polling it again can show nothing new.
var importTerminalStates = []string{"completed", "partial", "failed", "aborted"}

// importEnexCmd uploads an .enex file straight to storage and starts an import.
var importEnexCmd = &cobra.Command{
	Use:   "enex <file.enex>",
	Short: "Import an Evernote .enex export",
	Args:  cobra.ExactArgs(1),
	Long: `Upload an Evernote .enex file and import it.

The file is pushed straight to Harbor's storage in chunks — its bytes never pass
through the API — and the server then imports it in the background. This command
WAITS for that import to finish and prints the final counters; pass --no-wait to
return as soon as the upload is accepted and poll it later with 'harbor import
status'. Ctrl-C during the upload cancels it cleanly on the server.

By default the notes land in a new notebook named after the file — use
--notebook to force them into an existing (non-encrypted) one.

A completion email is sent only when you are not waiting for the result: this
command prints the outcome itself, so --no-wait asks for one and the default
waiting mode does not. --notify-email=true|false overrides either way.`,
	Example: `  harbor import enex evernote.enex
  harbor import enex backup.enex --notebook 5b1f2c9a --filename "My Export.enex"
  harbor import enex huge.enex --no-wait`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}

		// Open the .enex file; its base name doubles as the declared filename
		// (and the name of the auto-created notebook) unless --filename overrides.
		path := args[0]
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("cannot open file: %w", err)
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("cannot read file: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory, not an .enex file", path)
		}
		// The size is declared up front and is what the server chunks by, so a
		// zero-byte file is caught here rather than as a validation error on a
		// request that could never have worked.
		if info.Size() == 0 {
			return fmt.Errorf("%s is empty — there is nothing to import", path)
		}

		filename := stringFlag(cmd, "filename")
		if filename == "" {
			filename = filepathBase(path)
		}

		data, err := uploadImportFile(c, importEnexKind, f, info.Size(), filename,
			stringFlag(cmd, "notebook"), wantsImportEmail(cmd))
		if err != nil {
			return mapImportExportError(err)
		}

		// --no-wait stops here: the upload landed and the import is queued, so
		// the job id and the poll hint are the whole answer.
		if boolFlag(cmd, "no-wait") {
			importEnexAsync = true
			printResult(data, displayImportJob)
			return importJobFailure(data)
		}

		// Poll to a terminal state. The last body seen is printed either way —
		// on a timeout it is the progress the user waited for, and on success it
		// is the result — and only then does the error decide the exit code.
		jobID := str(parseJSON(client.UnwrapData(data)), "import_job_id")
		final, err := importPollJob(c, importEnexKind, jobID,
			durationFlag(cmd, "poll-interval"), durationFlag(cmd, "timeout"))
		if final != nil {
			printResult(final, displayImportStatus)
		}
		if err != nil {
			return err
		}
		return importJobFailure(final)
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
		data, err := c.ImportStatus(importEnexKind, args[0])
		if err != nil {
			return mapImportExportError(err)
		}
		printResult(data, displayImportStatus)
		return nil
	},
}

// importAbortCmd cancels an upload that never finished handing over its bytes.
//
// It exists because an upload interrupted in a way the CLI could not clean up
// after — the abort call itself failed, the machine lost power, the process was
// killed — leaves a job sitting in awaiting_upload with a half-written multipart
// object behind it. That is the only state this can act on: once the bytes are
// staged the import belongs to the server and there is nothing to abort.
var importAbortCmd = &cobra.Command{
	Use:   "abort <job-id>",
	Short: "Abort an import whose upload never finished",
	Args:  cobra.ExactArgs(1),
	Long: `Cancel an import job that is still awaiting its bytes and release the
partial upload behind it.

You need this only when an upload died without cleaning up after itself — the
CLI aborts automatically on a failed chunk or a Ctrl-C, and tells you this
command's exact invocation on the rare occasion that automatic abort fails.

A job that already has its bytes is the server's to finish and cannot be
aborted; poll it with 'harbor import status' instead.`,
	Example: "  harbor import abort 0f9c2b1e-1a2b-3c4d-5e6f-7a8b9c0d1e2f",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.AbortImportUpload(importEnexKind, args[0]); err != nil {
			return mapImportExportError(err)
		}
		fmt.Println("Upload aborted — nothing was imported.")
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
					dim("note:"), skip, pluralize(skip, "note", "notes"))
			}
		}
		return nil
	},
}

// mapImportExportError gives friendly messages for the import/export codes.
//
// There is deliberately no invalid_enex case: nothing validates the file at
// request time any more, because the server never holds it. A file that is not
// an <en-export> is discovered by the worker and reported on the JOB as
// status failed with a failure_reason — see importReasonSentence.
func mapImportExportError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "enex_too_large":
			return errors.New("that file exceeds the maximum import size")
		case "cannot_import_into_encrypted":
			return errors.New("cannot import into an encrypted notebook (the server holds no key) — choose a different --notebook")
		case "import_upload_incomplete":
			return errors.New("the upload did not finish — not every chunk reached storage, so the file would have imported truncated. Run the import again")
		}
	}
	return err
}

// ===========================================================================
// Direct-to-storage upload
// ===========================================================================

// uploadImportFile runs the four-call direct upload and returns the body of the
// complete call — the job the caller then polls.
//
// The file's bytes go straight to object storage: the API only ever sees the
// declared size, a request for presigned URLs, and the list of ETags they
// answered with. That is what lets an import be arbitrarily large; there is no
// endpoint that takes the bytes, and buffering them here would defeat the point.
//
// Any failure in the upload window — a rejected chunk, a Ctrl-C — aborts the
// upload server-side. Without that the job sits in awaiting_upload with a
// half-written multipart object behind it until a sweeper reclaims it.
func uploadImportFile(c *client.Client, kind string, f *os.File, size int64, filename, notebookID string, notifyEmail bool) ([]byte, error) {
	created, err := c.CreateImportUpload(kind, size, filename, notebookID, notifyEmail)
	if err != nil {
		return nil, err
	}
	plan := parseJSON(client.UnwrapData(created))
	jobID := str(plan, "import_job_id")
	partSize := int64(num(plan, "part_size"))
	partCount := int(num(plan, "part_count"))
	if jobID == "" || partSize <= 0 || partCount <= 0 {
		return nil, fmt.Errorf("the server returned an unusable upload plan (job %q, part_size %d, part_count %d)",
			jobID, partSize, partCount)
	}

	// The interrupt handler is installed only for the upload window. Once the
	// bytes are staged the import belongs to the server, and a Ctrl-C then should
	// do what it always does rather than be swallowed by a cancel nobody reads.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	parts, err := uploadImportParts(ctx, c, kind, jobID, f, size, partSize, partCount)
	interrupted := ctx.Err() != nil
	// The upload window is over either way, so give Ctrl-C back to the runtime
	// now: a second one has to be able to kill the abort call below rather than
	// be swallowed by a handler with nothing left to cancel.
	stop()

	if err != nil {
		_, abortErr := c.AbortImportUpload(kind, jobID)
		if abortErr != nil {
			// Never silent: the job keeps its staged bytes until a sweeper
			// reclaims it, and the id is the only handle the user has on it.
			// Under --json the same facts ride on the returned error instead,
			// so stderr stays a single parseable envelope.
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "%s could not abort the upload: %v\n", colorize("warning:", text.FgYellow), abortErr)
				fmt.Fprintf(os.Stderr, "  the upload is still open — abort it with: harbor import abort %s\n", jobID)
			}
			return nil, abortFailedError(jobID, interrupted)
		}
		if interrupted {
			return nil, fmt.Errorf("import canceled — the upload was aborted and nothing was imported (job %s)", jobID)
		}
		return nil, err
	}
	return c.CompleteImportUpload(kind, jobID, parts)
}

// wantsImportEmail decides whether the server should email when the import
// finishes.
//
// It follows --no-wait unless the user said otherwise: waiting prints the
// outcome to the terminal you are sitting at, so an email is noise, while
// --no-wait is the "I am walking away" case where it is the only signal you get.
// An explicit --notify-email wins over both.
func wantsImportEmail(cmd *cobra.Command) bool {
	if cmd.Flags().Changed("notify-email") {
		return boolFlag(cmd, "notify-email")
	}
	return boolFlag(cmd, "no-wait")
}

// abortFailedError describes an upload whose abort did not go through, carrying
// the job id and the recovery command as structured details.
//
// It is a *client.APIError so --json reports one parseable envelope: the id is
// the only handle left on a job still holding its staged bytes, and making a
// script regex it out of a prose sentence would put it out of reach.
func abortFailedError(jobID string, interrupted bool) error {
	msg := "the upload could not be aborted and is still open"
	if interrupted {
		msg = "import canceled, but the upload could NOT be aborted"
	}
	return &client.APIError{
		Code:    "import_abort_failed",
		Message: fmt.Sprintf("%s (job %s)", msg, jobID),
		Details: map[string]any{
			"job_id":  jobID,
			"recover": "harbor import abort " + jobID,
		},
	}
}

// uploadImportParts slices the file into the plan's chunks and PUTs each one to
// its presigned URL, returning the part/ETag list the complete call assembles
// from.
//
// URLs are presigned a batch at a time rather than all at once: they share one
// expiry clock, so signing 1,600 of them up front would hand the tail of a long
// upload a set of URLs that went stale while the head was still transferring.
// Each chunk is read as a section of the open file, so nothing larger than the
// HTTP buffer is ever held in memory however big the export is.
func uploadImportParts(ctx context.Context, c *client.Client, kind, jobID string, f *os.File, size, partSize int64, partCount int) ([]client.ImportUploadPart, error) {
	parts := make([]client.ImportUploadPart, 0, partCount)
	progress := newImportProgress(size)

	for first := 1; first <= partCount; first += importPresignBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		last := min(first+importPresignBatch-1, partCount)
		numbers := make([]int, 0, last-first+1)
		for n := first; n <= last; n++ {
			numbers = append(numbers, n)
		}

		signed, err := c.PresignImportParts(kind, jobID, numbers)
		if err != nil {
			return nil, err
		}
		urls := importPartURLs(signed)

		for _, n := range numbers {
			url, ok := urls[n]
			if !ok {
				return nil, fmt.Errorf("the server did not presign part %d of %d", n, partCount)
			}
			offset := int64(n-1) * partSize
			length := min(partSize, size-offset)
			// A plan whose part count overruns the file would ask for an empty (or
			// negative) chunk, which the store rejects. Say what is wrong instead.
			if length <= 0 {
				return nil, fmt.Errorf("the server's upload plan asks for %d parts of %d bytes, which overruns the %d-byte file",
					partCount, partSize, size)
			}
			etag, err := c.UploadImportPart(ctx, url, io.NewSectionReader(f, offset, length), length)
			if err != nil {
				return nil, err
			}
			parts = append(parts, client.ImportUploadPart{PartNumber: n, ETag: etag})
			progress.advance(length)
		}
	}
	progress.done()
	return parts, nil
}

// importPartURLs indexes a presign response by part number so the upload loop
// can pair each chunk with its URL regardless of the order they came back in.
func importPartURLs(data []byte) map[int]string {
	out := map[int]string{}
	for _, p := range toSlice(parseJSON(client.UnwrapData(data))["parts"]) {
		if url := str(p, "url"); url != "" {
			out[int(num(p, "part_number"))] = url
		}
	}
	return out
}

// importProgress reports upload progress on STDERR.
//
// Stderr, not stdout, for the same reason the export poller uses it: this is the
// command someone leaves running on a multi-gigabyte file, and it must still be
// possible to pipe 'harbor import enex --json' into jq. On a terminal the line
// is redrawn in place; piped to a file it prints one line per 10% so a log stays
// readable instead of collecting a thousand carriage returns.
type importProgress struct {
	total   int64
	sent    int64
	lastPct int
	tty     bool
}

// newImportProgress starts a progress reporter for a transfer of total bytes.
func newImportProgress(total int64) *importProgress {
	return &importProgress{
		total:   total,
		lastPct: -1,
		tty:     term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// advance records another n bytes transferred and redraws when the reported
// figure would actually change.
func (p *importProgress) advance(n int64) {
	p.sent += n
	if jsonOutput || p.total <= 0 {
		return
	}
	pct := int(p.sent * 100 / p.total)
	if pct == p.lastPct {
		return
	}
	// Off a terminal there is no redraw, so report in tens rather than leaving a
	// hundred lines in a log — but never skip the first one.
	if !p.tty && p.lastPct >= 0 && pct/10 == p.lastPct/10 {
		return
	}
	p.lastPct = pct
	line := fmt.Sprintf("Uploading… %d%% (%s of %s)", pct, bytesHuman(float64(p.sent)), bytesHuman(float64(p.total)))
	if p.tty {
		fmt.Fprintf(os.Stderr, "\r\033[K%s", dim(line))
		return
	}
	fmt.Fprintln(os.Stderr, dim(line))
}

// done closes off an in-place progress line so the next output starts clean.
func (p *importProgress) done() {
	if p.tty && p.lastPct >= 0 && !jsonOutput {
		fmt.Fprintln(os.Stderr)
	}
}

// ===========================================================================
// Polling
// ===========================================================================

// importIsTerminal reports whether an import status is final.
func importIsTerminal(status string) bool {
	for _, s := range importTerminalStates {
		if status == s {
			return true
		}
	}
	return false
}

// importPollJob polls an import until it reaches a terminal state and returns
// the last status body it saw.
//
// The body is returned even when the poll gives up, so the caller can still show
// the user where the import got to; the error alongside it says the wait ended
// without an answer, not that the import failed. A zero timeout means no limit —
// the import is done when it is done, and Ctrl-C is always available (the upload
// is over by now, so interrupting here leaves the server importing happily).
// Progress goes to stderr, one line per change, so --json stays pipeable.
func importPollJob(c *client.Client, kind, jobID string, interval, timeout time.Duration) ([]byte, error) {
	if jobID == "" {
		return nil, errors.New("the server did not return an import job id to poll")
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	var last []byte
	lastLine := ""
	for {
		data, err := c.ImportStatus(kind, jobID)
		if err != nil {
			return last, mapImportExportError(err)
		}
		last = data
		job := parseJSON(client.UnwrapData(data))
		if importIsTerminal(str(job, "status")) {
			return data, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return data, fmt.Errorf("still importing after %s — it keeps going on the server; check it with 'harbor import status %s'",
				timeout, jobID)
		}
		if line := importProgressLine(job); line != "" && line != lastLine && !jsonOutput {
			fmt.Fprintln(os.Stderr, dim(line))
			lastLine = line
		}
		time.Sleep(interval)
	}
}

// importProgressLine renders a one-line description of a running import, or ""
// when there is nothing worth saying yet. The note counts are the useful signal:
// the server pre-counts the file's notes before it starts, so "X of Y" is honest
// from the first poll rather than climbing towards a total that is still unknown.
func importProgressLine(job map[string]any) string {
	switch str(job, "status") {
	case "queued", "awaiting_upload":
		return "Queued — waiting for a worker to pick the import up."
	case "running":
		total := int64(num(job, "total_notes"))
		done := int64(num(job, "imported_notes")) + int64(num(job, "failed_notes")) + int64(num(job, "skipped_notes"))
		if total <= 0 {
			return "Importing…"
		}
		return fmt.Sprintf("Importing… %s / %s notes", commaNum(done), commaNum(total))
	}
	return ""
}

// ===========================================================================
// Helpers
// ===========================================================================

// importEnexAsync records whether the summary being rendered belongs to an
// import this command is NOT going to wait for, so displayImportJob can print
// the "poll it" hint. printResult only forwards the response body, so the fact
// is threaded through this package-level var.
var importEnexAsync bool

// importJobFailure reports an import that did not import everything, so the exit
// code says so.
//
// An import is accepted whether it goes on to import every note or none of them
// — the outcome lives in the counters — so without this, an import that dropped
// every note on the floor exits 0 and a script moves on believing the data is in
// Harbor. The API's own vocabulary decides: `failed` (including a truncated
// read), `partial` (the file was read, some notes failed) and `aborted` (the
// upload was cancelled) all mean notes were lost, and failed_notes catches a
// server that reports the count without moving off `completed`.
//
// An import still in flight (`queued`/`running`/`awaiting_upload`) has not
// failed at anything yet — it has not run — so it stays a success; the job id is
// printed and 'harbor import status' is where its outcome is judged.
//
// The two bodies this reads name the job differently: the complete call answers
// `import_job_id`, the poller answers `id`. Both are accepted so the follow-up
// hint survives whichever one the caller passed in.
func importJobFailure(data []byte) error {
	j := parseJSON(client.UnwrapData(data))
	if j == nil {
		return nil
	}
	status := str(j, "status")
	failed := int(num(j, "failed_notes"))
	if !importJobLostNotes(status, failed) {
		return nil
	}

	total, imported := int(num(j, "total_notes")), int(num(j, "imported_notes"))
	msg := fmt.Sprintf("the import did not finish cleanly (status %s): %d of %d %s imported",
		status, imported, total, pluralize(total, "note", "notes"))
	details := map[string]any{}
	if failed > 0 {
		details["failed_notes"] = failed
	}
	if skipped := int(num(j, "skipped_notes")); skipped > 0 {
		details["skipped_notes"] = skipped
	}
	if why := importReasonSentence(str(j, "failure_reason")); why != "" {
		details["reason"] = why
	}
	if id := importJobID(j); id != "" {
		details["per-note reasons"] = "harbor import status " + id
	}
	return &client.APIError{Code: "import_incomplete", Message: msg, Details: details}
}

// importJobLostNotes reports whether a job's status and failure count together
// mean notes did not make it in.
func importJobLostNotes(status string, failedNotes int) bool {
	switch status {
	case "failed", "partial", "aborted":
		return true
	}
	return failedNotes > 0
}

// importJobID reads a job's id from either shape the API returns it in: the
// complete call names it import_job_id, the status document names it id.
func importJobID(job map[string]any) string {
	if id := str(job, "import_job_id"); id != "" {
		return id
	}
	return str(job, "id")
}

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
	jobID := importJobID(j)
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
	failureReason := str(j, "failure_reason")
	printKV([][2]string{
		{"Job ID", bold(importJobID(j))},
		{"Status", colorizeStatus(str(j, "status"))},
		{"Total notes", trimFloat(num(j, "total_notes"))},
		{"Imported", trimFloat(num(j, "imported_notes"))},
		{"Skipped", trimFloat(num(j, "skipped_notes"))},
		{"Failed", trimFloat(num(j, "failed_notes"))},
		{"Updated", epochMS(num(j, "updated_at"))},
	})

	// A job-level failure explains itself in one sentence, and it is the only
	// thing that says whether re-running the same file can help. It goes UNDER
	// the card rather than in it: these sentences are long enough that a
	// two-column table would stretch past the terminal to hold one.
	if why := importReasonSentence(failureReason); why != "" {
		fmt.Printf("%s %s\n", dim("Why:"), why)
	}

	// errors is always present (never null); render the per-note failures, if
	// any, under the counters. A job-level failure carries note_index -1.
	errRows := make([][]string, 0, len(toSlice(j["errors"])))
	for _, e := range toSlice(j["errors"]) {
		idx := int(num(e, "note_index"))
		reason := str(e, "reason")
		// A job-level entry that just restates failure_reason is the line above,
		// printed twice.
		if idx < 0 && reason == failureReason {
			continue
		}
		idxStr := strconv.Itoa(idx)
		if idx < 0 {
			idxStr = dim("job")
		}
		errRows = append(errRows, []string{
			idxStr,
			truncate(str(e, "title"), 30),
			importNoteReasonSentence(reason, failureReason),
		})
	}
	if len(errRows) == 0 {
		return
	}
	fmt.Println(dim("Errors:"))
	printTableWrapped([]string{"NOTE", "TITLE", "REASON"}, errRows, 3, 60)
}

// importReasonSentences maps the server's per-note and job-level failure codes
// to the sentence shown to the user.
//
// The wire value is a CODE from a closed set, never a message, and the API
// contract is explicit that a client must not render it — an unrecognised code
// is reported as `unknown` precisely so a client running ahead of its server
// cannot leak one. The wording is Harbor's own, shared with the web client, so
// the same failure reads the same wherever someone meets it.
var importReasonSentences = map[string]string{
	"attachment_unreadable": "An attachment in this note was corrupted and couldn't be read.",
	"storage_unavailable":   "Harbor's file storage was temporarily unavailable, so this note's attachment couldn't be saved. Re-running the import will retry it.",
	"note_malformed":        "This note's formatting couldn't be understood by the importer.",
	"file_truncated":        "The file couldn't be read all the way through — the upload may have been cut off.",
	"not_enex":              "This file isn't an Evernote export, so there was nothing to import.",
	"not_obsidian_zip":      "This file isn't a readable vault zip, so there was nothing to import.",
	"not_standardnotes":     "This file isn't a decrypted Standard Notes backup, so there was nothing to import.",
	"upload_incomplete":     "The upload didn't finish, so the file couldn't be read. Try importing it again.",
	"source_missing":        "The uploaded file was no longer available to import. Try importing it again.",
	"notebook_unavailable":  "Harbor couldn't open the notebook these notes were being imported into.",
	"unsafe_zip_entry":      "A file in the zip had an unsafe path and was refused.",
	"note_too_large":        "This note is bigger than Harbor's per-note limit.",
	"attachment_missing":    "An attachment this note references couldn't be read from the zip.",
	"link_ambiguous":        "A link in this note matches more than one file, so it was left as text.",
	"truncated_source":      "The Evernote export itself ends mid-note, so the missing notes aren't in the file — re-export from Evernote to recover them.",
	"incomplete_read":       "The transfer ended before the whole file was read. Run the import again to finish it.",
	"unknown":               "Something went wrong while importing this note.",
}

// importNoteNotInFile is what file_truncated means on a job whose read reached
// the end of a short EXPORT: not a cut-off upload but a note Evernote never
// wrote. Retrying the same file cannot recover it, so the generic "the upload
// may have been cut off" wording would send someone to the one action that
// definitely will not work.
const importNoteNotInFile = "The export file ends in the middle of this note — re-export from Evernote to recover it."

// importReasonSentence renders one failure code as a sentence, falling back to
// the generic one rather than printing an unrecognised code raw.
func importReasonSentence(code string) string {
	if code == "" {
		return ""
	}
	if sentence, ok := importReasonSentences[code]; ok {
		return sentence
	}
	return importReasonSentences["unknown"]
}

// importNoteReasonSentence renders a PER-NOTE failure code, which the job's own
// reason can reinterpret — see importNoteNotInFile.
func importNoteReasonSentence(code, jobFailureReason string) string {
	if code == "file_truncated" && jobFailureReason == "truncated_source" {
		return importNoteNotInFile
	}
	return importReasonSentence(code)
}

func init() {
	importEnexCmd.Flags().String("notebook", "", "Force all notes into this existing (non-encrypted) notebook id")
	importEnexCmd.Flags().String("filename", "", "Original file name (names the auto-created notebook; defaults to the file's base name)")
	importEnexCmd.Flags().Bool("no-wait", false, "Return once the upload is accepted instead of waiting for the import to finish")
	importEnexCmd.Flags().Duration("poll-interval", 2*time.Second, "How often to poll while waiting")
	importEnexCmd.Flags().Duration("timeout", 0, "Give up waiting after this long (0 = no limit; the import keeps running)")
	importEnexCmd.Flags().Bool("notify-email", false, "Email me when the import finishes (defaults to on with --no-wait, off when waiting)")
	importCmd.AddCommand(importEnexCmd, importStatusCmd, importAbortCmd)
	rootCmd.AddCommand(importCmd)

	exportEnexCmd.Flags().String("notebook", "", "DEPRECATED — use 'harbor account export --format enex --notebook <id>'. Exports this notebook's live notes (the successor also includes trashed ones)")
	exportEnexCmd.Flags().String("notes", "", "Export exactly these note ids (comma-separated)")
	exportEnexCmd.Flags().Bool("include-resources", false, "Inline each linked attachment's bytes as base64")
	exportEnexCmd.Flags().String("output", "", "Output path, or - for stdout (required)")
	exportCmd.AddCommand(exportEnexCmd)
	rootCmd.AddCommand(exportCmd)
}

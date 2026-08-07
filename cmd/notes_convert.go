// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/spf13/cobra"
)

// ===========================================================================
// Converting a note between plaintext and encrypted, in place
// ===========================================================================
//
// `notes create --encrypt` decides a note's encryption at birth and `notes
// update` only ever RE-seals a note that is already encrypted, so until now the
// only way to change a note's mind was to delete it and type it again. These two
// commands close that: `notes encrypt` seals an existing plaintext note, `notes
// decrypt` opens it back up, and the note keeps its id, its tasks, its
// attachments, its tags and its created_at through both. Its version HISTORY
// does not survive either direction — see historyLossCaveat.
//
// IT IS A REMEDY, NOT JUST A FEATURE. Four sibling clients shipped write paths
// that saved PLAINTEXT into notebooks marked default_encrypt (harbor-swift#817,
// harbor-android#236, harbor-windows#221, harbor-webclipper#91). Those notes are
// on real accounts right now, unencrypted, in notebooks their owner believes are
// encrypted. `harbor notes encrypt --notebook <id>` is how they get fixed without
// losing anything, which is why the sweep exists and why every guard below is
// written to fail with NOTHING WRITTEN rather than to get halfway.
//
// WHY THIS IS SAFE TO DO IN PLACE. The HRBC2 field AAD binds to the note ID and
// the field name (crypto/README.md), and the id does not change across an update
// — so a body sealed today opens tomorrow under the same note. Nothing about the
// envelope depends on when it was written.
//
// THE SHAPE OF EVERY CONVERSION, both directions:
//
//	unlock → read the note → transform in memory → verify the transform → ONE PATCH
//
// The order is the whole safety argument. Nothing is written until every step
// that can fail has already succeeded, and the write itself is a single PATCH
// that the server applies inside one transaction (notes.ApplyContentWrite). So
// there is no half-converted state to land in: kill the command at any point
// before the PATCH and the note is exactly as it was; kill it during, and the
// transaction either committed or rolled back. The PATCH also carries base_usn,
// so a note that changed between the read and the write is refused (409
// note_usn_stale) instead of being overwritten by a body that predates the change.
//
// WHAT THE SERVER DOES WITH EACH DIRECTION, which is where the data-loss risk
// actually lives (app.harbor.my internal/notes/notes.go afterContentWrite):
//
//   - ENCRYPTING skips the whole derive-from-body pass — no link extraction, no
//     attachment junction sync, no task reconciliation — because the server
//     cannot parse ciphertext. Tasks, inline attachments and note→note links
//     therefore survive untouched; they are simply frozen until the note is
//     readable again.
//   - DECRYPTING is the first save since then that the server CAN read, so all
//     of that runs against the newly-plaintext body. Links and attachment
//     junctions are re-derived from the body that carries them, which restores
//     them. Tasks are the exception worth guarding: a task still linked to the
//     note whose <harbor-task> block the body does not carry is TOMBSTONED, not
//     detached (app.harbor.my#461). guardConversionTaskLoss below refuses that.

// ===========================================================================
// Unlocking
// ===========================================================================

// conversionKey unlocks the master key for a conversion, and is the answer to
// "what happens when the vault is locked?" — the command stops here, before it
// has read or written anything.
//
// This is the requirement the whole issue turns on. Every other unlock in the CLI
// FAILS OPEN by design: a list with no passphrase shows ciphertext and warns
// (decryptResult), a create in a notebook whose flag could not be read writes in
// the clear and warns (notebookWantsEncryption). Both are recoverable — nothing
// was lost, re-run with the passphrase. Here, failing open would mean writing a
// note the user asked to have encrypted and leaving it in the clear, which is
// EXACTLY the bug the sibling clients shipped and exactly what this command
// exists to clean up. So it fails closed, loudly, with nothing written.
func conversionKey(c *client.Client, creds *config.Credentials) ([]byte, error) {
	key, err := unlockMasterKey(c, creds)
	if err == nil {
		return key, nil
	}
	switch {
	case errors.Is(err, errPassphraseNotSet):
		return nil, fmt.Errorf("this needs your encryption passphrase and %s is not set, so nothing was written.\n\n"+
			"  export %s=$(op read \"op://Vault/Harbor/passphrase\")\n\n"+
			"Without the key there is no way to seal or open a note, and writing one anyway would\n"+
			"leave a note you believe is encrypted sitting on the server in the clear",
			passphraseEnv, passphraseEnv)
	case errors.Is(err, errNoKeystore):
		return nil, errors.New("this account has no encryption keys yet, so nothing was written — run 'harbor crypto setup' first (or 'harbor crypto sync' if you set them up on another device)")
	}
	return nil, fmt.Errorf("could not unlock encryption, so nothing was written: %w", err)
}

// ===========================================================================
// Choosing what to convert
// ===========================================================================

// conversionTargets resolves the notes a conversion runs over: the ids named as
// arguments, or every note in --notebook that is not already in the target state.
// The bool reports whether the notebook was read WHOLE — see the sweep below.
//
// Ids and --notebook are mutually exclusive rather than additive. They answer the
// same question two ways, and a command that silently unions them turns a typo
// ("I meant to convert just this one, but the notebook flag was still in my
// shell history") into a hundred writes.
func conversionTargets(cmd *cobra.Command, c *client.Client, args []string, toEncrypted bool) ([]string, bool, error) {
	notebook := stringFlag(cmd, "notebook")
	switch {
	case notebook != "" && len(args) > 0:
		return nil, false, errors.New("pass note ids or --notebook, not both — they name the same thing two different ways")
	case notebook == "" && len(args) == 0:
		return nil, false, errors.New("name at least one note id, or pass --notebook <id> to convert a whole notebook")
	case notebook == "":
		return args, true, nil
	}
	return sweepNotebook(c, notebook, toEncrypted)
}

// sweepNotebook lists a notebook and returns the ids of the notes that are not
// already in the target state, plus whether the listing was read whole.
//
// It reads EVERY page (walkCollection), for the reason issue #67 documented: an
// internal read that stops at the first page silently answers "that is all of
// them" for an account big enough to have more, and here that would mean
// reporting a notebook fixed while leaving notes in it unencrypted — the precise
// failure this command exists to repair.
//
// A short read is reported rather than refused. Converting some notes is not
// destructive and the operation is idempotent (a note already in the target state
// is skipped), so "re-run to catch the rest" is a real fix; refusing outright
// would leave a large notebook with no way through at all.
func sweepNotebook(c *client.Client, notebookID string, toEncrypted bool) ([]string, bool, error) {
	ids := []string{}
	complete, err := walkCollection(
		func(params map[string]string) ([]byte, error) {
			params["notebook_id"] = notebookID
			// The bodies are not wanted here — only each note's id and its current
			// encryption marker, both of which survive the meta projection. On a
			// notebook of any size that is the difference between listing it and
			// downloading it.
			params["fields"] = "meta"
			return c.ListNotes(params)
		},
		func(raw json.RawMessage) bool {
			n := parseJSON(raw)
			if id := str(n, "id"); id != "" && boolean(n, "is_encrypted") != toEncrypted {
				ids = append(ids, id)
			}
			return true
		})
	if err != nil {
		return nil, false, fmt.Errorf("could not list notebook %s, so nothing was written: %w", notebookID, err)
	}
	return ids, complete, nil
}

// ===========================================================================
// Building the write
// ===========================================================================

// readNoteForConversion fetches the note a conversion is about to rewrite.
//
// format=html asks for the note as the server STORES it, which matters in both
// directions: what gets sealed on the way in is then exactly what a decrypt
// writes back on the way out, so a round trip is a no-op rather than a
// re-rendering. (The server ignores the format parameter for an encrypted note —
// there is nothing it could convert — so the same call serves both commands.)
func readNoteForConversion(c *client.Client, noteID string) (map[string]any, error) {
	raw, err := c.GetNote(noteID, map[string]string{"format": "html"})
	if err != nil {
		return nil, mapNoteError(err)
	}
	note := parseJSON(client.UnwrapData(raw))
	if note == nil {
		// Fail closed: converting a note we could not read would mean sealing or
		// publishing a body nobody has seen.
		return nil, fmt.Errorf("could not read note %s before converting it, so nothing was written", noteID)
	}
	return note, nil
}

// planEncrypt reads a note and builds the PATCH body that seals it. A nil body
// means the note is already encrypted and there is nothing to do — not an error,
// because a sweep re-run over a half-fixed notebook must be able to say so
// without failing.
//
// THIS IS THE ONLY PLACE PLAINTEXT BECOMES CIPHERTEXT IN PLACE. `notes encrypt`
// and the sealed move in cmd/notes_move.go both come through here, and that is a
// requirement rather than a tidiness: the web reference forked this step for its
// move path and the two copies disagreed — on the already-encrypted recheck and
// on what a post-commit failure means — inside a single pull request
// (app.harbor.my#1267, D3). A fork here would have to re-derive the server-
// authoritative recheck above, the seal-then-open check, and the compare-and-set,
// and the way that goes wrong is that it re-derives two of the three.
//
// THE RE-READ ABOVE IS LOAD-BEARING AND MUST STAY. The caller's copy of the note
// is a fast path and nothing more; the note this seals is the one the SERVER has
// right now. Sealing a note another device has already sealed since the caller
// last looked would wrap ciphertext in a second envelope — a note that decrypts
// once into more ciphertext — which is exactly the bug the reference shipped and
// then fixed (commit 117a377).
//
// replace, when non-nil, supplies plaintext to seal INSTEAD of what the note
// currently holds, for the case where one command both edits and seals a note
// (`notes update --title X --notebook <an encrypting notebook>`). Only "title"
// and "content" are read from it. Without this the command would have to seal the
// stored text and then send the user's new text alongside it in the clear, which
// is not an update and not an encryption.
func planEncrypt(c *client.Client, key []byte, noteID string, replace map[string]any) (map[string]any, error) {
	note, err := readNoteForConversion(c, noteID)
	if err != nil {
		return nil, err
	}
	if boolean(note, "is_encrypted") {
		return nil, nil
	}

	body := map[string]any{"is_encrypted": true}
	setConversionBaseUSN(body, note)

	// An encrypted note may have an EMPTY title (the server's
	// validateEncryptedFields allows it) but never a plaintext one, so an empty
	// title is left alone and anything else is sealed. An empty title the CALLER
	// asked for is different from one the note already had, and is echoed back
	// explicitly so this function's return is a complete write on its own. Today's
	// only caller merges it into a body that already carries the same empty title,
	// so the branch changes nothing for it — it is here so that what planEncrypt
	// returns does not depend on the caller having done that.
	title, retitled := replaceField(replace, "title")
	if !retitled {
		title = str(note, "title")
	}
	switch {
	case title != "":
		sealed, serr := sealAndVerify(key, noteID, "title", title)
		if serr != nil {
			return nil, serr
		}
		body["title"] = sealed
	case retitled:
		body["title"] = ""
	}
	// Content is sealed unconditionally, empty body included: the server requires
	// a well-formed content envelope on every encrypted note.
	content, rewritten := replaceField(replace, "content")
	if !rewritten {
		content = str(note, "content")
	}
	sealed, err := sealAndVerify(key, noteID, "content", content)
	if err != nil {
		return nil, err
	}
	body["content"] = sealed
	// No content_format: the server stores an encrypted body verbatim and never
	// renders it, so a format would be a claim about bytes it will not look at.
	return body, nil
}

// replaceField reads one of planEncrypt's replacement fields, reporting whether
// the caller supplied it at all. Presence and emptiness are different answers
// here — "" means "clear this field", absent means "keep what the note holds" —
// so a plain type assertion, which cannot tell a missing key from an empty
// string, would silently blank a title on every ordinary encrypt.
func replaceField(replace map[string]any, field string) (string, bool) {
	v, ok := replace[field]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// planDecrypt reads an encrypted note and builds the PATCH body that writes it
// back in the clear. A nil body means the note is already plaintext.
//
// format says how the server should interpret the body it is handed back. html
// is the default and the right answer for any note this CLI sealed by conversion
// (planEncrypt seals the STORED html, so html is its exact inverse). A note
// created encrypted from Markdown source holds that source instead, and
// --format markdown is how the user says so.
func planDecrypt(c *client.Client, key []byte, noteID, format string, allowTaskLoss bool) (map[string]any, error) {
	note, err := readNoteForConversion(c, noteID)
	if err != nil {
		return nil, err
	}
	if !boolean(note, "is_encrypted") {
		return nil, nil
	}
	// A note that lives in a notebook whose notes are always encrypted cannot be
	// opened where it stands, and the refusal comes BEFORE anything is read out of
	// the envelope. Checked per note rather than once per run because the ids a
	// user names can be spread across any number of notebooks.
	if err := guardDecryptInEncryptedNotebook(c, noteID, note); err != nil {
		return nil, err
	}

	content := str(note, "content")
	if !crypto.IsEnvelope(content) {
		// The note claims to be encrypted and its body is not an HRBC2 envelope, so
		// there is nothing here to open. Writing it back as "plaintext" would
		// publish whatever that body actually holds under a flag saying it is
		// readable — a guess, on the one command where a wrong guess is public.
		// (The local dev API's SPNC2 placeholder fixtures land exactly here.)
		return nil, fmt.Errorf("note %s is marked encrypted but its body is not an HRBC2 envelope, so there is nothing to decrypt — nothing was written", noteID)
	}
	plainContent, err := crypto.OpenField(key, noteID, "content", content)
	if err != nil {
		return nil, fmt.Errorf("could not decrypt note %s, so nothing was written: %w\n\n"+
			"The note may have been sealed under a different passphrase than the one %s holds",
			noteID, err, passphraseEnv)
	}

	body := map[string]any{"is_encrypted": false, "content": plainContent, "content_format": format}
	setConversionBaseUSN(body, note)
	if title := str(note, "title"); crypto.IsEnvelope(title) {
		plainTitle, terr := crypto.OpenField(key, noteID, "title", title)
		if terr != nil {
			return nil, fmt.Errorf("could not decrypt note %s's title, so nothing was written: %w", noteID, terr)
		}
		body["title"] = plainTitle
	}
	if err := guardConversionTaskLoss(c, noteID, plainContent, format, allowTaskLoss); err != nil {
		return nil, err
	}
	return body, nil
}

// sealAndVerify seals one field and then OPENS WHAT IT JUST SEALED, refusing
// unless the round trip returns the original bytes.
//
// The check costs one AES-GCM open against a body already in memory, and it is
// the difference between the two ways this command can fail. Without it, a seal
// that produced an envelope this key cannot open would be written to the server
// and the plaintext replaced — a note nobody can ever read again, which no amount
// of re-running fixes. With it, the same fault is a refusal with nothing written.
// GCM makes that failure vanishingly unlikely; the cost of being wrong is why it
// is checked anyway.
func sealAndVerify(key []byte, noteID, field, plaintext string) (string, error) {
	sealed, err := crypto.SealField(key, noteID, field, plaintext)
	if err != nil {
		return "", fmt.Errorf("could not encrypt note %s's %s, so nothing was written: %w", noteID, field, err)
	}
	back, err := crypto.OpenField(key, noteID, field, sealed)
	if err != nil {
		return "", fmt.Errorf("refusing to write note %s: the %s was encrypted but could not be read back (%w). Nothing was written", noteID, field, err)
	}
	if back != plaintext {
		return "", fmt.Errorf("refusing to write note %s: the encrypted %s did not read back as what went in. Nothing was written", noteID, field)
	}
	return sealed, nil
}

// setConversionBaseUSN puts the note's current usn on the outgoing write as a
// compare-and-set precondition (app.harbor.my#1153). A conversion is a
// read-then-write, so without it an edit that lands in between — from a phone,
// another agent, the web app — would be silently overwritten by a body that
// predates it. With it the server answers 409 note_usn_stale and writes nothing.
// Servers too old to know the field ignore it.
func setConversionBaseUSN(body, note map[string]any) {
	if usn := int64(num(note, "usn")); usn > 0 {
		body["base_usn"] = usn
	}
}

// ===========================================================================
// The one thing a decrypt can destroy
// ===========================================================================

// guardConversionTaskLoss refuses a decrypt that would delete the note's tasks.
//
// While a note is encrypted the server does not reconcile its tasks — it cannot
// read the body — so the link between the note and its tasks is frozen. The
// decrypt write is the first save since then that the server CAN read, and it
// reconciles: every task still pointing at this note whose <harbor-task> block
// the newly-plaintext body does not carry is TOMBSTONED (app.harbor.my#461's
// delete-on-release), not merely detached.
//
// In the ordinary case nothing is at risk, because the body being written is the
// same body that was sealed, blocks and all. It is the STRANDED task this
// catches: one linked to the note but no longer referenced by it — an
// app.harbor.my#1094 orphan, a task attached through sync while the note was
// ciphertext, one left behind by a history revert. That task exists only in the
// task list, and it is exactly the kind of quiet deletion issues #62 and #67
// were about.
//
// The current body is passed as "" on purpose: the note's stored body is
// ciphertext, which references nothing the server can read, so the task LIST is
// the only source of truth here.
func guardConversionTaskLoss(c *client.Client, noteID, plainContent, format string, allowLoss bool) error {
	atRisk, complete := noteTasksAtRisk(c, noteID, "")
	if !complete && !allowLoss {
		return fmt.Errorf("could not read note %s's task list in full, so nothing has been written.\n\n"+
			"Decrypting is the first save the server can read since this note was encrypted, so it\n"+
			"reconciles the note's tasks against the decrypted body — and a task it cannot see is a\n"+
			"task it cannot protect. Re-run to try again, or pass --allow-task-loss to proceed anyway.", noteID)
	}
	if len(atRisk) == 0 {
		return nil
	}
	carried := map[string]bool{}
	for _, id := range noteTaskBlockIDs(plainContent, format) {
		carried[id] = true
	}
	lost := make([]noteTask, 0, len(atRisk))
	for _, t := range atRisk {
		if !carried[t.ID] {
			lost = append(lost, t)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	if allowLoss {
		fmt.Fprintf(os.Stderr, "%s Decrypting note %s deletes %s its body no longer carries.\n",
			redWarn("Warning:"), noteID, countTasks(len(lost)))
		return nil
	}
	return errors.New(conversionTaskLossRefusal(noteID, lost))
}

// conversionTaskLossRefusal builds the message for a decrypt held back by the
// guard above: what would be deleted, why decrypting is what deletes it, and the
// two ways forward. Naming the tasks matters — a bare count leaves the user
// unable to tell a task they deliberately dropped from one they never knew was
// attached.
func conversionTaskLossRefusal(noteID string, lost []noteTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "decrypting note %s would delete %s, so nothing has been written:\n", noteID, countTasks(len(lost)))
	for _, t := range lost {
		if t.Title != "" {
			fmt.Fprintf(&b, "  • %s (%s)\n", t.Title, t.ID)
			continue
		}
		fmt.Fprintf(&b, "  • %s\n", t.ID)
	}
	b.WriteString("\nThese tasks are linked to the note, but the decrypted body carries no\n")
	b.WriteString("<harbor-task> block for them. While the note is encrypted the server cannot read\n")
	b.WriteString("its body, so it leaves the tasks alone; the moment the body is readable it\n")
	b.WriteString("reconciles them, and a task the body does not reference is deleted, not detached.\n\n")
	b.WriteString("Two ways forward:\n\n")
	b.WriteString("  KEEP THEM — put their blocks back into the note's body first, then decrypt:\n\n")
	fmt.Fprintf(&b, "    harbor notes get %s --format html\n", noteID)
	fmt.Fprintf(&b, "    harbor notes update %s --format html --content '<the body>%s'\n\n",
		noteID, taskBlockHTML(lost[0].ID))
	b.WriteString("  DELETE THEM — that is what you meant:\n\n")
	b.WriteString("    --allow-task-loss\n")
	return b.String()
}

// ===========================================================================
// Running a conversion over one or many notes
// ===========================================================================

// conversionFailure is one note a conversion could not convert, for --json.
type conversionFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// conversionReport is the machine-readable account of a whole run. Failures are
// listed rather than summed, so a script fixing a notebook can retry precisely
// the notes that did not make it.
type conversionReport struct {
	Converted []string            `json:"converted"`
	Skipped   []string            `json:"skipped"`
	Failed    []conversionFailure `json:"failed"`

	// Complete is false when the notebook sweep could not read the whole
	// notebook, i.e. there may be notes it never even considered.
	Complete bool `json:"complete"`
}

// runConversion applies one note's conversion to every target and reports what
// happened. apply returns (converted, error); (false, nil) means the note was
// already in the target state.
//
// A failure does NOT stop the run. The remedy case is "fix this notebook", and
// one note that cannot be converted — a stranded task, a body sealed under
// another passphrase — is no reason to leave the other ninety-nine unfixed. Every
// failure is reported and the command still exits non-zero, so a script cannot
// mistake a partial run for a clean one.
func runConversion(ids []string, complete bool, verb string, apply func(id string) (bool, error)) error {
	report := conversionReport{
		Converted: []string{},
		Skipped:   []string{},
		Failed:    []conversionFailure{},
		Complete:  complete,
	}
	// A single named note is the common case, and there its error IS the command's
	// error: returned unwrapped so it renders in full through the usual path
	// instead of being flattened into a per-line summary.
	single := len(ids) == 1

	for _, id := range ids {
		converted, err := apply(id)
		switch {
		case err != nil:
			if single {
				return err
			}
			report.Failed = append(report.Failed, conversionFailure{ID: id, Error: err.Error()})
			if !jsonOutput {
				fmt.Printf("%s %s: %s\n", redWarn("✗"), id, err.Error())
			}
		case converted:
			report.Converted = append(report.Converted, id)
			if !jsonOutput {
				fmt.Printf("%s %s %s\n", boolMark(true), id, verb)
			}
		default:
			report.Skipped = append(report.Skipped, id)
			if !jsonOutput {
				fmt.Println(dim(fmt.Sprintf("· %s already %s — skipped", id, verb)))
			}
		}
	}

	if jsonOutput {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	} else {
		printConversionSummary(report, verb)
	}
	if n := len(report.Failed); n > 0 {
		return fmt.Errorf("%d of %d %s could not be %s — each failure is listed above",
			n, len(ids), pluralize(len(ids), "note", "notes"), verb)
	}
	return nil
}

// printConversionSummary prints the human closing line of a run: what changed,
// what was already done, and — the part that must not be quiet — whether the
// notebook was read whole.
func printConversionSummary(report conversionReport, verb string) {
	converted, skipped := len(report.Converted), len(report.Skipped)
	// A sweep that found nothing to do says that in words. "Encrypted 0 notes."
	// is true and reads like a failure; on the remedy path — where the answer the
	// user wants is "this notebook is clean" — it should say so.
	if converted == 0 && skipped == 0 && len(report.Failed) == 0 {
		fmt.Printf("Nothing to do — every note asked about is already %s.\n", verb)
		if !report.Complete {
			printIncompleteSweepWarning()
		}
		return
	}
	fmt.Printf("\n%s %d %s.", bold(strings.ToUpper(verb[:1])+verb[1:]), converted, pluralize(converted, "note", "notes"))
	if skipped > 0 {
		fmt.Printf(" %d already %s.", skipped, verb)
	}
	if n := len(report.Failed); n > 0 {
		fmt.Printf(" %s", redWarn(fmt.Sprintf("%d failed.", n)))
	}
	fmt.Println()
	if !report.Complete {
		printIncompleteSweepWarning()
	}
}

// printIncompleteSweepWarning says that the notebook was not read whole. Silence
// here would read as "the notebook is done", which is the one thing such a run
// cannot claim.
func printIncompleteSweepWarning() {
	fmt.Println(redWarn("Warning:") + " the notebook could not be listed in full, so there may be more notes" +
		"\nthat were never looked at. Re-run this command — notes already converted are skipped.")
}

// ===========================================================================
// Commands
// ===========================================================================

// notesEncryptCmd seals existing plaintext notes.
var notesEncryptCmd = &cobra.Command{
	Use:   "encrypt [id...]",
	Short: "Encrypt existing plaintext notes in place (requires HARBOR_PASSPHRASE)",
	Args:  cobra.ArbitraryArgs,
	Long: `Encrypt notes that were saved in the clear, keeping everything else about them.

The note keeps its id, tasks, attachments, tags, reminders and created_at — only
its title and body change, into HRBC2 envelopes only your passphrase can open.
Nothing is written unless every step before the write succeeded, so a failure
leaves the note exactly as it was.

Use --notebook to fix a whole notebook at once: several Harbor clients shipped
write paths that saved plaintext into notebooks marked "encrypt by default", and
this is how those notes get repaired without deleting and re-creating them. Notes
already encrypted are skipped, so it is safe to re-run.

` + bulletCaveat(historyLossCaveat) + `
` + bulletCaveat(attachmentCaveat) + `

Because of the first one you will be asked to type "yes" unless you pass --yes,
which is required in --json or non-interactive use.

While a note is encrypted the server cannot read it: it is excluded from search,
and its tasks, links and attachment references stop being reconciled until it is
decrypted again.`,
	Example: `  harbor notes encrypt 9c2e...
  harbor notes encrypt 9c2e... 7d4a... 1f0b...
  harbor notes encrypt --notebook 5b1f... --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		// The key comes first. A locked vault is a refusal, and refusing before the
		// listing means a sweep of a thousand notes does not read them all to find
		// out it could never have written any of them.
		key, err := conversionKey(c, creds)
		if err != nil {
			return err
		}
		ids, complete, err := conversionTargets(cmd, c, args, true)
		if err != nil {
			return err
		}
		// Asked once for the whole run and BEFORE anything is written, for the same
		// reason decrypt is: the user is consenting to destroying these notes'
		// version history, and being asked per note over a notebook sweep is how a
		// person learns to type "yes" without reading.
		if err := notesConfirmEncrypt(len(ids), boolFlag(cmd, "yes")); err != nil {
			return err
		}
		sealed := 0
		err = runConversion(ids, complete, "encrypted", func(id string) (bool, error) {
			body, perr := planEncrypt(c, key, id, nil)
			if perr != nil || body == nil {
				return false, perr
			}
			data, werr := c.ConvertNoteToEncrypted(id, body)
			if werr != nil {
				return false, mapNoteError(werr)
			}
			// Counted only once the server has confirmed the note came back
			// encrypted. A write that landed but did not take is not a sealed note,
			// and must not be spoken about as one.
			if verr := verifyConversionLanded(data, true, id); verr != nil {
				return false, verr
			}
			sealed++
			return true, nil
		})
		// The caveat goes out BEFORE the error return, because a run that failed
		// part way is exactly when it matters most: a sweep where one note failed
		// still left the others genuinely sealed on the server, and returning first
		// gave the user a partial success with none of the warning that makes it
		// honest. It is keyed off what was actually sealed, not off whether the run
		// as a whole succeeded.
		if sealed > 0 {
			printHistoryCaveat()
		}
		return err
	},
}

// notesEncryptConfirmation gates `notes encrypt`.
//
// It exists because the write DESTROYS data, which is not what "encrypt" sounds
// like it does. Every snapshot in the note's history that disagrees with the
// note's new is_encrypted state is hard-deleted server-side — not tombstoned,
// not re-sealed — so encrypting a note discards every earlier version of it.
// note_history does not sync and is not indexed, so there is nothing to
// propagate and nothing to recover from another device.
//
// The wording leads with the deletion rather than with the encryption, because
// the encryption is the part the user already wants and the deletion is the part
// they do not know about. See app.harbor.my/docs/encryption.md, "Changing
// encryption discards history".
var notesEncryptConfirmation = registerConfirmation("harbor notes encrypt", confirmation{
	Warning: "This DELETES every earlier version. Encrypting discards a note's whole\n" +
		"version history on the server — permanently, and it cannot be recovered from\n" +
		"another device. The current contents are kept and sealed; everything\n" +
		"'harbor history list' would have shown you is destroyed.",
	Prompt:      `Type "yes" to confirm: `,
	Affirmative: "yes",
	Unattended:  "refusing to destroy these notes' version history without confirmation — pass --yes",
	Aborted:     "aborted — nothing was encrypted",
})

// notesConfirmEncrypt gates `notes encrypt`, resolving the ambient state and
// handing the decision to the shared confirmDestructive. The count is printed
// alongside the registered warning so a --notebook sweep says how many notes'
// histories it is about to delete rather than asking about the idea in general.
func notesConfirmEncrypt(count int, yes bool) error {
	// Nothing to convert, nothing to consent to. The sweep is documented as safe
	// to re-run, so an idempotent repair script must not need --yes to be told a
	// notebook is already clean.
	if count == 0 {
		return nil
	}
	if !yes && !jsonOutput && stdinIsInteractive() {
		fmt.Printf("About to encrypt %d %s, deleting the version history of %s.\n",
			count, pluralize(count, "note", "notes"), pluralize(count, "it", "each of them"))
	}
	return confirmDestructive(notesEncryptConfirmation, jsonOutput, stdinIsInteractive(), yes, askLine)
}

// notesDecryptConfirmation gates `notes decrypt`. The wording leads with where
// the plaintext GOES rather than with "this is irreversible", because the note
// itself is trivially re-encryptable and that is not the point: the moment the
// body is written in the clear it is indexed for search and snapshotted into the
// note's history on the server, and re-encrypting afterwards does not unsend it.
var notesDecryptConfirmation = registerConfirmation("harbor notes decrypt", confirmation{
	Warning: "This writes these notes' contents to the server in the CLEAR — indexed for search\n" +
		"and written into the note's version history. Encrypting them again protects them\n" +
		"from then on; it does not un-store what the server has already been given.\n" +
		"It ALSO deletes every earlier version of these notes: the encrypted snapshots\n" +
		"are discarded permanently, the same way encrypting discards the plaintext ones.",
	Prompt:      `Type "yes" to confirm: `,
	Affirmative: "yes",
	Unattended:  "refusing to write notes back as plaintext without confirmation — pass --yes",
	Aborted:     "aborted — nothing was decrypted",
})

// notesDecryptCmd opens encrypted notes back into plaintext.
var notesDecryptCmd = &cobra.Command{
	Use:   "decrypt [id...]",
	Short: "Decrypt notes back to plaintext on the server (requires HARBOR_PASSPHRASE)",
	Args:  cobra.ArbitraryArgs,
	Long: `Write encrypted notes back to the server as plaintext.

This is a DOWNGRADE, and the confirmation is not a formality. Decrypting sends the
note's contents to the server in the clear, where they are indexed for search and
snapshotted into the note's version history. Encrypting the note again afterwards
protects the note from then on; it does not retract what was already stored. You
will be asked to type "yes" unless you pass --yes, which is required in --json or
non-interactive use.

` + bulletCaveat(historyLossCaveat) + `

Everything else about the note survives: id, tasks, attachments, tags, reminders,
created_at. The one thing that can be lost is a task LINKED to the note whose
<harbor-task> block the decrypted body does not carry — the server deletes such a
task the moment it can read the body again — so that case is refused with nothing
written unless you pass --allow-task-loss.

--format says how to interpret the decrypted body. html is right for any note
this CLI encrypted (it seals the note exactly as the server stored it, so
decrypting is the exact inverse). Use markdown for a note created encrypted from
Markdown source.`,
	Example: `  harbor notes decrypt 9c2e...
  harbor notes decrypt 9c2e... --format markdown
  harbor notes decrypt --notebook 5b1f... --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		key, err := conversionKey(c, creds)
		if err != nil {
			return err
		}
		ids, complete, err := conversionTargets(cmd, c, args, false)
		if err != nil {
			return err
		}
		// Asked once for the whole run, and asked BEFORE anything is written: the
		// user is consenting to publishing these notes, and being asked per note
		// over a notebook sweep is how a person learns to type "yes" without
		// reading. len(ids) is in the question so the answer is informed.
		if err := notesConfirmDecrypt(len(ids), boolFlag(cmd, "yes")); err != nil {
			return err
		}
		format := stringFlag(cmd, "format")
		allowTaskLoss := boolFlag(cmd, "allow-task-loss")
		opened := 0
		err = runConversion(ids, complete, "decrypted", func(id string) (bool, error) {
			body, perr := planDecrypt(c, key, id, format, allowTaskLoss)
			if perr != nil || body == nil {
				return false, perr
			}
			data, werr := c.ConvertNoteToPlaintext(id, body)
			if werr != nil {
				return false, mapNoteError(werr)
			}
			if verr := verifyConversionLanded(data, false, id); verr != nil {
				return false, verr
			}
			opened++
			return true, nil
		})
		// Same reasoning as the encrypt path: changing the flag in EITHER direction
		// discards the note's history, and a partial run is exactly when saying so
		// matters most.
		if opened > 0 {
			printHistoryCaveat()
		}
		return err
	},
}

// notesConfirmDecrypt gates `notes decrypt`, resolving the ambient state and
// handing the decision to the shared confirmDestructive. The count is printed
// alongside the registered warning so the user is answering about the run in
// front of them, not about the idea in general.
func notesConfirmDecrypt(count int, yes bool) error {
	if !yes && !jsonOutput && stdinIsInteractive() {
		fmt.Printf("About to decrypt %d %s.\n", count, pluralize(count, "note", "notes"))
	}
	return confirmDestructive(notesDecryptConfirmation, jsonOutput, stdinIsInteractive(), yes, askLine)
}

// attachmentCaveat is what sealing a note does NOT cover, and it lives in one
// place because two commands have to say it in the same words.
//
// `notes encrypt` has always documented it in its help text. The sealed move
// (cmd/notes_move.go) inherits exactly the same limit and PRINTS it, because
// there the encryption is a side effect of a command the user ran for another
// reason — nobody reads `notes update --help` looking for what encryption covers.
// Two hand-written versions of this sentence would be two versions to keep true;
// worse, the honest one would be the one nobody was looking at.
//
// It is also not a CLI-only apology. Every Harbor client leaves some attachment
// bytes readable — even the web app, which does re-encrypt them, misses the ones
// an ENEX import attached (app.harbor.my#1260). So it is worded as the limit it
// is, and it is not softened.
const attachmentCaveat = "It does not encrypt the BYTES of attached files. The note's body — including\n" +
	"the references to them — becomes ciphertext, but the attachments themselves\n" +
	"are stored as they were, and can still be downloaded and read in full."

// historyLossCaveat is what changing a note's encryption DESTROYS, and like the
// attachment caveat it lives in one place because several commands must say it
// in the same words.
//
// This sentence used to say the opposite — that earlier versions "stay readable"
// — with a long paragraph about the server's history coalescing as the rare
// exception. The exception is now the rule. Every snapshot that disagrees with
// the note's new is_encrypted state is hard-deleted by the write that changes
// the flag: not tombstoned, not re-sealed. note_history neither syncs nor is
// indexed, so nothing propagates and nothing is recoverable from another device.
//
// It is deliberate server-side behaviour, not a bug to route around: sealing old
// snapshots instead would need a per-version crypto pass in all five clients,
// and the guarantee would only ever be as strong as the weakest one. See
// app.harbor.my/docs/encryption.md, "Changing encryption discards history".
const historyLossCaveat = "It DELETES the note's version history. Every earlier version is discarded\n" +
	"server-side when the encryption changes — permanently, and not recoverable\n" +
	"from another device. The current contents are kept; nothing older survives."

// bulletCaveat renders a caveat as one of the "  • " bullets the encrypt help
// text is built from, indenting its continuation lines to line up under the
// first. It is what lets the same sentence be both help text and a printed
// warning without being typed twice.
func bulletCaveat(s string) string {
	return "  • " + strings.ReplaceAll(s, "\n", "\n    ")
}

// printAttachmentCaveat says the attachment limit out loud, on stderr so it
// cannot corrupt a piped --json stdout.
func printAttachmentCaveat() {
	fmt.Fprintln(os.Stderr, dim(attachmentCaveat))
}

// printHistoryCaveat says out loud, after the fact, that the note's earlier
// versions are gone.
//
// It is printed as well as confirmed because the two land on different people.
// The confirmation is skipped entirely by --yes and by any non-interactive run,
// which is exactly where a scripted sweep can delete the history of a thousand
// notes with nobody reading anything; and `notes update --notebook` seals notes
// as a SIDE EFFECT of a command run for another reason, where nobody consulted
// `notes encrypt --help` at all. So the fact is stated unconditionally on the
// way out, on stderr so it cannot corrupt a piped --json stdout.
func printHistoryCaveat() {
	fmt.Fprintln(os.Stderr, dim(historyLossCaveat))
}

// verifyConversionLanded checks the server's own answer rather than assuming a
// 200 means what we asked for happened. The PATCH response carries the saved
// note, so the flag we just set can be read straight back off it.
//
// It exists for one direction in particular. A server that ignored is_encrypted
// — an older build, a proxy that dropped the field — would answer 200 to an
// encrypt and leave the note in the clear, and the CLI would print a green tick
// over a note the user now believes is protected. That is the exact failure this
// whole command was written to clean up, so it is not taken on trust.
func verifyConversionLanded(data []byte, wantEncrypted bool, noteID string) error {
	note, _ := extractNote(data)
	if note == nil {
		return nil // nothing to check against; the write itself succeeded
	}
	if got := boolean(note, "is_encrypted"); got != wantEncrypted {
		return fmt.Errorf("the server accepted the write for note %s but reports is_encrypted=%v, not %v — check the note before trusting it ('harbor notes get %s')",
			noteID, got, wantEncrypted, noteID)
	}
	return nil
}

func init() {
	notesEncryptCmd.Flags().String("notebook", "", "Convert every note in this notebook instead of naming ids")
	notesEncryptCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")

	notesDecryptCmd.Flags().String("notebook", "", "Convert every note in this notebook instead of naming ids")
	notesDecryptCmd.Flags().String("format", "html", "How to interpret the decrypted body: html or markdown")
	notesDecryptCmd.Flags().Bool("allow-task-loss", false, "Proceed even though decrypting deletes tasks the body no longer carries")
	notesDecryptCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive use)")

	notesCmd.AddCommand(notesEncryptCmd, notesDecryptCmd)
}

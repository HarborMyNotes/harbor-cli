// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-02

package cmd

import (
	"fmt"
	"os"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
)

// ===========================================================================
// Encryption follows the notebook
// ===========================================================================
//
// A notebook marked "encrypt new notes by default" only ever kept that promise
// for notes CREATED in it. Move an existing note in — `harbor notes update <id>
// --notebook <that notebook>` is the only way the CLI moves a note — and it went
// in exactly as it was: readable, indexed for search, sitting in a notebook whose
// owner believes everything in it is sealed. Nothing was checked and nothing was
// said. This file closes that, and the matching hole on the way back out: a note
// living in such a notebook can no longer be decrypted where it stands.
//
// THE RULE, in the same shape the server enforces it (app.harbor.my
// internal/notes/notes.go:918):
//
//	the destination is a DIFFERENT notebook  &&  it encrypts by default
//	                                         &&  the note is not already encrypted
//
// All three, and each one earns its place:
//
//   - DIFFERENT. `notebook_id` echoed back unchanged is not a move, and a
//     plaintext note already sitting in an encrypting notebook may be re-saved
//     without being sealed. The server allows that deliberately and pins it with
//     a test, so a CLI that refused it would refuse something the API accepts.
//     The comparison is against the RESOLVED destination, because `--notebook ""`
//     means "my default notebook" and has to be expanded before it can be
//     compared to anything.
//   - ENCRYPTS BY DEFAULT. Asked of the server, on this run. The flag can have
//     been flipped from another device a minute ago.
//   - NOT ALREADY ENCRYPTED. Decided by the SERVER, not by whatever copy of the
//     note happens to be in hand — see planEncrypt, which re-reads. Sealing a
//     note that is already ciphertext wraps it in a second envelope and produces
//     a note that decrypts once into more ciphertext.
//
// AND IT IS ONE WRITE. The ciphertext, `is_encrypted: true` and `notebook_id` go
// out in a single PATCH, so there is no window in which the note is in the
// encrypting notebook and still readable. Everything that can fail — the
// notebook lookup, the unlock, the seal, the seal's own read-back — happens
// before it, and any of them failing leaves the note plaintext AND where it was.

// noteMovePlan is what the notebook check decided about an update carrying
// --notebook. It is returned rather than printed on the spot because both facts
// in it are only true once the write has landed, and the write can still fail.
type noteMovePlan struct {
	// sealed reports that the body now carries ciphertext and is_encrypted, so
	// the caller must send it as a conversion and verify what came back.
	sealed bool

	// staysEncrypted marks the move-OUT of an encrypting notebook: nothing about
	// the note changes, which is correct and surprising enough to say out loud.
	staysEncrypted bool
}

// prepareNoteMove applies the rule above to an update body, sealing it in place
// when the move requires it. It returns what happened so the caller can send the
// right kind of write and say the right thing afterwards.
//
// note is an already-read copy of the note or nil to fetch one — a
// content-carrying update has just read it for the task guard. It is used as a
// FAST PATH ONLY: the decision that actually gates the encryption is made
// against the server's own answer inside planEncrypt.
//
// Every return before the seal leaves body exactly as it arrived, so a plain move
// is still a plain move. It is not free, though, and the honest accounting is:
// every --notebook update now costs a notebook read AND a full note read on top of
// the PATCH it always cost. The note read is what the encrypted-note fast path and
// the move-out notice are decided from, so it is not avoidable without dropping
// one of them; a sealed move pays one more read inside planEncrypt. Moving a note
// is a deliberate, occasional act, which is the only reason that is an acceptable
// price.
func prepareNoteMove(c *client.Client, creds *config.Credentials, noteID string, body, note map[string]any) (noteMovePlan, error) {
	dest, moving := body["notebook_id"].(string)
	if !moving {
		return noteMovePlan{}, nil
	}

	// The destination is asked what it wants BEFORE this run is asked whether it
	// can deliver it, for the same reason the create path was rewritten to (#78):
	// whether a notebook requires encryption is the server's answer and has
	// nothing to do with whether this shell happens to hold a passphrase.
	nb := notebookEncryptionOf(c, dest)

	if note == nil {
		var err error
		if note, err = readNoteForConversion(c, noteID); err != nil {
			return noteMovePlan{}, err
		}
	}
	current := str(note, "notebook_id")
	move, resolvable := isNotebookMove(nb, current)

	switch {
	case boolean(note, "is_encrypted"):
		// R2 and R3 are the same code path because they are the same fact: an
		// encrypted note is moved as it is, in either direction. Nothing is
		// re-encrypted and nothing is re-keyed — the field AAD binds to the note id,
		// not to the notebook, so the envelopes are still exactly right where they
		// are going.
		return noteMovePlan{staysEncrypted: move && nb.Known && !nb.Wants &&
			notebookEncryptionOf(c, current).Wants}, nil

	case resolvable && !move:
		// A no-op echo of the note's own notebook_id. Not a move, so not this
		// function's business, even when the notebook it is already in encrypts.
		return noteMovePlan{}, nil

	case nb.Known && !nb.Wants:
		// An ordinary destination. The overwhelmingly common case, and it costs the
		// one notebook read above and nothing else.
		return noteMovePlan{}, nil

	case !nb.Known:
		// FAIL CLOSED, and this is the one place in the CLI where an unanswered
		// notebook lookup does. Creating a note is allowed to proceed on an
		// unanswered lookup (warnEncryptionUnknown) because the note is new, nothing
		// is lost, and re-running fixes it. Here the note already exists and is
		// already somewhere safe: proceeding would move it into a notebook that may
		// well encrypt by default and leave it there in the clear, which is not a
		// state re-running gets you out of — the note is already exposed. So an
		// unanswerable lookup means the note does not move.
		return noteMovePlan{}, errMoveEncryptionUnknown(nb, dest)
	}

	// Everything below here is the sealed move. It reuses the ONE conversion
	// pipeline rather than reimplementing it: the unlock that refuses rather than
	// writing plaintext, the server-authoritative re-read, the seal that opens what
	// it just sealed, and the compare-and-set.
	key, err := conversionKey(c, creds)
	if err != nil {
		return noteMovePlan{}, err
	}
	seal, err := planEncrypt(c, key, noteID, body)
	if err != nil {
		return noteMovePlan{}, err
	}
	if seal == nil {
		// The SERVER says the note is already ciphertext, whatever the copy above
		// said. Another device sealed it in between. Move it and encrypt nothing —
		// sealing it again would wrap the envelope in an envelope.
		return noteMovePlan{}, nil
	}
	mergeSealIntoUpdate(body, seal)
	// The body being sent is now an envelope, and a content_format alongside it
	// would be a claim about bytes the server will never look at. planEncrypt does
	// not produce one; a --content/--file/--stdin flag on the same command does.
	delete(body, "content_format")
	return noteMovePlan{sealed: true}, nil
}

// mergeSealIntoUpdate folds the seal's write into the update the user asked for,
// and refuses to let the seal move the compare-and-set forward.
//
// THE base_usn MUST STAY ON THE EARLIEST READ ANY DECISION WAS MADE AGAINST.
// A `notes update --content … --notebook <encrypting>` reads the note twice: once
// for the task guard, which works out from THAT body which tasks the new one
// would delete, and again inside planEncrypt, which is the server-authoritative
// re-read of whether the note is already ciphertext. Both reads are necessary and
// both stay.
//
// What must not happen is the second read's usn replacing the first one's on the
// way out. base_usn is the promise "nothing has changed since I looked", and the
// thing that looked — the task guard — looked at the FIRST read. Re-basing onto
// the second read renews that promise across a window nobody inspected: a save
// from a phone, another agent or the web app landing between the two reads would
// be silently overwritten by this write instead of refused, and the task guard
// would have authorised a write against a body that no longer exists. Keeping the
// earlier usn means the server answers 409 note_usn_stale and writes nothing,
// which is the whole point of sending it.
//
// planEncrypt still sets base_usn for the caller that established none — that is
// `notes encrypt`, where its own read is the only read there was.
func mergeSealIntoUpdate(body, seal map[string]any) {
	_, callerSetBase := body["base_usn"]
	for k, v := range seal {
		if k == "base_usn" && callerSetBase {
			continue
		}
		body[k] = v
	}
}

// isNotebookMove reports whether an update's destination is a genuine move away
// from the note's current notebook, and — separately — whether that could be
// decided at all.
//
// The second return is not pedantry. The destination is unresolvable exactly when
// the user wrote `--notebook ""` (meaning their default notebook) and the lookup
// that would have named it failed, and in that case there is no id to compare
// against: answering "not a move" would wave through the very write the guard
// exists to stop, and answering "a move" would refuse a no-op. Neither is an
// answer, so it says so and the caller fails closed.
func isNotebookMove(nb notebookEncryption, current string) (move, resolvable bool) {
	if nb.ID == "" {
		return false, false
	}
	return nb.ID != current, true
}

// errMoveEncryptionUnknown is the refusal above: the destination could not be
// asked whether its notes are encrypted, so the note has not been moved.
//
// A destination that came back 404 is answered separately, because it is not the
// same problem. "Could not read it, re-run to try again" is advice that can never
// work for a notebook id with a typo in it — and a mistyped id is by far the most
// likely way anyone arrives here.
//
// The second line is indented to sit under the first once renderError has
// prefixed it with "Error: " (7 characters).
func errMoveEncryptionUnknown(nb notebookEncryption, dest string) error {
	if nb.Missing {
		return fmt.Errorf("there is no notebook %s, so nothing was written and the note has not been moved\n"+
			"       list them with 'harbor notebooks list'", dest)
	}
	return fmt.Errorf("could not read whether notes in %s are encrypted by default, so nothing was written and the note has not been moved\n"+
		"       moving a plaintext note into a notebook that encrypts has to seal it first, and that is not a\n"+
		"       decision to make on a guess — re-run to try again",
		notebookLabel(nb.Name, dest))
}

// announce says what the write that just landed did beyond moving the note.
// Both branches go to stderr so they cannot corrupt a piped --json stdout.
func (p noteMovePlan) announce() {
	switch {
	case p.sealed:
		// The web app's toast for the same event, in the same words
		// (app.harbor.my#1267, MoveToNotebookDialog.tsx).
		fmt.Fprintln(os.Stderr, dim("Note moved and encrypted — this notebook keeps its notes encrypted."))
		// Both caveats are the ones `notes encrypt` carries, and they apply here for
		// the same reasons — this note was plaintext a moment ago, so its history
		// rows are plaintext and its attached files still are. They matter MORE
		// here: the user ran a move, and the encryption came along with it.
		printAttachmentCaveat()
		printHistoryCaveat()
	case p.staysEncrypted:
		fmt.Fprintln(os.Stderr, dim("note stays encrypted — encryption is per-note, moving it out does not remove it"))
	}
}

// writeNoteUpdate sends a `notes update` and, when the update sealed the note,
// checks the server's own answer rather than reading a 200 as agreement.
//
// A sealed move goes out through ConvertNoteToEncrypted rather than UpdateNote —
// the same PATCH, named for what it does. The two calls say different things
// about a note, and someone auditing where this CLI turns a readable note into a
// ciphertext one should find that name here too, not only in `notes encrypt`.
func writeNoteUpdate(c *client.Client, noteID string, body map[string]any, move noteMovePlan) ([]byte, error) {
	if !move.sealed {
		data, err := c.UpdateNote(noteID, body)
		if err != nil {
			return nil, mapNoteError(err)
		}
		return data, nil
	}
	data, err := c.ConvertNoteToEncrypted(noteID, body)
	if err != nil {
		return nil, mapNoteError(err)
	}
	if err := verifyConversionLanded(data, true, noteID); err != nil {
		return nil, err
	}
	return data, nil
}

// ===========================================================================
// The way back out
// ===========================================================================

// guardDecryptInEncryptedNotebook refuses to decrypt a note that lives in a
// notebook whose notes are always encrypted. It is this CLI's form of the dialog
// the other clients put in front of the same action.
//
// It is a RULE, not a destructive-action confirmation, which is why --yes does
// not reach it: --yes says "I know decrypting publishes this note", and that is a
// different question from "this notebook does not hold readable notes". The way
// through is the one the message names — move the note out first, which is
// allowed and leaves it encrypted (see the move-out branch above).
//
// A lookup that could not answer FALLS THROUGH and decrypts, which is the
// opposite of what the move path does two hundred lines up and is deliberate in
// both places. Here the gate is a courtesy on a note the user owns and has
// already unlocked, and the invariant that actually protects the notebook is
// enforced on the way IN, by both this CLI and the server. Refusing to decrypt
// because a metadata GET timed out would be the wrong trade (app.harbor.my#1267,
// D7).
func guardDecryptInEncryptedNotebook(c *client.Client, noteID string, note map[string]any) error {
	nbID := str(note, "notebook_id")
	nb := notebookEncryptionOf(c, nbID)
	if !nb.Known || !nb.Wants {
		return nil
	}
	return fmt.Errorf("notes in %s are always encrypted\n"+
		"       move the note out first: harbor notes update %s --notebook <notebook-id>",
		notebookLabel(nb.Name, nbID), noteID)
}

// ===========================================================================
// Asking the server about a notebook, once
// ===========================================================================

// notebookEncryptionLookups memoizes notebookWantsEncryption for the life of the
// process. A CLI invocation runs one command, so a notebook cannot change its
// mind inside one — but a `notes decrypt --notebook <id>` sweep asks the same
// question once per note, and a hundred-note notebook would otherwise mean a
// hundred identical reads of the same row.
var notebookEncryptionLookups map[string]notebookEncryption

// notebookEncryptionOf is notebookWantsEncryption asked at most once per
// notebook. A FAILED lookup is cached too, on purpose: whatever made the first
// read fail is not going to be fixed by asking again inside the same second, and
// re-asking would turn one unavailable server into one timeout per note.
func notebookEncryptionOf(c *client.Client, notebookID string) notebookEncryption {
	if nb, ok := notebookEncryptionLookups[notebookID]; ok {
		return nb
	}
	nb := notebookWantsEncryption(c, notebookID)
	if notebookEncryptionLookups == nil {
		notebookEncryptionLookups = map[string]notebookEncryption{}
	}
	notebookEncryptionLookups[notebookID] = nb
	return nb
}

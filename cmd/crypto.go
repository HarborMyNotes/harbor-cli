// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/spf13/cobra"
)

// passphraseEnv is the environment variable that, when set and non-empty, turns
// on transparent end-to-end encryption: notes are decrypted on read and (in a
// default_encrypt notebook or with --encrypt) encrypted on write. It is read
// straight into memory and never persisted. The recommended source is a secret
// manager, e.g. `export HARBOR_PASSPHRASE=$(op read "op://Vault/Harbor/passphrase")`.
const passphraseEnv = "HARBOR_PASSPHRASE"

// newPassphraseEnv supplies the replacement passphrase to `crypto rotate`
// non-interactively (e.g. from a secret manager in CI). When unset, rotate
// prompts for the new passphrase twice.
const newPassphraseEnv = "HARBOR_NEW_PASSPHRASE"

// Process-level session state. A CLI invocation runs a single command, so the
// master key is derived at most once and cached in memory for the duration.
var (
	sessionKey     []byte
	sessionErr     error
	sessionUnlockd bool
	decryptWarned  bool

	// encryptCheckWarned tracks the "could not tell whether this note should be
	// encrypted" warning separately from decryptWarned. They are different facts, and
	// sharing one flag would let whichever fired first silence the other.
	encryptCheckWarned bool
)

// Sentinel errors for the encryption session.
var (
	errPassphraseNotSet = fmt.Errorf("%s is not set", passphraseEnv)
	errNoKeystore       = errors.New("no encryption keystore found — run 'harbor crypto setup' first")
)

// passphraseFromEnv returns the passphrase from the environment and whether it is
// set and non-empty.
func passphraseFromEnv() (string, bool) {
	v := os.Getenv(passphraseEnv)
	return v, v != ""
}

// encryptionEnabled reports whether transparent encryption is active for this
// invocation (i.e. HARBOR_PASSPHRASE is set).
func encryptionEnabled() bool {
	_, ok := passphraseFromEnv()
	return ok
}

// resolveScopeIDValue returns the sync scope_id (the user id) without needing a
// command for flags: the cached user id, else a profile lookup cached back to
// credentials.
func resolveScopeIDValue(c *client.Client, creds *config.Credentials) (string, error) {
	if creds.UserID != "" {
		return creds.UserID, nil
	}
	data, err := c.GetProfile()
	if err != nil {
		return "", fmt.Errorf("could not resolve your user id from profile: %w", err)
	}
	id := str(parseJSON(client.UnwrapData(data)), "id")
	if id == "" {
		return "", errors.New("could not resolve your user id")
	}
	creds.UserID = id
	_ = config.Save(creds)
	return id, nil
}

// fetchKeystoreRecord pulls the single live keystore record from sync and returns
// its id, opaque blob, and current usn. found is false when the user has never
// set up encryption. (When issue #200's dedicated GET /keystore lands, this can
// be swapped for a one-shot fetch.)
func fetchKeystoreRecord(c *client.Client, creds *config.Credentials) (id, blob string, usn int64, found bool, err error) {
	scopeID, err := resolveScopeIDValue(c, creds)
	if err != nil {
		return "", "", 0, false, err
	}
	pull, err := runSyncPull(c, scopeID, "", 0, 0, true)
	if err != nil {
		return "", "", 0, false, err
	}
	for _, raw := range chunkRaw(pull) {
		env := parseJSON(raw)
		if str(env, "type") != "keystore" || boolean(env, "deleted") {
			continue
		}
		rec := nested(env, "record")
		if rec == nil {
			continue
		}
		return str(rec, "id"), str(rec, "blob"), int64(num(env, "usn")), true, nil
	}
	return "", "", 0, false, nil
}

// putKeystoreRecord writes the keystore blob through sync/push (last-write-wins),
// reusing the record id on rotation so it stays the single live row.
func putKeystoreRecord(c *client.Client, creds *config.Credentials, id, blob string, baseUSN int64) error {
	scopeID, err := resolveScopeIDValue(c, creds)
	if err != nil {
		return err
	}
	changeID, err := crypto.NewUUIDv4()
	if err != nil {
		return err
	}
	change := map[string]any{
		"type":      "keystore",
		"id":        id,
		"change_id": changeID,
		"base_usn":  baseUSN,
		"record":    map[string]any{"id": id, "blob": blob},
	}
	data, err := c.SyncPush(map[string]any{"scope_id": scopeID, "device_id": creds.DeviceID, "changes": []any{change}})
	if err != nil {
		return err
	}
	// Surface a per-change rejection as an error rather than a silent no-op.
	for _, r := range toSlice(parseJSON(data)["results"]) {
		if str(r, "status") == "rejected" {
			return fmt.Errorf("keystore write rejected: %s", str(r, "error"))
		}
	}
	return nil
}

// ensureKeystoreBlob returns the keystore blob, preferring the local 0600 cache
// and falling back to a sync fetch (which it then caches). Returns "" when no
// keystore exists yet.
func ensureKeystoreBlob(c *client.Client, creds *config.Credentials) (string, error) {
	blob, err := config.LoadKeystoreBlob()
	if err != nil {
		return "", err
	}
	if blob != "" {
		return blob, nil
	}
	_, fetched, _, found, err := fetchKeystoreRecord(c, creds)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	_ = config.SaveKeystoreBlob(fetched)
	return fetched, nil
}

// unlockMasterKey derives and caches the master key for this process: it reads
// HARBOR_PASSPHRASE, loads the keystore (cache or sync), and unwraps the key. It
// is memoized so a list of many encrypted notes derives the key only once.
func unlockMasterKey(c *client.Client, creds *config.Credentials) ([]byte, error) {
	if sessionUnlockd {
		return sessionKey, sessionErr
	}
	sessionUnlockd = true

	pass, ok := passphraseFromEnv()
	if !ok {
		sessionErr = errPassphraseNotSet
		return nil, sessionErr
	}
	blob, err := ensureKeystoreBlob(c, creds)
	if err != nil {
		sessionErr = err
		return nil, err
	}
	if blob == "" {
		sessionErr = errNoKeystore
		return nil, sessionErr
	}
	ks, err := crypto.ParseKeystore(blob)
	if err != nil {
		sessionErr = err
		return nil, err
	}
	key, err := crypto.UnwrapMasterKey(ks, pass)
	if err != nil {
		sessionErr = err
		return nil, err
	}
	sessionKey = key
	return key, nil
}

// warnDecryptOnce prints a single stderr warning per process so a wrong
// passphrase over a long listing does not spam one line per note.
func warnDecryptOnce(msg string) {
	if decryptWarned {
		return
	}
	decryptWarned = true
	fmt.Fprintln(os.Stderr, dim("⚠ "+msg))
}

// decryptResult transparently decrypts any encrypted note fields in an API
// response when encryption is enabled, so both the table and --json output show
// plaintext. On any failure it warns once and returns the data untouched
// (ciphertext is shown rather than the command failing).
func decryptResult(c *client.Client, creds *config.Credentials, data []byte) []byte {
	if !encryptionEnabled() {
		return data
	}
	key, err := unlockMasterKey(c, creds)
	if err != nil {
		warnDecryptOnce(fmt.Sprintf("could not unlock encryption (%v); showing ciphertext", err))
		return data
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return data
	}
	if !walkDecrypt(root, key) {
		return data
	}
	out, err := json.Marshal(root)
	if err != nil {
		return data
	}
	return out
}

// walkDecrypt recursively rewrites encrypted note title/content to plaintext in a
// decoded JSON tree. It targets any object with is_encrypted == true, using the
// object's id (or note_id) for the field AAD. Returns whether anything changed.
func walkDecrypt(v any, key []byte) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		if enc, _ := t["is_encrypted"].(bool); enc {
			id, _ := t["id"].(string)
			if id == "" {
				id, _ = t["note_id"].(string)
			}
			if id != "" {
				for _, field := range []string{"title", "content"} {
					s, ok := t[field].(string)
					if !ok || !crypto.IsEnvelope(s) {
						continue
					}
					pt, err := crypto.OpenField(key, id, field, s)
					if err != nil {
						warnDecryptOnce("some encrypted fields could not be decrypted; showing ciphertext")
						continue
					}
					t[field] = pt
					changed = true
				}
			}
		}
		for _, val := range t {
			if walkDecrypt(val, key) {
				changed = true
			}
		}
	case []any:
		for _, item := range t {
			if walkDecrypt(item, key) {
				changed = true
			}
		}
	}
	return changed
}

// ===========================================================================
// Encrypt-on-write
// ===========================================================================

// shouldEncryptCreate decides whether a new note should be encrypted: --plaintext
// forces off, --encrypt forces on (and requires a passphrase), otherwise it
// encrypts when the target notebook is marked default_encrypt — and REFUSES the
// create when that notebook wants encryption and no passphrase is available.
func shouldEncryptCreate(cmd *cobra.Command, c *client.Client, notebookID string) (bool, error) {
	// The sanctioned way to put an unencrypted note in an encrypting notebook. It is
	// read first, so it costs no lookup and nothing below can second-guess it — the
	// whole point of the guard further down is that skipping encryption is something
	// the user ASKS for here, rather than something an unset variable does for them.
	if boolFlag(cmd, "plaintext") {
		return false, nil
	}
	if boolFlag(cmd, "encrypt") {
		if !encryptionEnabled() {
			return false, fmt.Errorf("--encrypt requires %s to be set", passphraseEnv)
		}
		return true, nil
	}

	// The notebook is asked what it wants BEFORE this invocation is asked whether it
	// can deliver it. The old order returned early on "no passphrase" and so never
	// reached the question at all, which is how a create into an encrypt-by-default
	// notebook wrote plaintext with nothing said (#78). Whether a notebook requires
	// encryption is the server's answer and has nothing to do with this shell's
	// environment; the passphrase only decides what happens next.
	//
	// This does cost one request per create now, including on an account that has
	// never touched encryption — that is the price of the question having an answer.
	// The lookup also reports whether it could ANSWER, not just what the answer was;
	// see below and notebookWantsEncryption.
	nb := notebookWantsEncryption(c, notebookID)

	// FAIL CLOSED, matching --encrypt just above and the conversion commands
	// (conversionKey, cmd/notes_convert.go). The notebook says its notes are
	// encrypted and this invocation holds no key to do that with, so the note is not
	// written at all. This covers the account's DEFAULT notebook too, because
	// notebookWantsEncryption resolves it when no --notebook was given.
	if nb.Wants && !encryptionEnabled() {
		return false, errEncryptedNotebookNeedsPassphrase(nb.Name, notebookID)
	}

	// An UNANSWERED lookup still warns and proceeds, and that is deliberate — it is
	// NOT the same oversight as the branch above. There the notebook said "encrypted"
	// and was overruled; here nothing was established either way, and by far the most
	// likely reason to be standing here is an ordinary account with no encryption
	// anywhere near it whose notebook read happened to fail. Refusing every create on
	// a failed GET would block plain note-taking to protect a setting that is
	// probably not set, so the user is told on stderr and can re-run. (Same reasoning
	// as notebookWantsEncryption, which fails open for the same reason.)
	if !nb.Known {
		warnEncryptionUnknown(notebookID)
	}
	return nb.Wants, nil
}

// errEncryptedNotebookNeedsPassphrase is the fail-closed refusal: the destination
// encrypts every note in it and this run has no passphrase to do that with, so
// nothing is written. It names both ways forward — supply the key, or say out loud
// that this one note is meant to be in the clear.
//
// The second line is indented to sit under the first once renderError has prefixed
// it with "Error: " (7 characters).
func errEncryptedNotebookNeedsPassphrase(name, notebookID string) error {
	return fmt.Errorf("notes in %s are encrypted by default, and no passphrase is set\n"+
		"       set %s, or pass --plaintext to create this note unencrypted anyway",
		notebookLabel(name, notebookID), passphraseEnv)
}

// notebookLabel names a notebook for a user-facing message: its own name when the
// lookup returned one, else the id the user typed, else the account default — which
// the user never named, so neither can this.
func notebookLabel(name, notebookID string) string {
	switch {
	case name != "":
		return `"` + name + `"`
	case notebookID != "":
		return "notebook " + notebookID
	default:
		return "your default notebook"
	}
}

// warnEncryptionUnknown says out loud that a note is going out UNENCRYPTED because
// a notebook lookup could not establish whether it should be, rather than because
// the notebook said no. Once per process, on stderr, so it cannot corrupt a piped
// --json stdout.
func warnEncryptionUnknown(notebookID string) {
	if encryptCheckWarned {
		return
	}
	encryptCheckWarned = true
	target := "the default notebook's"
	if notebookID != "" {
		target = "notebook " + notebookID + "'s"
	}
	// --encrypt is only useful advice when there is a key to encrypt WITH. Without a
	// passphrase this branch is now reachable (the lookup runs either way), and
	// telling the user to pass --encrypt would just swap this warning for a different
	// error, so point at the thing that is actually missing.
	remedy := "re-run, or pass --encrypt to encrypt it regardless"
	if !encryptionEnabled() {
		remedy = "re-run with " + passphraseEnv + " set if it should be encrypted"
	}
	fmt.Fprintln(os.Stderr, dim("⚠ could not read "+target+" encryption setting, so this note is being "+
		"written UNENCRYPTED — "+remedy))
}

// notebookEncryption is everything a notebook lookup established about one
// notebook, and it is four facts rather than one bool because each of them is
// answerable on its own and the callers branch on different ones.
//
// ID is the RESOLVED id — the notebook the server will actually act on, which is
// not always the one the user typed: an empty --notebook means "my default
// notebook" and resolves to whatever that is (app.harbor.my
// internal/notes/notes.go resolveNotebook). The move guard needs it because the
// question "is this even a move?" is `resolved != the note's current notebook`,
// and comparing against the raw flag would call every no-op echo of a note's own
// notebook_id a move.
//
// Known is separate from Wants because a lookup that FAILED and a notebook that
// said no are different facts, and only the caller can decide what to do about
// the difference — a create says so and proceeds, a move refuses.
//
// Name is for messages only, never for a decision. It is "" whenever the lookup
// did not reach a notebook, and the callers that print it fall back to the id
// (see notebookLabel).
// Missing separates the one unanswered lookup that is actually an ANSWER. A
// notebook the server returns 404 for does not exist, and that is settled — it
// will not be there on a retry, and it cannot have an encryption setting to read.
// Folding it in with "the read did not work" is how a mistyped id ends up
// answered with "re-run to try again".
type notebookEncryption struct {
	ID      string
	Name    string
	Wants   bool
	Known   bool
	Missing bool
}

// notebookWantsEncryption reports what the target notebook (or the default
// notebook, when none is given) says about encrypting new notes — and whether
// that could be established at all. See notebookEncryption for what each field
// means and why the pair matters.
//
// WHY BOTH BRANCHES REPORT IT. This function fails OPEN — an unanswerable lookup
// writes the note in the clear — and that is deliberate: failing closed would mean
// encrypting on a guess, and a note sealed under a passphrase the user did not mean
// to use is unrecoverable, where a note written in the clear can simply be re-saved.
// But "false" as the sole answer let a failed GetNotebook read back at the call site
// as "this notebook does not want encryption", which is how a named notebook marked
// default_encrypt silently produced a plaintext note. The signal is returned rather
// than warned about in here so there is no branch left that can forget to.
//
// The DEFAULT-notebook lookup also WALKS THE PAGES. GET /notebooks is paged and has
// no "just the default one" filter, so the default notebook can sit on any page —
// reading only the first would answer "no" for an account with more notebooks than
// fit in it. That is the same one-page assumption as issue #67, found while looking
// for its siblings; it is the only other internal collection read in the command
// tree. The walk stops at the default, so an ordinary account still pays one request
// — and a walk that never reaches a default answers UNKNOWN, not "no" (see below).
func notebookWantsEncryption(c *client.Client, notebookID string) notebookEncryption {
	// A NAMED notebook is the common path — it needs no big account to reach, just a
	// --notebook flag — and it is a plain GET, so "known" is simply "the read worked".
	//
	// The ID is filled in whether or not that read works, because it does not depend
	// on it: the id is the one the user typed and the one the server will resolve to
	// the same notebook. Only default_encrypt and the name are in doubt. That is what
	// lets a failed lookup still tell a no-op echo apart from a real move, instead of
	// refusing a write that was never going anywhere.
	if notebookID != "" {
		out := notebookEncryption{ID: notebookID}
		data, err := c.GetNotebook(notebookID, false)
		if err != nil {
			var apiErr *client.APIError
			out.Missing = errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
			return out
		}
		nb := parseJSON(client.UnwrapData(data))
		out.Wants = boolean(nb, "default_encrypt")
		out.Name = str(nb, "name")
		out.Known = true
		return out
	}

	var out notebookEncryption

	// known is set inside the visit callback and NOWHERE else, so it is true exactly
	// when a notebook flagged is_default was actually read — which is the only thing
	// that can answer the question. Once it is set, whatever the walk did afterwards
	// cannot unmake that answer, so the walk's own result is deliberately discarded.
	//
	// A COMPLETED WALK THAT NEVER FOUND A DEFAULT IS "UNKNOWN", NOT "NO". It used to
	// report known=true, which read back at the call site as "the default notebook
	// does not encrypt" even though no default notebook had been seen at all — the
	// same silent plaintext write this guard exists to stop, arriving through a
	// different door. It was unreachable before, because a run with no passphrase
	// never got this far; now every create walks it, so it has to be right. Unknown
	// already has defined behaviour at the call site (say so, then proceed), and that
	// is where this belongs rather than in a third state of its own.
	_, _ = walkCollection(
		func(params map[string]string) ([]byte, error) { return c.ListNotebooks(params) },
		func(raw json.RawMessage) bool {
			n := parseJSON(raw)
			if !boolean(n, "is_default") {
				return true
			}
			out.ID = str(n, "id")
			out.Wants = boolean(n, "default_encrypt")
			out.Name = str(n, "name")
			out.Known = true
			return false
		})
	return out
}

// encryptCreateBody seals a create body's title and content into HRBC2 envelopes
// under a freshly generated note id (sent as `id` so the server stores it under
// the id the AAD is bound to), and marks the note encrypted.
func encryptCreateBody(c *client.Client, creds *config.Credentials, body map[string]any) error {
	key, err := unlockMasterKey(c, creds)
	if err != nil {
		return err
	}
	id, err := crypto.NewUUIDv4()
	if err != nil {
		return err
	}
	body["id"] = id
	body["is_encrypted"] = true
	if title, _ := body["title"].(string); title != "" {
		sealed, err := crypto.SealField(key, id, "title", title)
		if err != nil {
			return err
		}
		body["title"] = sealed
	}
	content, _ := body["content"].(string)
	sealed, err := crypto.SealField(key, id, "content", content)
	if err != nil {
		return err
	}
	body["content"] = sealed
	delete(body, "content_format") // server keeps encrypted content opaque
	return nil
}

// encryptUpdateBody re-seals an update's title/content when the target note is
// encrypted. It reads the note's encryption marker first. A plaintext note is
// left untouched; an encrypted note with no passphrase is a hard error so the CLI
// never clobbers ciphertext with plaintext.
//
// note is an already-read copy of the note, or nil to fetch one. A content-carrying
// `notes update` has just read it for the task guard (cmd/note_tasks.go), and
// reading it a second time here would be a round trip that answers a question we
// already hold the answer to.
func encryptUpdateBody(c *client.Client, creds *config.Credentials, noteID string, body, note map[string]any) error {
	_, hasTitle := body["title"]
	_, hasContent := body["content"]
	if !hasTitle && !hasContent {
		return nil // only metadata is changing; the body is untouched
	}

	if note == nil {
		meta, err := c.GetNote(noteID, nil)
		if err != nil {
			return mapNoteError(err)
		}
		note = parseJSON(client.UnwrapData(meta))
	}
	if !boolean(note, "is_encrypted") {
		return nil // plaintext note → normal update
	}
	if !encryptionEnabled() {
		return errors.New("this note is encrypted — set HARBOR_PASSPHRASE to edit it (the CLI won't write plaintext into an encrypted note)")
	}
	key, err := unlockMasterKey(c, creds)
	if err != nil {
		return err
	}
	body["is_encrypted"] = true
	if hasTitle {
		if title, _ := body["title"].(string); title != "" {
			sealed, serr := crypto.SealField(key, noteID, "title", title)
			if serr != nil {
				return serr
			}
			body["title"] = sealed
		}
	}
	if hasContent {
		content, _ := body["content"].(string)
		sealed, serr := crypto.SealField(key, noteID, "content", content)
		if serr != nil {
			return serr
		}
		body["content"] = sealed
		delete(body, "content_format")
	}
	return nil
}

// ===========================================================================
// Commands
// ===========================================================================

// cryptoCmd is the parent for end-to-end encryption management.
var cryptoCmd = &cobra.Command{
	Use:     "crypto",
	Short:   "Manage end-to-end note encryption (setup, status, rotate)",
	GroupID: groupAccount,
	Long: `Manage Harbor's client-side, end-to-end note encryption.

Encryption is transparent: set HARBOR_PASSPHRASE and the CLI decrypts notes on
read automatically, and encrypts on write in a default_encrypt notebook (or with
--encrypt). The server is zero-knowledge — it only ever stores ciphertext.

  export HARBOR_PASSPHRASE=$(op read "op://Vault/Harbor/passphrase")

WARNING: there is no recovery. If you lose the passphrase, the master key — and
every encrypted note — is permanently unrecoverable. There is no escrow or reset.`,
}

// cryptoSetupCmd performs first-time encryption setup.
var cryptoSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Set up end-to-end encryption (one time)",
	Long: `Generate your encryption keys for the first time: a random master key wrapped
by a key derived from your passphrase, written to the synced keystore so every
device can unlock with the same passphrase.

The passphrase comes from HARBOR_PASSPHRASE if set, otherwise you are prompted.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, _, _, found, err := fetchKeystoreRecord(c, creds); err != nil {
			return err
		} else if found {
			return errors.New("encryption is already set up — use 'harbor crypto rotate' to change the passphrase, or 'harbor crypto sync' to re-cache the keystore")
		}

		pass, err := passphraseForSetup()
		if err != nil {
			return err
		}

		blob, _, err := crypto.NewKeystore(pass, crypto.DefaultArgon2Params)
		if err != nil {
			return err
		}
		id, err := crypto.NewUUIDv4()
		if err != nil {
			return err
		}
		if err := putKeystoreRecord(c, creds, id, blob, 0); err != nil {
			return err
		}
		if err := config.SaveKeystoreBlob(blob); err != nil {
			return err
		}

		fmt.Println(bold("Encryption is set up."))
		fmt.Println()
		fmt.Println(redWarn("IMPORTANT: there is no recovery. If you lose this passphrase, every"))
		fmt.Println(redWarn("encrypted note is permanently unrecoverable. Store it in a password"))
		fmt.Println(redWarn("manager now."))
		fmt.Println()
		fmt.Printf("Set %s (e.g. from 1Password) and your notes decrypt automatically:\n", bold(passphraseEnv))
		fmt.Printf("  export %s=$(op read \"op://Vault/Harbor/passphrase\")\n", passphraseEnv)
		return nil
	},
}

// cryptoStatusCmd reports encryption state without revealing any secret.
var cryptoStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show encryption status (keystore present, unlockable)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		_, blob, _, found, err := fetchKeystoreRecord(c, creds)
		if err != nil {
			return err
		}
		passSet := encryptionEnabled()
		unlockable := "—"
		if found && passSet {
			if ks, perr := crypto.ParseKeystore(blob); perr == nil {
				if pass, _ := passphraseFromEnv(); pass != "" {
					if _, uerr := crypto.UnwrapMasterKey(ks, pass); uerr == nil {
						unlockable = boolMark(true)
					} else {
						unlockable = boolMark(false) + dim(" (wrong passphrase)")
					}
				}
			}
		}
		cached, _ := config.LoadKeystoreBlob()
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{
				"keystore_present": found,
				"passphrase_set":   passSet,
				"cached_locally":   cached != "",
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		printKV([][2]string{
			{"Keystore present", boolMark(found)},
			{passphraseEnv + " set", boolMark(passSet)},
			{"Cached locally", boolMark(cached != "")},
			{"Unlockable now", unlockable},
		})
		return nil
	},
}

// cryptoSyncCmd refreshes the local keystore cache from the server (e.g. after a
// passphrase rotation on another device).
var cryptoSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Re-fetch and cache the keystore from the server",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		_, blob, _, found, err := fetchKeystoreRecord(c, creds)
		if err != nil {
			return err
		}
		if !found {
			return errNoKeystore
		}
		if err := config.SaveKeystoreBlob(blob); err != nil {
			return err
		}
		fmt.Println("Keystore cached locally.")
		return nil
	},
}

// cryptoRotateCmd changes the passphrase by re-wrapping the same master key.
var cryptoRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Change your encryption passphrase (re-wraps the master key)",
	Long: `Change your passphrase. The master key is unchanged, so no note is
re-encrypted; only the wrapped key in the keystore is rewritten. Other devices
pick up the change on their next sync. Remember to update HARBOR_PASSPHRASE.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		id, blob, usn, found, err := fetchKeystoreRecord(c, creds)
		if err != nil {
			return err
		}
		if !found {
			return errNoKeystore
		}
		ks, err := crypto.ParseKeystore(blob)
		if err != nil {
			return err
		}
		oldPass, ok := passphraseFromEnv()
		if !ok {
			if oldPass, err = promptPassword("Current passphrase: "); err != nil {
				return err
			}
		}
		newPass, err := newPassphraseForRotate()
		if err != nil {
			return err
		}
		newBlob, err := crypto.RewrapMasterKey(ks, oldPass, newPass, crypto.DefaultArgon2Params)
		if err != nil {
			return err
		}
		if err := putKeystoreRecord(c, creds, id, newBlob, usn); err != nil {
			return err
		}
		if err := config.SaveKeystoreBlob(newBlob); err != nil {
			return err
		}
		fmt.Println("Passphrase rotated. Update HARBOR_PASSPHRASE (and your password manager) to the new value.")
		return nil
	},
}

// passphraseForSetup gets the setup passphrase from the env or an interactive
// double prompt, rejecting an empty value.
func passphraseForSetup() (string, error) {
	if pass, ok := passphraseFromEnv(); ok {
		return pass, nil
	}
	return promptNewPassphrase()
}

// newPassphraseForRotate returns the replacement passphrase from
// HARBOR_NEW_PASSPHRASE when set, otherwise an interactive double prompt.
func newPassphraseForRotate() (string, error) {
	if v := os.Getenv(newPassphraseEnv); v != "" {
		return v, nil
	}
	return promptNewPassphrase()
}

// promptNewPassphrase reads a new passphrase twice and confirms it matches.
func promptNewPassphrase() (string, error) {
	p1, err := promptPassword("New passphrase: ")
	if err != nil {
		return "", err
	}
	if p1 == "" {
		return "", errors.New("passphrase must not be empty")
	}
	p2, err := promptPassword("Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", errors.New("passphrases do not match")
	}
	return p1, nil
}

func init() {
	cryptoCmd.AddCommand(cryptoSetupCmd, cryptoStatusCmd, cryptoSyncCmd, cryptoRotateCmd)
	rootCmd.AddCommand(cryptoCmd)
}

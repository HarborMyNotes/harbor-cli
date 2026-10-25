// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-10-24

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/jedib0t/go-pretty/v6/text"
)

// `harbor files download --note <id>` is the CLI's "Save all attachments": every
// file linked to one note, saved into a folder under its real name. The apps
// share one behaviour, and the web code is the reference:
//
//   - Files: every file the note links, embedded or not, from GET /files?note_id=
//     (all pages). One hash is saved once; the first row wins.
//   - Order: the files the body shows, in body order, then the rest by stored
//     name (UTF-16 code-unit order), then by hash. Order only decides which of
//     two equal names gets "(2)".
//   - Names: realAttachmentName. A file already in the folder counts as taken,
//     so nothing is overwritten, and only a file that arrived takes a name.
//   - Encryption: encrypted bytes are decrypted with HARBOR_PASSPHRASE and never
//     written as they are. A file that cannot be decrypted is skipped.
//   - A file that cannot be fetched is skipped; the rest are still saved.

// noteAttachmentsPageOrder is the order the file list is paged in. Renaming a
// file does not change created_at, so rows cannot move between pages while the
// list is read.
const noteAttachmentsPageOrder = "created_at"

// maxNameAttempts bounds the retries when a chosen name turns out to exist on
// disk after all (another program wrote it after the folder was read).
const maxNameAttempts = 1000

// noteAttachment is one file linked to a note, as the file list reports it.
type noteAttachment struct {
	Hash     string
	Filename string
	MIME     string
}

// savedAttachment is one file a run saved, as --json reports it.
type savedAttachment struct {
	Hash     string `json:"hash"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}

// skippedAttachment is one file a run could not save, and why.
type skippedAttachment struct {
	Hash     string `json:"hash"`
	Filename string `json:"filename"`
	Reason   string `json:"reason"`
}

// saveAllReport is the --json result of a run.
type saveAllReport struct {
	NoteID    string              `json:"note_id"`
	OutputDir string              `json:"output_dir"`
	Total     int                 `json:"total"`
	Saved     []savedAttachment   `json:"saved"`
	Skipped   []skippedAttachment `json:"skipped"`
}

// saveNoteAttachments saves every file linked to a note into dir and reports
// each one. It returns an error when any file was skipped, so the exit code is
// 0 only when everything was saved.
func saveNoteAttachments(c *client.Client, creds *config.Credentials, noteID, dir string, raw bool) error {
	note, err := fetchNoteForAttachments(c, noteID)
	if err != nil {
		return err
	}
	files, err := listNoteAttachments(c, noteID)
	if err != nil {
		return err
	}

	report := saveAllReport{NoteID: noteID, OutputDir: dir, Total: len(files),
		Saved: []savedAttachment{}, Skipped: []skippedAttachment{}}
	if len(files) == 0 {
		if jsonOutput {
			return printSaveAllJSON(report)
		}
		fmt.Println("This note has no attachments.")
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create the folder %s: %w", dir, err)
	}
	used, err := takenNames(dir)
	if err != nil {
		return err
	}

	body := noteBodyForOrder(c, creds, noteID, note)
	for _, f := range orderAttachments(files, body) {
		path, n, reason := saveOneAttachment(c, creds, dir, used, f, raw)
		if reason != "" {
			report.Skipped = append(report.Skipped, skippedAttachment{Hash: f.Hash, Filename: f.Filename, Reason: reason})
			if !jsonOutput {
				fmt.Printf("%s  %s: %s\n", colorize("skipped", text.FgYellow), attachmentLabel(f), reason)
			}
			continue
		}
		report.Saved = append(report.Saved, savedAttachment{Hash: f.Hash, Filename: f.Filename, Path: path, Size: n})
		if !jsonOutput {
			fmt.Printf("%s  %s\n", colorize("saved", text.FgGreen), path)
		}
	}

	if jsonOutput {
		if err := printSaveAllJSON(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("%d of %d attachments saved\n", len(report.Saved), report.Total)
	}
	if n := len(report.Skipped); n > 0 {
		return fmt.Errorf("%d of %d attachments could not be saved — each one is listed above", n, report.Total)
	}
	return nil
}

// printSaveAllJSON prints a run's report as indented JSON.
func printSaveAllJSON(report saveAllReport) error {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// fetchNoteForAttachments reads the note whose files are being saved: its body
// gives the order, and a missing note is better reported as such than as a note
// with no files. A note in the Trash reads as missing, as it does everywhere.
func fetchNoteForAttachments(c *client.Client, noteID string) (map[string]any, error) {
	raw, err := c.GetNote(noteID, map[string]string{"format": "html"})
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "not_found" {
			friendly := *apiErr
			friendly.Message = "no note with that id (a note in the Trash has to be restored first)"
			return nil, &friendly
		}
		return nil, err
	}
	note := parseJSON(client.UnwrapData(raw))
	if note == nil {
		return nil, errors.New("could not read the note")
	}
	return note, nil
}

// listNoteAttachments returns every file linked to the note, each hash once (the
// first row wins). A list that could not be read whole is an error rather than a
// smaller save, so "every attachment" stays true.
func listNoteAttachments(c *client.Client, noteID string) ([]noteAttachment, error) {
	var out []noteAttachment
	seen := map[string]bool{}
	complete, err := walkCollection(func(params map[string]string) ([]byte, error) {
		params["note_id"] = noteID
		params["order"] = noteAttachmentsPageOrder
		return c.ListFiles(params)
	}, func(item json.RawMessage) bool {
		f := parseJSON(item)
		hash := str(f, "hash")
		if hash == "" || seen[hash] {
			return true
		}
		seen[hash] = true
		out = append(out, noteAttachment{Hash: hash, Filename: str(f, "filename"), MIME: str(f, "mime")})
		return true
	})
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, errors.New("could not read the note's whole file list, so nothing was saved — try again")
	}
	return out, nil
}

// noteBodyForOrder returns the note's HTML body, decrypted when the note is
// encrypted and the key is available. A body that will not decrypt reads as
// empty, which only costs the files their body order.
func noteBodyForOrder(c *client.Client, creds *config.Credentials, noteID string, note map[string]any) string {
	content := str(note, "content")
	if !crypto.IsEnvelope(content) {
		return content
	}
	key, err := unlockMasterKey(c, creds)
	if err != nil {
		return ""
	}
	plain, err := crypto.OpenField(key, noteID, "content", content)
	if err != nil {
		return ""
	}
	return plain
}

// harborEmbedTag and embedResourceAttr find the files a note body shows: each
// <harbor-embed> tag and its resource="sha256:…" attribute.
var (
	harborEmbedTag    = regexp.MustCompile(`(?i)<harbor-embed\b[^>]*>`)
	embedResourceAttr = regexp.MustCompile(`(?i)\bresource=["']([^"']+)["']`)
	sha256Hex         = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// embeddedHashes returns the sha256 hashes a note body's <harbor-embed> tags
// show, lowercased, each once, in the order they first appear. A reference that
// is not a bare sha256 (an Evernote MD5, a URL) is ignored, as the server does.
func embeddedHashes(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tag := range harborEmbedTag.FindAllString(body, -1) {
		m := embedResourceAttr.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		hash := strings.TrimPrefix(strings.ToLower(m[1]), "sha256:")
		if !sha256Hex.MatchString(hash) || seen[hash] {
			continue
		}
		seen[hash] = true
		out = append(out, hash)
	}
	return out
}

// orderAttachments puts the files the body shows first, in body order, then the
// rest by stored name, then by hash. Names compare by UTF-16 code units, the
// order every app can reproduce, so all of them number the same file "(2)".
func orderAttachments(files []noteAttachment, body string) []noteAttachment {
	shown := map[string]int{}
	for i, h := range embeddedHashes(body) {
		shown[h] = i
	}
	place := func(f noteAttachment) int {
		if i, ok := shown[strings.ToLower(f.Hash)]; ok {
			return i
		}
		return len(shown)
	}
	out := append([]noteAttachment(nil), files...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if pa, pb := place(a), place(b); pa != pb {
			return pa < pb
		}
		if c := compareUTF16(a.Filename, b.Filename); c != 0 {
			return c < 0
		}
		return compareUTF16(a.Hash, b.Hash) < 0
	})
	return out
}

// compareUTF16 compares two strings by their UTF-16 code units, the plain
// ordinal order of JavaScript, Swift's utf16 view, Kotlin and C#. It differs
// from Go's byte order for characters above U+FFFF.
func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

// saveOneAttachment downloads one file, decrypts it when it is sealed, and saves
// it in dir under its real name. It returns the saved path and size, or the
// reason it was skipped.
func saveOneAttachment(c *client.Client, creds *config.Credentials, dir string, used map[string]bool, f noteAttachment, raw bool) (string, int64, string) {
	body, _, _, err := openFileDownload(c, f.Hash, raw)
	if err != nil {
		return "", 0, downloadFailureReason(err)
	}
	defer body.Close()

	content, reason := openAttachmentBytes(c, creds, body)
	if reason != "" {
		return "", 0, reason
	}
	path, n, err := saveUnderRealName(dir, used, f.Filename, f.MIME, content)
	if err != nil {
		return "", 0, err.Error()
	}
	return path, n, ""
}

// downloadFailureReason words a failed download for the skip line: the API's
// message (made friendly for the file codes), or the error itself.
func downloadFailureReason(err error) string {
	var apiErr *client.APIError
	if errors.As(mapFileError(err), &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}

// openAttachmentBytes returns a file's bytes as they should be saved: plain
// bytes as they are, and sealed bytes decrypted. Sealed bytes that cannot be
// decrypted give a reason instead, so no ciphertext is ever written. It checks
// the bytes themselves rather than the is_encrypted flags, so a sealed file is
// caught even when a flag is missing.
func openAttachmentBytes(c *client.Client, creds *config.Credentials, body io.Reader) (io.Reader, string) {
	content, sealed, err := sniffEnvelope(body)
	if err != nil {
		return nil, "download failed: " + err.Error()
	}
	if !sealed {
		return content, ""
	}
	key, err := unlockMasterKey(c, creds)
	if err != nil {
		switch {
		case errors.Is(err, errPassphraseNotSet):
			return nil, "it is encrypted and " + passphraseEnv + " is not set"
		case errors.Is(err, errNoKeystore):
			return nil, "it is encrypted and this account has no encryption keys on this machine — run 'harbor crypto sync'"
		}
		return nil, "it is encrypted and the key could not be unlocked: " + err.Error()
	}
	envelope, err := io.ReadAll(content)
	if err != nil {
		return nil, "download failed: " + err.Error()
	}
	plain, err := crypto.OpenBytes(key, envelope)
	if err != nil {
		return nil, "it is encrypted and did not decrypt with your key"
	}
	return bytes.NewReader(plain), ""
}

// attachmentLabel names a skipped file in the report: its stored name, or its
// hash when it has none.
func attachmentLabel(f noteAttachment) string {
	if name := strings.TrimSpace(f.Filename); name != "" {
		return name
	}
	return f.Hash
}

// takenNames returns the collisionKey of every entry already in dir, so a save
// into it never takes a name that is there. A folder that does not exist yet
// holds nothing.
func takenNames(dir string) (map[string]bool, error) {
	used := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return used, nil
		}
		return nil, fmt.Errorf("cannot read the folder %s: %w", dir, err)
	}
	for _, e := range entries {
		used[collisionKey(e.Name())] = true
	}
	return used, nil
}

// saveUnderRealName writes r into dir under the real-name rule's name for the
// file and returns the path and byte count. The file is created only if nothing
// has that name, so even a file that appeared after the folder was read is never
// overwritten: its name is marked taken and the next one is tried. If the write
// fails, the partial file is removed and its name is given back, so a file that
// was not saved never makes another one "(2)".
func saveUnderRealName(dir string, used map[string]bool, filename, mimeType string, r io.Reader) (string, int64, error) {
	for attempt := 0; attempt < maxNameAttempts; attempt++ {
		name := realAttachmentName(used, filename, mimeType)
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			delete(used, collisionKey(name))
			return "", 0, fmt.Errorf("cannot create %s: %w", path, err)
		}
		n, err := io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
			delete(used, collisionKey(name))
			return "", n, fmt.Errorf("cannot write %s: %w", path, err)
		}
		return path, n, nil
	}
	return "", 0, fmt.Errorf("could not find a free name for %q in %s", filename, dir)
}

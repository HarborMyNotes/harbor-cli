// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/spf13/cobra"
)

// filesCmd is the parent for attachment commands.
//
// Note: the CLI deliberately omits the internal presign-upload and commit
// endpoints. Uploads go through the direct multipart endpoint, which computes
// the sha256 server-side.
var filesCmd = &cobra.Command{
	Use:     "files",
	Aliases: []string{"file"},
	Short:   "Manage file attachments (list, check, upload, download, rename)",
	GroupID: groupSync,
	Long: `Work with content-addressed file attachments. Upload uses the direct
multipart endpoint (the server computes the sha256); download follows a
short-lived presigned URL, or streams through the API with --raw.`,
}

// filesListCmd lists files with their linked notes.
var filesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List files with their linked notes",
	Example: `  harbor files list --mime image/
  harbor files list --note-id a1b2... --order -size`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		params := pagingParams(cmd)
		for flag, key := range map[string]string{
			"mime": "mime", "note-id": "note_id", "ocr-status": "ocr_status", "updated-since": "updated_since",
		} {
			if s := stringFlag(cmd, flag); s != "" {
				params[key] = s
			}
		}
		if cmd.Flags().Changed("encrypted") {
			params["is_encrypted"] = boolStr(boolFlag(cmd, "encrypted"))
		}
		data, err := c.ListFiles(params)
		if err != nil {
			return err
		}
		printResult(data, displayFiles)
		return nil
	},
}

// filesCheckCmd checks whether a blob exists by hash (or computed from a file).
var filesCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check whether a blob already exists",
	Long: `Check by --hash (and optional --size), or pass --file to compute the sha256 and
size locally.

--file hashes the file as it sits on disk, so it only answers for uploads that
were NOT encrypted. An encrypted blob is stored as an HRBC2 envelope and its
content address covers that ciphertext, which carries a fresh nonce every time —
so a file uploaded with 'files upload --encrypted' will always report "does not
exist" here, and encrypted uploads never deduplicate.`,
	Example: `  harbor files check --hash e3b0c442...b855
  harbor files check --file diagram.png`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		hash := stringFlag(cmd, "hash")
		size := int64(intFlag(cmd, "size"))
		if file := stringFlag(cmd, "file"); file != "" {
			h, n, herr := hashFile(file)
			if herr != nil {
				return herr
			}
			hash, size = h, n
		}
		if hash == "" {
			return errors.New("pass --hash (or --file to compute it)")
		}
		data, err := c.CheckFile(hash, size)
		if err != nil {
			return err
		}
		printResult(data, displayFileCheck)
		return nil
	},
}

// filesUploadCmd uploads a file via direct multipart.
var filesUploadCmd = &cobra.Command{
	Use:   "upload <path>",
	Short: "Upload a file",
	Args:  cobra.ExactArgs(1),
	Long: `Upload a file and get back its content-addressed resource record.

With --encrypted the bytes are sealed on this machine before they leave it: the
file is wrapped in an HRBC2 binary envelope under your master key, and the server
only ever sees ciphertext. It needs HARBOR_PASSPHRASE and refuses rather than
uploading anything in the clear.

The filename and MIME type are recorded as they were, in plaintext — the same
accepted trade every other Harbor client makes, so the file stays recognisable in
listings. The stored size is the envelope's (33 bytes larger than the original),
and because the content hash covers the ciphertext, an encrypted upload can never
deduplicate against an existing blob.`,
	Example: `  harbor files upload diagram.png
  harbor files upload report.pdf --mime application/pdf
  harbor files upload secrets.pdf --encrypted`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		path, mimeType, filename := args[0], stringFlag(cmd, "mime"), stringFlag(cmd, "filename")

		if err := checkUploadSize(c, path); err != nil {
			return err
		}

		if !boolFlag(cmd, "encrypted") {
			data, uerr := c.UploadFile(path, mimeType, filename, false)
			if uerr != nil {
				return mapFileError(uerr)
			}
			printResult(data, displayResource)
			return nil
		}

		// Fail closed: without the key we would otherwise upload the file in the
		// clear while stamping it is_encrypted, which is worse than not uploading.
		key, err := filesKey(c, creds)
		if err != nil {
			return err
		}
		data, err := uploadEncrypted(c, key, path, mimeType, filename)
		if err != nil {
			return err
		}
		printResult(data, displayResource)
		return nil
	},
}

// uploadEncrypted seals a file on this machine and uploads the envelope, so the
// server never receives the plaintext.
//
// It is a named function rather than inline RunE so a test can point it at a
// mock server and assert what actually goes on the wire. That matters more here
// than usual: the bug this replaced was an upload that stamped the resource
// is_encrypted while sending the file in the clear, and every unit test still
// passed. Asserting the multipart body carries the envelope and NOT the
// plaintext is the only check that catches a regression to it.
func uploadEncrypted(c *client.Client, key []byte, path, mimeType, filename string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read file: %w", err)
	}
	// Resolve both from the ORIGINAL file, before sealing — sniffing the
	// envelope would record every encrypted upload as octet-stream.
	if filename == "" {
		filename = filepath.Base(path)
	}
	if mimeType == "" {
		mimeType = client.DetectMIME(path)
	}
	sealed, err := crypto.SealBytes(key, content)
	if err != nil {
		return nil, fmt.Errorf("encrypting %s: %w", filepath.Base(path), err)
	}
	data, err := c.UploadBytes(sealed, mimeType, filename, true)
	if err != nil {
		return nil, mapFileError(err)
	}
	return data, nil
}

// filesGetCmd shows the presigned download URL + basic metadata for a blob.
var filesGetCmd = &cobra.Command{
	Use:     "get <hash>",
	Short:   "Get a file's presigned download URL and metadata (no bytes)",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor files get e3b0c442...b855",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetFileDownload(args[0])
		if err != nil {
			return err
		}
		printResult(data, displayDownloadInfo)
		return nil
	},
}

// filesDownloadCmd downloads one file's bytes, or every file of a note.
var filesDownloadCmd = &cobra.Command{
	Use:   "download {<hash> | --note <id>}",
	Short: "Download a file's bytes, or every file of a note",
	Args:  filesDownloadArgs,
	Long: `Download a file by hash, or every file linked to a note with --note.

Files are saved under their own names: the stored name, with only what a
filesystem cannot hold changed (/ \ : < > " | ? * # % become _), and the type's
extension added when the name has none. Nothing is overwritten: when the name is
taken in the folder, the file is saved as "name (2).ext", then "name (3).ext".

By default a download follows a presigned URL; --raw streams it through the API
instead. --output names the file yourself (- for stdout); it writes over a file
that is already there.

--note <id> saves every file linked to the note, once each, into --output-dir
(default: the current folder; created if missing). It prints one line per file
and a summary, and exits 1 if any file was skipped.

Encrypted files are decrypted automatically when HARBOR_PASSPHRASE is set. When
it is not, downloading an encrypted file fails rather than writing ciphertext you
cannot use — pass --ciphertext to write the raw envelope anyway (for backups or
moving bytes between machines). Without --output it is saved under the file's
hash, so sealed bytes never take the real file's name. With --note an encrypted
file is skipped instead, and the rest are still saved.`,
	Example: `  harbor files download e3b0...
  harbor files download e3b0... --output diagram.png
  harbor files download e3b0... --raw --output -
  harbor files download e3b0... --ciphertext --output sealed.bin
  harbor files download --note 9c2e... --output-dir ./attachments`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if noteID := stringFlag(cmd, "note"); noteID != "" {
			dir := stringFlag(cmd, "output-dir")
			if dir == "" {
				dir = "."
			}
			return saveNoteAttachments(c, creds, noteID, dir, boolFlag(cmd, "raw"))
		}

		hash := args[0]
		out := stringFlag(cmd, "output")
		body, filename, mimeType, err := openFileDownload(c, hash, boolFlag(cmd, "raw"))
		if err != nil {
			return err
		}
		defer body.Close()

		content, err := decryptDownload(c, creds, body, boolFlag(cmd, "ciphertext"))
		if err != nil {
			return err
		}
		var n int64
		switch {
		case out != "":
			n, err = writeOutput(out, content)
		case boolFlag(cmd, "ciphertext"):
			out, n, err = saveUnderRealName(".", takenNames("."), hash, "", content)
		default:
			if filename == "" {
				filename, mimeType = storedNameAndType(c, hash, mimeType)
			}
			out, n, err = saveUnderRealName(".", takenNames("."), filename, mimeType, content)
		}
		if err != nil {
			return err
		}
		if out != "-" {
			fmt.Printf("Wrote %s to %s\n", bytesHuman(float64(n)), out)
		}
		return nil
	},
}

// filesDownloadArgs checks the two ways to call download: one hash, or --note
// with no hash. Flags that only fit one of them are refused with the other, so
// a mixed-up command fails before anything is fetched.
func filesDownloadArgs(cmd *cobra.Command, args []string) error {
	if stringFlag(cmd, "note") == "" {
		if cmd.Flags().Changed("output-dir") {
			return errors.New("--output-dir goes with --note; to name one file, use --output")
		}
		return cobra.ExactArgs(1)(cmd, args)
	}
	switch {
	case len(args) > 0:
		return errors.New("pass a file hash or --note, not both")
	case cmd.Flags().Changed("output"):
		return errors.New("--output names one file; with --note, choose the folder with --output-dir")
	case boolFlag(cmd, "ciphertext"):
		return errors.New("--ciphertext cannot be used with --note: saving a note's files never writes encrypted bytes")
	}
	return nil
}

// openFileDownload starts downloading one file and returns its body (the caller
// closes it) with the stored name and type. The default path asks the API for a
// presigned URL, whose answer carries both. Raw streams the bytes through the
// API, and the name and type come from the response headers; the server sends
// no name there, so a caller that needs it asks storedNameAndType.
func openFileDownload(c *client.Client, hash string, raw bool) (io.ReadCloser, string, string, error) {
	if raw {
		resp, err := c.RawDownload(hash)
		if err != nil {
			return nil, "", "", err
		}
		filename := filenameFromContentDisposition(resp.Header.Get("Content-Disposition"))
		return resp.Body, filename, resp.Header.Get("Content-Type"), nil
	}
	meta, err := c.GetFileDownload(hash)
	if err != nil {
		return nil, "", "", err
	}
	info := parseJSON(meta)
	url := str(info, "download_url")
	if url == "" {
		return nil, "", "", errors.New("no download URL returned")
	}
	resp, err := c.FetchURL(url)
	if err != nil {
		return nil, "", "", err
	}
	return resp.Body, str(info, "filename"), str(info, "mime"), nil
}

// storedNameAndType looks up a file's stored name and type, for a --raw download
// whose response did not name it. If the lookup fails, the type the download
// reported is kept and the name stays empty, which the rule saves as
// "attachment".
func storedNameAndType(c *client.Client, hash, mimeType string) (string, string) {
	meta, err := c.GetFileDownload(hash)
	if err != nil {
		return "", mimeType
	}
	info := parseJSON(meta)
	if m := str(info, "mime"); m != "" {
		mimeType = m
	}
	return str(info, "filename"), mimeType
}

// filesRenameCmd renames a file, keeping its extension.
var filesRenameCmd = &cobra.Command{
	Use:   "rename <hash> <new-name>",
	Short: "Rename a file (the extension is kept)",
	Args:  cobra.ExactArgs(2),
	Long: `Give a file a new name. A file has one name for the whole account, so the new
name shows in every note that holds the file.

The extension cannot change. Type the name with or without it: when the name
does not already end with the file's extension, the extension is added for you.

<hash> is the full 64-character hash ('harbor files list --json' shows it).`,
	Example: `  harbor files rename e3b0c442...b855 "Scan 2015-01-06"
  harbor files rename e3b0c442...b855 invoice-march.pdf`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		hash := args[0]
		stored, err := storedFilename(c, hash)
		if err != nil {
			return mapFileError(err)
		}
		data, err := c.RenameFile(hash, withStoredExtension(args[1], stored))
		if err != nil {
			return mapFileError(err)
		}
		printResult(data, displayResource)
		return nil
	},
}

// storedFilename returns the name a file has now, which a rename needs to know
// the extension it must keep.
//
// The lookup also checks that the bytes are in storage, and a file whose bytes
// are missing can still be renamed. That one failure returns an empty name, so
// the new name goes to the server as typed.
func storedFilename(c *client.Client, hash string) (string, error) {
	meta, err := c.GetFileDownload(hash)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "blob_missing" {
			return "", nil
		}
		return "", err
	}
	return str(parseJSON(meta), "filename"), nil
}

// withStoredExtension returns the name to send for a rename: name as typed when
// it already ends with the stored file's extension (ignoring case), otherwise
// with that extension added.
//
// A dot in the typed name is not read as an extension of its own, because a
// name such as "Scan v1.2" has one that is not: for a PDF it becomes
// "Scan v1.2.pdf".
func withStoredExtension(name, stored string) string {
	name = strings.TrimSpace(name)
	ext := fileExtension(stored)
	if ext == "" {
		return name
	}
	if cut := len(name) - len(ext); cut >= 0 && strings.EqualFold(name[cut:], ext) {
		return name
	}
	return name + ext
}

// fileExtension returns a filename's extension, dot included, or "" when it has
// none. The extension is the text from the last dot, when that dot is not the
// first character and only ASCII letters or digits follow it: "a.tar.gz" has
// ".gz", while ".bashrc", "README", "notes." and "Dr. Smith" have none.
//
// This is the server's rule, which filepath.Ext does not follow (it calls
// ".bashrc" and ". Smith" extensions), and the two must agree on what a rename
// has to keep.
func fileExtension(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	for _, r := range name[i+1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return name[i:]
}

// filesKey unlocks the master key for an encrypted upload, turning the two
// sentinel unlock failures into actionable refusals. It fails closed on purpose:
// the alternative is uploading a file in the clear while stamping the resource
// is_encrypted, which leaves the user believing a plaintext blob is sealed.
func filesKey(c *client.Client, creds *config.Credentials) ([]byte, error) {
	key, err := unlockMasterKey(c, creds)
	if err == nil {
		return key, nil
	}
	switch {
	case errors.Is(err, errPassphraseNotSet):
		return nil, fmt.Errorf("--encrypted needs your encryption passphrase and %s is not set, so nothing was uploaded.\n\n"+
			"  export %s=$(op read \"op://Vault/Harbor/passphrase\")\n\n"+
			"Uploading anyway would put the file on the server in the clear while marking it encrypted",
			passphraseEnv, passphraseEnv)
	case errors.Is(err, errNoKeystore):
		return nil, errors.New("this account has no encryption keys yet, so nothing was uploaded — run 'harbor crypto setup' first (or 'harbor crypto sync' if you set them up on another device)")
	}
	return nil, fmt.Errorf("could not unlock encryption, so nothing was uploaded: %w", err)
}

// decryptDownload transparently unwraps an encrypted blob on its way to disk.
//
// It sniffs the leading bytes for the HRBC2 binary magic rather than trusting
// resource metadata, because the presigned-download path returns no is_encrypted
// field — and sniffing is what the other clients do too. A plaintext blob is
// passed straight through as a stream, so ordinary downloads keep their memory
// profile; only an envelope is buffered, which AES-GCM requires anyway since the
// authentication tag lives at the end.
//
// With no passphrase it REFUSES rather than writing ciphertext into a file the
// user will think is their document. That matches web and macOS/iOS, which both
// decline to hand over bytes they cannot read. Android and Windows still have
// surfaces that pass the raw envelope through (Android opens the presigned URL
// in a browser; Windows' in-note save path writes ciphertext even when
// unlocked) — those are bugs on those clients, not a different design, so
// refusing here is the parity-correct behaviour rather than a deviation.
// wantCiphertext is the explicit opt-out for backups and moving bytes between
// machines.
func decryptDownload(c *client.Client, creds *config.Credentials, body io.Reader, wantCiphertext bool) (io.Reader, error) {
	rest, sealed, err := sniffEnvelope(body)
	if err != nil {
		return nil, err
	}
	if !sealed {
		return rest, nil
	}
	if wantCiphertext {
		fmt.Fprintln(os.Stderr, dim("This file is encrypted; writing the raw envelope as asked (--ciphertext)."))
		return rest, nil
	}

	key, err := unlockMasterKey(c, creds)
	if err != nil {
		switch {
		case errors.Is(err, errPassphraseNotSet):
			return nil, fmt.Errorf("this file is encrypted and %s is not set, so nothing was written.\n\n"+
				"  export %s=$(op read \"op://Vault/Harbor/passphrase\")\n\n"+
				"Re-run with --ciphertext to write the sealed bytes instead",
				passphraseEnv, passphraseEnv)
		case errors.Is(err, errNoKeystore):
			return nil, errors.New("this file is encrypted but this account has no encryption keys cached — run 'harbor crypto sync' first, or re-run with --ciphertext to write the sealed bytes")
		}
		return nil, fmt.Errorf("this file is encrypted and the key could not be unlocked, so nothing was written: %w", err)
	}

	envelope, err := io.ReadAll(rest)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.OpenBytes(key, envelope)
	if err != nil {
		// Also reachable for a plaintext file that happens to begin with the
		// ASCII bytes "HRBC2" — magic-sniffing cannot tell those apart, so name
		// the escape hatch rather than insisting the key is wrong.
		return nil, fmt.Errorf("this file did not decrypt with your key, so nothing was written "+
			"(if it was never encrypted, re-run with --ciphertext to write it as stored): %w", err)
	}
	return bytes.NewReader(plain), nil
}

// sniffEnvelope reads the start of a download to see whether it is an HRBC2
// binary envelope, and returns a reader that still yields every byte. Only the
// first few bytes are held, so a plain file keeps streaming.
func sniffEnvelope(body io.Reader) (io.Reader, bool, error) {
	head := make([]byte, crypto.BinaryEnvelopeMinBytes)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}
	head = head[:n]
	return io.MultiReader(bytes.NewReader(head), body), crypto.IsBinaryEnvelope(head), nil
}

// mapFileError gives friendly messages for file-specific codes while keeping
// the typed *client.APIError, so the display layer can still render the code
// line, the detail bullets and --verbose's http/request_id, and --json still
// reports the code the server sent rather than a generic cli_error.
//
// Only the message is swapped; every other field is carried through. A code
// this does not recognise passes through untouched, which is what keeps the
// plan-limit walkthrough and its exit code working for uploads.
func mapFileError(err error) error {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	var friendly string
	switch apiErr.Code {
	case "file_too_large":
		friendly = "the file exceeds the maximum upload size"
	case "unsupported_type":
		friendly = "that media type is not allowed by the server policy"
	case "blob_missing":
		friendly = "the blob bytes are not stored"
	case "not_found":
		friendly = "no file with that hash — pass the full 64-character hash ('harbor files list --json' shows it)"
	default:
		return err
	}
	rewritten := *apiErr
	rewritten.Message = friendly
	return &rewritten
}

// uploadPolicyProbeTimeout bounds the /client-flags fetch that precedes an
// upload. The document is tiny and static, so anything slower than this is a
// sick network — and waiting on it costs more than the pre-check saves.
const uploadPolicyProbeTimeout = 5 * time.Second

// uploadSizeCap returns the server's published per-file upload cap in bytes, or
// 0 when it cannot be established.
//
// Zero means "check nothing". An unreachable or older server, a malformed
// document, or a missing field all fail OPEN and leave the upload behaving
// exactly as it did before this check existed. There is deliberately no
// fallback constant: a client that invents the cap refuses perfectly good files
// the day an operator raises FILES_MAX_UPLOAD_BYTES, which is a worse failure
// than the round-trip it saves.
func uploadSizeCap(c *client.Client) int64 {
	// A dedicated anonymous client, for two reasons. /client-flags is public, so
	// the probe needs no bearer. And it must not inherit the 30-second timeout
	// sized for real API calls: this check exists to SAVE the user time, so on a
	// network that swallows packets it has to give up quickly and let the upload
	// try, rather than doubling the wait before the same failure.
	probe := client.NewClient(c.BaseURL, "")
	probe.HTTPClient = &http.Client{Timeout: uploadPolicyProbeTimeout}

	data, err := probe.ClientFlags()
	if err != nil {
		return 0
	}
	limit := int64(num(parseJSON(data), "max_upload_bytes"))
	if limit <= 0 {
		return 0
	}
	return limit
}

// checkUploadSize refuses an over-cap file before a single byte is streamed,
// naming the file, its size and the limit.
//
// The comparison is ">", not ">=", mirroring the server's own predicate: a file
// sized exactly at the cap is accepted, and a client that refused it would
// block an upload the server would take with no round-trip to explain why.
//
// The refusal carries the server's own file_too_large code rather than a bare
// string, so catching it early is indistinguishable from catching it late: a
// script reading --json branches on the same code either way, and the terminal
// prints the same two lines.
//
// It measures the file as it sits on disk. With --encrypted the HRBC2 envelope
// adds 33 bytes, so a file within 33 bytes of the cap still reaches the server
// and is refused there — the same answer either way, and it keeps the message
// honest about the size the user can actually see.
//
// The local checks come first so a typo'd path costs no network round-trip.
func checkUploadSize(c *client.Client, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		// Not ours to report: the upload opens the file next and says something
		// better about why it could not be read.
		return nil
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", filepath.Base(path))
	}
	limit := uploadSizeCap(c)
	if limit <= 0 || info.Size() <= limit {
		return nil
	}
	return &client.APIError{
		Code: "file_too_large",
		Message: fmt.Sprintf("%q is %s — the per-file limit is %s",
			filepath.Base(path), limitBytesHuman(info.Size(), true), limitBytesHuman(limit, false)),
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// hashFile computes the sha256 (hex) and byte size of a file.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("cannot open file: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("cannot read file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// writeOutput streams r to a path ("-" = stdout) and returns the byte count.
// If the stream fails part way, a regular file at the path is removed: a
// truncated download that looks like a finished one is worse than no file at
// all. Anything else there — a symlink, a device such as /dev/null, a named
// pipe — belongs to the user and is left alone.
func writeOutput(path string, r io.Reader) (int64, error) {
	if path == "-" {
		return io.Copy(os.Stdout, r)
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("cannot create output file: %w", err)
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeIfRegularFile(path)
		return n, fmt.Errorf("cannot write output file: %w", err)
	}
	return n, nil
}

// removeIfRegularFile deletes path only when it is a plain file. Lstat, not
// Stat, so a symlink is judged as itself rather than by what it points to.
func removeIfRegularFile(path string) {
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		_ = os.Remove(path)
	}
}

// filenameFromContentDisposition extracts a filename from a Content-Disposition
// header, if present.
//
// It parses rather than scanning for the next ";" because the name is often a
// note TITLE, and titles contain semicolons: quoted per the spec,
// `filename="Q3 planning; Dana.md"` is one name, not two. Parsing also picks up
// RFC 5987's filename* form. A header too malformed to parse falls through to
// the scan, which still recovers a usable name from most of them.
func filenameFromContentDisposition(cd string) string {
	if _, params, err := mime.ParseMediaType(cd); err == nil {
		// Go decodes filename* without checking what it decoded to, and prefers it
		// over filename — so a broken one must not beat a good plain name.
		if name := params["filename"]; name != "" && utf8.ValidString(name) {
			return name
		}
	}
	const marker = "filename="
	i := strings.Index(cd, marker)
	if i < 0 {
		return ""
	}
	name := cd[i+len(marker):]
	if j := strings.Index(name, ";"); j >= 0 {
		name = name[:j]
	}
	return strings.Trim(strings.TrimSpace(name), `"`)
}

// boolStr renders a bool as "true"/"false" for query params.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ===========================================================================
// Display
// ===========================================================================

// displayFiles renders a file collection with a linked-note count.
func displayFiles(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"HASH", "MIME", "SIZE", "OCR", "THUMB", "FILENAME", "NOTES"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		f := parseJSON(raw)
		notes := toSlice(f["notes"])
		rows = append(rows, []string{
			shortID(str(f, "hash"), 12),
			str(f, "mime"),
			bytesHuman(num(f, "size")),
			colorizeStatus(str(f, "ocr_status")),
			colorizeStatus(str(f, "thumb_status")),
			truncate(str(f, "filename"), 30),
			fmt.Sprintf("%d", len(notes)),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

// displayResource renders a single resource object (e.g. after upload).
func displayResource(data []byte) {
	r := parseJSON(client.UnwrapData(data))
	if r == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"Hash", bold(str(r, "hash"))},
		{"Filename", str(r, "filename")},
		{"MIME", str(r, "mime")},
		{"Size", bytesHuman(num(r, "size"))},
		{"Encrypted", boolMark(boolean(r, "is_encrypted"))},
		{"OCR status", colorizeStatus(str(r, "ocr_status"))},
		{"Thumbnail status", colorizeStatus(str(r, "thumb_status"))},
		{"USN", str(r, "usn")},
		{"Created", epochMS(num(r, "created_at"))},
	})
}

// displayFileCheck renders the result of a check call.
func displayFileCheck(data []byte) {
	r := parseJSON(data)
	exists := boolean(r, "exists")
	pairs := [][2]string{
		{"Hash", str(r, "hash")},
		{"Exists", boolMark(exists)},
	}
	if exists {
		pairs = append(pairs, [2]string{"Size", bytesHuman(num(r, "size"))}, [2]string{"MIME", str(r, "mime")})
	}
	printKV(pairs)
}

// displayDownloadInfo renders the presigned download URL and metadata.
func displayDownloadInfo(data []byte) {
	r := parseJSON(data)
	printKV([][2]string{
		{"Download URL", str(r, "download_url")},
		{"MIME", str(r, "mime")},
		{"Size", bytesHuman(num(r, "size"))},
		{"Expires", epochMS(num(r, "expires_at"))},
	})
}

func init() {
	addPagingFlags(filesListCmd)
	filesListCmd.Flags().String("mime", "", "Filter by exact MIME or a type/ prefix (e.g. image/)")
	filesListCmd.Flags().String("note-id", "", "Only files linked to this note id")
	filesListCmd.Flags().String("ocr-status", "", "Filter by OCR status")
	filesListCmd.Flags().String("updated-since", "", "Only files updated at or after this epoch-ms")
	filesListCmd.Flags().Bool("encrypted", false, "Filter by encryption state (use =true/=false)")

	filesCheckCmd.Flags().String("hash", "", "sha256 hash to check")
	filesCheckCmd.Flags().Int("size", 0, "Optional declared size in bytes")
	filesCheckCmd.Flags().String("file", "", "Compute the hash and size from this file")

	filesUploadCmd.Flags().String("mime", "", "MIME type (server sniffs when omitted)")
	filesUploadCmd.Flags().String("filename", "", "Stored filename (defaults to the base name)")
	filesUploadCmd.Flags().Bool("encrypted", false, "Encrypt the bytes on this machine before uploading (requires HARBOR_PASSPHRASE)")

	filesDownloadCmd.Flags().String("output", "", "Output path, or - for stdout (default: the file's own name, never overwriting)")
	filesDownloadCmd.Flags().String("note", "", "Save every file linked to this note id")
	filesDownloadCmd.Flags().String("output-dir", "", "With --note: the folder to save into (default: the current folder)")
	filesDownloadCmd.Flags().Bool("raw", false, "Stream through the API instead of following a presigned URL")
	filesDownloadCmd.Flags().Bool("ciphertext", false, "Write an encrypted file's raw envelope instead of decrypting it (default name: the hash)")

	filesCmd.AddCommand(filesListCmd, filesCheckCmd, filesUploadCmd, filesGetCmd, filesDownloadCmd, filesRenameCmd)
	rootCmd.AddCommand(filesCmd)
}

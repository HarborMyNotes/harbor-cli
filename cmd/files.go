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
	"os"
	"path/filepath"
	"strings"
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
	Short:   "Manage file attachments (list, check, upload, download)",
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

// filesDownloadCmd downloads the blob bytes to a file or stdout.
var filesDownloadCmd = &cobra.Command{
	Use:   "download <hash>",
	Short: "Download a file's bytes",
	Args:  cobra.ExactArgs(1),
	Long: `Download a blob. By default it follows a presigned URL; --raw streams through
the API instead. Writes to --output (default: the stored filename, or - for stdout).

Encrypted files are decrypted automatically when HARBOR_PASSPHRASE is set. When
it is not, the download is refused rather than writing ciphertext you cannot use
— pass --ciphertext to write the raw envelope anyway (for backups or moving bytes
between machines).`,
	Example: `  harbor files download e3b0... --output diagram.png
  harbor files download e3b0... --raw --output -
  harbor files download e3b0... --ciphertext --output sealed.bin`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		hash := args[0]
		out := stringFlag(cmd, "output")

		var body io.ReadCloser
		suggestedName := hash
		if boolFlag(cmd, "raw") {
			resp, derr := c.RawDownload(hash)
			if derr != nil {
				return derr
			}
			body = resp.Body
			// Base, because this becomes the output path when --output is
			// omitted and the name arrives over the network: one holding ".."
			// would write outside the directory the command was run in.
			if fn := filenameFromContentDisposition(resp.Header.Get("Content-Disposition")); fn != "" {
				suggestedName = filepath.Base(fn)
			}
		} else {
			meta, derr := c.GetFileDownload(hash)
			if derr != nil {
				return derr
			}
			info := parseJSON(meta)
			url := str(info, "download_url")
			if url == "" {
				return errors.New("no download URL returned")
			}
			resp, ferr := c.FetchURL(url)
			if ferr != nil {
				return ferr
			}
			body = resp.Body
		}
		defer body.Close()

		if out == "" {
			out = suggestedName
		}
		content, err := decryptDownload(c, creds, body, boolFlag(cmd, "ciphertext"))
		if err != nil {
			return err
		}
		n, err := writeOutput(out, content)
		if err != nil {
			return err
		}
		if out != "-" {
			fmt.Printf("Wrote %s to %s\n", bytesHuman(float64(n)), out)
		}
		return nil
	},
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
	head := make([]byte, crypto.BinaryEnvelopeMinBytes)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	head = head[:n]
	rest := io.MultiReader(bytes.NewReader(head), body)

	if !crypto.IsBinaryEnvelope(head) {
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

	sealed, err := io.ReadAll(rest)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.OpenBytes(key, sealed)
	if err != nil {
		// Also reachable for a plaintext file that happens to begin with the
		// ASCII bytes "HRBC2" — magic-sniffing cannot tell those apart, so name
		// the escape hatch rather than insisting the key is wrong.
		return nil, fmt.Errorf("this file did not decrypt with your key, so nothing was written "+
			"(if it was never encrypted, re-run with --ciphertext to write it as stored): %w", err)
	}
	return bytes.NewReader(plain), nil
}

// mapFileError gives friendly messages for file-specific codes.
func mapFileError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "file_too_large":
			return errors.New("the file exceeds the maximum upload size")
		case "unsupported_type":
			return errors.New("that media type is not allowed by the server policy")
		case "blob_missing":
			return errors.New("the blob bytes are not stored")
		}
	}
	return err
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
func writeOutput(path string, r io.Reader) (int64, error) {
	if path == "-" {
		return io.Copy(os.Stdout, r)
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("cannot create output file: %w", err)
	}
	defer f.Close()
	return io.Copy(f, r)
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

	filesDownloadCmd.Flags().String("output", "", "Output path, or - for stdout (default: the stored filename)")
	filesDownloadCmd.Flags().Bool("raw", false, "Stream through the API instead of following a presigned URL")
	filesDownloadCmd.Flags().Bool("ciphertext", false, "Write an encrypted file's raw envelope instead of decrypting it")

	filesCmd.AddCommand(filesListCmd, filesCheckCmd, filesUploadCmd, filesGetCmd, filesDownloadCmd)
	rootCmd.AddCommand(filesCmd)
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
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
	Long:  "Check by --hash (and optional --size), or pass --file to compute the sha256 and size locally.",
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
	Example: `  harbor files upload diagram.png
  harbor files upload report.pdf --mime application/pdf`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.UploadFile(args[0], stringFlag(cmd, "mime"), stringFlag(cmd, "filename"), boolFlag(cmd, "encrypted"))
		if err != nil {
			return mapFileError(err)
		}
		printResult(data, displayResource)
		return nil
	},
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
	Long:  "Download a blob. By default it follows a presigned URL; --raw streams through the API instead. Writes to --output (default: the stored filename, or - for stdout).",
	Example: `  harbor files download e3b0... --output diagram.png
  harbor files download e3b0... --raw --output -`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
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
			if fn := filenameFromContentDisposition(resp.Header.Get("Content-Disposition")); fn != "" {
				suggestedName = fn
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
		n, err := writeOutput(out, body)
		if err != nil {
			return err
		}
		if out != "-" {
			fmt.Printf("Wrote %s to %s\n", bytesHuman(float64(n)), out)
		}
		return nil
	},
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
func filenameFromContentDisposition(cd string) string {
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
	filesUploadCmd.Flags().Bool("encrypted", false, "Mark the upload as client-encrypted (opaque bytes)")

	filesDownloadCmd.Flags().String("output", "", "Output path, or - for stdout (default: the stored filename)")
	filesDownloadCmd.Flags().Bool("raw", false, "Stream through the API instead of following a presigned URL")

	filesCmd.AddCommand(filesListCmd, filesCheckCmd, filesUploadCmd, filesGetCmd, filesDownloadCmd)
	rootCmd.AddCommand(filesCmd)
}

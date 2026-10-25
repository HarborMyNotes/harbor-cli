// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-10-24

package cmd

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// This file is a port of Harbor's "real name" rule, exportname.RealAttachmentName
// in app.harbor.my. Every app and the HTML export name saved attachments by it, so
// one note saved from two apps gives the same file names. Where this port and the
// server's Go code disagree, the server is right.

// realAttachmentName returns the name a file is saved under when a person, not a
// program, will read it: the stored name, kept as typed, changed only where a
// filesystem cannot store it. The rule, in order:
//
//  1. Trim the stored name: Unicode white space (unicode.IsSpace) and U+FEFF.
//     Empty becomes "attachment".
//  2. Split at the last dot. The part after it is the extension only when it is
//     1–10 ASCII letters or digits with at least one letter, so "2026.10.23" has
//     none. The extension is kept exactly as typed, except ".bin" (any case) when
//     realTypeExt lists the type: it counts as no extension, because the Evernote
//     importer stores a file that had no name as "attachment.bin".
//  3. No extension: add the one realTypeExt lists for the type. A type not in the
//     list adds nothing.
//  4. Clean the stem with sanitizeFileName. A name with no extension of its own
//     also loses its trailing dots and spaces: Windows drops them, so "Report."
//     and "Report" would be one file there. Nothing left becomes "attachment".
//  5. A name already in used (compared by collisionKey) becomes "stem (2).ext",
//     then "stem (3).ext", and so on.
//
// used holds collisionKey values and is updated with the name returned. To save
// into a folder that already has files, add the collisionKey of each name in it
// first, so nothing there is overwritten.
func realAttachmentName(used map[string]bool, filename, mimeType string) string {
	name := strings.TrimFunc(filename, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
	if name == "" {
		name = "attachment"
	}

	stem, ext := splitRealExt(name)
	if strings.EqualFold(ext, ".bin") && typeExt(mimeType) != "" {
		ext = ""
	}
	ownExt := ext != ""
	if !ownExt {
		ext = typeExt(mimeType)
	}

	stem = sanitizeFileName(stem)
	if !ownExt {
		stem = strings.TrimRight(stem, ". ")
	}
	if stem == "" {
		stem = "attachment"
	}

	final := stem + ext
	for n := 2; used[collisionKey(final)]; n++ {
		final = fmt.Sprintf("%s (%d)%s", stem, n, ext)
	}
	used[collisionKey(final)] = true
	return final
}

// splitRealExt splits a name at its last dot into a stem and an extension (with
// its dot). The suffix counts as an extension only when it is 1–10 ASCII letters
// or digits with at least one letter; otherwise the whole name is the stem.
// ASCII, because every app's language agrees on what an ASCII letter is and not
// on what a Unicode one is.
func splitRealExt(name string) (stem, ext string) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return name, ""
	}
	suffix := name[i+1:]
	if len(suffix) < 1 || len(suffix) > 10 {
		return name, ""
	}
	hasLetter := false
	for j := 0; j < len(suffix); j++ {
		c := suffix[j]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			hasLetter = true
		case c >= '0' && c <= '9':
		default:
			return name, ""
		}
	}
	if !hasLetter {
		return name, ""
	}
	return name[:i], name[i:]
}

// realTypeExt is the extension realAttachmentName adds, by type, to a name that
// has none. It is a fixed list rather than the operating system's MIME table,
// because that table differs between machines and no other app can reproduce
// it. A type not listed adds nothing.
var realTypeExt = map[string]string{
	"image/jpeg": ".jpg", "image/jpg": ".jpg", "image/png": ".png", "image/gif": ".gif",
	"image/webp": ".webp", "image/svg+xml": ".svg", "image/heic": ".heic", "image/heif": ".heif",
	"image/tiff": ".tiff", "image/bmp": ".bmp",
	"application/pdf": ".pdf",
	"text/plain":      ".txt", "text/html": ".html", "text/markdown": ".md", "text/csv": ".csv",
	"application/json": ".json", "application/zip": ".zip", "application/rtf": ".rtf",
	"audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/x-m4a": ".m4a", "audio/aac": ".aac",
	"audio/wav": ".wav", "audio/x-wav": ".wav", "audio/ogg": ".ogg", "audio/webm": ".webm",
	"audio/amr": ".amr", "audio/mp3": ".mp3", "audio/m4a": ".m4a", "audio/wave": ".wav",
	"audio/flac": ".flac", "audio/x-flac": ".flac", "audio/3gpp": ".3gp",
	"video/mp4": ".mp4", "video/quicktime": ".mov", "video/webm": ".webm", "video/3gpp": ".3gp",
	"image/avif": ".avif", "image/heic-sequence": ".heic", "image/heif-sequence": ".heif",
	"application/x-zip-compressed": ".zip", "application/epub+zip": ".epub",
	"message/rfc822": ".eml", "text/calendar": ".ics", "text/vcard": ".vcf",
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.ms-powerpoint":                                             ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
}

// typeExt looks a type up in realTypeExt, ignoring case, parameters and
// surrounding spaces, so "Text/Plain; charset=utf-8" is text/plain.
func typeExt(mimeType string) string {
	t, _, _ := strings.Cut(mimeType, ";")
	return realTypeExt[strings.ToLower(strings.TrimSpace(t))]
}

// sanitizeFileName turns a name into one path segment that every filesystem can
// store, changing it as little as possible. It removes only what cannot sit in a
// file name or a URL:
//
//   - "/", "\" and ":" (path separators and the Windows drive marker), and the
//     rest of Windows' reserved set: < > " | ? *;
//   - "#" and "%", which a browser reads as a fragment and an escape when the name
//     becomes a link;
//   - control characters, which are illegal in a path;
//   - leading dots (a hidden file, or "." and "..") and leading or trailing
//     spaces, which Windows strips anyway.
//
// Accents, CJK, emoji and punctuation stay. An all-unsafe name yields "", and
// the caller supplies its own fallback.
func sanitizeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			continue
		case strings.ContainsRune(`/\:<>"|?*#%`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	// A trailing dot stays: it is part of the name ("Etc."), and the extension
	// follows it.
	out := strings.TrimLeft(b.String(), ". ")
	out = strings.TrimRight(out, " ")

	return avoidReservedDeviceName(truncateRunes(out, 80, 200))
}

// reservedDeviceNames is the set of MS-DOS device names Windows resolves as
// devices rather than files, in any folder and with any extension. Microsoft
// lists them under "Naming Files, Paths, and Namespaces", superscripts included.
var reservedDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"CONIN$": true, "CONOUT$": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"COM\u00B9": true, "COM\u00B2": true, "COM\u00B3": true, "LPT\u00B9": true, "LPT\u00B2": true, "LPT\u00B3": true,
}

// avoidReservedDeviceName appends "_" to a name whose first dot-separated part is
// a Windows device name, so the file can be written on Windows: "CON.txt" is the
// console there, not a file. The match ignores case and trailing spaces, as
// Windows does. The "_" goes after the first part because that is the part that
// must stop matching: "AUX." becomes "AUX_.", not "AUX._".
func avoidReservedDeviceName(name string) string {
	head := name
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[:i]
	}
	if !reservedDeviceNames[strings.ToUpper(strings.TrimRight(head, " "))] {
		return name
	}
	return head + "_" + name[len(head):]
}

// truncateRunes caps s at maxRunes code points and maxBytes bytes, whichever is
// reached first, and always cuts between code points. Both caps are needed: a
// byte cap alone can split a character, and a code-point cap alone lets 80 emoji
// grow past the 255-byte name limit of most filesystems.
func truncateRunes(s string, maxRunes, maxBytes int) string {
	runes, bytes := 0, 0
	for i, r := range s {
		if runes >= maxRunes || bytes+utf8.RuneLen(r) > maxBytes {
			return s[:i]
		}
		runes++
		bytes += utf8.RuneLen(r)
	}
	return s
}

// collisionKey maps a file name to the identity a case-blind filesystem judges it
// by, which is what two saved names have to differ in. macOS and Windows ignore
// case, and macOS also treats the NFC and NFD spellings of "é" as one name, so
// the key folds both away: NFC, full case folding (so "Straße" and "STRASSE"
// match, as they do on macOS), lowercase, NFC. The lowercase step is there
// because x/text's Fold swaps Cherokee upper and lower case instead of folding it.
func collisionKey(name string) string {
	return norm.NFC.String(strings.ToLower(cases.Fold().String(norm.NFC.String(name))))
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-10-24

package cmd

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// TestRealAttachmentName pins the real-name rule every app ports. Each case runs
// its files in order through one shared used map, the way one export or one
// "Save all attachments" does. taken lists names already in the destination
// folder. The first block is the spec's example table, row for row. Every
// character outside ASCII is written as an escape, so a port copies the exact
// code points rather than whatever its editor turns them into.
func TestRealAttachmentName(t *testing.T) {
	const (
		xlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		docx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		pdf  = "application/pdf"
		png  = "image/png"
		jpeg = "image/jpeg"
		txt  = "text/plain"
	)
	type file struct{ stored, mime, want string }
	cases := []struct {
		label string
		taken []string
		files []file
	}{
		// The spec's example table.
		{"keeps the name", nil, []file{{"Q3 Budget.xlsx", xlsx, "Q3 Budget.xlsx"}}},
		{"keeps the extension's case", nil, []file{{"Scan.PDF", pdf, "Scan.PDF"}}},
		{"adds the type's extension", nil, []file{{"Report", pdf, "Report.pdf"}}},
		{"unsafe characters become _", nil, []file{{"a/b:c?.pdf", pdf, "a_b_c_.pdf"}}},
		{"empty name", nil, []file{{"", png, "attachment.png"}}},
		{"same name twice", nil, []file{
			{"attachment.png", png, "attachment.png"},
			{"attachment.png", png, "attachment (2).png"},
		}},
		{"same name, different case", nil, []file{
			{"Photo.JPG", jpeg, "Photo.JPG"},
			{"photo.jpg", jpeg, "photo (2).jpg"},
		}},
		{"Windows device name", nil, []file{{"CON.txt", txt, "CON_.txt"}}},
		{"accents stay", nil, []file{{"Caf\u00E9 menu.docx", docx, "Caf\u00E9 menu.docx"}}},

		// Length caps: 80 code points and 200 bytes, whichever binds first, cut
		// between code points. A code point is not what a reader sees as one
		// character: a decomposed e-acute is two code points, and a flag is two.
		{"81-character stem", nil, []file{{strings.Repeat("a", 81) + ".pdf", pdf, strings.Repeat("a", 80) + ".pdf"}}},
		{"250-byte emoji stem", nil, []file{{strings.Repeat("\U0001F600", 62) + "ab.png", png, strings.Repeat("\U0001F600", 50) + ".png"}}},
		{"81 decomposed \u00E9 keep 40", nil, []file{{strings.Repeat("e\u0301", 81) + ".pdf", pdf, strings.Repeat("e\u0301", 40) + ".pdf"}}},
		{"the byte cap can split a flag", nil, []file{{"a" + strings.Repeat("\U0001F1FA\U0001F1F8", 41) + ".png", png, "a" + strings.Repeat("\U0001F1FA\U0001F1F8", 24) + "\U0001F1FA.png"}}},

		// Two spellings a filesystem treats as one name.
		{"NFC and NFD \u00E9", nil, []file{
			{"Caf\u00E9.pdf", pdf, "Caf\u00E9.pdf"},
			{"Cafe\u0301.pdf", pdf, "Cafe\u0301 (2).pdf"},
		}},
		{"\u0130 precomposed and decomposed", nil, []file{
			{"\u0130.txt", txt, "\u0130.txt"},
			{"I\u0307.txt", txt, "I\u0307 (2).txt"},
		}},
		{"\u0130 and i are different names", nil, []file{
			{"\u0130.txt", txt, "\u0130.txt"},
			{"i.txt", txt, "i.txt"},
		}},
		{"\u00DF folds to ss", nil, []file{
			{"Stra\u00DFe.pdf", pdf, "Stra\u00DFe.pdf"},
			{"STRASSE.pdf", pdf, "STRASSE (2).pdf"},
		}},
		{"final sigma folds to sigma", nil, []file{
			{"\u039A\u03BF\u03C3\u03BC\u03BF\u03C2.txt", txt, "\u039A\u03BF\u03C3\u03BC\u03BF\u03C2.txt"},
			{"\u039A\u039F\u03A3\u039C\u039F\u03A3.txt", txt, "\u039A\u039F\u03A3\u039C\u039F\u03A3 (2).txt"},
		}},
		{"Cherokee case pair", nil, []file{
			{"\u13A0.txt", txt, "\u13A0.txt"},
			{"\uAB70.txt", txt, "\uAB70 (2).txt"},
		}},
		{"dotless i and I are different names", nil, []file{
			{"\u0131.txt", txt, "\u0131.txt"},
			{"I.txt", txt, "I.txt"},
		}},
		{"long s folds to s", nil, []file{
			{"\u017F.txt", txt, "\u017F.txt"},
			{"s.txt", txt, "s (2).txt"},
		}},

		// Windows device names.
		{"COM0 is a device", nil, []file{{"COM0.txt", txt, "COM0_.txt"}}},
		{"COM\u00B9 is a device", nil, []file{{"COM\u00B9.txt", txt, "COM\u00B9_.txt"}}},
		{"lowercase device name, no extension", nil, []file{{"lpt\u00B3", txt, "lpt\u00B3_.txt"}}},
		{"device name, type added", nil, []file{{"con", txt, "con_.txt"}}},

		// Collisions.
		{"only unsafe characters", nil, []file{{"?*", txt, "__.txt"}}},
		{"three of one name", nil, []file{
			{"attachment.png", png, "attachment.png"},
			{"attachment.png", png, "attachment (2).png"},
			{"attachment.png", png, "attachment (3).png"},
		}},
		{"a real name that looks like a numbered one", nil, []file{
			{"a (2).png", png, "a (2).png"},
			{"a.png", png, "a.png"},
			{"a.png", png, "a (3).png"},
		}},
		{"a file already in the folder", []string{"Report.pdf"}, []file{{"Report.pdf", pdf, "Report (2).pdf"}}},
		{"no extension, twice", nil, []file{
			{"notes", "", "notes"},
			{"notes", "", "notes (2)"},
		}},

		// What counts as an extension: 1-10 ASCII letters or digits, one a letter.
		{"own extension beats the type", nil, []file{{"Q3 Budget.xlsx", png, "Q3 Budget.xlsx"}}},
		{"digits inside an extension", nil, []file{{"song.mp3", "audio/mpeg", "song.mp3"}}},
		{"digit first in an extension", nil, []file{{"backup.7z", "application/x-7z-compressed", "backup.7z"}}},
		{"10 characters is an extension", nil, []file{{"notes.abcdefghij", txt, "notes.abcdefghij"}}},
		{"11 characters is not", nil, []file{{"notes.abcdefghijk", txt, "notes.abcdefghijk.txt"}}},
		{"digits only is not", nil, []file{{"2026.10.23", pdf, "2026.10.23.pdf"}}},
		{"one digit is not", nil, []file{{"file.2", txt, "file.2.txt"}}},
		{"non-ASCII is not", nil, []file{{"\u62A5\u544A.\u6587\u6863", txt, "\u62A5\u544A.\u6587\u6863.txt"}}},
		{"only the last dot splits", nil, []file{{"archive.tar.gz", "application/gzip", "archive.tar.gz"}}},
		{"own .bin extension stays", nil, []file{{"data.bin", "application/octet-stream", "data.bin"}}},
		{"stored .bin with a listed type", nil, []file{{"attachment.bin", "audio/amr", "attachment.amr"}}},
		{"stored .BIN with a listed type", nil, []file{{"attachment.BIN", "audio/wav", "attachment.wav"}}},
		{"stored .bin with an unlisted type", nil, []file{{"firmware.bin", "application/x-harbor-unknown", "firmware.bin"}}},
		{"two stored .bin files", nil, []file{
			{"attachment.bin", "audio/amr", "attachment.amr"},
			{"attachment.bin", "audio/amr", "attachment (2).amr"},
		}},

		// The type list (realTypeExt): case, parameters and spaces are ignored,
		// and a type it does not list adds nothing.
		{"Office type", nil, []file{{"Budget", "application/vnd.ms-excel", "Budget.xls"}}},
		{"type with parameters", nil, []file{{"Budget", " Text/Plain; charset=utf-8", "Budget.txt"}}},
		{"upper-case type", nil, []file{{"Photo", "image/JPEG", "Photo.jpg"}}},
		{"image/jpg", nil, []file{{"Photo", "image/jpg", "Photo.jpg"}}},
		{"video", nil, []file{{"Clip", "video/mp4", "Clip.mp4"}}},
		{"html", nil, []file{{"Page", "text/html", "Page.html"}}},
		{"voice memo", nil, []file{{"Voice memo", "audio/x-m4a", "Voice memo.m4a"}}},
		{"3gp audio", nil, []file{{"Voice", "audio/3gpp", "Voice.3gp"}}},
		{"unlisted type adds nothing", nil, []file{{"notes", "application/x-harbor-unknown", "notes"}}},
		{"octet-stream adds nothing", nil, []file{{"notes", "application/octet-stream", "notes"}}},
		{"no type adds nothing", nil, []file{{"notes", "", "notes"}}},

		// Trimming and cleaning.
		{"surrounding spaces", nil, []file{{"  Q3 Budget.xlsx  ", "", "Q3 Budget.xlsx"}}},
		{"tab and no-break space are white space", nil, []file{{"\tReport.pdf\u00A0", pdf, "Report.pdf"}}},
		{"NEL and line separator are white space", nil, []file{{"\u0085Report.pdf\u2028", pdf, "Report.pdf"}}},
		{"byte-order mark is trimmed", nil, []file{{"\uFEFFReport.pdf", pdf, "Report.pdf"}}},
		{"only spaces", nil, []file{{"   ", png, "attachment.png"}}},
		{"extension only", nil, []file{{".pdf", pdf, "attachment.pdf"}}},
		{"leading dots", nil, []file{{"..hidden.txt", txt, "hidden.txt"}}},
		{"trailing dot, type added", nil, []file{{"Report.", pdf, "Report.pdf"}}},
		{"trailing dot, no type", nil, []file{{"Report.", "", "Report"}}},
		{"# and % become _", nil, []file{{"Invoice #42 100%.pdf", pdf, "Invoice _42 100_.pdf"}}},
		{"control characters are dropped", nil, []file{{"Re\x00po\x1frt.pdf", pdf, "Report.pdf"}}},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			used := map[string]bool{}
			for _, name := range tc.taken {
				used[collisionKey(name)] = true
			}
			for _, f := range tc.files {
				if got := realAttachmentName(used, f.stored, f.mime); got != f.want {
					t.Errorf("realAttachmentName(%q, %q) = %q, want %q", f.stored, f.mime, got, f.want)
				}
			}
		})
	}
}

// TestRealTypeExtTable keeps the type list usable: every key is the bare,
// lower-case form typeExt looks up, and every value is an extension
// realAttachmentName would itself recognize.
func TestRealTypeExtTable(t *testing.T) {
	for typ, ext := range realTypeExt {
		if typ != strings.ToLower(typ) || strings.ContainsAny(typ, "; ") {
			t.Errorf("type %q must be lower case with no parameters", typ)
		}
		if _, got := splitRealExt("x" + ext); got != ext {
			t.Errorf("%q maps to %q, which is not an extension by the rule", typ, ext)
		}
	}
}

// TestCollisionKeyFoldsCaseVariants sweeps every code point: each one and its
// simple case variants (unicode.SimpleFold) must land on one key, or a case-blind
// filesystem would keep only one of the two files.
func TestCollisionKeyFoldsCaseVariants(t *testing.T) {
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		for o := unicode.SimpleFold(r); o != r; o = unicode.SimpleFold(o) {
			if collisionKey(string(r)) != collisionKey(string(o)) {
				t.Errorf("U+%04X and U+%04X are case variants but get different keys", r, o)
			}
		}
	}
}

// TestCollisionKeyAgreesAcrossNormalForms sweeps every code point: its NFC and
// NFD spellings must land on one key, or both would be written and macOS would
// keep only one of them.
func TestCollisionKeyAgreesAcrossNormalForms(t *testing.T) {
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		s := string(r)
		if collisionKey(s) != collisionKey(norm.NFD.String(s)) {
			t.Errorf("U+%04X: NFC and NFD spellings get different keys", r)
		}
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ===========================================================================
// Prompt seams (shared by every destructive-command test)
// ===========================================================================

// answerPrompt makes a run behave as though a person is sitting at a terminal
// and types reply at the next confirmation. Both seams are restored when the
// test ends, so one test cannot leave the process pretending to be a TTY.
//
// It returns a pointer to the labels the command actually prompted with, so a
// test can assert WHAT the user was asked as well as what happened after they
// answered — and, just as usefully, assert that they were not asked at all.
func answerPrompt(t *testing.T, reply string) *[]string {
	t.Helper()
	asked := []string{}
	swapPromptSeams(t, func() bool { return true }, func(label string) (string, error) {
		asked = append(asked, label)
		return reply, nil
	})
	return &asked
}

// pipedStdin makes a run look like a script or CI job: stdin is not a terminal,
// and any attempt to prompt anyway fails the test rather than blocking on a read
// nobody will answer.
func pipedStdin(t *testing.T) {
	t.Helper()
	swapPromptSeams(t, func() bool { return false }, func(label string) (string, error) {
		t.Errorf("an unattended run must never prompt (it asked %q)", label)
		return "", nil
	})
}

// swapPromptSeams installs interactivity and prompt stand-ins for the duration
// of one test.
func swapPromptSeams(t *testing.T, interactive func() bool, ask func(string) (string, error)) {
	t.Helper()
	origInteractive, origAsk := stdinIsInteractive, askLine
	t.Cleanup(func() { stdinIsInteractive, askLine = origInteractive, origAsk })
	stdinIsInteractive, askLine = interactive, ask
}

// ===========================================================================
// The decision itself
// ===========================================================================

// testConfirmation is a stand-in with wording no real command uses, so the cases
// below test the RULE rather than one command's sentences.
var testConfirmation = confirmation{
	Warning:     "This eats the widget. It cannot be un-eaten.",
	Prompt:      `Type "yes" to confirm: `,
	Affirmative: "yes",
	Unattended:  "refusing to eat the widget without confirmation — pass --yes",
	Aborted:     "aborted — the widget was not eaten",
}

// TestConfirmDestructive walks every branch of the gate. The wrong-answer rows
// are the point of the whole exercise: an irreversible action must treat
// anything that is not the affirmative word as "stop", including near-misses a
// hurried person plausibly types.
func TestConfirmDestructive(t *testing.T) {
	answers := func(reply string) func(string) (string, error) {
		return func(string) (string, error) { return reply, nil }
	}
	refuse := func(label string) (string, error) {
		t.Errorf("the user must not be prompted in this case (asked %q)", label)
		return "", nil
	}

	cases := []struct {
		name        string
		jsonMode    bool
		interactive bool
		yes         bool
		ask         func(string) (string, error)
		wantErr     string // "" = must proceed
	}{
		{name: "--yes proceeds without asking", yes: true, interactive: true, ask: refuse},
		{name: "--yes proceeds unattended too", yes: true, ask: refuse},
		{name: "--yes wins over --json", yes: true, jsonMode: true, ask: refuse},
		{name: "typed yes proceeds", interactive: true, ask: answers("yes")},
		{name: "typed no aborts", interactive: true, ask: answers("no"), wantErr: "aborted"},
		{name: "empty answer aborts", interactive: true, ask: answers(""), wantErr: "aborted"},
		{name: "bare y aborts", interactive: true, ask: answers("y"), wantErr: "aborted"},
		{name: "YES is not yes", interactive: true, ask: answers("YES"), wantErr: "aborted"},
		{name: "Yes is not yes", interactive: true, ask: answers("Yes"), wantErr: "aborted"},
		{name: "yes with a tail aborts", interactive: true, ask: answers("yes please"), wantErr: "aborted"},
		{name: "a stray keystroke aborts", interactive: true, ask: answers("q"), wantErr: "aborted"},
		{name: "piped stdin refuses", ask: refuse, wantErr: "--yes"},
		{name: "--json refuses even on a terminal", jsonMode: true, interactive: true, ask: refuse, wantErr: "--yes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			// The gate prints its warning; keep it out of the test output.
			captureStdout(t, func() {
				err = confirmDestructive(testConfirmation, tc.jsonMode, tc.interactive, tc.yes, tc.ask)
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want proceed, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got nil — the destructive action would have run", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestConfirmDestructiveAbortsWhenTheAnswerCannotBeRead pins the quietest
// failure: if the read itself breaks, that is not consent.
//
// It must also abort in the CLI's own words. Pressing ^D at the prompt is a
// person declining, and the raw read error surfaces as "Error: EOF" — the one
// destructive refusal in the CLI that reads like a crash rather than a decision.
func TestConfirmDestructiveAbortsWhenTheAnswerCannotBeRead(t *testing.T) {
	for _, readErr := range []error{io.EOF, io.ErrUnexpectedEOF} {
		captureStdout(t, func() {
			err := confirmDestructive(testConfirmation, false, true, false, func(string) (string, error) {
				return "", readErr
			})
			if err == nil {
				t.Fatal("a failed prompt read must abort, never fall through to the destructive action")
			}
			if err.Error() != testConfirmation.Aborted {
				t.Errorf("err = %q, want the declared abort message %q", err.Error(), testConfirmation.Aborted)
			}
		})
	}
}

// TestConfirmDestructiveSaysWhatItIsAboutToDo checks the other half of the
// split: the question is asked before the answer is read, and it is the wording
// the command declared rather than something invented at the call site.
func TestConfirmDestructiveSaysWhatItIsAboutToDo(t *testing.T) {
	var asked string
	out := captureStdout(t, func() {
		_ = confirmDestructive(testConfirmation, false, true, false, func(label string) (string, error) {
			asked = label
			return "yes", nil
		})
	})
	if !strings.Contains(out, testConfirmation.Warning) {
		t.Errorf("the warning must be printed before the prompt, got:\n%s", out)
	}
	if asked != testConfirmation.Prompt {
		t.Errorf("prompt = %q, want %q", asked, testConfirmation.Prompt)
	}
}

// ===========================================================================
// The wording every destructive command declares
// ===========================================================================

// destructiveConfirmations is NOT declared here — it is built in confirm.go by
// registerConfirmation as each command declares its wording, so this file cannot
// go stale relative to the code. The checks below then close the other
// direction: they fail when a destructive command exists that never registered
// anything. A hand-written list can only shrink silently; these cannot.

// TestEveryConfirmationIsWellFormed states the rules once instead of per
// command: say what happens, name the escape hatch, and spell out the answer.
func TestEveryConfirmationIsWellFormed(t *testing.T) {
	for name, c := range destructiveConfirmations {
		t.Run(name, func(t *testing.T) {
			if c.Warning == "" {
				t.Error("no warning: the user is asked to confirm something unstated")
			}
			if c.Affirmative == "" {
				t.Error("no affirmative: every answer would proceed")
			}
			if !strings.Contains(c.Prompt, c.Affirmative) {
				t.Errorf("prompt %q does not tell the user to type %q", c.Prompt, c.Affirmative)
			}
			if !strings.Contains(c.Unattended, "--yes") {
				t.Errorf("unattended refusal %q does not name the flag that would have worked", c.Unattended)
			}
			if !strings.HasPrefix(c.Aborted, "aborted") {
				t.Errorf("abort message %q should start with \"aborted\" so it reads the same everywhere", c.Aborted)
			}
		})
	}
}

// ===========================================================================
// Registry completeness
// ===========================================================================

// irreversibleClientCalls are the client methods that destroy user data with no
// way back. Extend this when the API grows another one.
//
// The check keys off the WIRE CALL rather than help-text wording on purpose. An
// earlier draft of this test matched prose ("permanent", "cannot be undone") and
// flagged `notes links`, `notes backlinks` and `notes audit` — three read-only
// commands whose help merely EXPLAINS what an expunged note is. Prose describes
// destruction at least as often as it performs it; the request on the wire does
// not lie.
// "Destroys" is meant broadly enough to cover the last entry, which destroys no
// note at all: writing a note's contents back to the server in the clear cannot
// be taken back either, because the plaintext is indexed and snapshotted into the
// note's history the moment it lands. Re-encrypting protects the note from then
// on; it does not retract what was already stored. Both are "you cannot get the
// old state back", which is the property the confirmation exists for.
var irreversibleClientCalls = []string{
	"c.EmptyTrash(",             // every note in the bin, gone
	"c.ExpungeNote(",            // one note, gone
	"c.DeleteNote(",             // trashes by default, but expunges with --permanent
	"c.DeleteAccountExport(",    // the archive and its emailed link
	"c.RequestAccountDeletion(", // the whole account, after a grace window
	"c.ConvertNoteToPlaintext(", // a note's contents, published to the server
	"c.ConvertNoteToEncrypted(", // the note's whole version history, hard-deleted
}

// TestEveryIrreversibleCommandAsksFirst is the check that would have caught the
// gap this issue is about, and the reason the registry is no longer a list
// anybody maintains by hand.
//
// `notes delete --permanent` reached the same expunge as `trash expunge` with no
// prompt at all, and nothing noticed — because "which commands are destructive"
// lived in a test file that somebody had to remember to append to. This derives
// it from the source instead: any cobra command whose body issues one of the
// irreversible calls above must also consult a confirmation. A new destructive
// command fails this by default, which is the whole point.
// It also insists the call it found REACHES confirmDestructive, rather than
// merely being spelled like it does. Matching the name alone was not enough: a
// helper called verifyConversionLanded — a post-write sanity check with no
// prompt in it — was originally named confirmConversionLanded, and with that name
// it satisfied this test on its own. The gate could then be deleted outright and
// nothing failed. A command is only asking first if the thing it calls actually
// ends up at the one function that reads an answer.
func TestEveryIrreversibleCommandAsksFirst(t *testing.T) {
	// Every gate is reached through a helper named *Confirm*/*confirm*, so this
	// looks for a CALL rather than the word. That distinction is load-bearing:
	// scanning the whole declaration for "confirm" passed even with the gate
	// deleted, because the help text says "you will be asked to confirm".
	confirmCall := regexp.MustCompile(`(?i)\b(\w*confirm\w*)\(`)
	bodies := packageFuncBodies(t)

	for _, block := range cobraCommandBlocks(t) {
		call := ""
		for _, c := range irreversibleClientCalls {
			if strings.Contains(block.runE, c) {
				call = strings.TrimSuffix(c, "(")
				break
			}
		}
		if call == "" {
			continue
		}
		gated := false
		for _, m := range confirmCall.FindAllStringSubmatch(block.runE, -1) {
			if reachesConfirmDestructive(m[1], bodies, map[string]bool{}) {
				gated = true
				break
			}
		}
		if !gated {
			t.Errorf("%s (%s) calls %s in its RunE but never consults a confirmation — it destroys without asking",
				block.varName, block.use, call)
		}
	}
}

// reachesConfirmDestructive reports whether calling name eventually reaches the
// gate — directly, or through however many package-level helpers sit in between.
// A name that resolves to no package-level function (a method, a local closure,
// something from another package) reaches nothing, which is the answer that
// matters: it cannot be shown to ask.
func reachesConfirmDestructive(name string, bodies map[string]string, seen map[string]bool) bool {
	if name == "confirmDestructive" {
		return true
	}
	if seen[name] {
		return false // a cycle; it has not reached the gate by going round again
	}
	seen[name] = true
	body, ok := bodies[name]
	if !ok {
		return false
	}
	for _, m := range regexp.MustCompile(`\b(\w+)\(`).FindAllStringSubmatch(body, -1) {
		if reachesConfirmDestructive(m[1], bodies, seen) {
			return true
		}
	}
	return false
}

// packageFuncBodies returns every package-level function in the command sources,
// keyed by name. It ends each body at the next top-level declaration for the same
// reason cobraCommandBlocks does — brace counting walks straight off the end of a
// function containing a raw string with braces in it.
func packageFuncBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	// Plain functions only: a method's receiver makes it unreachable by bare name
	// from a RunE, so including one would let an unrelated same-named method
	// answer for it.
	marker := regexp.MustCompile(`(?m)^func (\w+)\(`)
	nextDecl := regexp.MustCompile(`(?m)^(var|func) `)

	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, m := range marker.FindAllStringSubmatchIndex(text, -1) {
			rest := text[m[1]:]
			end := len(rest)
			if next := nextDecl.FindStringIndex(rest); next != nil {
				end = next[0]
			}
			out[text[m[2]:m[3]]] = rest[:end]
		}
	}
	if len(out) < 50 {
		t.Fatalf("only found %d package functions in the sources; the scan is broken, so this test proves nothing", len(out))
	}
	return out
}

// commandBlock is one `var xxxCmd = &cobra.Command{...}` declaration, as source.
// runE is only the action body: help text talks ABOUT confirming and destroying,
// so matching against it proves nothing about what the command does.
type commandBlock struct {
	varName string
	use     string
	runE    string
}

// cobraCommandBlocks reads the command sources and returns one entry per cobra
// command declaration.
//
// The block ends at the next top-level declaration rather than by matching
// braces: several commands carry jq examples like `{id, title}` inside a raw
// string, and brace-counting walks straight off the end of the command it is in.
func cobraCommandBlocks(t *testing.T) []commandBlock {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	marker := regexp.MustCompile(`(?m)^var (\w+) = &cobra\.Command\{`)
	nextDecl := regexp.MustCompile(`(?m)^(var|func) `)
	useLine := regexp.MustCompile(`Use:\s+"([^"]*)"`)

	var blocks []commandBlock
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, m := range marker.FindAllStringSubmatchIndex(text, -1) {
			rest := text[m[1]:]
			end := len(rest)
			if next := nextDecl.FindStringIndex(rest); next != nil {
				end = next[0]
			}
			decl := rest[:end]
			use := ""
			if u := useLine.FindStringSubmatch(decl); u != nil {
				use = u[1]
			}
			runE := ""
			if i := strings.Index(decl, "RunE:"); i >= 0 {
				runE = decl[i:]
			}
			blocks = append(blocks, commandBlock{varName: text[m[2]:m[3]], use: use, runE: runE})
		}
	}
	if len(blocks) < 20 {
		t.Fatalf("only found %d cobra commands in the sources; the scan is broken, so this test proves nothing", len(blocks))
	}
	return blocks
}

// TestEveryGatedCommandIsRegistered closes the loop from the other side: a --yes
// flag is this CLI's marker for "there is a confirmation to skip", so a command
// carrying one must have wording in the registry. A gate whose wording is not
// registered is a gate the rules above never check.
func TestEveryGatedCommandIsRegistered(t *testing.T) {
	prepareCommandTree()

	forEachCommand(t, func(c *cobra.Command) {
		if c.Flags().Lookup("yes") == nil {
			return
		}
		if _, ok := destructiveConfirmations[c.CommandPath()]; !ok {
			t.Errorf("%s declares --yes but registered no confirmation, so its wording is unchecked", c.CommandPath())
		}
	})
}

// TestEveryRegisteredConfirmationHasACommand catches the reverse drift: wording
// registered under a path that no longer exists (a renamed or deleted command)
// would keep passing the rules above while gating nothing at all.
func TestEveryRegisteredConfirmationHasACommand(t *testing.T) {
	prepareCommandTree()

	paths := map[string]bool{}
	forEachCommand(t, func(c *cobra.Command) { paths[c.CommandPath()] = true })

	for path := range destructiveConfirmations {
		if !paths[path] {
			t.Errorf("a confirmation is registered for %q, which is not a command in the tree", path)
		}
	}
}

// forEachCommand walks the whole prepared command tree, root included.
func forEachCommand(t *testing.T, fn func(*cobra.Command)) {
	t.Helper()
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		fn(c)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// TestEveryConfirmationRefusesAWrongAnswer applies the branch that matters to
// every registered command at once, so a new destructive command cannot ship
// with a gate that proceeds on "n".
func TestEveryConfirmationRefusesAWrongAnswer(t *testing.T) {
	for name, c := range destructiveConfirmations {
		for _, answer := range []string{"no", "n", "", "y", "YES", "yes!"} {
			t.Run(name+"/"+answer, func(t *testing.T) {
				var err error
				captureStdout(t, func() {
					err = confirmDestructive(c, false, true, false, func(string) (string, error) {
						return answer, nil
					})
				})
				if err == nil {
					t.Fatalf("%s proceeded on the answer %q", name, answer)
				}
				if err.Error() != c.Aborted {
					t.Errorf("err = %q, want the declared abort message %q", err.Error(), c.Aborted)
				}
			})
		}
	}
}

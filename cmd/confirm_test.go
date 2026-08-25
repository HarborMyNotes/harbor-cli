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
	// BOTH halves of the question follow helpers, and they have to. A RunE that
	// calls neither the destructive client method nor the gate by name is the
	// normal shape once a command grows past a few lines — `notes update` reaches
	// ConvertNoteToEncrypted through writeNoteUpdate and its confirmation through
	// prepareNoteMove, and neither name appears in its RunE at all. Scanning only
	// the literal RunE text let that command destroy a note's whole version
	// history without ever entering this check.
	//
	// Names are taken from CALL POSITION rather than by matching the word
	// "confirm", and then resolved against real package functions. Prose cannot
	// survive that: help text has no call parens, and an identifier that is not a
	// package-level function reaches nothing.
	bodies := packageFuncBodies(t)
	reaches := irreversibleReach(t)

	for _, block := range cobraCommandBlocks(t) {
		called := []string{}
		for _, m := range callName.FindAllStringSubmatch(block.runE, -1) {
			called = append(called, m[1])
		}

		call := ""
		for _, c := range irreversibleClientCalls {
			found := strings.Contains(block.runE, c)
			for _, name := range called {
				if found {
					break
				}
				found = reaches[name][c]
			}
			if found {
				call = strings.TrimSuffix(c, "(")
				break
			}
		}
		if call == "" {
			continue
		}
		gated := false
		for _, name := range called {
			if reachesConfirmDestructive(name, bodies, map[string]bool{}) {
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

// callName matches an identifier in call position. It is package-level because
// the walks below run it over every function body in the package, and compiling
// it per frame would dominate their cost.
var callName = regexp.MustCompile(`\b(\w+)\(`)

// irreversibleReach is the reachability map the guard below asks its questions
// of, built in one place so a test can assert what went into it. Methods are
// part of the input, and that is the whole reason this is not inlined: wiring
// the closure back to functions alone would silently reopen the blind spot the
// method scan exists to close.
func irreversibleReach(t *testing.T) map[string]map[string]bool {
	t.Helper()
	return irreversibleCallClosure(packageCallableBodies(t, packageFuncBodies(t)))
}

// irreversibleCallClosure maps every package function to the irreversible client
// calls it can reach — its own body's, plus everything its callees can reach,
// however many helpers deep. It is the mirror image of what
// reachesConfirmDestructive does for the gate, and exists for the same reason: a
// destructive call hidden in a helper is exactly as invisible to a literal text
// scan as a confirmation hidden in one.
//
// It is a single fixed point over the whole call graph because the question is
// asked for every command against every destructive call: resolving each pair by
// its own walk repeats the same traversals O(commands x calls) times, and this
// check runs in every CI job.
//
// METHODS COUNT HERE, and they do not for the gate. An unresolvable name reaches
// no confirmation, which makes a command look ungated and fails the test — safe.
// The same blind spot on this side makes a command look harmless, so a
// destructive call moved into a method would leave the gate deletable with
// nothing failing. Both are indexed by bare name, and a name shared by a
// function and a method merges their bodies: an over-approximation, which on
// this side means asking too often rather than missing a destroyed history.
func irreversibleCallClosure(bodies map[string]string) map[string]map[string]bool {
	callees := map[string][]string{}
	reaches := map[string]map[string]bool{}

	for name, body := range bodies {
		reaches[name] = map[string]bool{}
		for _, c := range irreversibleClientCalls {
			if strings.Contains(body, c) {
				reaches[name][c] = true
			}
		}
		for _, m := range callName.FindAllStringSubmatch(body, -1) {
			callees[name] = append(callees[name], m[1])
		}
	}

	// Propagate along call edges until nothing new appears. Cycles terminate on
	// their own: a round that adds nothing is the last one.
	for changed := true; changed; {
		changed = false
		for name, called := range callees {
			for _, callee := range called {
				for c := range reaches[callee] {
					if !reaches[name][c] {
						reaches[name][c] = true
						changed = true
					}
				}
			}
		}
	}
	return reaches
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
	for _, m := range callName.FindAllStringSubmatch(body, -1) {
		if reachesConfirmDestructive(m[1], bodies, seen) {
			return true
		}
	}
	return false
}

// packageCallableBodies is packageFuncBodies plus every METHOD body, keyed by
// method name, for the destructive-call closure to walk.
//
// Bodies are concatenated rather than replaced when a method and a function
// share a name. The closure only ever asks "can this reach a destructive call",
// so blending two bodies can over-report and never under-report — and
// over-reporting here means a command is asked to prove it confirms, which is
// the answer this check should default to.
func packageCallableBodies(t *testing.T, funcs map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, body := range funcs {
		out[name] = body
	}
	for name, body := range packageMethodBodies(t) {
		out[name] += "\n" + body
	}
	return out
}

// packageMethodBodies collects method bodies keyed by the method's own name,
// receiver discarded.
func packageMethodBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	marker := regexp.MustCompile(`(?m)^func \([^)]+\) (\w+)\(`)
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
			out[text[m[2]:m[3]]] += "\n" + rest[:end]
		}
	}
	// The package is written in free functions, so this is a handful, not a
	// census. It is a tripwire for the scan regressing to zero, nothing more.
	if len(out) < 3 {
		t.Fatalf("only found %d methods in the sources; the scan is broken, so the closure below proves less than it claims", len(out))
	}
	return out
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

// TestTheClosureSeesMethods pins the half of the scan that has no destructive
// call to exercise it today.
//
// Nothing in cmd/ currently reaches an irreversible client call through a
// method, so the edge the closure walks is real but unexercised — and if the
// method scan or its wiring regressed, TestEveryIrreversibleCommandAsksFirst
// would keep passing while a call moved behind a receiver became invisible to
// it. The split is asserted in both directions, because it is deliberate: the
// gate half must NOT see methods, where an unresolvable name is what makes an
// ungated command fail.
func TestTheClosureSeesMethods(t *testing.T) {
	funcs := packageFuncBodies(t)
	callable := packageCallableBodies(t, funcs)

	// The map the guard actually consults, not just the scan behind it — wiring
	// it back to functions alone is the cheapest way to undo this.
	if _, ok := irreversibleReach(t)["announce"]; !ok {
		t.Error("the guard's reachability map has no methods in it, so a destructive call behind a receiver reaches nothing")
	}

	// A method that exists for its side effects on the sealed-move path, so it is
	// the natural one to notice going missing.
	const method, marker = "announce", "printHistoryCaveat("

	if body, ok := funcs[method]; ok && strings.Contains(body, marker) {
		t.Errorf("packageFuncBodies has started indexing methods; the gate half relies on an unresolvable name failing closed")
	}
	body, ok := callable[method]
	if !ok {
		t.Fatalf("the method scan found no %q; a destructive call behind a receiver would be invisible to the guard", method)
	}
	if !strings.Contains(body, marker) {
		t.Errorf("%q was indexed but its body did not come with it, so the closure walks nothing", method)
	}
}

// TestTheClosurePropagatesThroughHelpers pins the fixed point itself on a graph
// small enough to reason about: three frames and a cycle, none of which name a
// destructive call except the last.
func TestTheClosurePropagatesThroughHelpers(t *testing.T) {
	reaches := irreversibleCallClosure(map[string]string{
		"outer":     "middle(",
		"middle":    "inner( outer(",
		"inner":     "c.EmptyTrash(",
		"unrelated": "printResult(",
	})

	if !reaches["outer"]["c.EmptyTrash("] {
		t.Error("the closure stops before the third frame, so a destructive call two helpers deep is invisible")
	}
	if reaches["unrelated"]["c.EmptyTrash("] {
		t.Error("the closure reports a call that is not reachable, which would demand confirmations of harmless commands")
	}
}

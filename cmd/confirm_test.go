// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"io"
	"strings"
	"testing"
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
func TestConfirmDestructiveAbortsWhenTheAnswerCannotBeRead(t *testing.T) {
	captureStdout(t, func() {
		err := confirmDestructive(testConfirmation, false, true, false, func(string) (string, error) {
			return "", io.ErrUnexpectedEOF
		})
		if err == nil {
			t.Error("a failed prompt read must abort, never fall through to the destructive action")
		}
	})
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

// destructiveConfirmations is every confirmation the CLI defines. Registering a
// new one here is what subjects it to the rules below — the point being that the
// next irreversible command inherits the coverage instead of copying a shape and
// quietly losing it, which is how three commands ended up sharing one untested
// branch.
var destructiveConfirmations = map[string]confirmation{
	"trash empty":                 trashEmptyConfirmation,
	"account export-delete":       accountExportDeleteConfirmation,
	"profile inbound-email reset": inboundEmailResetConfirmation,
}

// TestEveryConfirmationIsWellFormed states the rules once instead of per
// command: say what happens, name the escape hatch, and accept exactly one word.
func TestEveryConfirmationIsWellFormed(t *testing.T) {
	for name, c := range destructiveConfirmations {
		t.Run(name, func(t *testing.T) {
			if c.Warning == "" {
				t.Error("no warning: the user is asked to confirm something unstated")
			}
			if c.Affirmative != "yes" {
				t.Errorf("affirmative = %q; every prompt in the CLI asks for %q so muscle memory means the same thing everywhere", c.Affirmative, "yes")
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

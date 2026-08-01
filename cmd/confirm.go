// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// ===========================================================================
// Destructive confirmations
// ===========================================================================
//
// Every irreversible command in the CLI asks the same question in the same
// shape: say what is about to be destroyed, read one word, proceed only on an
// exact match. Written inline each time, that shape reads its answer straight
// off the real terminal — which puts the branch that matters most (the user
// typed something that is not "yes") out of reach of any test. A gate nobody can
// exercise is a gate that rots silently, and the thing on the other side of this
// one is data that does not come back.
//
// So it is split in two. A `confirmation` is WHAT we ask — plain data a test can
// read and assert on. confirmDestructive is HOW we read the reply — a function
// whose interactivity and prompt are parameters, so every branch, including the
// wrong-answer one, is reachable from a test.

// confirmation is the complete wording of one destructive confirmation. Keeping
// it as a value rather than string literals scattered through a RunE means the
// question a command asks can be asserted without anyone having to type at it.
type confirmation struct {
	// Warning is printed before the prompt: what is destroyed, and that it
	// cannot be undone.
	Warning string

	// Prompt is the label the answer is typed against.
	Prompt string

	// Affirmative is the ONLY answer that proceeds. It is compared verbatim
	// against the trimmed line, so "y", "YES" and "" all abort — a near-miss on
	// an irreversible action is a reason to stop, not to guess.
	Affirmative string

	// Unattended is the refusal when there is nobody at a keyboard to ask
	// (--json, or stdin is a pipe). It names the flag that would have worked.
	Unattended string

	// Aborted is the refusal when the answer was anything but Affirmative.
	Aborted string
}

// confirmDestructive decides whether an irreversible command may proceed.
//
// Interactivity and the prompt reader are parameters rather than ambient state
// so that every path below can be driven by a test — that is the whole point of
// the split, not an incidental style choice.
//
// Rules:
//   - --yes proceeds without asking; that is the flag's entire job.
//   - Otherwise, in --json mode or when stdin is not a terminal (scripts, CI, AI
//     agents), refuse rather than prompt. Nobody is there to answer, and a
//     prompt read off a pipe would take whatever byte happened to be next as
//     consent.
//   - On a terminal, warn plainly, then require the affirmative word exactly.
//   - A failed read aborts. An unreadable answer is not an answer.
//
// The only route to nil is --yes or an exact match. Every other path returns an
// error, so a caller that forgets to check the return value fails closed at the
// call site's own error handling rather than deleting anything.
func confirmDestructive(c confirmation, jsonMode, interactive, yes bool, ask func(string) (string, error)) error {
	if yes {
		return nil
	}
	if jsonMode || !interactive {
		return errors.New(c.Unattended)
	}
	fmt.Println(c.Warning)
	answer, err := ask(c.Prompt)
	if err != nil {
		return err
	}
	if answer != c.Affirmative {
		return errors.New(c.Aborted)
	}
	return nil
}

// stdinIsInteractive reports whether stdin is a terminal we can prompt on.
// Scripts and CI pipe stdin, which reads false.
//
// It is a variable, not a plain function, so a test can run the REAL command
// through its interactive branch. Off a terminal that branch is reachable only
// from a human's keyboard, which is exactly how it went unpinned.
var stdinIsInteractive = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// askLine is the reader destructive confirmations use for the typed answer. A
// variable for the same reason as stdinIsInteractive: it is the seam a test
// types "no" through.
var askLine = promptLine

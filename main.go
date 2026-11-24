// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package main

import (
	"github.com/HarborMyNotes/harbor-cli/cmd"

	// A plain date means that day in the user's time zone, and TZ is how a
	// UTC server or container names it. Bundling the zone data keeps TZ
	// working on machines that ship none, where Go would quietly use UTC.
	_ "time/tzdata"
)

// main is the entry point for the harbor CLI tool. It delegates to the cobra
// command tree defined in the cmd package.
func main() {
	cmd.Execute()
}

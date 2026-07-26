// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package main

import "github.com/HarborMyNotes/harbor-cli/cmd"

// main is the entry point for the harbor CLI tool. It delegates to the cobra
// command tree defined in the cmd package.
func main() {
	cmd.Execute()
}

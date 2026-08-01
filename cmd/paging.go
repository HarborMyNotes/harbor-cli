// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"encoding/json"
	"strconv"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// ===========================================================================
// Reading a collection WHOLE (internal reads)
// ===========================================================================
//
// A list COMMAND is paged by the user: --limit/--offset go to the server and a
// footer says there is more. An INTERNAL read is different — the CLI is asking
// the question for itself, so nobody sees the footer and "the first page" is
// silently taken for "all of it". That assumption is invisible until an account
// is big enough to break it, which is exactly how issue #67 happened: the guard
// that protects a note's tasks from a whole-body write read one 500-task page,
// so a note with more than 500 tasks had tasks the guard never saw.
//
// walkCollection is where an internal read goes instead. It costs the common
// case nothing — one request, the same as before — because a first page the
// server marks `has_more: false` ends the walk.

const (
	// collectionPageSize is the page size for an internal read: 500 is the hard
	// cap every Harbor list endpoint clamps to, so it is the fewest round trips a
	// full read can take.
	collectionPageSize = 500

	// collectionPageCap bounds the walk at 20,000 items. It is a backstop against
	// a server that never stops saying `has_more`, not a real ceiling — no Harbor
	// account is near it — and hitting it is reported rather than swallowed, so a
	// caller that must not under-count can refuse instead of guessing.
	collectionPageCap = 40
)

// walkCollection reads a paged collection endpoint from offset 0 and hands each
// item to visit in page order, continuing until the server stops reporting more.
//
// visit returns false to stop the walk early — it has what it came for, and the
// remaining pages are round trips nobody needs.
//
// The bool result says whether the collection was seen WHOLE. It is true unless
// the walk stopped while the server was still reporting `has_more`: the page cap,
// or a page that came back empty while claiming more (a server that cannot
// advance). An early stop by visit is complete — the caller ended the walk on
// purpose. A fetch error returns (false, err) and leaves the "what now" to the
// caller, because degrading and refusing are both right in different places.
func walkCollection(fetch func(params map[string]string) ([]byte, error), visit func(item json.RawMessage) bool) (bool, error) {
	for page := 0; page < collectionPageCap; page++ {
		data, err := fetch(map[string]string{
			"limit":  strconv.Itoa(collectionPageSize),
			"offset": strconv.Itoa(page * collectionPageSize),
		})
		if err != nil {
			return false, err
		}
		items := client.CollectionItems(data)
		for _, item := range items {
			if !visit(item) {
				return true, nil
			}
		}
		// No paging block means the endpoint is not paged (or a server too old to
		// say), and there is nothing that claims otherwise — that is the whole
		// collection as far as anything here can tell.
		p, ok := client.DecodePaging(data)
		if !ok || !p.HasMore {
			return true, nil
		}
		if len(items) == 0 {
			// The server says there is more and hands back nothing, so the walk
			// cannot advance. Calling that complete would be the same "we saw
			// everything" assumption this function exists to end.
			return false, nil
		}
	}
	return false, nil
}

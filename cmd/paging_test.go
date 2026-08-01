// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// pageBody renders a collection page of n items whose ids count on from start,
// with the paging block a real endpoint would send.
func pageBody(start, n, total int, hasMore bool) []byte {
	rows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, fmt.Sprintf(`{"id":"i%d"}`, start+i))
	}
	return []byte(fmt.Sprintf(`{"data":[%s],"paging":{"limit":500,"offset":%d,"total":%d,"has_more":%t}}`,
		strings.Join(rows, ","), start, total, hasMore))
}

// collectIDs is the visitor every test below uses: it records each item's id and
// never stops early.
func collectIDs(out *[]string) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		*out = append(*out, str(parseJSON(raw), "id"))
		return true
	}
}

// A single page the server marks `has_more: false` is the whole collection, and it
// costs one request. This is the common case, and the reason paging is affordable
// at all: the ordinary note pays nothing for the walk.
func TestWalkCollectionStopsAtASinglePage(t *testing.T) {
	calls := 0
	var got []string

	complete, err := walkCollection(func(map[string]string) ([]byte, error) {
		calls++
		return pageBody(0, 3, 3, false), nil
	}, collectIDs(&got))

	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v, want true/nil", complete, err)
	}
	if calls != 1 {
		t.Errorf("made %d requests for a single-page collection, want 1", calls)
	}
	if len(got) != 3 {
		t.Errorf("read %d items, want 3", len(got))
	}
}

// The point of the whole exercise: when the server says there is more, the walk
// asks for more — at the right offset — until it says there is not.
func TestWalkCollectionReadsEveryPage(t *testing.T) {
	var offsets []string
	var got []string

	complete, err := walkCollection(func(params map[string]string) ([]byte, error) {
		offsets = append(offsets, params["offset"])
		switch params["offset"] {
		case "0":
			return pageBody(0, 500, 501, true), nil
		default:
			return pageBody(500, 1, 501, false), nil
		}
	}, collectIDs(&got))

	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v, want true/nil", complete, err)
	}
	if len(got) != 501 {
		t.Fatalf("read %d items, want 501 — the tail of the collection was dropped", len(got))
	}
	if got[500] != "i500" {
		t.Errorf("last item is %q, want the one that only page 2 carries", got[500])
	}
	if strings.Join(offsets, ",") != "0,500" {
		t.Errorf("offsets requested were %v, want 0 then 500", offsets)
	}
}

// A visitor that has found what it came for ends the walk, and that counts as a
// complete read: the caller stopped on purpose, so nothing it cares about is unseen.
func TestWalkCollectionStopsEarlyWhenTheVisitorIsSatisfied(t *testing.T) {
	calls := 0

	complete, err := walkCollection(func(map[string]string) ([]byte, error) {
		calls++
		return pageBody(0, 500, 5000, true), nil
	}, func(json.RawMessage) bool { return false })

	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v, want true/nil", complete, err)
	}
	if calls != 1 {
		t.Errorf("kept paging after the visitor said stop: %d requests", calls)
	}
}

// A server that never stops saying `has_more` must not spin forever — and the walk
// must SAY it was cut short rather than pass off a partial read as the whole thing.
// Reporting it is what lets the task guard refuse instead of guess (issue #67).
func TestWalkCollectionReportsAnIncompleteWalkAtTheCap(t *testing.T) {
	calls := 0
	var got []string

	complete, err := walkCollection(func(map[string]string) ([]byte, error) {
		calls++
		return pageBody(calls*500, 500, 1<<30, true), nil
	}, collectIDs(&got))

	if err != nil {
		t.Fatalf("walkCollection: %v", err)
	}
	if complete {
		t.Error("a walk stopped by the page cap reported the collection as read whole")
	}
	if calls != collectionPageCap {
		t.Errorf("made %d requests, want the %d-page cap", calls, collectionPageCap)
	}
}

// A page that comes back empty while the server still claims more is a walk that
// cannot advance. Calling that complete would be the same assumption in a new
// costume, so it is reported short — and it does not burn the whole page cap first.
func TestWalkCollectionReportsAnEmptyPageThatClaimsMore(t *testing.T) {
	calls := 0

	complete, err := walkCollection(func(map[string]string) ([]byte, error) {
		calls++
		return []byte(`{"data":[],"paging":{"limit":500,"offset":0,"total":9,"has_more":true}}`), nil
	}, func(json.RawMessage) bool { return true })

	if err != nil {
		t.Fatalf("walkCollection: %v", err)
	}
	if complete {
		t.Error("an empty page that claimed more was treated as the end of the collection")
	}
	if calls != 1 {
		t.Errorf("retried a page that cannot advance %d times, want 1", calls)
	}
}

// A response with no paging block is not paged (or comes from a server too old to
// say). Nothing claims there is more, so that is the collection.
func TestWalkCollectionAcceptsAResponseWithNoPagingBlock(t *testing.T) {
	var got []string

	complete, err := walkCollection(func(map[string]string) ([]byte, error) {
		return []byte(`{"data":[{"id":"i0"},{"id":"i1"}]}`), nil
	}, collectIDs(&got))

	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v, want true/nil", complete, err)
	}
	if len(got) != 2 {
		t.Errorf("read %d items, want 2", len(got))
	}
}

// A fetch error is handed back whole. The walk does not decide whether that is
// fatal — degrading and refusing are both right, in different callers.
func TestWalkCollectionReturnsTheFetchError(t *testing.T) {
	boom := errors.New("boom")

	complete, err := walkCollection(func(map[string]string) ([]byte, error) {
		return nil, boom
	}, func(json.RawMessage) bool { return true })

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the fetch error", err)
	}
	if complete {
		t.Error("a failed fetch reported the collection as read whole")
	}
}

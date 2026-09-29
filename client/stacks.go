// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package client

import "net/url"

// ListStacks returns the user's stacks (collection envelope): each one's name,
// live notebook count and metadata. The query is built by the caller because the
// metadata filters repeat a parameter (meta_has) and can carry an empty value
// (meta.KEY= means "equals the empty string"), which the map form would drop.
func (c *Client) ListStacks(q url.Values) ([]byte, error) {
	return c.doGetQuery("/stacks", q)
}

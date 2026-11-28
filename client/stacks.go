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

// UpdateStack changes a stack addressed by its current name and answers the
// stack's list object. The name is free text, so it is escaped — a space or `?`
// in a name would otherwise change what the URL means.
func (c *Client) UpdateStack(name string, body map[string]any) ([]byte, error) {
	return c.doPatch("/stacks/"+url.PathEscape(name), body)
}

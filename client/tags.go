// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import "net/url"

// ListTags returns the user's tags (collection envelope). The query is built by
// the caller so it can express the three parent_id modes: absent (all tags),
// empty (top-level only), or a specific parent (its direct children).
func (c *Client) ListTags(q url.Values) ([]byte, error) {
	return c.doGetQuery("/tags", q)
}

// GetTag fetches one tag by id, optionally including a tombstoned one.
func (c *Client) GetTag(id string, includeDeleted bool) ([]byte, error) {
	params := map[string]string{}
	if includeDeleted {
		params["include_deleted"] = "true"
	}
	return c.doGet("/tags/"+id, params)
}

// CreateTag creates a tag and returns the bare created object.
func (c *Client) CreateTag(body map[string]any) ([]byte, error) {
	return c.doPost("/tags", body)
}

// UpdateTag renames, re-parents and/or recolors a tag and returns the bare
// updated object.
func (c *Client) UpdateTag(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/tags/"+id, body)
}

// DeleteTag tombstones a tag. childrenPolicy is "reparent_to_grandparent"
// (default) or "orphan".
func (c *Client) DeleteTag(id, childrenPolicy string) ([]byte, error) {
	params := map[string]string{}
	if childrenPolicy != "" {
		params["children"] = childrenPolicy
	}
	return c.doDelete("/tags/"+id, params)
}

// ListTagNotes lists the live notes carrying a tag (collection of note objects).
func (c *Client) ListTagNotes(tagID string, params map[string]string) ([]byte, error) {
	return c.doGet("/tags/"+tagID+"/notes", params)
}

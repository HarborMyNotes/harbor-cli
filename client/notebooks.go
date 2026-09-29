// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import "net/url"

// ListNotebooks returns the user's notebooks (collection envelope). Accepts the
// standard list params (limit, offset, order, stack, include_deleted).
func (c *Client) ListNotebooks(params map[string]string) ([]byte, error) {
	return c.doGet("/notebooks", params)
}

// ListNotebooksQuery is ListNotebooks with a caller-built query, for the
// metadata filters: meta_has repeats and meta.KEY may carry an empty value, and
// the map form can express neither.
func (c *Client) ListNotebooksQuery(q url.Values) ([]byte, error) {
	return c.doGetQuery("/notebooks", q)
}

// GetNotebook fetches a single notebook by id. With includeDeleted, tombstoned
// notebooks are returned instead of 404.
func (c *Client) GetNotebook(id string, includeDeleted bool) ([]byte, error) {
	params := map[string]string{}
	if includeDeleted {
		params["include_deleted"] = "true"
	}
	return c.doGet("/notebooks/"+id, params)
}

// CreateNotebook creates a notebook from the given fields and returns the bare
// created object.
func (c *Client) CreateNotebook(body map[string]any) ([]byte, error) {
	return c.doPost("/notebooks", body)
}

// UpdateNotebook applies a partial update (only the fields present in body) and
// returns the bare updated object.
func (c *Client) UpdateNotebook(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/notebooks/"+id, body)
}

// DeleteNotebook tombstones a notebook. notesPolicy controls its live notes:
// "move_to_default" (default) reassigns them, "trash" tombstones them.
func (c *Client) DeleteNotebook(id, notesPolicy string) ([]byte, error) {
	params := map[string]string{}
	if notesPolicy != "" {
		params["notes"] = notesPolicy
	}
	return c.doDelete("/notebooks/"+id, params)
}

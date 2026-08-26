// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

// ListTemplates returns the user's note templates (collection envelope).
// Accepts the standard list params (limit, offset, order) plus
// include_deleted and include_system.
func (c *Client) ListTemplates(params map[string]string) ([]byte, error) {
	return c.doGet("/templates", params)
}

// GetTemplate fetches a single template by id, including its content. With
// includeDeleted, tombstoned templates are returned instead of 404.
func (c *Client) GetTemplate(id string, includeDeleted bool) ([]byte, error) {
	params := map[string]string{}
	if includeDeleted {
		params["include_deleted"] = "true"
	}
	return c.doGet("/templates/"+id, params)
}

// CreateTemplate creates a user template from the given fields and returns the
// bare created object (not wrapped in data).
func (c *Client) CreateTemplate(body map[string]any) ([]byte, error) {
	return c.doPost("/templates", body)
}

// UpdateTemplate applies a partial update (only the fields present in body) and
// returns the bare updated object. System templates are rejected by the server
// with 403 system_template_readonly.
func (c *Client) UpdateTemplate(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/templates/"+id, body)
}

// DeleteTemplate tombstones a user template. System templates are rejected by
// the server with 403 system_template_readonly.
func (c *Client) DeleteTemplate(id string) ([]byte, error) {
	return c.doDelete("/templates/"+id, nil)
}

// ApplyTemplate instantiates a new note from a template and returns a
// {note, usn, notice} envelope — CreateNote's shape plus one advisory line.
//
// notice is always present and is "" when there is nothing to say. It is
// populated when the template's own notebook was gone or encrypt-by-default and
// the note was filed in the account default instead, so it must be surfaced
// rather than dropped. The body fields (notebook_id, title, tags) are all
// optional overrides; an omitted tags inherits the template's, a sent one
// replaces them.
func (c *Client) ApplyTemplate(id string, body map[string]any) ([]byte, error) {
	return c.doPost("/templates/"+id+"/apply", body)
}

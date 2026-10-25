// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

// Search runs a search query and returns mixed note/attachment hits
// (collection envelope). params carries q, types, notebook_id, order, snippet,
// and the standard list params.
func (c *Client) Search(params map[string]string) ([]byte, error) {
	return c.doGet("/search", params)
}

// SearchCoordinates returns OCR word boxes for matched terms on one attachment
// (single data resource). params carries resource_id, q or terms, page, and
// max_boxes.
func (c *Client) SearchCoordinates(params map[string]string) ([]byte, error) {
	return c.doGet("/search/coordinates", params)
}

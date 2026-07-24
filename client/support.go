// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-23

package client

// SubmitSupportRequest files an in-app Contact Support request against the
// authenticated endpoint. The body carries category/subject/message plus an
// optional metadata object and attachments array (see cmd/support.go for the
// shape). The caller's name, email, and account are derived server-side from the
// bearer session and must NOT be sent. On success the server returns
// { "data": { "received": true, "reference": "..." } }.
func (c *Client) SubmitSupportRequest(body map[string]any) ([]byte, error) {
	return c.doPost("/support", body)
}

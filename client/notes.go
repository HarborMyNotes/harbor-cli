// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

// ListNotes returns the user's notes (collection envelope). Accepts limit,
// offset, order, notebook_id, tag, updated_since, deleted, and fields params.
func (c *Client) ListNotes(params map[string]string) ([]byte, error) {
	return c.doGet("/notes", params)
}

// GetNote fetches one note. params may include deleted=true and
// format=markdown.
func (c *Client) GetNote(id string, params map[string]string) ([]byte, error) {
	return c.doGet("/notes/"+id, params)
}

// CreateNote creates a note and returns the {note, usn} mutation envelope.
func (c *Client) CreateNote(body map[string]any) ([]byte, error) {
	return c.doPost("/notes", body)
}

// UpdateNote applies a partial update and returns {note, usn}.
func (c *Client) UpdateNote(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/notes/"+id, body)
}

// ConvertNoteToEncrypted turns an existing plaintext note into an encrypted one:
// the same PATCH as UpdateNote, carrying `is_encrypted: true` plus the HRBC2
// envelopes for title and content. It is a method of its own rather than a call
// to UpdateNote because the two say different things about the note — an update
// changes what a note holds, this changes how it is STORED — and a reader
// auditing where the CLI flips a note's encryption should find one name, not a
// map[string]any three call frames away.
func (c *Client) ConvertNoteToEncrypted(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/notes/"+id, body)
}

// ConvertNoteToPlaintext writes a decrypted note back to the server in the clear:
// `is_encrypted: false` with the opened title and content.
//
// It is separate from UpdateNote for a stronger reason than its sibling above.
// This is the one note write that CANNOT be taken back — not because the note is
// destroyed (re-encrypting it is one command away), but because the plaintext
// reaches the server, where it is indexed for search and snapshotted into the
// note's version history. Re-encrypting the note afterwards does not unsend it.
// So it is named on its own, and cmd/confirm_test.go's irreversibleClientCalls
// registry lists it: any command whose RunE issues this call must consult a
// confirmation first, and the test fails if one ever does not.
func (c *Client) ConvertNoteToPlaintext(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/notes/"+id, body)
}

// AppendNote appends a fragment to the end of a note's body and returns
// {note, usn}. Encrypted notes are rejected by the server.
func (c *Client) AppendNote(id string, body map[string]any) ([]byte, error) {
	return c.doPost("/notes/"+id+"/append", body)
}

// DeleteNote trashes a note (recoverable) by default, or expunges it directly
// when permanent is true.
func (c *Client) DeleteNote(id string, permanent bool) ([]byte, error) {
	params := map[string]string{}
	if permanent {
		params["permanent"] = "true"
	}
	return c.doDelete("/notes/"+id, params)
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

// ListTrash returns the notes currently in the trash (collection envelope),
// most-recently-trashed first. Accepts the standard list params (limit, offset,
// order). Sortable orders: trashed_at, updated_at, created_at, title (prefix -
// for descending; the default is -trashed_at).
func (c *Client) ListTrash(params map[string]string) ([]byte, error) {
	return c.doGet("/trash", params)
}

// RestoreNote returns a note from the trash to the live set, clearing in_trash
// and bumping its USN. It returns the restored note object (bare, not wrapped
// in a data envelope).
func (c *Client) RestoreNote(id string) ([]byte, error) {
	return c.doPost("/notes/"+id+"/restore", nil)
}

// ExpungeNote permanently deletes a note: it promotes the note to the canonical
// sync tombstone (deleted=1) so the deletion propagates to every device. It
// works whether or not the note is currently in the trash and returns no body
// (204 No Content).
func (c *Client) ExpungeNote(id string) ([]byte, error) {
	return c.doPost("/notes/"+id+"/expunge", nil)
}

// EmptyTrash expunges every note currently in the trash. The server answers in
// one of two bare shapes:
//
//   - 200 {"expunged": N} for a small trash: the notes are already gone.
//   - 202 {"expunged": N, "job": {...}} for a big one: a background job deletes
//     them, and N is how many it WILL delete. Poll the job with
//     GetTrashEmptyJob until its status is completed or failed. If an Empty
//     Trash is already running, the answer is that job; no second one starts.
func (c *Client) EmptyTrash() ([]byte, error) {
	return c.doDelete("/trash", nil)
}

// GetTrashEmptyJob returns one background Empty Trash job, data-wrapped:
// {"data": {id, status, total_notes, processed_notes, error, ...}}. status is
// queued, running, completed or failed; error is the sentence to show when it
// failed.
func (c *Client) GetTrashEmptyJob(id string) ([]byte, error) {
	return c.doGet("/trash/empty-jobs/"+id, nil)
}

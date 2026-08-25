// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

// StartAccountExport starts an export job. Both arguments are optional: an empty
// pair starts a whole-account ENEX export. format selects the archive kind —
// "enex" (the GDPR ZIP: one ENEX per notebook, the attachment bytes, a
// manifest), "html" (a self-contained, browsable offline website) or "markdown"
// (one .md per note in folders matching the notebooks, attachments alongside) —
// and notebookID scopes the export to a single notebook, for any format.
// Returns the data-wrapped job ({export_job_id, status, format, notebook_id,
// notebook_name}); the response status is 202 Accepted.
//
// An account holds at most one live export PER FORMAT, and scope does not create
// a slot of its own. A queued/running export of the same format is handed back rather
// than started twice; a completed, unexpired one is refused with 409
// export_exists whose details carry export_job_id, format, scope, notebook_id,
// notebook_name and result_expires_at — everything needed to explain the refusal
// without a second request.
func (c *Client) StartAccountExport(format, notebookID string) ([]byte, error) {
	body := map[string]any{}
	if format != "" {
		body["format"] = format
	}
	if notebookID != "" {
		body["notebook_id"] = notebookID
	}
	return c.doPost("/account/export", body)
}

// ListAccountExports returns what this account's export slots hold right now:
// the most recent export per format (at most three entries — one ENEX, one HTML,
// one Markdown),
// each in exactly the shape GetAccountExport returns, including a freshly minted
// download_url for a ready one. A format with no export history is simply absent
// from the array, and an account that has never exported returns an empty one.
//
// This is how a client that did not start the export — a fresh shell, another
// machine — discovers it at all: export state lives on the server, so it has to
// be read from there rather than remembered locally.
func (c *Client) ListAccountExports() ([]byte, error) {
	return c.doGet("/account/export", nil)
}

// DeleteAccountExport deletes an export's archive from the blob store and frees
// that format's slot so a new export can start immediately. It is idempotent:
// deleting an export whose archive is already gone returns 200 carrying the
// status it already has, so an expired export is reported back as expired rather
// than rewritten to deleted. An export that is still being built is refused with
// 409 export_in_progress — the queue has no per-job cancellation.
func (c *Client) DeleteAccountExport(id string) ([]byte, error) {
	return c.doDelete("/account/export/"+id, nil)
}

// GetAccountExport polls an export job by id. When the job is completed and the
// result blob has not expired, the data-wrapped job includes a short-lived
// presigned download_url, the filename the archive should be saved as, and a
// result_expires_at. Progress is reported in NOTES via total_units/done_units, a
// queued job carries its 1-based queue_position, a completed one its
// completed_at, and a failed one its error_text.
func (c *Client) GetAccountExport(id string) ([]byte, error) {
	return c.doGet("/account/export/"+id, nil)
}

// RequestAccountDeletion schedules a grace-period account deletion. It requires
// re-auth via current_password AND the exact confirmation phrase (sent verbatim
// as confirm). No data is destroyed now: the response carries the scheduled
// status, purge_after, grace_days, and can_cancel_until window.
func (c *Client) RequestAccountDeletion(currentPassword, confirm string) ([]byte, error) {
	return c.doPost("/account/delete", map[string]any{
		"current_password": currentPassword,
		"confirm":          confirm,
	})
}

// CancelAccountDeletion cancels a pending deletion within the grace window,
// re-authenticating via current_password. Returns the data-wrapped reactivated
// status ({status: "active"}).
func (c *Client) CancelAccountDeletion(currentPassword string) ([]byte, error) {
	return c.doPost("/account/delete/cancel", map[string]any{
		"current_password": currentPassword,
	})
}

// RequestAccountClear empties the account without deleting it: every note,
// notebook, tag, task, file and the encryption keystore go, and the account,
// its plan, its sessions and its tokens stay. Returns the data-wrapped job
// ({clear_job_id, status, started_at, finished_at}) with status 202 Accepted.
//
// IT IS NOT DELETE. There is no grace period and nothing to cancel — the work
// starts immediately, which is why the server demands both a re-auth proof and
// a confirmation phrase of its own. The phrase is compared byte for byte, so
// send exactly what the user typed rather than a trimmed or upper-cased copy;
// normalising it here would turn a phrase the server rejects into one it
// accepts.
//
// The 202 is the job being QUEUED, not the account being empty. A large account
// spends minutes deleting attachment blobs, so a caller that reports success
// off this response tells the user their notes are gone while the server still
// has them. Poll GetAccountClear.
func (c *Client) RequestAccountClear(currentPassword, confirm string) ([]byte, error) {
	return c.doPost("/account/clear", map[string]any{
		"current_password": currentPassword,
		"confirm":          confirm,
	})
}

// GetAccountClear returns the account's current (or last) clear job, in the same
// shape RequestAccountClear returns.
//
// An account that has never been cleared answers 404 not_found. That is an
// ANSWER, not a failure — the caller decides what it means, because the same
// code means "no such export job" elsewhere in this domain.
func (c *Client) GetAccountClear() ([]byte, error) {
	return c.doGet("/account/clear", nil)
}

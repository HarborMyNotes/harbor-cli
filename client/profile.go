// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

// GetProfile fetches the authenticated user's profile (data-wrapped). The
// profile id doubles as the sync scope_id.
func (c *Client) GetProfile() ([]byte, error) {
	return c.doGet("/profile", nil)
}

// UpdateProfile applies a profile update (name/locale/timezone immediately; an
// email change is staged and requires current_password). Only the fields in
// body are touched. Returns the updated profile.
func (c *Client) UpdateProfile(body map[string]any) ([]byte, error) {
	return c.doPut("/profile", body)
}

// ChangePassword changes the account password (re-auth via current_password).
// The server revokes every other session on success.
func (c *Client) ChangePassword(currentPassword, newPassword string) ([]byte, error) {
	return c.doPost("/profile/change-password", map[string]any{
		"current_password": currentPassword,
		"new_password":     newPassword,
	})
}

// SetAvatar points the avatar at an already-uploaded image blob, by its content
// sha256 hash. Returns the updated profile.
func (c *Client) SetAvatar(hash string) ([]byte, error) {
	return c.doPost("/profile/avatar", map[string]any{"hash": hash})
}

// RemoveAvatar clears the avatar reference.
func (c *Client) RemoveAvatar() ([]byte, error) {
	return c.doDelete("/profile/avatar", nil)
}

// SetInboundEmailEnabled turns the account's email-to-note address on or off.
// It is a plain preference — no re-auth — and never touches the stored address,
// so disabling and re-enabling yields the SAME address. Returns the updated
// profile.
func (c *Client) SetInboundEmailEnabled(enabled bool) ([]byte, error) {
	return c.doPut("/profile", map[string]any{"inbound_email_enabled": enabled})
}

// ResetInboundEmail rotates the account's email-to-note address, regenerating
// the random token half while preserving the human-readable slug. The old
// address stops resolving immediately, so this is the revocation path for an
// address that leaked. Takes no body and does not change the on/off switch.
// Returns { "data": { "inbound_email_address": "…" } }.
func (c *Client) ResetInboundEmail() ([]byte, error) {
	return c.doPost("/profile/inbound-email/reset", nil)
}

// ConfirmEmailChange consumes a staged email-change token (public — no bearer).
func (c *Client) ConfirmEmailChange(token string) ([]byte, error) {
	return c.postJSONNoRefresh("/profile/email/confirm", map[string]string{"token": token})
}

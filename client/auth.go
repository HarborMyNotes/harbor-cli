// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"net/http"
)

// postJSONNoRefresh performs a JSON POST that never attempts a transparent
// refresh. The OAuth/auth-recovery endpoints must use this: they are public
// (no bearer) and a refresh during a token exchange would be nonsensical and
// could rotate the refresh token recursively.
func (c *Client) postJSONNoRefresh(path string, body any) ([]byte, error) {
	full, err := c.buildURL(path, nil)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	return c.request(http.MethodPost, full, raw, "application/json", false)
}

// AuthorizationCodeGrant exchanges a PKCE authorization code for an access +
// refresh token pair — the token step of the browser login flow. redirectURI
// must be byte-identical to the loopback URI the code was minted against, and
// codeVerifier is the PKCE secret whose S256 challenge was sent to
// /oauth/authorize. Public (no bearer) and uses the no-refresh path, since this
// call is itself the token acquisition. The code is single-use and expires ~60s
// after issuance, so exchange it immediately on receipt.
func (c *Client) AuthorizationCodeGrant(clientID, code, redirectURI, codeVerifier string) ([]byte, *TokenResponse, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"code":          code,
		"redirect_uri":  redirectURI,
		"code_verifier": codeVerifier,
	}
	data, err := c.postJSONNoRefresh("/oauth/token", body)
	if err != nil {
		return nil, nil, err
	}
	tok, err := DecodeToken(data)
	return data, tok, err
}

// PersonalAccessToken is the response from minting a PAT (POST /tokens). Token
// holds the raw hbp_… secret, which the server reveals exactly once at creation.
// ExpiresAt is nil for a never-expiring token.
type PersonalAccessToken struct {
	ID        string   `json:"id"`
	Token     string   `json:"token"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	TokenKind string   `json:"token_kind"`
	ExpiresAt *int64   `json:"expires_at"`
	CreatedAt int64    `json:"created_at"`
}

// CreatePAT mints a Personal Access Token for the signed-in user. It requires a
// bearer whose grant includes the profile scope. expiresIn is the lifetime in
// seconds; pass 0 to omit it and mint a token that NEVER expires — this is how
// the CLI obtains a long-lived credential after the browser login. The returned
// raw token is shown only once, so callers must persist it immediately.
func (c *Client) CreatePAT(name string, scopes []string, expiresIn int64) ([]byte, *PersonalAccessToken, error) {
	body := map[string]any{
		"name":   name,
		"scopes": scopes,
	}
	if expiresIn > 0 {
		body["expires_in"] = expiresIn
	}
	data, err := c.doPost("/tokens", body)
	if err != nil {
		return nil, nil, err
	}
	var pat PersonalAccessToken
	if err := json.Unmarshal(UnwrapData(data), &pat); err != nil {
		return nil, nil, err
	}
	return data, &pat, nil
}

// RefreshGrant rotates a single-use refresh token into a new access + refresh
// pair. scope, when set, may only narrow the existing grant. Presenting an
// already-rotated token returns invalid_grant and revokes the whole family.
func (c *Client) RefreshGrant(clientID, refreshToken, scope string) ([]byte, *TokenResponse, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": refreshToken,
	}
	if scope != "" {
		body["scope"] = scope
	}
	data, err := c.postJSONNoRefresh("/oauth/token", body)
	if err != nil {
		return nil, nil, err
	}
	tok, err := DecodeToken(data)
	return data, tok, err
}

// Revoke revokes a token (RFC 7009 style). A refresh token revokes its whole
// family; an access token revokes just itself. Always succeeds server-side,
// even for an unknown token (no validity leak).
func (c *Client) Revoke(token, tokenTypeHint string) error {
	body := map[string]string{"token": token}
	if tokenTypeHint != "" {
		body["token_type_hint"] = tokenTypeHint
	}
	_, err := c.postJSONNoRefresh("/oauth/revoke", body)
	return err
}

// Logout revokes the current session server-side (or every session when
// allDevices is true). Requires a bearer token.
func (c *Client) Logout(allDevices bool) error {
	_, err := c.doPost("/auth/logout", map[string]bool{"all_devices": allDevices})
	return err
}

// VerifyEmail consumes an email-verification token. Public.
func (c *Client) VerifyEmail(token string) ([]byte, error) {
	return c.postJSONNoRefresh("/auth/verify-email", map[string]string{"token": token})
}

// ResendVerification requests a fresh verification email. When a bearer is
// present the server uses it; otherwise the email body is required (and the
// response is always "sent", to avoid account enumeration).
func (c *Client) ResendVerification(email string) ([]byte, error) {
	body := map[string]string{}
	if email != "" {
		body["email"] = email
	}
	// Uses the standard path so a present bearer is honored; no refresh needed.
	return c.postJSONNoRefresh("/auth/verify-email/resend", body)
}

// ForgotPassword starts a password reset. Always reports success (anti-
// enumeration). Public.
func (c *Client) ForgotPassword(email string) ([]byte, error) {
	return c.postJSONNoRefresh("/auth/password/forgot", map[string]string{"email": email})
}

// ResetPassword completes a password reset with a reset token and new
// password. Revokes all sessions on success. Public.
func (c *Client) ResetPassword(token, password string) ([]byte, error) {
	return c.postJSONNoRefresh("/auth/password/reset", map[string]string{
		"token":    token,
		"password": password,
	})
}

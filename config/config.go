// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

// Package config persists the logged-in Harbor session — OAuth tokens plus the
// API target and device identity — to ~/.config/harbor/credentials.json with
// 0600 permissions. It never logs or prints token material.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the production Harbor API endpoint that real customers
	// always use. It includes the /api/v1 prefix; domain client methods use
	// paths relative to it. The override (api_url / --api-url / HARBOR_API_URL)
	// exists only so the maintainer can point the CLI at staging or local
	// environments — it is not a customer-facing feature.
	DefaultBaseURL = "https://app.harbor.my/api/v1"

	// DefaultClientID is the fallback OAuth client id used only when a saved
	// credential predates the browser-login change and has no client_id. New
	// logins store "harbor-cli" explicitly; legacy sessions minted under
	// "harbor-app" keep working because they carry their own client_id.
	DefaultClientID = "harbor-app"

	// configDir is the directory name under ~/.config for Harbor credentials.
	configDir = "harbor"

	// configFile is the credentials file name.
	configFile = "credentials.json"

	// keystoreCacheFile caches the opaque HRBK1 encryption keystore blob so the
	// CLI need not re-pull it from sync on every encrypt/decrypt. The blob is the
	// WRAPPED master key — useless without the passphrase — so caching it on disk
	// does not weaken the zero-knowledge model. The passphrase and the unwrapped
	// master key are NEVER written to disk.
	keystoreCacheFile = "keystore.json"
)

// Credentials is the on-disk session: API target, identity, and the OAuth token
// pair. Times are UTC epoch milliseconds (matching the API), except where noted.
type Credentials struct {
	APIURL       string `json:"api_url"`
	ClientID     string `json:"client_id"`
	Email        string `json:"email"`
	UserID       string `json:"user_id,omitempty"` // resolved lazily; used as the sync scope_id
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresAt    int64  `json:"expires_at"` // epoch-ms when the access token expires
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name"`
}

// ConfigDirPath returns the full path to the Harbor configuration directory
// (~/.config/harbor).
func ConfigDirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", configDir), nil
}

// ConfigFilePath returns the full path to the credentials file.
func ConfigFilePath() (string, error) {
	dir, err := ConfigDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFile), nil
}

// Load reads and parses the credentials file. A missing file returns an
// actionable error telling the user to run `harbor login` first.
func Load() (*Credentials, error) {
	path, err := ConfigFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not logged in — run 'harbor login' first")
		}
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}

	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("unable to parse credentials file (re-run 'harbor login'): %w", err)
	}

	return &c, nil
}

// Save writes the credentials to disk atomically with 0600 permissions,
// creating the 0700 config directory if needed. It writes to a temp file in the
// same directory and renames, so a crash never leaves a half-written (and thus
// session-losing) credentials file.
func Save(c *Credentials) error {
	dir, err := ConfigDirPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("unable to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("unable to marshal credentials: %w", err)
	}

	path := filepath.Join(dir, configFile)
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("unable to create temp credentials file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("unable to set credentials permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("unable to write credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("unable to flush credentials: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("unable to finalize credentials file: %w", err)
	}
	return nil
}

// Clear removes the credentials file. A missing file is not an error, so
// logout is idempotent.
func Clear() error {
	path, err := ConfigFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unable to remove credentials file: %w", err)
	}
	return nil
}

// keystoreCachePath returns the full path to the cached keystore-blob file.
func keystoreCachePath() (string, error) {
	dir, err := ConfigDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, keystoreCacheFile), nil
}

// LoadKeystoreBlob returns the cached HRBK1 keystore blob, or "" (no error) when
// no cache exists yet. The blob is the wrapped master key and is safe at rest.
func LoadKeystoreBlob() (string, error) {
	path, err := keystoreCachePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("unable to read keystore cache: %w", err)
	}
	var c struct {
		Blob string `json:"blob"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("unable to parse keystore cache: %w", err)
	}
	return c.Blob, nil
}

// SaveKeystoreBlob atomically writes the keystore blob cache with 0600
// permissions, creating the config directory if needed.
func SaveKeystoreBlob(blob string) error {
	dir, err := ConfigDirPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("unable to create config directory: %w", err)
	}
	data, err := json.MarshalIndent(map[string]string{"blob": blob}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, keystoreCacheFile)
	tmp, err := os.CreateTemp(dir, ".keystore-*.tmp")
	if err != nil {
		return fmt.Errorf("unable to create temp keystore file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("unable to set keystore permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("unable to write keystore cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("unable to flush keystore cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("unable to finalize keystore cache: %w", err)
	}
	return nil
}

// ClearKeystoreBlob removes the cached keystore blob. A missing file is not an
// error, so logout/reset is idempotent.
func ClearKeystoreBlob() error {
	path, err := keystoreCachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unable to remove keystore cache: %w", err)
	}
	return nil
}

// BaseURL returns the configured API base URL, falling back to the production
// default when unset. The returned value includes the /api/v1 prefix.
func (c *Credentials) BaseURL() string {
	if c == nil || strings.TrimSpace(c.APIURL) == "" {
		return DefaultBaseURL
	}
	return c.APIURL
}

// EffectiveClientID returns the configured OAuth client id, defaulting to the
// first-party client.
func (c *Credentials) EffectiveClientID() string {
	if c == nil || strings.TrimSpace(c.ClientID) == "" {
		return DefaultClientID
	}
	return c.ClientID
}

// IsExpired reports whether the access token is expired (or will be within the
// given skew). A zero ExpiresAt is treated as expired so callers refresh. The
// skew lets callers refresh proactively before a request rather than racing
// expiry mid-flight.
func (c *Credentials) IsExpired(skew time.Duration) bool {
	if c == nil || c.ExpiresAt == 0 {
		return true
	}
	deadline := time.UnixMilli(c.ExpiresAt).Add(-skew)
	return time.Now().After(deadline)
}

// ExpiresAtTime returns the access-token expiry as a time.Time (zero if unset).
func (c *Credentials) ExpiresAtTime() time.Time {
	if c == nil || c.ExpiresAt == 0 {
		return time.Time{}
	}
	return time.UnixMilli(c.ExpiresAt)
}

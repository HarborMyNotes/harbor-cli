// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempHome points HOME at a throwaway dir so tests never touch the real
// ~/.config/harbor. t.Setenv restores the prior value automatically.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestSaveLoadRoundTripAndPerms(t *testing.T) {
	home := withTempHome(t)

	in := &Credentials{
		APIURL:       "http://localhost:8472/api/v1",
		ClientID:     "harbor-app",
		Email:        "you@example.com",
		AccessToken:  "at_test_abc",
		RefreshToken: "rt_test_def",
		TokenType:    "Bearer",
		Scope:        "notes sync",
		ExpiresAt:    1750000000000,
		DeviceID:     "cli-test",
		DeviceName:   "harbor-cli on testhost",
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	path := filepath.Join(home, ".config", "harbor", "credentials.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}

	out, err := Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if out.Email != in.Email || out.AccessToken != in.AccessToken || out.ExpiresAt != in.ExpiresAt {
		t.Errorf("round trip mismatch: %+v", out)
	}
}

func TestLoadMissingIsActionable(t *testing.T) {
	withTempHome(t)
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if got := err.Error(); got == "" || !contains(got, "harbor login") {
		t.Errorf("error = %q, want mention of 'harbor login'", got)
	}
}

func TestClearIsIdempotent(t *testing.T) {
	withTempHome(t)
	// Clearing when nothing exists is not an error.
	if err := Clear(); err != nil {
		t.Fatalf("Clear on empty error: %v", err)
	}
	_ = Save(&Credentials{Email: "x@example.com"})
	if err := Clear(); err != nil {
		t.Fatalf("Clear error: %v", err)
	}
	if _, err := Load(); err == nil {
		t.Error("credentials should be gone after Clear")
	}
}

// TestKeystoreBlobRoundTripAndPerms covers writePrivateFile through its first
// caller: the blob comes back, the file is 0600, the directory 0700, and no
// temp file is left beside it.
func TestKeystoreBlobRoundTripAndPerms(t *testing.T) {
	home := withTempHome(t)
	if err := SaveKeystoreBlob("HRBK1-test-blob"); err != nil {
		t.Fatalf("SaveKeystoreBlob: %v", err)
	}
	if got, err := LoadKeystoreBlob(); err != nil || got != "HRBK1-test-blob" {
		t.Errorf("LoadKeystoreBlob = %q, %v", got, err)
	}

	dir := filepath.Join(home, ".config", "harbor")
	if info, err := os.Stat(filepath.Join(dir, "keystore.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("keystore.json stat = %v, %v; want mode 0600", info, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Errorf("config dir stat = %v, %v; want mode 0700", info, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "keystore.json" {
			t.Errorf("unexpected file %q left in the config dir", e.Name())
		}
	}
}

func TestBaseURLDefaulting(t *testing.T) {
	var nilCreds *Credentials
	if got := nilCreds.BaseURL(); got != DefaultBaseURL {
		t.Errorf("nil BaseURL = %q", got)
	}
	if got := (&Credentials{}).BaseURL(); got != DefaultBaseURL {
		t.Errorf("empty BaseURL = %q, want default", got)
	}
	if got := (&Credentials{APIURL: "http://x/api/v1"}).BaseURL(); got != "http://x/api/v1" {
		t.Errorf("set BaseURL = %q", got)
	}
}

func TestEffectiveClientID(t *testing.T) {
	if got := (&Credentials{}).EffectiveClientID(); got != DefaultClientID {
		t.Errorf("default client id = %q", got)
	}
	if got := (&Credentials{ClientID: "custom"}).EffectiveClientID(); got != "custom" {
		t.Errorf("custom client id = %q", got)
	}
}

func TestIsExpired(t *testing.T) {
	if !(&Credentials{ExpiresAt: 0}).IsExpired(0) {
		t.Error("zero ExpiresAt should be expired")
	}
	past := time.Now().Add(-time.Hour).UnixMilli()
	if !(&Credentials{ExpiresAt: past}).IsExpired(0) {
		t.Error("past expiry should be expired")
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	if (&Credentials{ExpiresAt: future}).IsExpired(0) {
		t.Error("future expiry should not be expired")
	}
	// With a large skew, a near-future token is treated as expired.
	soon := time.Now().Add(30 * time.Second).UnixMilli()
	if !(&Credentials{ExpiresAt: soon}).IsExpired(60 * time.Second) {
		t.Error("token within skew window should be expired")
	}
}

// contains is a tiny substring helper to avoid importing strings here.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

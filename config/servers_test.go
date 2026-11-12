// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-11-12

package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestServerNameRoundTripKeyedByBaseURL proves each API base URL keeps its own
// name, that a trailing slash maps to the same entry, and that the file is
// written 0600 like keystore.json.
func TestServerNameRoundTripKeyedByBaseURL(t *testing.T) {
	home := withTempHome(t)
	const staging = "https://staging.example.com/api/v1"
	const local = "http://localhost:8472/api/v1"

	if err := SaveServerName(staging+"/", "app-harbor-my-02"); err != nil {
		t.Fatalf("SaveServerName: %v", err)
	}
	if err := SaveServerName(local, "laptop"); err != nil {
		t.Fatalf("SaveServerName: %v", err)
	}

	for base, want := range map[string]string{staging: "app-harbor-my-02", local: "laptop", "https://other.example.com/api/v1": ""} {
		got, err := LoadServerName(base)
		if err != nil || got != want {
			t.Errorf("LoadServerName(%q) = %q, %v; want %q", base, got, err, want)
		}
	}

	info, err := os.Stat(filepath.Join(home, ".config", "harbor", "servers.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
}

// TestSaveEmptyServerNameRemovesOnlyThatEntry is the 421 path: clearing one
// API's name leaves every other API's name in place.
func TestSaveEmptyServerNameRemovesOnlyThatEntry(t *testing.T) {
	withTempHome(t)
	_ = SaveServerName("https://a.example.com/api/v1", "app-a")
	_ = SaveServerName("https://b.example.com/api/v1", "app-b")

	if err := SaveServerName("https://a.example.com/api/v1", ""); err != nil {
		t.Fatalf("SaveServerName: %v", err)
	}
	if got, _ := LoadServerName("https://a.example.com/api/v1"); got != "" {
		t.Errorf("a = %q, want it removed", got)
	}
	if got, _ := LoadServerName("https://b.example.com/api/v1"); got != "app-b" {
		t.Errorf("b = %q, want app-b kept", got)
	}
}

// TestLoadServerNameMissingFile proves a device that never saw a name reads as
// "no name", not an error.
func TestLoadServerNameMissingFile(t *testing.T) {
	withTempHome(t)
	got, err := LoadServerName("https://a.example.com/api/v1")
	if err != nil || got != "" {
		t.Errorf("LoadServerName = %q, %v; want \"\", nil", got, err)
	}
}

// TestDamagedServerNamesFileIsReplaced proves a file that does not parse is an
// error to read, and that the next save writes a good file over it.
func TestDamagedServerNamesFileIsReplaced(t *testing.T) {
	home := withTempHome(t)
	dir := filepath.Join(home, ".config", "harbor")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "servers.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadServerName("https://a.example.com/api/v1"); err == nil {
		t.Error("LoadServerName on a damaged file returned no error")
	}
	if err := SaveServerName("https://a.example.com/api/v1", "app-a"); err != nil {
		t.Fatalf("SaveServerName: %v", err)
	}
	if got, err := LoadServerName("https://a.example.com/api/v1"); err != nil || got != "app-a" {
		t.Errorf("after the save: %q, %v; want app-a", got, err)
	}
}

// TestClearServerNamesIsIdempotent proves logout removes the file, and that a
// second logout with no file is not an error.
func TestClearServerNamesIsIdempotent(t *testing.T) {
	home := withTempHome(t)
	_ = SaveServerName("https://a.example.com/api/v1", "app-a")

	for i := 0; i < 2; i++ {
		if err := ClearServerNames(); err != nil {
			t.Fatalf("ClearServerNames #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "harbor", "servers.json")); !os.IsNotExist(err) {
		t.Errorf("servers.json still exists after ClearServerNames (stat err %v)", err)
	}
}

// TestSaveServerNameConcurrent saves many APIs' names at once and checks none
// is lost, which needs the read-modify-write to be guarded.
func TestSaveServerNameConcurrent(t *testing.T) {
	withTempHome(t)
	bases := []string{"https://a.example.com", "https://b.example.com", "https://c.example.com", "https://d.example.com"}

	var wg sync.WaitGroup
	for _, base := range bases {
		wg.Add(1)
		go func(base string) {
			defer wg.Done()
			if err := SaveServerName(base, "app-1"); err != nil {
				t.Errorf("SaveServerName(%q): %v", base, err)
			}
		}(base)
	}
	wg.Wait()

	for _, base := range bases {
		if got, _ := LoadServerName(base); got != "app-1" {
			t.Errorf("%s = %q, want app-1 (an entry was lost)", base, got)
		}
	}
}

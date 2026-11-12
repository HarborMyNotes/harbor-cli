// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-11-12

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// serverNamesFile holds the X-Harbor-Server name last seen for each API base
// URL. Every command is a new process, so the name must be on disk for the
// first request of the next command to carry it. It is written under
// HARBOR_TOKEN too: it holds no secret.
const serverNamesFile = "servers.json"

// serverNamesMu serializes the read-modify-write of the file, so two requests
// saving at once cannot drop each other's entry.
var serverNamesMu sync.Mutex

// serverNamesDoc is the file's JSON shape: base URL → server name.
type serverNamesDoc struct {
	Servers map[string]string `json:"servers"`
}

// serverNamesPath returns the full path to the server-names file.
func serverNamesPath() (string, error) {
	dir, err := ConfigDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, serverNamesFile), nil
}

// serverNameKey is the file's key for a base URL. A trailing slash is dropped,
// so the same API always maps to one entry.
func serverNameKey(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}

// readServerNames reads the file. A missing file is an empty map, not an
// error. The caller holds serverNamesMu.
func readServerNames() (map[string]string, error) {
	path, err := serverNamesPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("unable to read %s: %w", serverNamesFile, err)
	}
	var doc serverNamesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unable to parse %s: %w", serverNamesFile, err)
	}
	if doc.Servers == nil {
		doc.Servers = map[string]string{}
	}
	return doc.Servers, nil
}

// LoadServerName returns the saved server name for an API base URL, or "" (no
// error) when none is saved.
func LoadServerName(baseURL string) (string, error) {
	serverNamesMu.Lock()
	defer serverNamesMu.Unlock()
	names, err := readServerNames()
	if err != nil {
		return "", err
	}
	return names[serverNameKey(baseURL)], nil
}

// SaveServerName records the server name for an API base URL; an empty name
// removes the entry. Other base URLs' entries are kept. A file that cannot be
// parsed is replaced, since it only holds routing hints the next response
// gives back.
func SaveServerName(baseURL, name string) error {
	serverNamesMu.Lock()
	defer serverNamesMu.Unlock()
	names, err := readServerNames()
	if err != nil {
		names = map[string]string{}
	}
	key := serverNameKey(baseURL)
	if names[key] == name {
		return nil
	}
	if name == "" {
		delete(names, key)
	} else {
		names[key] = name
	}
	data, err := json.MarshalIndent(serverNamesDoc{Servers: names}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(serverNamesFile, data)
}

// ClearServerNames removes the server-names file. A missing file is not an
// error, so logout stays idempotent.
func ClearServerNames() error {
	serverNamesMu.Lock()
	defer serverNamesMu.Unlock()
	path, err := serverNamesPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unable to remove %s: %w", serverNamesFile, err)
	}
	return nil
}

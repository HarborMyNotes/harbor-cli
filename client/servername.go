// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-11-12

package client

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// serverHeader names the app server that answered a request. The client sends
// the last name back on every request so a proxy can route it straight to that
// server instead of looking the user up.
const serverHeader = "X-Harbor-Server"

// maxServerNameLen is the longest server name the client accepts.
const maxServerNameLen = 64

// maxRedirects is Go's default redirect limit, which checkRedirect keeps.
const maxRedirects = 10

// serverNames caches one server name per API base URL for the whole process.
// It is package-level, not on Client, because sign-in, whoami and the
// upload-limits probe each build their own short-lived client, and every one
// must send the same name. load and save make the cache outlive the process;
// with neither set it lives in memory only.
var serverNames = struct {
	sync.Mutex

	// byBase maps a base URL to its name. A key that is present means the store
	// was already read for it; an empty value means there is no name.
	byBase map[string]string

	load func(baseURL string) (string, error)
	save func(baseURL, name string) error
}{byBase: map[string]string{}}

// SetServerNameStore makes server names survive between commands, so the first
// request of a new command carries the name. load returns the saved name for a
// base URL ("" when there is none); save records one, and "" removes it. It
// also drops the in-memory cache, so the next request reads the store.
func SetServerNameStore(load func(baseURL string) (string, error), save func(baseURL, name string) error) {
	serverNames.Lock()
	defer serverNames.Unlock()
	serverNames.load = load
	serverNames.save = save
	serverNames.byBase = map[string]string{}
}

// ForgetServerNames drops the in-memory names, so the next request reads the
// store again. Logout calls it after it removes the saved file.
func ForgetServerNames() {
	serverNames.Lock()
	defer serverNames.Unlock()
	serverNames.byBase = map[string]string{}
}

// serverKey is the cache key for this client: its base URL without a trailing
// slash, so "…/api/v1" and "…/api/v1/" share one name.
func (c *Client) serverKey() string {
	return strings.TrimRight(c.BaseURL, "/")
}

// serverName returns the cached name for this client's base URL, reading the
// store the first time this process asks. A store that cannot be read counts as
// no name: the request then goes without the header, which always works.
func (c *Client) serverName() string {
	key := c.serverKey()
	serverNames.Lock()
	defer serverNames.Unlock()
	if name, ok := serverNames.byBase[key]; ok {
		return name
	}
	name := ""
	if serverNames.load != nil {
		if saved, err := serverNames.load(key); err == nil && validServerName(saved) {
			name = saved
		}
	}
	serverNames.byBase[key] = name
	return name
}

// setServerName caches name for this client's base URL and saves it; "" forgets
// it. An unchanged name writes nothing. A failed save is ignored: the name is a
// routing hint, and the request it came from has already succeeded or failed on
// its own.
func (c *Client) setServerName(name string) {
	key := c.serverKey()
	serverNames.Lock()
	defer serverNames.Unlock()
	if cur, ok := serverNames.byBase[key]; ok && cur == name {
		return
	}
	serverNames.byBase[key] = name
	if serverNames.save != nil {
		_ = serverNames.save(key, name)
	}
}

// noteServerName updates the cache from an API response of any status. A 421
// means the name was wrong, so it is forgotten and the next request goes
// without it; the 421's own header names a server that does not hold this
// user, so it is not learned. Otherwise a valid header replaces the cached
// name, and a missing or invalid one changes nothing. A response that came from
// another host after a redirect is ignored.
func (c *Client) noteServerName(resp *http.Response) {
	if resp == nil || (resp.Request != nil && !c.onAPIOrigin(resp.Request.URL)) {
		return
	}
	if resp.StatusCode == http.StatusMisdirectedRequest {
		c.setServerName("")
		return
	}
	if name := resp.Header.Get(serverHeader); validServerName(name) {
		c.setServerName(name)
	}
}

// validServerName reports whether s is a server name the client will cache and
// send: 1 to 64 characters, each a letter, a digit, '.', '_' or '-'.
func validServerName(s string) bool {
	if s == "" || len(s) > maxServerNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			ch == '.' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// onAPIOrigin reports whether u is on the same scheme and host as the API base
// URL. The server name is for the Harbor API only, never for storage or any
// other host.
func (c *Client) onAPIOrigin(u *url.URL) bool {
	base, err := url.Parse(c.BaseURL)
	if err != nil || u == nil {
		return false
	}
	return strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host)
}

// checkRedirect is the API client's redirect policy. Go copies custom headers
// onto a redirect to any host, so it removes the server name from a hop that
// leaves the API origin. It keeps Go's default limit of 10 redirects.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("stopped after 10 redirects")
	}
	if !c.onAPIOrigin(req.URL) {
		req.Header.Del(serverHeader)
	}
	return nil
}

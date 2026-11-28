// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package cmd

import (
	"strings"
	"testing"
)

// TestDisplayStacks covers the table, with the META column only when a stack
// has metadata.
func TestDisplayStacks(t *testing.T) {
	plain := []byte(`{"data":[{"name":"Archive","notebook_count":1,"metadata":{}},{"name":"Empty","notebook_count":0,"metadata":{}}],` +
		`"paging":{"limit":2,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayStacks(plain) })
	if !strings.Contains(out, "Archive") || !strings.Contains(out, "Empty") {
		t.Errorf("missing stacks:\n%s", out)
	}
	if strings.Contains(out, "META") {
		t.Errorf("META column shown with no metadata:\n%s", out)
	}

	labelled := []byte(`{"data":[{"name":"Projects","notebook_count":3,"metadata":{"client":"acme"}}],"paging":{"total":1}}`)
	out = captureStdout(t, func() { displayStacks(labelled) })
	if !strings.Contains(out, "META") || !strings.Contains(out, "client=acme") {
		t.Errorf("META column missing:\n%s", out)
	}
}

// TestStacksListForwardsFilters pins the query `stacks list` sends.
func TestStacksListForwardsFilters(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/stacks": {Status: 200, Body: `{"data":[],"paging":{"total":0}}`},
	})
	if _, err := runCLI(t, m, "stacks", "list", "--meta-eq", "client=acme", "--meta-has", "billable", "--order", "-notebook_count"); err != nil {
		t.Fatalf("stacks list: %v", err)
	}
	q := m.queryOf(t, "GET /api/v1/stacks")
	if q.Get("meta.client") != "acme" || q.Get("meta_has") != "billable" || q.Get("order") != "-notebook_count" {
		t.Errorf("query = %v", q)
	}
	if _, ok := q["limit"]; ok {
		t.Errorf("an unset --limit was sent, which would page a list whose default is every stack: %v", q)
	}
}

// TestStacksMetaAddressesTheStackByName covers a name with a space, which must
// reach the server as one path segment.
func TestStacksMetaAddressesTheStackByName(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/stacks/Client Work/metadata": {Status: 200, Body: `{"metadata":{"client":"acme"}}`},
	})
	out, err := runCLI(t, m, "stacks", "meta", "Client Work", "--set", "client=acme")
	if err != nil {
		t.Fatalf("stacks meta: %v", err)
	}
	if got := m.rawBodyOf(t, "PATCH /api/v1/stacks/Client Work/metadata"); got != `{"client":"acme"}` {
		t.Errorf("body = %s", got)
	}
	if !strings.Contains(out, "acme") {
		t.Errorf("output:\n%s", out)
	}
}

// TestStacksMetaUnknownStack covers the friendly 404.
func TestStacksMetaUnknownStack(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/stacks/Nope/metadata": {Status: 404, Body: apiErrorBody("stack_not_found", "Stack not found.")},
	})
	_, err := runCLI(t, m, "stacks", "meta", "Nope")
	if err == nil || !strings.Contains(err.Error(), "harbor stacks list") {
		t.Errorf("err = %v", err)
	}
}

// TestDisplayStacksShowsColorName covers the COLOR column and the detail view
// `stacks update` prints.
func TestDisplayStacksShowsColorName(t *testing.T) {
	out := captureStdout(t, func() {
		displayStacks([]byte(`{"data":[{"name":"Projects","notebook_count":3,"color":"#22c55e","metadata":{}},{"name":"Plain","notebook_count":1,"color":"","metadata":{}}],"paging":{"total":2}}`))
	})
	if !strings.Contains(out, "COLOR") || !strings.Contains(out, "green") {
		t.Errorf("color column missing:\n%s", out)
	}
	out = captureStdout(t, func() {
		displayStack([]byte(`{"name":"Projects","notebook_count":3,"color":"#f97316","metadata":{"client":"acme"}}`))
	})
	for _, want := range []string{"Projects", "orange", "3", "client=acme"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q:\n%s", want, out)
		}
	}
}

// TestStacksUpdateSendsColor covers set and clear on a name with a space and a
// "?", which must reach the server as one path segment rather than start a
// query string.
func TestStacksUpdateSendsColor(t *testing.T) {
	for in, want := range map[string]string{
		"green": `{"color":"#22c55e"}`,
		"none":  `{"color":""}`,
	} {
		m := newAPIMock(t, map[string]mockReply{
			"PATCH /api/v1/stacks/Client Work?": {Status: 200, Body: `{"name":"Client Work?","notebook_count":2,"color":"#22c55e","metadata":{}}`},
		})
		out, err := runCLI(t, m, "stacks", "update", "Client Work?", "--color", in)
		if err != nil {
			t.Fatalf("stacks update --color %s: %v", in, err)
		}
		if got := m.rawBodyOf(t, "PATCH /api/v1/stacks/Client Work?"); got != want {
			t.Errorf("--color %s: body = %s, want %s", in, got, want)
		}
		if !strings.Contains(out, "Client Work") {
			t.Errorf("output:\n%s", out)
		}
	}
}

// TestStacksUpdateNeedsColor covers a missing or bad --color: no request is sent.
func TestStacksUpdateNeedsColor(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"stacks", "update", "Projects"}, "--color"},
		{[]string{"stacks", "update", "Projects", "--color", "chartreuse"}, "harbor-blue"},
	} {
		m := newAPIMock(t, map[string]mockReply{})
		_, err := runCLI(t, m, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v", tc.args, err)
		}
		if calls := m.calls(); len(calls) != 0 {
			t.Errorf("%v: sent %v", tc.args, calls)
		}
	}
}

// TestStacksUpdateUnknownStack covers the friendly 404.
func TestStacksUpdateUnknownStack(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"PATCH /api/v1/stacks/Nope": {Status: 404, Body: apiErrorBody("stack_not_found", "Stack not found.")},
	})
	_, err := runCLI(t, m, "stacks", "update", "Nope", "--color", "red")
	if err == nil || !strings.Contains(err.Error(), "harbor stacks list") {
		t.Errorf("err = %v", err)
	}
}

// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

func TestDisplayNotebooks(t *testing.T) {
	data := []byte(`{"data":[
		{"id":"nb1","name":"Work","stack":"Projects","is_default":false,"default_encrypt":false,"is_public":false,"usn":42,"updated_at":1750000000000},
		{"id":"nb2","name":"Inbox","is_default":true,"usn":1,"updated_at":1750000000000}
	],"paging":{"limit":100,"offset":0,"total":2,"has_more":false}}`)
	out := captureStdout(t, func() { displayNotebooks(data) })
	if !strings.Contains(out, "Work") || !strings.Contains(out, "Inbox") {
		t.Errorf("missing notebook names:\n%s", out)
	}
	if !strings.Contains(out, "★") {
		t.Errorf("default notebook star missing:\n%s", out)
	}
	if !strings.Contains(out, "showing 1–2 of 2") {
		t.Errorf("paging footer missing:\n%s", out)
	}
}

func TestDisplayNotebookDetail(t *testing.T) {
	data := []byte(`{"id":"nb1","name":"Work","stack":"Projects","is_default":false,"usn":42,"updated_at":1750000000000,"created_at":1749000000000}`)
	out := captureStdout(t, func() { displayNotebook(data) })
	if !strings.Contains(out, "nb1") || !strings.Contains(out, "Work") {
		t.Errorf("detail view missing fields:\n%s", out)
	}
}

func TestMapNotebookError(t *testing.T) {
	cases := map[string]string{
		"notebook_name_exists":  "already exists",
		"cannot_delete_default": "cannot be deleted",
		"cannot_unset_default":  "always be a default",
	}
	for code, sub := range cases {
		got := mapNotebookError(apiErr(code))
		if !strings.Contains(got.Error(), sub) {
			t.Errorf("mapNotebookError(%s) = %q, want substring %q", code, got.Error(), sub)
		}
	}
}

// nbGuardServer stands in for the API when the guard needs to read a notebook's
// current state. It records whether the GET happened, so the tests can prove the
// no-fetch cases really do not spend a request.
func nbGuardServer(t *testing.T, isDefault, defaultEncrypt bool, fetched *int) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*fetched++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"nb1","name":"Work","is_default":%t,"default_encrypt":%t}`, isDefault, defaultEncrypt)
	}))
	t.Cleanup(srv.Close)
	return client.NewClient(srv.URL, "at_test")
}

// TestGuardRefusesBothDirections pins the rule in both directions: turning
// encryption on for the current default, and promoting a notebook that already
// encrypts. Either way the CLI refuses before spending a request on a 422.
func TestGuardRefusesBothDirections(t *testing.T) {
	cases := []struct {
		name           string
		isDefault      bool
		defaultEncrypt bool
		body           map[string]any
		wantRefused    bool
	}{
		{"encrypt-on for the current default", true, false,
			map[string]any{"default_encrypt": true}, true},
		{"promote a notebook that encrypts", false, true,
			map[string]any{"is_default": true}, true},
		{"encrypt-on for a non-default notebook", false, false,
			map[string]any{"default_encrypt": true}, false},
		{"promote a notebook that does not encrypt", false, false,
			map[string]any{"is_default": true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetched := 0
			c := nbGuardServer(t, tc.isDefault, tc.defaultEncrypt, &fetched)
			err := guardDefaultNotebookEncrypt(c, "nb1", tc.body)
			if tc.wantRefused && err == nil {
				t.Fatal("the banned pair was allowed through")
			}
			if !tc.wantRefused && err != nil {
				t.Fatalf("a legal update was refused: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "encrypt a different notebook instead") {
				t.Errorf("refusal is not the server's wording: %v", err)
			}
		})
	}
}

// TestGuardJudgesTheResultingState is the case the issue calls out explicitly:
// one request carrying BOTH fields legally promotes a notebook while switching
// encryption off. Judging on "does the request mention both flags" would block
// the single command that fixes an encrypting notebook someone wants as default.
func TestGuardJudgesTheResultingState(t *testing.T) {
	fetched := 0
	c := nbGuardServer(t, false, true, &fetched)

	// Promote while switching encryption OFF — legal, and decided with no fetch.
	if err := guardDefaultNotebookEncrypt(c, "nb1", map[string]any{"is_default": true, "default_encrypt": false}); err != nil {
		t.Fatalf("promoting while turning encryption off was refused: %v", err)
	}
	if fetched != 0 {
		t.Errorf("the resulting state was fully stated, but the guard still fetched %d time(s)", fetched)
	}

	// Both ON in one request — refused, also with no fetch.
	if err := guardDefaultNotebookEncrypt(c, "nb1", map[string]any{"is_default": true, "default_encrypt": true}); err == nil {
		t.Fatal("setting both flags true in one request was allowed")
	}
	if fetched != 0 {
		t.Errorf("a request stating both fields needs no round trip, but the guard fetched %d time(s)", fetched)
	}
}

// TestGuardDoesNotFetchWhenIrrelevant proves the guard costs nothing on the
// updates that cannot produce the pair — renames, stack moves, and any request
// that turns encryption off.
func TestGuardDoesNotFetchWhenIrrelevant(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"rename only":     {"name": "Work — Active"},
		"encryption off":  {"default_encrypt": false},
		"off and renamed": {"default_encrypt": false, "name": "x"},
	} {
		fetched := 0
		c := nbGuardServer(t, true, true, &fetched)
		if err := guardDefaultNotebookEncrypt(c, "nb1", body); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
		if fetched != 0 {
			t.Errorf("%s should need no round trip, but the guard fetched %d time(s)", name, fetched)
		}
	}
}

// TestGuardFailsOpenOnAnUnreadableNotebook proves a transient read error does not
// become a refusal to write. The server enforces the same rule, so letting the
// write through costs a 422 at worst; refusing here would block a legal update
// because an unrelated GET failed.
func TestGuardFailsOpenOnAnUnreadableNotebook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := guardDefaultNotebookEncrypt(client.NewClient(srv.URL, "at_test"), "nb1", map[string]any{"default_encrypt": true}); err != nil {
		t.Fatalf("an unreadable notebook should not block the write: %v", err)
	}
}

// TestDefaultCannotEncryptServerMessageIsNotParaphrased pins that the server's
// 422 reaches the user in the server's own words.
//
// The message is user-facing copy shared with the web app, and the issue asks for
// it verbatim. mapNotebookError paraphrases the other notebook codes, so the risk
// is somebody "helpfully" adding a case for this one; the CLI's default renderer
// already prints an APIError's message, so the correct handling is to leave it
// alone. This fails if a mapping is ever added.
func TestDefaultCannotEncryptServerMessageIsNotParaphrased(t *testing.T) {
	const serverMessage = "The default notebook can't encrypt notes by default — forwarded email, imports, and notes with no notebook land there; encrypt a different notebook instead."
	apiError := &client.APIError{Code: "default_notebook_cannot_encrypt", Message: serverMessage, Status: 422}

	got := mapNotebookError(apiError)
	if got.Error() != apiError.Error() {
		t.Fatalf("the server's message was rewritten locally:\n got: %s\nwant: %s", got.Error(), apiError.Error())
	}
	var still *client.APIError
	if !errors.As(got, &still) {
		t.Fatal("the APIError was replaced, so the renderer can no longer print its message and details")
	}
}

// TestLocalRefusalMatchesTheServerWording keeps the refusal the CLI raises on its
// own in step with the sentence the server would have sent. Two wordings for one
// rule is how a user learns to distrust one of them.
func TestLocalRefusalMatchesTheServerWording(t *testing.T) {
	const serverMessage = "The default notebook can't encrypt notes by default — forwarded email, imports, and notes with no notebook land there; encrypt a different notebook instead."
	normalized := strings.TrimSuffix(strings.ToLower(serverMessage), ".")
	if defaultCannotEncryptMessage != normalized {
		t.Errorf("the local refusal has drifted from the server's copy:\n local: %s\nserver: %s", defaultCannotEncryptMessage, normalized)
	}
}

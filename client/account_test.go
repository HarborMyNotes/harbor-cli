// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestStartAccountExport verifies the export start posts to /account/export.
func TestStartAccountExport(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 202, `{"data":{"export_job_id":"e1","status":"queued"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).StartAccountExport("", ""); err != nil {
		t.Fatalf("StartAccountExport error: %v", err)
	}
	if rec.Method != "POST" {
		t.Errorf("method = %s", rec.Method)
	}
	if rec.Path != "/account/export" {
		t.Errorf("path = %s", rec.Path)
	}
	if rec.Auth != "Bearer at_test_token" {
		t.Errorf("auth = %q", rec.Auth)
	}
}

// TestStartAccountExportOmitsUnsetFields pins that an unscoped, unformatted start
// sends an EMPTY body. Sending format:"" would fail the server's enum check, and
// hardcoding "enex" here would silently pin a default that belongs to the server.
func TestStartAccountExportOmitsUnsetFields(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 202, `{"data":{"export_job_id":"e1","status":"queued"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).StartAccountExport("", ""); err != nil {
		t.Fatalf("StartAccountExport error: %v", err)
	}
	if body := string(rec.Body); body != "{}" {
		t.Errorf("body = %s, want {}", body)
	}
}

// TestStartAccountExportScoped verifies the format and notebook scope ride in the
// body under the names the API expects.
func TestStartAccountExportScoped(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 202, `{"data":{"export_job_id":"e1","status":"queued","format":"html","notebook_id":"nb1","notebook_name":"Recipes"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).StartAccountExport("html", "nb1"); err != nil {
		t.Fatalf("StartAccountExport error: %v", err)
	}
	if !containsAll(string(rec.Body), `"format"`, `"html"`, `"notebook_id"`, `"nb1"`) {
		t.Errorf("body missing scope fields: %s", rec.Body)
	}
}

// TestListAccountExports verifies the slot listing hits GET /account/export with
// no id — the endpoint a client uses to find an export it did not start itself.
func TestListAccountExports(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":[{"id":"e1","format":"enex","status":"completed"}]}`)
	defer srv.Close()
	data, err := testClient(srv.URL).ListAccountExports()
	if err != nil {
		t.Fatalf("ListAccountExports error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/account/export" {
		t.Errorf("request = %s %s, want GET /account/export", rec.Method, rec.Path)
	}
	if len(CollectionItems(data)) != 1 {
		t.Errorf("expected one slot row, got %d", len(CollectionItems(data)))
	}
}

// TestDeleteAccountExport verifies the delete hits DELETE /account/export/:id.
func TestDeleteAccountExport(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"id":"e1","status":"deleted"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).DeleteAccountExport("e1"); err != nil {
		t.Fatalf("DeleteAccountExport error: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/account/export/e1" {
		t.Errorf("request = %s %s, want DELETE /account/export/e1", rec.Method, rec.Path)
	}
}

// TestStartAccountExportConflictDetails pins that the 409 refusal's details
// survive decoding into the typed error. They are what lets the CLI say WHICH
// export is in the way — there is no list endpoint on the refusal path, so a
// client that dropped them would have to make a second request to say anything
// useful.
func TestStartAccountExportConflictDetails(t *testing.T) {
	body := `{"error":{"code":"export_exists","message":"You already have an export.","details":{` +
		`"export_job_id":"e1","format":"enex","scope":"notebook","notebook_id":"nb1",` +
		`"notebook_name":"Recipes","result_expires_at":"1750003600000"}}}`
	srv := newTestServer(t, nil, 409, body)
	defer srv.Close()

	_, err := testClient(srv.URL).StartAccountExport("enex", "nb1")
	if err == nil {
		t.Fatal("expected a 409 error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "export_exists" || apiErr.Status != 409 {
		t.Errorf("code/status = %s/%d", apiErr.Code, apiErr.Status)
	}
	for _, key := range []string{"export_job_id", "format", "scope", "notebook_name", "result_expires_at"} {
		if _, ok := apiErr.Details[key]; !ok {
			t.Errorf("details missing %q: %v", key, apiErr.Details)
		}
	}
}

// TestGetAccountExport verifies the poll hits GET /account/export/:id.
func TestGetAccountExport(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"id":"e1","status":"completed","download_url":"https://s3/x"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).GetAccountExport("e1"); err != nil {
		t.Fatalf("GetAccountExport error: %v", err)
	}
	if rec.Method != "GET" {
		t.Errorf("method = %s", rec.Method)
	}
	if rec.Path != "/account/export/e1" {
		t.Errorf("path = %s", rec.Path)
	}
}

// TestRequestAccountDeletion verifies the delete request posts both the password
// (as current_password) and the confirmation phrase (as confirm).
func TestRequestAccountDeletion(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"status":"scheduled","purge_after":1752592000000,"grace_days":30}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).RequestAccountDeletion("hunter2", "DELETE MY ACCOUNT"); err != nil {
		t.Fatalf("RequestAccountDeletion error: %v", err)
	}
	if rec.Method != "POST" {
		t.Errorf("method = %s", rec.Method)
	}
	if rec.Path != "/account/delete" {
		t.Errorf("path = %s", rec.Path)
	}
	body := string(rec.Body)
	if !containsAll(body, `"current_password"`, "hunter2", `"confirm"`, "DELETE MY ACCOUNT") {
		t.Errorf("body missing fields: %s", body)
	}
}

// TestCancelAccountDeletion verifies cancel posts the password to the cancel path.
func TestCancelAccountDeletion(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"status":"active"}}`)
	defer srv.Close()
	if _, err := testClient(srv.URL).CancelAccountDeletion("hunter2"); err != nil {
		t.Fatalf("CancelAccountDeletion error: %v", err)
	}
	if rec.Method != "POST" {
		t.Errorf("method = %s", rec.Method)
	}
	if rec.Path != "/account/delete/cancel" {
		t.Errorf("path = %s", rec.Path)
	}
	body := string(rec.Body)
	if !strings.Contains(body, "hunter2") || strings.Contains(body, "confirm") {
		t.Errorf("cancel body should carry only current_password: %s", body)
	}
}

// TestRequestAccountClear pins the endpoint and the two fields the server reads.
func TestRequestAccountClear(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 202, `{"data":{"clear_job_id":"c1","status":"queued","started_at":1750000000000}}`)
	defer srv.Close()

	if _, err := testClient(srv.URL).RequestAccountClear("pw", "CLEAR MY ACCOUNT"); err != nil {
		t.Fatalf("RequestAccountClear error: %v", err)
	}

	if rec.Method != "POST" || rec.Path != "/account/clear" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["current_password"] != "pw" {
		t.Errorf("current_password = %v", body["current_password"])
	}
	if body["confirm"] != "CLEAR MY ACCOUNT" {
		t.Errorf("confirm = %v", body["confirm"])
	}
}

// TestRequestAccountClearSendsThePhraseUntouched keeps the client out of the
// comparison. The server matches byte for byte, so normalising here would turn
// a phrase it rejects into one it accepts — and the user would be told a phrase
// they never typed did not match.
func TestRequestAccountClearSendsThePhraseUntouched(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 202, `{"data":{}}`)
	defer srv.Close()

	const typed = "  clear my account  "
	if _, err := testClient(srv.URL).RequestAccountClear("pw", typed); err != nil {
		t.Fatalf("RequestAccountClear error: %v", err)
	}

	var body map[string]any
	_ = json.Unmarshal(rec.Body, &body)
	if body["confirm"] != typed {
		t.Errorf("confirm = %q, want it sent exactly as typed", body["confirm"])
	}
}

// TestGetAccountClear pins the polling endpoint, and that a never-cleared
// account's 404 arrives as a typed error the caller can recognise rather than
// something it has to sniff out of a message.
func TestGetAccountClear(t *testing.T) {
	var rec recordedRequest
	srv := newTestServer(t, &rec, 200, `{"data":{"clear_job_id":"c1","status":"completed","started_at":1,"finished_at":2}}`)
	defer srv.Close()

	if _, err := testClient(srv.URL).GetAccountClear(); err != nil {
		t.Fatalf("GetAccountClear error: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/account/clear" {
		t.Errorf("%s %s", rec.Method, rec.Path)
	}

	missing := newTestServer(t, nil, 404, `{"error":{"code":"not_found","message":"nope"}}`)
	defer missing.Close()

	_, err := testClient(missing.URL).GetAccountClear()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Errorf("err = %v, want an APIError with code not_found", err)
	}
}

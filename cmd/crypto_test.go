// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/HarborMyNotes/harbor-cli/crypto"
	"github.com/spf13/cobra"
)

// testParams is a cheap Argon2id profile so the wiring tests stay fast.
var testParams = crypto.Argon2Params{MemKiB: 8192, Iterations: 1, Parallelism: 1}

// resetSession clears the memoized per-process encryption session so each test
// re-derives from its own fixture.
func resetSession() {
	sessionUnlockd = false
	sessionKey = nil
	sessionErr = nil
	decryptWarned = false
	encryptCheckWarned = false
}

// setupEncryption isolates HOME, writes a cached keystore, and sets the
// passphrase env, returning the master key for building ciphertext fixtures. With
// the keystore cached locally, the unlock path needs no network/client.
func setupEncryption(t *testing.T, pass string) []byte {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	resetSession()
	blob, key, err := crypto.NewKeystore(pass, testParams)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	if err := config.SaveKeystoreBlob(blob); err != nil {
		t.Fatalf("SaveKeystoreBlob: %v", err)
	}
	t.Setenv("HARBOR_PASSPHRASE", pass)
	return key
}

// TestDecryptResult_MutationEnvelope proves a {note:{…}} response is decrypted in
// place (the shape get/create/update return).
func TestDecryptResult_MutationEnvelope(t *testing.T) {
	key := setupEncryption(t, "pw")
	id := "11111111-2222-3333-4444-555555555555"
	encTitle, _ := crypto.SealField(key, id, "title", "Secret Title")
	encBody, _ := crypto.SealField(key, id, "content", "Secret Body")
	in, _ := json.Marshal(map[string]any{
		"note": map[string]any{"id": id, "is_encrypted": true, "title": encTitle, "content": encBody},
		"usn":  5,
	})
	out := string(decryptResult(nil, &config.Credentials{}, in))
	if !strings.Contains(out, "Secret Title") || !strings.Contains(out, "Secret Body") {
		t.Fatalf("expected decrypted fields, got: %s", out)
	}
	if strings.Contains(out, "HRBC2.") {
		t.Fatalf("ciphertext still present: %s", out)
	}
}

// TestDecryptResult_Collection proves a {data:[…]} list is walked and each
// encrypted note decrypted, while a plaintext note is left alone.
func TestDecryptResult_Collection(t *testing.T) {
	key := setupEncryption(t, "pw")
	id := "22222222-3333-4444-5555-666666666666"
	encTitle, _ := crypto.SealField(key, id, "title", "Encrypted One")
	in, _ := json.Marshal(map[string]any{
		"data": []any{
			map[string]any{"id": id, "is_encrypted": true, "title": encTitle},
			map[string]any{"id": "plain", "is_encrypted": false, "title": "Plain Note"},
		},
		"paging": map[string]any{"total": 2},
	})
	out := string(decryptResult(nil, &config.Credentials{}, in))
	if !strings.Contains(out, "Encrypted One") || !strings.Contains(out, "Plain Note") {
		t.Fatalf("unexpected output: %s", out)
	}
}

// TestDecryptResult_NoPassphrasePassthrough proves that without HARBOR_PASSPHRASE
// the data is returned untouched (ciphertext shown).
func TestDecryptResult_NoPassphrasePassthrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetSession()
	in := []byte(`{"note":{"id":"x","is_encrypted":true,"title":"HRBC2.AAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAA"}}`)
	out := decryptResult(nil, &config.Credentials{}, in)
	if string(out) != string(in) {
		t.Fatalf("expected passthrough, got %s", out)
	}
}

// TestDecryptResult_WrongPassphraseFallsBack proves a wrong passphrase warns and
// shows ciphertext rather than failing the command.
func TestDecryptResult_WrongPassphraseFallsBack(t *testing.T) {
	key := setupEncryption(t, "right")
	id := "33333333-4444-5555-6666-777777777777"
	enc, _ := crypto.SealField(key, id, "content", "body")
	in, _ := json.Marshal(map[string]any{"id": id, "is_encrypted": true, "content": enc})

	t.Setenv("HARBOR_PASSPHRASE", "wrong")
	resetSession()
	out := string(decryptResult(nil, &config.Credentials{}, in))
	if !strings.Contains(out, "HRBC2.") {
		t.Fatalf("expected ciphertext fallback, got %s", out)
	}
}

// TestEncryptCreateBody proves create encryption seals both fields under a
// generated id, marks the note encrypted, drops content_format, and round-trips.
func TestEncryptCreateBody(t *testing.T) {
	key := setupEncryption(t, "pw")
	body := map[string]any{"title": "Hello", "content": "World", "content_format": "markdown"}
	if err := encryptCreateBody(nil, &config.Credentials{}, body); err != nil {
		t.Fatalf("encryptCreateBody: %v", err)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatal("expected a generated note id")
	}
	if enc, _ := body["is_encrypted"].(bool); !enc {
		t.Fatal("expected is_encrypted true")
	}
	if _, ok := body["content_format"]; ok {
		t.Fatal("content_format should be removed for an encrypted note")
	}
	title, _ := body["title"].(string)
	content, _ := body["content"].(string)
	if !crypto.IsEnvelope(title) || !crypto.IsEnvelope(content) {
		t.Fatalf("fields not sealed: title=%q content=%q", title, content)
	}
	if got, err := crypto.OpenField(key, id, "title", title); err != nil || got != "Hello" {
		t.Fatalf("title decrypt: %v %q", err, got)
	}
	if got, err := crypto.OpenField(key, id, "content", content); err != nil || got != "World" {
		t.Fatalf("content decrypt: %v %q", err, got)
	}
}

// TestEncryptCreateBody_EmptyTitleStaysEmpty proves an empty title is left empty
// (an encrypted note may have a non-envelope empty title) rather than sealed.
func TestEncryptCreateBody_EmptyTitleStaysEmpty(t *testing.T) {
	setupEncryption(t, "pw")
	body := map[string]any{"content": "body only"}
	if err := encryptCreateBody(nil, &config.Credentials{}, body); err != nil {
		t.Fatalf("encryptCreateBody: %v", err)
	}
	if _, ok := body["title"]; ok {
		t.Fatal("empty title should not be added/sealed")
	}
	if !crypto.IsEnvelope(body["content"].(string)) {
		t.Fatal("content should be sealed")
	}
}

// ===========================================================================
// The default notebook can be on any page (issue #67's sibling)
// ===========================================================================
//
// GET /notebooks is paged and carries no "just the default" filter, so which page
// the default notebook lands on is decided by its NAME. Reading only the first
// page answers "the default does not want encryption" for any account with more
// notebooks than fit in it — and the caller then writes the note in the clear.
// This is the same one-page assumption as the task guard, with a quieter failure:
// nothing refuses, nothing warns, the note is just not encrypted.

// notebookPageMock serves a notebooks list where the default notebook — the only
// one with default_encrypt set — sits on the SECOND page.
func notebookPageMock(t *testing.T) *apiMock {
	t.Helper()
	rows := make([]string, 0, collectionPageSize)
	for i := 0; i < collectionPageSize; i++ {
		rows = append(rows, fmt.Sprintf(`{"id":"nb%d","name":"Notebook %d","is_default":false,"default_encrypt":false}`, i, i))
	}
	first := fmt.Sprintf(`{"data":[%s],"paging":{"limit":500,"offset":0,"total":%d,"has_more":true}}`,
		strings.Join(rows, ","), collectionPageSize+1)
	second := fmt.Sprintf(`{"data":[{"id":"nbdefault","name":"Zed","is_default":true,"default_encrypt":true}],`+
		`"paging":{"limit":500,"offset":500,"total":%d,"has_more":false}}`, collectionPageSize+1)

	m := newAPIMock(t, map[string]mockReply{})
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "500" {
			_, _ = w.Write([]byte(second))
			return
		}
		_, _ = w.Write([]byte(first))
	}
	return m
}

// TestNotebookWantsEncryptionFindsADefaultPastTheFirstPage proves the lookup walks
// the pages. Against a single-page read this returns false and the note is written
// unencrypted, against the user's own stated default.
func TestNotebookWantsEncryptionFindsADefaultPastTheFirstPage(t *testing.T) {
	m := notebookPageMock(t)

	wants, known, name := notebookWantsEncryption(client.NewClient(m.baseURL(), "tok"), "")

	if !known {
		t.Fatal("the walk reached the default notebook but reported the answer as unknown")
	}
	if !wants {
		t.Error("the default notebook asks for encryption and the lookup said no — it only read page 1")
	}
	if name != "Zed" {
		t.Errorf("the notebook's name did not come back for the refusal message: %q", name)
	}
}

// TestNotebookWantsEncryptionStopsAtTheDefault proves the walk is not a tax on the
// ordinary account: it stops as soon as the default is in hand, so a default on the
// first page costs the one request it always cost.
func TestNotebookWantsEncryptionStopsAtTheDefault(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[{"id":"nb1","is_default":true,"default_encrypt":true}],` +
			`"paging":{"limit":500,"offset":0,"total":900,"has_more":true}}`},
	})

	if wants, known, _ := notebookWantsEncryption(client.NewClient(m.baseURL(), "tok"), ""); !wants || !known {
		t.Fatalf("the default notebook on page 1 was not found: wants=%v known=%v", wants, known)
	}
	if len(m.calls()) != 1 {
		t.Errorf("kept paging after finding the default: %v", m.calls())
	}
}

// ===========================================================================
// A lookup that FAILED is not a notebook that said no
// ===========================================================================
//
// The whole point of the (wants, known) pair. Both branches fail OPEN — an
// unanswerable lookup writes the note in the clear, deliberately, because failing
// closed would encrypt on a guess and a note sealed under the wrong passphrase is
// unrecoverable where an unencrypted one can be re-saved. What must not happen is
// that decision being made SILENTLY, and it must not happen on either branch: the
// named-notebook one needs no unusual account at all, just a --notebook flag.

// encryptDecisionCmd builds the flag set shouldEncryptCreate reads, so the decision
// point can be exercised without the whole `notes create` command around it. Any
// flag named in set is turned on.
func encryptDecisionCmd(set ...string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("plaintext", false, "")
	cmd.Flags().Bool("encrypt", false, "")
	for _, name := range set {
		_ = cmd.Flags().Set(name, "true")
	}
	return cmd
}

// runEncryptDecision runs the real decision with HARBOR_PASSPHRASE set to pass — an
// empty pass being the "no passphrase" case, which is what passphraseFromEnv already
// treats an empty value as. It returns the choice, the refusal (if any), and
// whatever was said on stderr, and it never fails the test itself: half these cases
// exist precisely to assert that an error WAS returned.
func runEncryptDecision(t *testing.T, m *apiMock, notebookID, pass string, flags ...string) (bool, error, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_PASSPHRASE", pass)
	resetSession()

	var enc bool
	var err error
	warned := captureStderr(t, func() {
		enc, err = shouldEncryptCreate(encryptDecisionCmd(flags...), client.NewClient(m.baseURL(), "tok"), notebookID)
	})
	return enc, err, warned
}

// decideEncrypt is runEncryptDecision for the cases that must not refuse: encryption
// enabled, and a returned error is the test failing.
func decideEncrypt(t *testing.T, m *apiMock, notebookID string) (bool, string) {
	t.Helper()
	enc, err, warned := runEncryptDecision(t, m, notebookID, "pw")
	if err != nil {
		t.Fatalf("shouldEncryptCreate: %v", err)
	}
	return enc, warned
}

// TestNamedNotebookLookupFailureIsAudible is the branch that matters most, because
// it takes no unusual account to reach — anyone who passes --notebook. A failed
// GetNotebook used to return plain `false`, indistinguishable at the call site from
// "this notebook does not want encryption", so a notebook marked default_encrypt
// produced a plaintext note with nothing said.
func TestNamedNotebookLookupFailureIsAudible(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks/nb1": {Status: 500, Body: apiErrorBody("internal", "boom")},
	})

	enc, warned := decideEncrypt(t, m, "nb1")

	if enc {
		t.Error("a failed notebook lookup encrypted the note on a guess")
	}
	if !strings.Contains(warned, "UNENCRYPTED") {
		t.Errorf("the note was written in the clear on an unanswered question, silently:\n%q", warned)
	}
	if !strings.Contains(warned, "nb1") {
		t.Errorf("the warning does not say which notebook could not be read:\n%q", warned)
	}
}

// The same requirement on the default-notebook branch: a walk that cannot finish
// leaves the setting unknown, and that is said rather than spent as a "no".
func TestDefaultNotebookLookupFailureIsAudible(t *testing.T) {
	// A list that claims more and hands back nothing: the walk cannot advance, so
	// the default notebook is never reached and its setting stays unknown.
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[],"paging":{"limit":500,"offset":0,"total":900,"has_more":true}}`},
	})

	enc, warned := decideEncrypt(t, m, "")

	if enc {
		t.Error("an unreadable notebook list encrypted the note on a guess")
	}
	if !strings.Contains(warned, "UNENCRYPTED") {
		t.Errorf("the note was written in the clear on an unanswered question, silently:\n%q", warned)
	}
}

// The warning is not noise. A lookup that ANSWERS says nothing, on either branch and
// whichever way it answered — otherwise every ordinary write in a plaintext notebook
// would carry a warning nobody can act on.
func TestAnAnsweredLookupIsQuiet(t *testing.T) {
	cases := map[string]struct {
		notebookID string
		routes     map[string]mockReply
	}{
		"named notebook, says no": {"nb1", map[string]mockReply{
			"GET /api/v1/notebooks/nb1": {Status: 200, Body: `{"id":"nb1","default_encrypt":false}`},
		}},
		"named notebook, says yes": {"nb1", map[string]mockReply{
			"GET /api/v1/notebooks/nb1": {Status: 200, Body: `{"id":"nb1","default_encrypt":true}`},
		}},
		"default notebook, says no": {"", map[string]mockReply{
			"GET /api/v1/notebooks": {Status: 200, Body: `{"data":[{"id":"nb1","is_default":true,"default_encrypt":false}],` +
				`"paging":{"limit":500,"offset":0,"total":1,"has_more":false}}`},
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, warned := decideEncrypt(t, newAPIMock(t, tc.routes), tc.notebookID); warned != "" {
				t.Errorf("a lookup that answered warned anyway:\n%q", warned)
			}
		})
	}
}

// A named notebook marked default_encrypt still encrypts — the guard against
// "fixed the warning, broke the feature".
func TestNamedNotebookThatWantsEncryptionStillEncrypts(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks/nb1": {Status: 200, Body: `{"id":"nb1","default_encrypt":true}`},
	})

	if enc, _ := decideEncrypt(t, m, "nb1"); !enc {
		t.Error("a notebook marked default_encrypt did not encrypt the note")
	}
}

// ===========================================================================
// No passphrase is not permission to ignore the notebook (#78)
// ===========================================================================
//
// The notebook says every note in it is encrypted. HARBOR_PASSPHRASE is unset, so
// this run CANNOT do that. The old answer was to write the note in the clear anyway
// and say nothing — the notebook's setting spent as a "no" because the environment
// could not honour it. The answer now is the same one --encrypt and the conversion
// commands already give: stop, write nothing, and name both ways forward.

// encryptingNotebookRoutes serves one named notebook that encrypts by default.
func encryptingNotebookRoutes() map[string]mockReply {
	return map[string]mockReply{
		"GET /api/v1/notebooks/nb1": {Status: 200, Body: `{"id":"nb1","name":"Encrypted Testing","default_encrypt":true}`},
	}
}

// The bug itself, on the branch anyone can reach with a --notebook flag.
func TestCreateIntoAnEncryptingNotebookIsRefusedWithoutAPassphrase(t *testing.T) {
	m := newAPIMock(t, encryptingNotebookRoutes())

	enc, err, _ := runEncryptDecision(t, m, "nb1", "")

	if err == nil {
		t.Fatalf("a note went into an encrypt-by-default notebook with no passphrase and no complaint (encrypt=%v)", enc)
	}
	if enc {
		t.Error("the refusal still asked for encryption it cannot perform")
	}
	// The message has to carry the user out of this, not just report it: the key to
	// supply, and the flag that says "no, I meant plaintext".
	for _, want := range []string{"Encrypted Testing", "encrypted by default", "HARBOR_PASSPHRASE", "--plaintext"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never mentions %q:\n%v", want, err)
		}
	}
}

// The same rule when nobody named a notebook at all. The note still lands somewhere
// — the account's default — and that notebook has the setting like any other. This
// mock puts the default on the SECOND page, so it also proves the guard is reached
// through the paged walk rather than only through a --notebook flag.
func TestCreateIntoTheEncryptingDefaultNotebookIsRefusedWithoutAPassphrase(t *testing.T) {
	m := notebookPageMock(t)

	enc, err, _ := runEncryptDecision(t, m, "", "")

	if err == nil {
		t.Fatalf("the default notebook encrypts by default and took a plaintext note silently (encrypt=%v)", enc)
	}
	if !strings.Contains(err.Error(), "Zed") {
		t.Errorf("the refusal does not name the default notebook it is talking about:\n%v", err)
	}
	if !strings.Contains(err.Error(), "--plaintext") {
		t.Errorf("the refusal does not offer the escape hatch:\n%v", err)
	}
}

// The other half of the pair: with the passphrase set, the very same notebook is
// sealed exactly as before. The fix is a new refusal, not a new behaviour.
func TestCreateIntoAnEncryptingNotebookStillSealsWithAPassphrase(t *testing.T) {
	m := newAPIMock(t, encryptingNotebookRoutes())

	enc, err, warned := runEncryptDecision(t, m, "nb1", "pw")

	if err != nil {
		t.Fatalf("a passphrase was set and the create was refused anyway: %v", err)
	}
	if !enc {
		t.Error("a notebook marked default_encrypt did not encrypt the note")
	}
	if warned != "" {
		t.Errorf("the ordinary encrypted create is not supposed to say anything:\n%q", warned)
	}
}

// --plaintext remains the sanctioned way to put an unencrypted note in an
// encrypting notebook — that is what makes the refusal above a fork in the road
// rather than a dead end. It is also read before the lookup, so it costs no request.
func TestPlaintextStillCreatesInAnEncryptingNotebook(t *testing.T) {
	m := newAPIMock(t, encryptingNotebookRoutes())

	enc, err, warned := runEncryptDecision(t, m, "nb1", "", "plaintext")

	if err != nil {
		t.Fatalf("--plaintext is the documented escape hatch and it was refused: %v", err)
	}
	if enc {
		t.Error("--plaintext encrypted the note")
	}
	if warned != "" {
		t.Errorf("--plaintext is an explicit choice and needs no warning:\n%q", warned)
	}
	if len(m.calls()) != 0 {
		t.Errorf("--plaintext asked the server a question whose answer it does not use: %v", m.calls())
	}
}

// The guard is not a tax on everyone else: a notebook that does not encrypt takes a
// plaintext note with no passphrase, no error and nothing said, exactly as before.
func TestAnOrdinaryNotebookIsUnaffectedWithoutAPassphrase(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks/nb1": {Status: 200, Body: `{"id":"nb1","name":"Inbox","default_encrypt":false}`},
	})

	enc, err, warned := runEncryptDecision(t, m, "nb1", "")

	if err != nil {
		t.Fatalf("an ordinary notebook refused an ordinary note: %v", err)
	}
	if enc {
		t.Error("a notebook that does not encrypt asked for encryption")
	}
	if warned != "" {
		t.Errorf("nothing was in doubt and something was said anyway:\n%q", warned)
	}
}

// The decision is only worth anything if `notes create` actually consults it, and
// "writes nothing" is a claim about the wire, not about a return value. This runs
// the real command tree: the mock routes the notebook read and NOTHING else, so a
// POST that got through would be an unrouted request and fail the test by itself.
func TestNotesCreateWritesNothingWhenItRefuses(t *testing.T) {
	m := newAPIMock(t, encryptingNotebookRoutes())
	t.Setenv("HARBOR_PASSPHRASE", "")
	resetSession()

	out, err := runCLI(t, m, "notes", "create", "--notebook", "nb1", "--title", "Blocked", "--content", "x")

	if err == nil {
		t.Fatal("notes create did not consult the guard — it exited 0 into an encrypt-by-default notebook")
	}
	if !strings.Contains(err.Error(), "--plaintext") {
		t.Errorf("the command surfaced some other failure, not the refusal:\n%v", err)
	}
	if out != "" {
		t.Errorf("a refused create printed a note anyway:\n%q", out)
	}
	for _, call := range m.calls() {
		if strings.HasPrefix(call, "POST") {
			t.Errorf("a refused create still wrote to the server: %v", m.calls())
		}
	}
}

// The UNKNOWN case is deliberately NOT the refusal above, and this pins that apart
// so a later reader does not "finish the job" by mistake. Nothing established that
// this notebook encrypts; the likeliest reason to be here is an account with no
// encryption at all whose notebook read failed, and refusing every create over that
// would break plain note-taking. It warns and proceeds — and the warning points at
// the passphrase rather than at --encrypt, which without a key would only trade this
// warning for a different error.
func TestAnUnreadableNotebookStillProceedsWithoutAPassphrase(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{
		"GET /api/v1/notebooks/nb1": {Status: 500, Body: apiErrorBody("internal", "boom")},
	})

	enc, err, warned := runEncryptDecision(t, m, "nb1", "")

	if err != nil {
		t.Fatalf("an unanswered lookup is not a notebook that said 'encrypted' — it must not refuse: %v", err)
	}
	if enc {
		t.Error("a failed notebook lookup encrypted the note on a guess")
	}
	if !strings.Contains(warned, "UNENCRYPTED") {
		t.Errorf("the note was written in the clear on an unanswered question, silently:\n%q", warned)
	}
	if !strings.Contains(warned, "HARBOR_PASSPHRASE") {
		t.Errorf("with no passphrase set, the advice has to name the passphrase:\n%q", warned)
	}
	if strings.Contains(warned, "--encrypt") {
		t.Errorf("--encrypt is useless advice with no key to encrypt with:\n%q", warned)
	}
}

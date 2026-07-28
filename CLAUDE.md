# CLAUDE.md — Harbor CLI architecture & contributor guide

This document orients a developer (human or AI) working on the Harbor CLI
codebase: how it is laid out, the patterns to follow, how to build and test, and
a step-by-step recipe for adding a new command. It is intentionally
self-contained — you do not need any private/internal knowledge to contribute.

## What this is

`harbor` is a Go + [cobra](https://github.com/spf13/cobra) command-line client
for the public Harbor notes API. It speaks the API's JSON wire contract and
renders results as styled tables (via [go-pretty](https://github.com/jedib0t/go-pretty))
or raw JSON (`--json`). It is a thin, well-tested client: business logic lives
in the API; the CLI's job is ergonomics, output, and auth.

## 🚫 NEVER create an account on production

**Do not sign up for a Harbor account on `app.harbor.my`. Ever.** Not through the web
signup form, not through the API, not through the CLI, not through a native app, not
through a browser-automation session, and not "just one to check something."

This is not a style preference. Production is Spicer's live product with real paying
customers in it. Agent-created signups have already piled up dozens of junk accounts
there — `testuser_explore123@example.com`, `newuser_harbor_test@example.com`,
`test_unique_12345@example.com`, a wall of "John Doe" and "Test User" rows — which pollute
the admin account list, distort counts, sit in the billing and entitlement tables, and
have to be cleaned up by hand. Every one of them came from an agent that needed somewhere
to test and picked the easiest target.

### Where to test instead — in this order

1. **A PR-preview sprite. This is the default and it is what you should reach for
   first.** Every PR against `HarborMyNotes/app.harbor.my` provisions its own throwaway
   sandbox (`harbor-pr-<PR#>` on sprites.app) with its own database. **If you need an
   environment to test against, open a DRAFT PR on `app.harbor.my` and use its sprite.**
   The authoritative URL, a one-click login link, and the seeded accounts are in the
   sticky **"🚢 Harbor preview"** comment on the PR — read it rather than hardcoding a
   host. Seeded accounts (all password `foobar123`):
   - `test@harbor.my` — starts empty → general verification
   - `seeded@harbor.my` — the Contractors ENEX corpus → anything needing real data
   - `empty@harbor.my` — deliberately empty → first-run / empty-state checks

   Create, edit, break and delete whatever you like there. The sprite is disposable and
   nothing in it is real.

2. **A local instance**, when the change doesn't need the hosted stack.

3. **`dev@harbor.my` — the ONE production account you may use.** It exists purely for QA.
   Create, update and delete whatever you need in it. Its password is in 1Password (Autumn
   vault, item "Harbor Dev Account"); fetch it with the service-account token, never
   hardcode it:
   ```bash
   export OP_SERVICE_ACCOUNT_TOKEN=$(jq -r .service_account_token ~/.config/1password.json)
   op read "op://Autumn/Harbor Dev Account/password"
   ```
   Reach production explicitly (e.g. `-HarborAPIBaseURL https://app.harbor.my`, or the
   client's equivalent) rather than by accident.

### Accounts you must never touch

- **`me@spicer.cc`** — Spicer's real personal account. Never sign in, never read, query,
  screenshot or export it, and never put its data in a commit, PR, issue or log.
- **`harbormaster@harbor.my`** — the pristine demo/marketing-screenshot account. Read-only
  as far as you are concerned: never create, edit or delete anything in it.
- **Any other production account.** If it isn't `dev@harbor.my`, it isn't yours.

### If you genuinely need a new production account

**Stop and ask Spicer.** Explain what you're testing and why a sprite, a local instance
and `dev@harbor.my` can't cover it. He may say yes — that's his call to make, and it takes
one message. Creating one and mentioning it afterwards is not the same thing.

## Layout

```
main.go                 → cmd.Execute()
cmd/                    cobra command tree — ONE FILE PER DOMAIN
  root.go               rootCmd, global flags, loadClientFromConfig, printResult
  display.go            color, JSON-navigation + formatting helpers, tables, error rendering
  flags.go              shared flag helpers (paging, partial-update body building)
  input.go              body input (--content/--file/--stdin) + flexible time parsing
  auth.go               login, logout, whoami, auth refresh, public recovery flows
  notebooks.go          notes.go  tags.go  note_tags.go  sync.go  files.go  search.go
  history.go  trash.go  templates.go  shortcuts.go  reminders.go  insights.go
  share.go  settings.go  profile.go  sessions.go  account.go  importexport.go  operational.go
  crypto.go             `harbor crypto setup/status/sync/rotate` + the transparent
                        encrypt-on-write / decrypt-on-read wiring (HARBOR_PASSPHRASE)
  skill.go              `harbor skill install/show/path` — installs the bundled agent skill (Claude/Codex/Cursor)
  assets/skill/         embedded skill content (SKILL.md + formatting.md + reference.md)
client/                 HTTP client + API methods — ONE FILE PER DOMAIN
  client.go             Client, HTTP verbs, transparent-refresh, request plumbing
  errors.go             APIError (typed error envelope)
  envelope.go           response-envelope decoders (collection/paging/data/token)
  auth.go  notebooks.go  notes.go  …                (mirror cmd/ domains)
crypto/                 client-side E2E note crypto (zero-knowledge server)
  crypto.go             HRBK1 keystore + HRBC2 field envelope (Argon2id KEK, AES-256-GCM)
  README.md             the canonical cross-client interop contract
config/
  config.go             credentials.json load/save (0600), expiry helpers; keystore-blob cache
Formula/harbor.rb       Homebrew formula (version auto-bumped by release CI)
.github/workflows/      test.yml (CI) + release.yml (CD)
```

Every source file `X.go` has a sibling `X_test.go` (one test file per file).

## The wire contract (what the client speaks)

- **Base URL** includes the `/api/v1` prefix; client method paths are relative
  (`/notes`, `/notebooks/:id`). The few **operational** probes (`/health`,
  `/ready`, `/version`) live at the **root** — derive it with `c.Origin()`.
- **JSON** request/response bodies; field names are `snake_case`.
- **Timestamps** are UTC epoch-milliseconds (integers). The OAuth `expires_in`
  is the lone exception (seconds).
- **Response envelopes** (see `client/envelope.go`):
  - collection: `{ "data": [...], "paging": { limit, offset, total, has_more } }`
  - wrapped single: `{ "data": {...} }`
  - bare single: many create/get/update endpoints return the resource directly
  - note mutations: `{ "note": {...}, "usn": N }`
  - OAuth token: a bare `{ access_token, refresh_token, … }`
- **Errors**: `{ "error": { code, message, details, request_id } }`, decoded into
  `*client.APIError`. Commands branch on `Code`.

## Core patterns (copy these)

**A command's `RunE`** loads a client, builds a request from flags, calls a
client method, and prints the result:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    c, _, err := loadClientFromConfig()
    if err != nil {
        return err
    }
    data, err := c.ListNotebooks(pagingParams(cmd))
    if err != nil {
        return mapNotebookError(err) // friendly per-domain messages
    }
    printResult(data, displayNotebooks) // JSON in --json mode, else the table
    return nil
}
```

- **Never `os.Exit` inside `RunE`** — return the error; `Execute` renders it
  (rich treatment for `*client.APIError`) and sets the exit code.
- **`printResult(data, displayFn)`** handles `--json` automatically; your
  `displayFn(data []byte)` only renders the table/detail view.
- **Partial updates**: only send fields the user set, via
  `addStringIfChanged` / `addBoolIfChanged` / `addIntIfChanged`.
- **Pagination**: `addPagingFlags(cmd)` + `pagingParams(cmd)` for consistent
  `--limit/--offset/--order` everywhere; call `printPagingFooter(data)` after a
  list table.
- **Public endpoints** (login, password reset, public share, ops) use
  `newAnonymousClient()` (no bearer); everything else uses
  `loadClientFromConfig()`.
- **Friendly errors**: add a `map<Domain>Error(err) error` that switches on the
  domain's `APIError.Code` values and returns `errors.New(...)`; let everything
  else fall through to the default renderer (which prints `details`).
- **Self-registration**: each domain file's `init()` calls
  `rootCmd.AddCommand(...)` (or attaches to an existing parent like `notesCmd`).
  Set a `GroupID` (`groupAuth`/`groupContent`/`groupOrg`/`groupSync`/
  `groupAccount`/`groupSystem`) so `--help` groups it.

## Add a new endpoint (recipe)

Say the API gains `GET /api/v1/widgets` and `POST /api/v1/widgets`.

1. **Client methods** — `client/widgets.go`:
   ```go
   func (c *Client) ListWidgets(params map[string]string) ([]byte, error) {
       return c.doGet("/widgets", params)
   }
   func (c *Client) CreateWidget(body map[string]any) ([]byte, error) {
       return c.doPost("/widgets", body)
   }
   ```
   Use `doGet/doPost/doPatch/doPut/doDelete`, `doMultipart` (uploads), or
   `doGetRaw` (streamed downloads). Need the HTTP status (e.g. 201-vs-200)?
   mirror `AttachTag` in `client/note_tags.go` (`requestWithStatus`).

2. **Commands** — `cmd/widgets.go`: a parent `widgetsCmd` + subcommands, a
   `displayWidgets`/`displayWidget` renderer using `printTable`/`printKV` and the
   shared helpers in `display.go`, a `mapWidgetError`, and an `init()` that wires
   flags and `rootCmd.AddCommand(widgetsCmd)`.

3. **Tests** — `client/widgets_test.go` asserts method/path/query/body against a
   mock server (`newTestServer` + `testClient`); `cmd/widgets_test.go` feeds
   fixture JSON to the display funcs via `captureStdout` and checks the error
   mapping with `apiErr`.

4. Put the Cloudmanic copyright header (current year + date) atop each new file,
   and a doc comment above every function. Run `make lint && make test`.

## Build & test

```sh
make build          # ./build/harbor, version injected via ldflags
make test           # go test ./... (no network, no config required)
make lint           # gofmt + go vet
make cross-build    # all release platforms into dist/
make run ARGS="notes list --json"
```

`go test ./...` MUST pass with **no network and no config**. Tests mock the API
with `httptest.NewServer` and use a temp `HOME` for config tests, so they never
touch a real server or your real `~/.config/harbor`.

### Testing recipe

- **Client tests** use the shared harness in `client/client_test.go`:
  `newTestServer(t, &rec, status, body)` records the request into a
  `recordedRequest` (method/path/query/body/headers); `testClient(url)` points a
  `Client` at it. Assert the verb, path, query, and decoded JSON body.
- **Command tests** use `captureStdout(t, fn)` (in `cmd/display_test.go`) to
  capture a display function's output, and `apiErr(code)` to build an
  `*client.APIError` for `map<Domain>Error` tests.
- **Config tests** call `t.Setenv("HOME", t.TempDir())` so the credentials file
  is isolated.
- Use obviously-fake fixture data (`you@example.com`, ids like `n1`).

## Credentials file

`~/.config/harbor/credentials.json` (mode `0600`), written atomically:

```json
{
  "api_url": "https://app.harbor.my/api/v1",
  "client_id": "harbor-app",
  "email": "you@example.com",
  "user_id": "…",
  "access_token": "at_…",
  "refresh_token": "rt_…",
  "token_type": "Bearer",
  "scope": "notes notebooks tags sync files search profile",
  "expires_at": 1750000000000,
  "device_id": "cli-…",
  "device_name": "harbor-cli on <host>"
}
```

`api_url` is omitted (defaults to production) for normal logins; it is set only
when a maintainer targets a non-default environment. Tokens are never logged or
printed (use `--show-token` to opt in explicitly).

## Releases

Pushing to `main` triggers `release.yml`: it tests, auto-increments the patch
version off the latest `vX.Y.Z` tag (first release `v0.1.0`), cross-compiles the
five platform binaries with the version baked in via ldflags, publishes a GitHub
release, and bumps `Formula/harbor.rb` so `brew upgrade harbor` works.

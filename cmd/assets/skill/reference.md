<!-- Harbor agent skill — command reference • v1.1.0 -->
<!-- Copyright (c) 2026 Cloudmanic Labs, LLC. All rights reserved. Date: 2026-06-22 -->

# Harbor CLI — full command reference

A flag-by-flag map of every `harbor` command, for when `SKILL.md` doesn't cover
what you need. The CLI's own help is always authoritative: `harbor <cmd> --help`.

## Conventions

- **Output:** styled tables by default; add **`--json`** for machine-readable
  output (use it for anything you parse).
- **Envelopes:** collections → `{ "data": [...], "paging": { limit, offset, total,
  has_more } }`; note mutations (create/update/append) → `{ "note": {…}, "usn": N
  }`; a single GET → the object directly. Errors → `{ "error": { code, message,
  details, request_id } }`.
- **IDs** are UUIDs. **Timestamps** are UTC epoch-milliseconds.
- **Paging:** `--limit` (default 100, cap 500), `--offset`, `--order` (e.g.
  `-updated_at,title`; leading `-` = descending). Check `.paging.has_more`.
- **Exit codes:** `0` ok, `1` error, `3` API unreachable (retryable), `4` plan
  limit reached (never retryable — free up room or upgrade). Errors always go to
  stderr; with `--json` they are the error envelope, not prose. Nothing exits
  `0` without doing the work: an unknown subcommand fails (`1`) instead of
  printing help, `sync push` exits `4`/`1` when the server refused changes
  inside its `200` (a `conflict` is not a refusal), and `import enex` exits `1`
  on a `partial`/`failed` import — both still print their results.

## Global flags (every command)

| Flag | Purpose |
|---|---|
| `--json` | Raw JSON instead of tables |
| `--no-color` | Disable ANSI color |
| `-v, --verbose` | Include `request_id` + HTTP status on errors |
| `--utc` | Render timestamps in UTC, not local |
| `--api-url <url>` | Override API base URL (dev/staging; rarely needed) |

Env: `HARBOR_API_URL` (same as `--api-url`), `NO_COLOR`.

---

## Authentication

| Command | What it does | Key flags |
|---|---|---|
| `harbor login` | OAuth2 password login (interactive prompt) | `--email`, `--scope`, `--client-id`, `--show-token` |
| `harbor logout` | Revoke session + clear local creds | `--all-devices` |
| `harbor whoami` | Show current session (alias of `auth status`) | `--show-token` |
| `harbor auth status` | Session status | `--show-token` |
| `harbor auth refresh` | Force a token refresh now | |
| `harbor auth forgot-password` | Request a reset email | `--email` |
| `harbor auth reset-password` | Complete a reset (revokes sessions) | `--token` |
| `harbor auth verify-email` | Verify email | `--token` |
| `harbor auth resend-verification` | Resend verification email | `--email` |

> Login is interactive (hidden password prompt). An agent normally can't perform
> it — ask the user to run `harbor login`.

---

## Notebooks  (aliases: `notebook`, `nb`)

Containers for notes; exactly one **default** notebook per account.

| Command | What it does | Key flags |
|---|---|---|
| `harbor notebooks list` | List notebooks | `--stack`, `--include-deleted`, paging |
| `harbor notebooks get <id>` | One notebook | `--include-deleted` |
| `harbor notebooks create` | Create | `--name` (req), `--stack`, `--default-encrypt` |
| `harbor notebooks update <id>` | Partial update | `--name`, `--stack`, `--public`, `--make-default`, `--default-encrypt` |
| `harbor notebooks delete <id>` | Tombstone | `--notes move_to_default\|trash` |

`--make-default` promotes a notebook (the prior default is demoted; you can't
"un-default" directly — promote another). The default notebook can't be deleted.

**The default notebook can never be encrypt-by-default.** Forwarded email,
imports, and notes created with no notebook all land in the default, and none of
those writers can client-side encrypt — so the pair is banned outright. The CLI
refuses it locally, in both directions: `--default-encrypt` on the default
notebook, and `--make-default` on a notebook that already encrypts. To promote an
encrypting notebook, turn encryption off in the same command:

```bash
harbor notebooks update <id> --make-default --default-encrypt=false
```

---

## Notes  (aliases: `note`, `n`)

Bodies are Markdown (default) or HTML (`--format`), supplied via `--content`,
`--file`, or `--stdin`. See `formatting.md`.

| Command | What it does | Key flags |
|---|---|---|
| `harbor notes list` | List notes | `--notebook`, `--tag`, `--updated-since`, `--deleted`, `--meta`, paging |
| `harbor notes get <id>` | One note | `--format markdown\|html`, `--deleted` |
| `harbor notes create` | Create (returns `{note,usn}`) | `--title`, `--notebook`, `--content/--file/--stdin`, `--format`, `--source-url`, `--author` |
| `harbor notes update <id>` | Partial update (**body is replaced** if sent) | same as create + `--notebook` (move) |
| `harbor notes append <id>` | Append to the END | `--content/--file/--stdin`, `--format` |
| `harbor notes delete <id>` | Trash (or expunge) | `--permanent` |
| `harbor notes tag <id>` | Attach a tag (idempotent) | `--tag-name` (creates if missing) or `--tag-id` |
| `harbor notes untag <id>` | Detach a tag | `--tag-id` (req) |
| `harbor notes tags <id>` | List a note's tags | paging |
| `harbor notes set-tags <id>` | Replace the whole tag set | `--tags id1,id2` (`""` clears) |
| `harbor notes links <id>` | Outgoing `harbor:note` links | paging |
| `harbor notes backlinks <id>` | Live notes linking here | paging |
| `harbor notes audit <id>` | Change log | `--action create\|update\|append\|delete\|restore\|tag\|move\|share`, `--order created_at\|usn`, paging |
| `harbor notes export <id>` | Write ONE note to a file | `--output` (req; `-` = stdout, a directory takes the server's filename), `--zip`, `--format markdown` |

`--meta` omits bodies for lighter list payloads. List sort fields: `updated_at`,
`created_at`, `title`, `usn`.

---

## Tags  (alias: `tag`)

Hierarchical (nested). A tag's parent is set with `--parent` (or `--top-level`).

| Command | What it does | Key flags |
|---|---|---|
| `harbor tags list` | List tags | `--top-level`, `--parent <id>`, `--include-deleted`, paging |
| `harbor tags get <id>` | One tag | `--include-deleted` |
| `harbor tags create` | Create | `--name` (req; no commas), `--parent` |
| `harbor tags update <id>` | Rename / re-parent | `--name`, `--parent`, `--top-level` |
| `harbor tags delete <id>` | Tombstone (untags notes) | `--children reparent_to_grandparent\|orphan` |
| `harbor tags notes <id>` | Notes carrying a tag | `--notebook`, paging |

---

## Search  (Evernote-style grammar)

`harbor search '<query>' [--json]`

| Operator | Meaning |
|---|---|
| `tag:VALUE` | carries this tag (`tag:"two words"` for spaces) |
| `notebook:VALUE` | in this notebook (id or name) |
| `intitle:VALUE` | term in the title |
| `resource:RTYPE` | has an attachment: `image\|pdf\|audio\|application\|any` |
| `created:RANGE` | created date: `YYYYMMDD`, `YYYYMMDD..YYYYMMDD`, `day-N` |
| `updated:RANGE` | last-updated date (same forms) |
| `"exact phrase"` | consecutive, in-order words |
| `term*` | prefix match |
| `-token` | negate any token |

Flags: `--notebook`, `--types note,attachment`, `--no-snippet`, paging.

`harbor search coordinates --resource-id <id> [--query … | --terms a,b] [--page N]`
returns OCR highlight boxes (pair with `--json`).

---

## Reminders  (aliases: `reminder`, `rem`)

Times: epoch-ms, RFC3339, `YYYY-MM-DD`, or relative (`in 2h`, `in 3d`).

| Command | What it does | Key flags |
|---|---|---|
| `harbor reminders set <id>` | Set/update due time | `--time` |
| `harbor reminders list` | Notes with reminders | `--status active\|done\|all`, `--due-before`, paging |
| `harbor reminders complete <id>` | Mark done | `--time` (completion moment) |
| `harbor reminders clear <id>` | Remove reminder | |

---

## Tasks  (alias: `task`)

A task is a standalone to-do that syncs like a note — a title plus an optional
due date, reminder, recurrence rule, priority (`none|low|medium|high`) and flag.

Times: epoch-ms, RFC3339, `YYYY-MM-DD`, or relative (`in 2h`). A bare
`YYYY-MM-DD` due is stored **date-only** (no time shown); anything else is
timed. Override with `--due-has-time=false|true`.

Recurrence: `daily`, `weekly`, `monthly`, `yearly`, `every:N:days|weeks|months|
years`, or an RRULE (`FREQ=WEEKLY;BYDAY=FR`).

| Command | What it does | Key flags |
|---|---|---|
| `harbor tasks list` | List tasks | `--status active\|today\|done\|all`, `--due-before`, `--note <note-id>`, paging |
| `harbor tasks get <id>` | One task | |
| `harbor tasks create` | New task | `--title` (required), `--due`, `--due-has-time`, `--reminder`, `--recurrence`, `--priority`, `--flag`, `--position` |
| `harbor tasks update <id>` | Change a task | same fields, plus `--clear-due`, `--clear-reminder`, `--clear-recurrence` |
| `harbor tasks done <id>` | Complete it | `--time` (completion moment) |
| `harbor tasks undone <id>` | Reopen it | |
| `harbor tasks delete <id>` | Delete it | |

`--order` sort keys: `due`, `priority`, `created`, `updated`, `position`, `usn`
(prefix `-` for descending). Within `--note`: `position`, `created`, `updated`,
`due`, `usn`. Anything else is rejected.

**`done` on a recurring task does not close it** — the task rolls forward to its
next occurrence and stays open. The CLI says which happened.

**A task cannot be attached to a note from here.** `--note` on `list` is a
read-only filter; there is no `--note` on `create`/`update`, because a note's
body owns its tasks and a link without the matching in-note block is removed on
the note's next save.

---

## Templates  (aliases: `template`, `tpl`)

Reusable note starting points. Built-in (system) templates are read-only.

| Command | What it does | Key flags |
|---|---|---|
| `harbor templates list` | List | `--include-system` (default true), `--include-deleted`, paging |
| `harbor templates get <id>` | One template | `--include-deleted` |
| `harbor templates create` | Create | `--name` (req), `--content/--file/--stdin`, `--format`, `--notebook`, `--tags id1,id2` |
| `harbor templates update <id>` | Update (user templates only) | `--name`, content flags, `--notebook`, `--tags` |
| `harbor templates delete <id>` | Delete (user templates only) | |
| `harbor templates apply <id>` | New note from template | `--title`, `--notebook`, `--tags id1,id2` |

A template can carry a **default notebook** (`--notebook`) and a **set of tags**
(`--tags`), which a note made from it inherits. On `update`, both follow the
partial-update rule: omit the flag to preserve the stored value, pass an empty
string to clear it. The notebook must be a live, non-encrypt-by-default notebook
— a plaintext template can never be materialized into an encrypted notebook.

`--tags` on **apply REPLACES** the template's tags rather than adding to them:
omit it and the note inherits the template's (stale ids are skipped for you),
pass a list and the note gets exactly that list, pass an empty string for none.
A sent list is validated strictly, so never echo a template's stored `tag_ids`
back without first filtering them against `harbor tags list` — one deleted tag
fails the whole apply.

`{{date}}`-style variables in a template are expanded **server-side at apply
time**, so `harbor templates apply` returns a note that is already filled in.
The CLI does no expansion of its own. Full token list: `docs/template-variables.md`
in `app.harbor.my`.

`harbor templates get` prints the default notebook and tags as **ids**; `--json`
carries `notebook_id` and `tag_ids` verbatim. Apply's response also carries a
`notice` — server-owned wording, printed verbatim — which says the template's
notebook was gone or encrypted so the note was filed in your default instead.

A notebook you name with `--notebook` on apply is **rejected** when it is
encrypt-by-default. A notebook the **template** remembers is **tolerated**: if it
has been deleted or turned encrypt-by-default since, the note is filed in your
default notebook and the `notice` says so. Reject what the caller just chose,
tolerate what a stored record remembers.

---

## Shortcuts  (aliases: `shortcut`, `sc`)

Ordered sidebar pointers to a record or a saved search.

| Command | What it does | Key flags |
|---|---|---|
| `harbor shortcuts list` | List (by position) | `--include-deleted`, paging |
| `harbor shortcuts get <id>` | One shortcut | `--include-deleted` |
| `harbor shortcuts create` | Create | `--type note\|notebook\|tag\|search` (req), `--target-id` *or* `--query`, `--label`, `--position` |
| `harbor shortcuts update <id>` | Update | `--label`, `--position`, `--target-id` *or* `--query` |
| `harbor shortcuts delete <id>` | Tombstone | |
| `harbor shortcuts reorder` | Renumber the whole list | `--order id1,id2,…` (every live id once) |

`--type note|notebook|tag` requires `--target-id`; `--type search` requires
`--query`.

---

## Sharing

Public, read-only links. **Confirm before publishing.** Encrypted notes can't be
shared.

| Command | What it does | Key flags |
|---|---|---|
| `harbor share publish <id>` | Publish (idempotent) → public URL | `--slug` |
| `harbor share unpublish <id>` | Revoke (idempotent) | |
| `harbor share open <token>` | Render a shared note (no login) | |

JSON: `harbor share publish <id> --json \| jq -r '.data.public_url'`.

---

## History  (alias: `hist`)

Forward-only version snapshots; revert restores a past version as a new one.

| Command | What it does | Key flags |
|---|---|---|
| `harbor history list <note-id>` | Snapshots (newest first) | paging |
| `harbor history show <note-id> <ver-id>` | Full snapshot incl. content | `--format markdown\|html` |
| `harbor history revert <note-id> <ver-id>` | Restore as new current version | |

---

## Trash  (aliases: `recycle`, `bin`)

Recoverable recycle bin.

| Command | What it does | Key flags |
|---|---|---|
| `harbor trash list` | Notes in the bin | paging |
| `harbor trash restore <id>` | Restore to live | |
| `harbor trash expunge <id>` | Permanently delete one | |
| `harbor trash empty` | Permanently delete ALL | `--yes` (required non-interactively) |

---

## Files / attachments  (alias: `file`)

Content-addressed (sha256) blobs.

| Command | What it does | Key flags |
|---|---|---|
| `harbor files upload <path>` | Upload (server sniffs MIME) | `--mime`, `--filename`, `--encrypted` |
| `harbor files list` | List with linked notes | `--mime`, `--note-id`, `--ocr-status`, `--encrypted`, `--updated-since`, paging |
| `harbor files get <hash>` | Presigned URL + metadata (no bytes) | |
| `harbor files check` | Does a blob exist? | `--hash` (+`--size`) or `--file` (hash computed locally) |
| `harbor files download <hash>` | Download bytes | `--output` (`-` = stdout), `--raw`, `--ciphertext` |

**Encrypted attachments.** `files upload --encrypted` seals the bytes on this
machine (HRBC2 binary envelope, master key) before uploading — it needs
`HARBOR_PASSPHRASE` and refuses rather than uploading in the clear. `files
download` detects an encrypted blob and decrypts it automatically when the
passphrase is set; without it the download is refused, and `--ciphertext` writes
the raw envelope instead. Filename and MIME stay plaintext on the record; the
stored size is 33 bytes larger than the original, and `files check --file` can
never match an encrypted upload because the content hash covers the ciphertext.

---

## Sync  (raw USN engine — JSON-first; advanced)

| Command | What it does | Key flags |
|---|---|---|
| `harbor sync pull` | Pull changes since a cursor | `--after-usn`, `--all`, `--limit`, `--device-id`, `--scope-id` |
| `harbor sync push` | Push change envelopes | `--file <json\|->`, `--device-id`, `--scope-id` |
| `harbor sync devices` | List devices + GC floor | |
| `harbor sync register-device` | Register/refresh a device | `--device-id`, `--name`, `--platform` |
| `harbor sync remove-device <id>` | Deregister | |
| `harbor sync ack` | Advance a device cursor | `--device-id`, `--acked-usn` |

Most note tasks never need `sync` — use the high-level commands.

---

## Encryption  (end-to-end)

Client-side, zero-knowledge note encryption. Set `HARBOR_PASSPHRASE` and the CLI
**decrypts notes on read automatically** and **encrypts on write** in a
`default_encrypt` notebook (or with `--encrypt`). The server only ever stores
ciphertext.

```bash
export HARBOR_PASSPHRASE=$(op read "op://Vault/Harbor/passphrase")   # 1Password
```

| Command | What it does |
|---|---|
| `harbor crypto setup` | One-time: generate the keystore (master key wrapped by your passphrase). **Lost passphrase = unrecoverable.** Uses `HARBOR_PASSPHRASE` or prompts. |
| `harbor crypto status` | Show whether a keystore exists, the passphrase is set, and it unlocks (no secrets printed). |
| `harbor crypto sync` | Re-fetch + cache the keystore (e.g. after a rotation on another device). |
| `harbor crypto rotate` | Change the passphrase (re-wraps the master key; no note re-encrypted). New passphrase via `HARBOR_NEW_PASSPHRASE` or prompt. |

Write flags on `harbor notes create`: `--encrypt` (force, needs the passphrase),
`--plaintext` (force off). Notes:

- **Creating in a `default_encrypt` notebook requires `HARBOR_PASSPHRASE`.** Without
  it the create **fails and writes nothing**, rather than quietly landing a plaintext
  note there. `--plaintext` is the sanctioned way to create an unencrypted note
  there anyway. The account **default** notebook can never carry the flag, so a
  note created with no `--notebook` is never affected by this.
- Decryption is automatic on `notes get/list`, `trash list`, `reminders list`.
  A wrong/absent passphrase shows ciphertext (`[encrypted]`), never an error.
- `notes update` re-seals an already-encrypted note; it refuses to write plaintext
  into one without `HARBOR_PASSPHRASE`. `notes append` is unsupported on encrypted
  notes; encrypted notes can't be searched, shared, or exported.
- **`notes update --notebook <default_encrypt notebook>` seals the note as part of
  the move**, in one write, so it is never sitting there readable. It needs
  `HARBOR_PASSPHRASE` or the move is refused with nothing written. Sealing
  **hard-deletes every earlier version** of the note server-side, so the move asks
  for confirmation when the note has any — which means `--json` and other
  non-interactive runs are refused unless you pass `--yes`. A note whose only
  stored version is its current contents loses nothing and moves silently.
  Moving a note back OUT does not decrypt it; encryption is per-note.
- Interop format (HRBK1 keystore + HRBC2 envelope) is documented in `crypto/README.md`.

---

## Settings  (aliases: `prefs`, `preferences`)

Account preferences (NOT synced; last-write-wins). `set` is a partial update.

| Command | What it does |
|---|---|
| `harbor settings get` | Effective settings |
| `harbor settings set` | Update (only flags you pass) |

`set` flags: `--theme system\|light\|dark`, `--default-notebook <id>` /
`--clear-default-notebook`, `--default-sort`, `--locale`, `--timezone`,
`--email-product-news`, `--email-reminders`, `--push-reminders`,
`--editor-font-size`, `--editor-font-family sans\|serif\|mono`,
`--editor-autosave`, `--editor-spellcheck`, `--editor-show-word-count`.

---

## Profile

| Command | What it does | Key flags |
|---|---|---|
| `harbor profile get` | Show profile | |
| `harbor profile update` | Update | `--name`, `--locale`, `--timezone`, `--email` (staged; needs password) |
| `harbor profile change-password` | Change password (interactive) | |
| `harbor profile confirm-email` | Confirm a staged email change | `--token` |
| `harbor profile set-avatar` | Avatar from an uploaded image | `--hash` |
| `harbor profile remove-avatar` | Remove avatar | |
| `harbor profile inbound-email show` | Show the email-to-note address + on/off | |
| `harbor profile inbound-email reset` | Rotate it (old address dies at once) | `--yes` (required in `--json`/non-interactive) |
| `harbor profile inbound-email enable` | Start accepting mail | |
| `harbor profile inbound-email disable` | Stop accepting mail (address kept) | |

**Email to notes.** Every account has a private address (`harbor profile get` shows
it as `Inbound email`). Mail forwarded there becomes a note: subject → title,
body → note, attachments → attachments. `@Notebook` files it in that notebook
(unknown name → default notebook); `#tag` applies existing tags (unknown tags are
ignored, never created). Both tokens work anywhere in the subject or body. The
address is a bearer secret — anyone holding it can create notes in the account,
and revoking a token does not revoke it — so never echo it into logs or shared
output; `reset` is the revocation path.

---

## Sessions

| Command | What it does |
|---|---|
| `harbor sessions list` | Active sessions (marks current) |
| `harbor sessions revoke <family-id>` | Revoke one |
| `harbor sessions revoke-others` | Revoke all but current |
| `harbor sessions revoke-all` | Revoke all (incl. current) |

---

## Account  (destructive — confirm)

| Command | What it does | Key flags |
|---|---|---|
| `harbor account export` | Start an export job | `--format enex\|html\|markdown`, `--notebook <id>`, `--wait`, `--download <path>` |
| `harbor account exports` | Show the current export per format | |
| `harbor account export-status <id>` | Poll / download the ZIP | `--download <path>`, `--wait` |
| `harbor account export-delete <id>` | Delete an export, freeing its slot | `--yes` |
| `harbor account clear` | **Empty** the account, keeping it | `--confirm "CLEAR MY ACCOUNT"`, `--yes`, `--no-wait` |
| `harbor account clear-status` | The account's current or last clear | |
| `harbor account delete` | Schedule deletion (grace period) | `--confirm "DELETE MY ACCOUNT"`, `--yes` |
| `harbor account cancel-delete` | Cancel within grace window | |

**Clear is not delete.** `clear` destroys every note, notebook, tag, task and
file **immediately** and keeps the account, the login, the sessions and the plan
— you end up signed in to an empty account with a fresh default notebook, and
there is nothing to cancel. `delete` is the opposite: a grace window, other
sessions revoked, nothing destroyed until it elapses, and `cancel-delete` until
then. Each takes a confirmation phrase the other does not satisfy, matched byte
for byte, plus the current password. Non-interactive and `--json` runs need both
`--confirm "<phrase>"` and `--yes`; the password reads from piped stdin. An
export still on the server is a copy of the notes, so a clear destroys it too.

The server answers as soon as the job is **queued**, not when it is done, so
`clear` waits for it and reports only when it has finished (`--no-wait` to be
handed the job instead). A clear also destroys the server-side encryption
keystore, and the CLI drops its cached copy to match.

**Exports.** You hold one export per format — one ENEX, one HTML, one Markdown —
and scoping to a notebook does not create a slot of its own. Starting a second of the same format while
one is ready is refused; delete it or wait for it to expire (72 hours). Exports
run one at a time server-wide, so a new job may sit `queued` (waiting its turn)
before it starts; progress is counted in **notes**. Lost the job id? `harbor
account exports` reads it back off the server — export state lives there, not in
your shell. One-shot: `harbor account export --wait --download ~/Downloads`.

`--format markdown` gives one `.md` per note in folders matching the notebooks,
attachments alongside — the shape Obsidian, Bear and iA Writer read, and the one
Harbor can import back. All three formats skip encrypted notes (the server holds
only ciphertext) and report the count.

**One note to a file** is `harbor notes export <id>`, a different command from
the account job: it returns the file directly rather than queueing anything.

```bash
harbor notes export "$NOTE_ID" --output note.md   # a note with no attachments
harbor notes export "$NOTE_ID" --output .         # server names it; .md or .zip
harbor notes export "$NOTE_ID" --zip --output .   # always the archive
```

The SAME command returns `text/markdown` for a note with no attachments and
`application/zip` (the `.md` plus `files/`) for one with them, so let `--output
.` take the server's own filename rather than choosing an extension yourself.
Rendering happens server-side, so this needs a network connection, and encrypted
notes are refused. **This is an export, not a read:** the file carries YAML front
matter and the title as a heading, so writing it back with `notes update` would
put all of that into the note — use `notes get --format markdown` for that.

---

## Import / Export (Evernote ENEX)

| Command | What it does | Key flags |
|---|---|---|
| `harbor import enex <file.enex>` | Import an Evernote export (uploads straight to storage in chunks, then waits for the import) | `--notebook`, `--filename`, `--no-wait`, `--poll-interval`, `--timeout`, `--notify-email` |
| `harbor import status <job-id>` | Poll an import job | |
| `harbor import abort <job-id>` | Cancel an import still awaiting its bytes | |
| `harbor export enex` | Export notes to `.enex` | `--notebook` *or* `--notes id1,id2`, `--include-resources`, `--output` (`-`=stdout) |

A completion email is sent only when you are not waiting for the result:
`--no-wait` asks for one, the default waiting mode does not, and
`--notify-email=true|false` overrides either way.

A failed chunk or a Ctrl-C aborts the upload automatically. If that automatic
abort itself fails, the CLI says so — as a stderr warning naming the exact
`harbor import abort` invocation, or under `--json` as `details.recover` on the
error, alongside `details.job_id`. The error still carries why the UPLOAD failed
and still exits `3` for a dropped connection. Until the abort runs the job holds
its staged bytes, though the server reclaims orphans on its own eventually.

`--notebook` here is **deprecated** — use `harbor account export --format enex
--notebook <id>`, which runs a real export job (progress, email, retention,
delete) and streams instead of building the document in memory. Not a drop-in:
the job path writes a ZIP and, being the GDPR archive, includes notes in the
trash that this command leaves out. `--notes` is **not** deprecated and stays
here — a note selection has no successor.

---

## Plans & limits (read-only — the CLI never takes payment)

| Command | What it does | Key flags |
|---|---|---|
| `harbor usage` | Used vs limit for every capped resource (notes, notebooks, tags, files, tasks); `∞` = unlimited. Also reports the read-only state | |
| `harbor plan` | The current plan: source (`free`/`stripe`/`apple`/`google`/`comp`), status, renewal, who owns billing | |
| `harbor plan list` | Plans offerable to this account, with prices and caps | `--limit`, `--offset`, `--order` |

**Hitting a cap.** A create past a plan limit fails with `403
plan_limit_reached` and **exit code 4**, and the CLI prints which resource, the
used/limit numbers, and an upgrade URL. Two gates share that code: a per-resource
cap (`details.gate = plan_limit`, blocks only that resource) and the
whole-account read-only freeze (`details.gate = account_read_only`, blocks every
create and edit; deletes and exports still work). Retrying never helps — free up
room or upgrade in the web app. Check `harbor usage --json` before a bulk import
to avoid failing halfway.

**How a slot is actually freed** differs per resource — do not assume "delete it":

| Resource | What frees a slot |
|---|---|
| notebooks, tags, tasks | `harbor <domain> delete <id>` — immediate |
| notes | Trashing does **not** free it. `harbor notes delete <id> --permanent`, or trash then `harbor trash empty` |
| files | **There is no `harbor files delete`.** A blob is released only when the notes holding it are **expunged** (and no other live-or-trashed note references it). An upload never attached to a note has no user-reachable delete at all |

---

## System / operational (public probes, no login)

| Command | What it does | Key flags |
|---|---|---|
| `harbor status` | Health: liveness + readiness + version | (exits non-zero if not ready) |
| `harbor api-version` | Server build version/commit | |
| `harbor openapi` | Fetch the OpenAPI 3.0 spec | `-o, --output` |

---

## Skill (this skill's installer)

| Command | What it does | Key flags |
|---|---|---|
| `harbor skill install` | Install/update this skill into your agent | `--agent claude\|codex\|cursor`, `--dir`, `--project`, `--force` |
| `harbor skill show [file]` | Print a bundled skill file (default `SKILL.md`) | |
| `harbor skill path` | Print the install path | `--agent`, `--dir`, `--project` |

`--agent` installs in each tool's native form: Claude Code → a
`~/.claude/skills/harbor/` skill directory; Codex → a managed block in
`~/.codex/AGENTS.md`; Cursor → a `.cursor/rules/harbor.mdc` rule file. `install`
backs up any existing copy first, so a CLI upgrade can refresh the skill without
clobbering user edits.

---
name: harbor
description: >-
  Create, edit, format, organize, search, and sync Harbor notes from the command
  line with the `harbor` CLI. Use whenever the user wants to capture, read,
  update, or manage notes, notebooks, tags, tasks, reminders, templates,
  attachments, or shared links in Harbor — especially building richly formatted notes (Markdown /
  HTML, checklists, tables, callouts, colors, note-to-note links, embedded files)
  and safely editing existing notes. Ideal for running Harbor as an AI notes
  assistant.
---

<!-- Harbor agent skill • v1.1.0 -->
<!-- Copyright (c) 2026 Cloudmanic Labs, LLC. All rights reserved. Date: 2026-06-22 -->

# Harbor CLI — managing notes from the terminal

Harbor is a notes service (think Evernote-class: notebooks, notes, nested tags,
attachments, search, reminders, sharing, version history, multi-device sync).
`harbor` is its official command-line client. It speaks the full API and prints
**styled tables by default** or **clean JSON with `--json`** — so it is equally
good for a human at a prompt and for an AI agent driving it programmatically.

This skill teaches you to use `harbor` to **create, read, edit, format, and
organize** a user's notes. Two companion files go deeper — read them when a task
needs them:

- **`formatting.md`** — the rich-formatting cookbook: Markdown/GFM, the HTML
  allowlist, colors and callouts, checklists, tables, embedding files, and
  note-to-note links. **Read this before building any non-trivial formatted note.**
- **`reference.md`** — the exhaustive command + flag reference for every domain.
  **Read this when you need a command or flag not covered below.**

---

## When to use this skill

Use it whenever the user wants to do anything with their Harbor notes, e.g.:

- "Add a note about …", "jot this down", "capture this" → **create a note**
- "Update / edit / revise / add to my note about …" → **edit a note**
- "Make me a checklist / meeting template / formatted doc" → **rich formatting**
- "Find my notes about …", "what did I write about …" → **search**
- "Organize / tag / file / move …" → **notebooks & tags**
- "Add a to-do", "what's due today", "mark that done" → **tasks**
- "Remind me to …", "share this note", "what changed in this note" → reminders,
  sharing, history

If the `harbor` command is not installed or the user is not logged in, see
**Setup** below before anything else.

---

## Setup (do this once, verify every session)

```bash
# Is the CLI installed?
harbor --version            # if "command not found": brew install HarborMyNotes/tap/harbor

# Is there a logged-in session? (prints email, scopes, expiry — exit 0 if logged in)
harbor whoami --json
```

- **Login is interactive** (`harbor login` prompts for a password that is never
  echoed). You, as an agent, generally **cannot** complete it — if `harbor whoami`
  fails with "not logged in", **stop and ask the user to run `harbor login`**.
  Do not try to script the password.
- Tokens are stored in `~/.config/harbor/credentials.json` (mode 0600) and
  **refresh transparently** — you never manage tokens by hand.
- Everything below assumes a logged-in session.

---

## Mental model (read this once)

- **Notebooks** contain **notes**. Every account has exactly one **default
  notebook**; a new note with no `--notebook` lands there.
- **Tags** are hierarchical (Evernote-style nesting) and are attached to notes
  many-to-many. A note can be in one notebook but carry many tags.
- A **note body is stored as a sanitized HTML fragment.** You write it as
  **Markdown (the CLI default)** or HTML (`--format html`); the server converts
  and sanitizes. You read it back as Markdown (`--format markdown`, best-effort)
  or as the exact stored HTML (`--format html`). Details: **`formatting.md`**.
- **IDs are UUIDs** (e.g. `9c2e7b10-1a2b-…`). Almost every command takes an id.
  Capture ids from JSON output — never guess them.
- **Deletes are a recoverable Trash by default** (`harbor trash restore`),
  expunged permanently only with `--permanent`.
- **`usn`** is a per-record version counter the sync engine uses; you mostly
  ignore it, but every mutation returns a fresh one.

---

## Golden rules for driving Harbor as an agent

1. **Use `--json` for anything you parse.** Tables are for humans; JSON is the
   contract. Pipe through `jq`.
2. **Capture the id the server returns** and reuse it. Create returns
   `{ "note": { "id": … }, "usn": … }` → the id is `.note.id`.
3. **Put the body on stdin or in a file — not in `--content` — for anything
   multi-line.** `--content "a\n\nb"` sends a *literal backslash-n*, not a line
   break (the CLI does no escape processing). Use `--stdin` (heredoc) or `--file`
   so real newlines reach the server. (In bash you *can* use `--content $'a\n\nb'`,
   but `--stdin`/`--file` is clearer and safer.)
4. **Markdown is the default input format.** Only pass `--format html` when you
   need HTML-only features (colors, embeds, precise structure — see
   `formatting.md`).
5. **To change part of an existing note, read-modify-write** (recipe below).
   `notes update --content/--file` **replaces the entire body** — it is not a
   partial patch. Use `notes append` only to add to the *end*.
6. **Confirm before destructive/outward-facing actions** — permanent deletes,
   emptying trash, publishing a note publicly, account deletion. Trashing
   (recoverable) is fine to do directly when asked.
7. **Don't invent flags.** If unsure, run `harbor <cmd> --help` or read
   `reference.md`.

---

## Core workflows

### Create a note

Single line is fine inline; for real content use a heredoc on stdin and **grab
the id**:

```bash
# Quick one-liner
harbor notes create --title "Buy milk" --content "Whole milk, 2%" --json

# Multi-line Markdown via stdin, capturing the new id for follow-up commands
NOTE_ID=$(harbor notes create --title "Q3 Plan" --stdin --json <<'MD' | jq -r '.note.id')
# Q3 Plan

## Goals
- Ship the mobile beta
- Cut onboarding time in half

## Risks
- Hiring timeline
MD
)
echo "created $NOTE_ID"
```

Useful create flags: `--notebook <id>` (else default notebook), `--format html`,
`--source-url <url>`, `--author "<name>"`. Full list: `reference.md`.

### Read a note

```bash
# The body as Markdown (best for understanding / editing plain notes)
harbor notes get "$NOTE_ID" --format markdown --json | jq -r '.content'

# The exact stored HTML (best when the note has colors/embeds/rich structure)
harbor notes get "$NOTE_ID" --format html --json | jq -r '.content'

# Metadata + a readable body preview (human view)
harbor notes get "$NOTE_ID"
```

### Edit an existing note — the read-modify-write pattern ⭐

This is the single most important workflow. `notes update` **replaces** the whole
body, so to make a *targeted* change you fetch the current body, modify it, and
write it back:

```bash
# 1. Fetch the current body to a temp file.
#    Plain note → markdown round-trips cleanly:
harbor notes get "$NOTE_ID" --format markdown --json | jq -r '.content' > /tmp/note.md
#    Rich note (colors/embeds/complex tables) → use HTML to avoid lossy conversion:
#    harbor notes get "$NOTE_ID" --format html --json | jq -r '.content' > /tmp/note.html

# 2. Edit /tmp/note.md however you like (add a section, fix a line, etc.).

# 3. Write the whole edited body back (match the --format to how you fetched it).
harbor notes update "$NOTE_ID" --file /tmp/note.md --json            # markdown (default)
# harbor notes update "$NOTE_ID" --file /tmp/note.html --format html  # html round-trip
```

Other edit shapes:

```bash
# Append to the END only (quick capture — no need to resend the body):
harbor notes append "$NOTE_ID" --content "- follow up with Dana" --json

# Change only metadata (title / notebook / attribution) — body untouched:
harbor notes update "$NOTE_ID" --title "Q3 Plan (final)" --json
harbor notes update "$NOTE_ID" --notebook "$ARCHIVE_NB" --json   # move to another notebook
```

> **Lossy-conversion warning:** `--format markdown` on read is *best-effort*. If a
> note contains colors, `<harbor-embed>` attachments, alignment, or intricate
> tables, **round-trip via HTML** (fetch `--format html`, edit, update
> `--format html`) so you don't silently drop that formatting. See `formatting.md`.

Every update is snapshotted — you can review or undo via `harbor history`
(see below), so edits are safe.

### Organize: notebooks & tags

```bash
# Notebooks
harbor notebooks list --json
harbor notebooks create --name "Work" --stack "Projects" --json     # --stack = grouping label

# Tags (hierarchical). Attach by NAME and the tag is created if missing — handy:
harbor notes tag "$NOTE_ID" --tag-name "Receipts" --json
harbor notes tag "$NOTE_ID" --tag-id "$TAG_ID" --json               # by existing id
harbor notes untag "$NOTE_ID" --tag-id "$TAG_ID" --json
harbor notes set-tags "$NOTE_ID" --tags "$T1,$T2" --json            # replace the whole set
harbor notes set-tags "$NOTE_ID" --tags "" --json                   # clear all tags

# List the notes under a notebook or a tag
harbor notes list --notebook "$NB_ID" --json
harbor tags notes "$TAG_ID" --json
```

### Search

Evernote-style query grammar (combine freely; bare words AND together):

```bash
harbor search 'budget' --json
harbor search 'tag:finance resource:pdf "q3 plan"' --json
harbor search 'report -draft intitle:weekly' --order -updated_at --json
```

Operators: `tag:`, `notebook:`, `intitle:`, `resource:image|pdf|audio|application|any`,
`created:YYYYMMDD..YYYYMMDD` (and `day-N`), `updated:…`, `"exact phrase"`,
`prefix*`, `-negate`. Full grammar: `reference.md` → Search.

### Reminders

Times accept epoch-ms, RFC3339, `YYYY-MM-DD`, or a relative offset like `in 2h` /
`in 3d`.

```bash
harbor reminders set "$NOTE_ID" --time "in 2h" --json
harbor reminders list --json                       # active reminders
harbor reminders list --due-before "in 24h" --json # overdue-ish view
harbor reminders complete "$NOTE_ID" --json
harbor reminders clear "$NOTE_ID" --json
```

### Tasks (standalone to-dos)

A task is its own syncable record, not a field on a note. A bare `YYYY-MM-DD`
due is stored date-only; a time-bearing value (`in 2h`, RFC3339, epoch-ms) is
timed.

```bash
harbor tasks create --title "Pay the invoice" --due 2026-08-01 --priority high --json
harbor tasks list --status today --json           # due today, still open
harbor tasks list --due-before "in 24h" --json    # upcoming/overdue
harbor tasks list --note "$NOTE_ID" --json        # a note's tasks (read-only)
harbor tasks update "$TASK_ID" --clear-due --json
harbor tasks done "$TASK_ID" --json               # recurring → advances instead
harbor tasks undone "$TASK_ID" --json
harbor tasks delete "$TASK_ID" --json
```

Recurrence is `daily|weekly|monthly|yearly`, `every:N:days|weeks|months|years`,
or an RRULE. Completing a recurring task rolls it to its next occurrence rather
than closing it. Tasks cannot be attached to a note from the CLI — a note's body
owns its tasks.

### Templates (reusable starting points)

```bash
harbor templates list --json
NEW_ID=$(harbor templates apply "$TPL_ID" --title "Standup $(date +%F)" --json | jq -r '.note.id')
harbor templates create --name "Meeting notes" --stdin <<'MD'
# Meeting — {fill in}
**Attendees:**

## Agenda

## Decisions

## Action items
- [ ]
MD
```

(Template content is copied verbatim into the new note; there is no token
expansion — fill placeholders after applying.)

### Sharing (public, read-only links)

Publishing makes a note readable by anyone with the link — **confirm with the
user first.**

```bash
harbor share publish "$NOTE_ID" --json | jq -r '.data.public_url'
harbor share publish "$NOTE_ID" --slug quarterly-plan --json
harbor share unpublish "$NOTE_ID" --json     # revoke (idempotent)
```

Encrypted notes can never be shared.

### Encryption (end-to-end, transparent)

Harbor supports client-side, zero-knowledge encryption. Set `HARBOR_PASSPHRASE`
and the CLI **decrypts notes on read automatically** and **encrypts on write**
when a notebook is `default_encrypt` (or with `--encrypt`). The server only stores
ciphertext — encrypted notes can't be searched, shared, or exported.

```bash
# Point HARBOR_PASSPHRASE at a secret manager (recommended):
export HARBOR_PASSPHRASE=$(op read "op://Vault/Harbor/passphrase")

harbor crypto setup                         # one time — LOST PASSPHRASE = UNRECOVERABLE
harbor crypto status                        # keystore present? unlocks?
harbor notes create --encrypt --title "Secret" --stdin <<<'top secret body'
harbor notes get "$NOTE_ID" --json | jq -r '.content'   # auto-decrypted
```

With `HARBOR_PASSPHRASE` set, `notes get/list`, `trash list`, and `reminders
list` all show plaintext; without it (or with the wrong one) you see ciphertext,
never an error. As an agent you generally **cannot** run `crypto setup`
interactively — if `harbor crypto status` shows no keystore, ask the user to set
it up. Full command list: `reference.md` → Encryption. Interop format:
`crypto/README.md`.

### Trash, restore, and history

```bash
harbor notes delete "$NOTE_ID"                 # → Trash (recoverable)
harbor trash list --json
harbor trash restore "$NOTE_ID"
harbor notes delete "$NOTE_ID" --permanent     # expunge (NOT recoverable) — confirm first

# Version history (forward-only; revert restores a past version as a new one)
harbor history list "$NOTE_ID" --json
harbor history show "$NOTE_ID" "$VERSION_ID" --json
harbor history revert "$NOTE_ID" "$VERSION_ID" --json
```

### Files / attachments

```bash
HASH=$(harbor files upload ./diagram.png --json | jq -r '.data.hash // .hash')
harbor files list --mime image/ --json
harbor files download "$HASH" --output ./diagram.png
```

Uploaded files are content-addressed by sha256. To **show** an image inside a
note body, embed it — see `formatting.md` → "Embedding files & images".

### Note-to-note links & backlinks

Link notes by putting a `harbor:note/<uuid>` link in the body (Markdown:
`[text](harbor:note/<uuid>)`). The server derives the link graph:

```bash
harbor notes links "$NOTE_ID" --json       # notes this note links TO
harbor notes backlinks "$NOTE_ID" --json   # live notes that link to this one
```

Details and exact syntax: `formatting.md` → "Linking notes".

---

## Rich formatting in one minute (then read formatting.md)

The note body is **sanitized HTML**, written as **Markdown by default**.

- **Plain structure** (headings, **bold**, *italic*, lists, links, code,
  blockquotes, `---` rules) → just write Markdown and `--stdin`/`--file`.
- **Checklists** → GFM task lists: `- [ ] todo` and `- [x] done`.
- **Tables** → GFM pipe tables.
- **Colors, background highlights, text alignment, callouts, embedded
  files/images, precise HTML** → need `--format html` (or inline HTML inside
  Markdown). The sanitizer keeps an allowlist: headings, lists, tables,
  blockquotes, code, emphasis, links (`http`, `https`, `mailto`, `harbor`), and a
  small inline-`style` set (`color`, `background-color`, `text-align`, font
  properties). It strips scripts, event handlers, and unsafe URLs.

```bash
# A formatted note with a checklist and a table, via stdin Markdown:
harbor notes create --title "Launch checklist" --stdin --json <<'MD'
# Launch checklist

## Pre-flight
- [x] Code freeze
- [ ] Smoke tests green
- [ ] Changelog written

| Owner | Task        | Status |
|-------|-------------|--------|
| Dana  | Marketing   | ✅      |
| Sam   | Infra       | ⏳      |
MD
```

**For colors, callouts, embeds, and note links, read `formatting.md` now** — it
has copy-paste recipes and the full allowlist.

---

## Scripting & automation notes

- **`--json`** everywhere you parse. Collections are
  `{ "data": [...], "paging": { limit, offset, total, has_more } }`; single
  mutations are `{ "note": {…}, "usn": N }`; a plain GET returns the object
  directly. Errors are `{ "error": { code, message, details, request_id } }`.
- **Paging:** lists default to 100, cap 500. Use `--limit`/`--offset` and check
  `.paging.has_more`. `--order` takes fields like `-updated_at,title` (`-` =
  descending).
- **Cheap listings:** `harbor notes list --meta --json` omits note bodies.
- **Exit codes:** `0` success, `1` any error, `3` the API was unreachable (worth
  retrying), `4` a plan limit blocked the write (never worth retrying). Safe to
  branch on `$?`. Errors go to stderr — with `--json` they are the error
  envelope, so you parse one shape either way.
- **Plan limits.** Harbor caps notes/notebooks/tags/files/tasks per plan. On
  exit `4`, stop and tell the user which limit they hit and that upgrading
  happens in the Harbor web app — the CLI cannot take payment. `harbor usage`
  shows used vs limit for everything (`∞` = unlimited) and `harbor plan` shows
  the current plan; check `harbor usage --json` before a bulk import rather than
  failing halfway through it.
- **Timestamps** are UTC epoch-milliseconds everywhere (e.g. `updated_at`).
- **No color / non-TTY:** output auto-plainifies; force with `--no-color`.
- **Targeting a non-default server** (rare; dev/staging): `--api-url` or
  `HARBOR_API_URL`. Normal users never set this.

---

## Safety & gotchas

- **`notes update` replaces the whole body.** Read-modify-write (above) for
  partial edits; `notes append` for end-only additions.
- **`--content` does not interpret `\n`.** Use `--stdin`/`--file` for multi-line.
- **Permanent deletes (`--permanent`), `trash empty`, `account delete`,
  `share publish` are consequential** — confirm with the user. `trash empty` and
  `account delete` require `--yes` (and a confirm phrase) in non-interactive use.
- **Encrypted notes** (`is_encrypted: true`) hold only ciphertext server-side:
  they can't be searched, appended to, shared, or converted — leave them alone
  unless the user has a client that handles encryption.
- **Markdown read-back is best-effort** — round-trip rich notes via HTML.
- **404 `not_found`** usually means a wrong or expunged id, or a trashed note
  fetched without `--deleted`/`--include-deleted`. Re-list to get the right id.
- **`note_too_large`** = body over 5 MiB; **`note_title_too_long`** = title over
  255 characters.

---

## Quick command map

| Want to… | Command |
|---|---|
| Create a note | `harbor notes create --title … --stdin` |
| Read a note | `harbor notes get <id> --format markdown --json` |
| Edit a note (targeted) | read-modify-write → `harbor notes update <id> --file …` |
| Add to the end | `harbor notes append <id> --content …` |
| List / filter notes | `harbor notes list [--notebook …] [--tag …] --json` |
| Search | `harbor search '<query>' --json` |
| Tag a note | `harbor notes tag <id> --tag-name …` |
| Notebooks | `harbor notebooks list \| create \| update \| delete` |
| Tags | `harbor tags list \| create \| update \| delete` |
| Reminders | `harbor reminders set \| list \| complete \| clear` |
| Tasks | `harbor tasks create \| list \| update \| done \| undone \| delete` |
| Templates | `harbor templates list \| apply \| create` |
| Share | `harbor share publish \| unpublish <id>` |
| Trash / restore | `harbor notes delete <id>` · `harbor trash restore <id>` |
| History | `harbor history list \| show \| revert <id>` |
| Files | `harbor files upload \| list \| download` |
| Links | `harbor notes links \| backlinks <id>` |
| Plan limits / usage | `harbor usage` · `harbor plan \| plan list` |
| Anything else | `harbor <cmd> --help` or read `reference.md` |

When in doubt: **`harbor --help`**, **`harbor <command> --help`**, or read
**`reference.md`** (full reference) and **`formatting.md`** (rich formatting).

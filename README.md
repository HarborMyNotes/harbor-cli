# Harbor CLI

A fast, friendly command-line client for the [Harbor](https://app.harbor.my) notes API.
`harbor` exposes the **entire** API — notebooks, notes, tags, sync, files,
search, sharing, and account management — as composable commands. Output is a
styled table by default and clean JSON with `--json`, so it is equally pleasant
for humans at a terminal and for scripts or AI agents.

```sh
harbor login
echo "# Standup\n\n- shipped the CLI" | harbor notes create --title "Standup" --stdin
harbor search "tag:work standup"
harbor notes list --json | jq '.data[] | {id, title}'
```

## Why a CLI?

- **Single static binary** — no runtime, no dependencies. Install and go.
- **Human-first, machine-ready** — beautiful colored tables by default;
  `--json` on **every** command for `jq`, scripts, and pipelines.
- **Built for AI agents** — every command is documented with `--help` and
  examples, errors are structured, and the full OpenAPI spec is one command
  away (`harbor openapi`). See [Using with AI agents](#using-with-ai-agents).
- **Transparent auth** — tokens are stored locally and refreshed automatically;
  you log in once.

## Install

### Homebrew (macOS / Linux)

```sh
brew tap HarborMyNotes/harbor https://github.com/HarborMyNotes/harbor-cli
brew install harbor
brew upgrade harbor   # later, to update
```

### Prebuilt binaries

Download the binary for your platform from the
[latest release](https://github.com/HarborMyNotes/harbor-cli/releases/latest)
(`harbor-<os>-<arch>`), make it executable, and put it on your `PATH`:

```sh
curl -L -o harbor https://github.com/HarborMyNotes/harbor-cli/releases/latest/download/harbor-darwin-arm64
chmod +x harbor && sudo mv harbor /usr/local/bin/
```

### From source

```sh
git clone https://github.com/HarborMyNotes/harbor-cli
cd harbor-cli
make build          # produces ./build/harbor
# or: go install github.com/HarborMyNotes/harbor-cli@latest
```

### Shell completion

```sh
harbor completion zsh  > "${fpath[1]}/_harbor"   # zsh
harbor completion bash > /etc/bash_completion.d/harbor
harbor completion fish > ~/.config/fish/completions/harbor.fish
```

## Quick start

```sh
# 1. Log in (prompts for your password; nothing is echoed).
harbor login --email you@example.com

# 2. Create a notebook and a note (Markdown is the default input format).
harbor notebooks create --name "Work"
harbor notes create --title "Quarterly plan" --content "# Goals\n\n- ship it"

# 3. Tag it, then find it.
harbor notes tag <note-id> --tag-name planning
harbor search "tag:planning intitle:quarterly"

# 4. Pipe JSON into your own tools.
harbor notes list --json | jq -r '.data[].title'
```

## Authentication & credentials

`harbor login` performs an OAuth2 password grant and stores the resulting
access + refresh tokens in:

```
~/.config/harbor/credentials.json   (file mode 0600)
```

- The **access token is refreshed transparently** — proactively before it
  expires and reactively on a `401`, rotating the single-use refresh token and
  persisting the new pair. You normally never think about it; `harbor auth refresh`
  forces it for diagnostics.
- `harbor whoami` (alias `harbor auth status`) shows your session: email,
  scopes, token expiry, and device.
- `harbor logout` revokes the session server-side and deletes the local
  credentials. `--all-devices` signs out everywhere.
- The API endpoint defaults to the production server. Maintainers can target a
  different environment with `--api-url`, the `HARBOR_API_URL` environment
  variable, or the `api_url` field in the credentials file — customers never
  need to.

## Global flags

| Flag | Description |
|---|---|
| `--json` | Emit raw JSON instead of formatted tables (honored by every command). |
| `--no-color` | Disable ANSI color. Also honors `NO_COLOR` and auto-disables when piped. |
| `-v, --verbose` | Include the `request_id` and HTTP status on errors. |
| `--utc` | Render timestamps in UTC instead of local time. |
| `--api-url` | Override the API base URL (maintainer use). |

## Command reference

Run `harbor <command> --help` for full flags and examples on any command.

### Authentication
| Command | Description |
|---|---|
| `harbor login` | Log in with email + password. |
| `harbor logout` | Revoke the session and clear local credentials (`--all-devices`). |
| `harbor whoami` | Show the current session. |
| `harbor auth refresh` | Force a token refresh. |
| `harbor auth verify-email --token …` | Verify your email. |
| `harbor auth resend-verification` | Resend the verification email. |
| `harbor auth forgot-password --email …` | Request a password reset. |
| `harbor auth reset-password --token …` | Complete a password reset. |

### Notebooks
| Command | Description |
|---|---|
| `harbor notebooks list` | List notebooks (`--stack`, `--order`, paging). |
| `harbor notebooks get <id>` | Show one notebook. |
| `harbor notebooks create --name …` | Create a notebook (`--stack`, `--default-encrypt`). |
| `harbor notebooks update <id>` | Update; `--make-default` promotes to default. |
| `harbor notebooks delete <id>` | Delete (`--notes move_to_default\|trash`). |

### Notes
| Command | Description |
|---|---|
| `harbor notes list` | List notes (`--notebook`, `--tag`, `--meta`, paging). |
| `harbor notes get <id>` | Show a note (`--format markdown\|html`). |
| `harbor notes create` | Create a note (`--content`/`--file`/`--stdin`, `--format`). |
| `harbor notes update <id>` | Update fields and/or body. |
| `harbor notes append <id>` | Append a fragment to the body. |
| `harbor notes delete <id>` | Trash (or `--permanent` to expunge). |
| `harbor notes tags <id>` | List a note's tags. |
| `harbor notes tag <id>` | Attach a tag (`--tag-id` or `--tag-name`). |
| `harbor notes set-tags <id> --tags …` | Replace the full tag set. |
| `harbor notes untag <id> --tag-id …` | Detach a tag. |
| `harbor notes links <id>` | Outgoing links. |
| `harbor notes backlinks <id>` | Incoming links. |
| `harbor notes audit <id>` | Per-note change audit log. |
| `harbor notes export <id>` | Export one note to a file: Markdown, or a ZIP when it has attachments (`--output`, `--zip`). |

### Tags
| Command | Description |
|---|---|
| `harbor tags list` | List tags (`--parent`, `--top-level`). |
| `harbor tags get <id>` | Show one tag. |
| `harbor tags create --name …` | Create a tag (`--parent`). |
| `harbor tags update <id>` | Rename / re-parent (`--top-level`). |
| `harbor tags delete <id>` | Delete (`--children reparent_to_grandparent\|orphan`). |
| `harbor tags notes <id>` | List notes carrying a tag. |

### Files
| Command | Description |
|---|---|
| `harbor files list` | List files with their linked notes (`--mime`, `--note-id`, …). |
| `harbor files check` | Check whether a blob exists (`--hash` or `--file`). |
| `harbor files upload <path>` | Upload a file (multipart; server computes the hash). |
| `harbor files get <hash>` | Show the presigned download URL + metadata. |
| `harbor files download <hash>` | Download bytes (`--output`, `--raw`). |

### Search
| Command | Description |
|---|---|
| `harbor search "<query>"` | Full-text search across notes and attachments. |
| `harbor search coordinates --resource-id …` | OCR highlight boxes for an attachment. |

The query grammar supports `tag:`, `notebook:`, `intitle:`, `resource:`,
`created:`/`updated:` date ranges, `"exact phrases"`, `prefix*`, and `-negation`.
See `harbor search --help`.

### Sync
| Command | Description |
|---|---|
| `harbor sync pull` | Pull changes since a USN (`--after-usn`, `--all`). |
| `harbor sync push --file …` | Push a batch of change envelopes. |
| `harbor sync devices` | List devices, scope max USN, and GC floor. |
| `harbor sync register-device` / `remove-device` | Manage devices. |
| `harbor sync ack` | Advance a device's acked cursor. |

### Advanced note features
| Command | Description |
|---|---|
| `harbor history list/show/revert <note-id>` | Note version history. |
| `harbor trash list/restore/expunge/empty` | The recycle bin. |
| `harbor templates list/get/create/update/delete/apply` | Note templates. |
| `harbor shortcuts list/get/create/update/delete/reorder` | Sidebar shortcuts. |
| `harbor reminders list/set/complete/clear` | Note reminders. |
| `harbor tasks list/get/create/update/done/undone/delete` | Standalone tasks (due dates, recurrence, priority). |
| `harbor share publish/unpublish/open` | Public read-only sharing. |

### Account & system
| Command | Description |
|---|---|
| `harbor profile get/update/change-password/…` | Manage your profile. |
| `harbor sessions list/revoke/revoke-others/revoke-all` | Manage login sessions. |
| `harbor settings get/set` | Account preferences. |
| `harbor usage` | Usage against your plan's limits (notes, notebooks, tags, files, tasks; `∞` = unlimited). |
| `harbor plan` / `harbor plan list` | Your current plan, and the plans on offer. Upgrading happens in the web app. |
| `harbor support` | Contact Harbor support (category, subject, message, attachments). |
| `harbor account export/exports/export-status/export-delete` | Data export: ENEX, HTML or Markdown, whole account or one notebook, with `--wait` and `--download`. |
| `harbor account clear/clear-status` | Destroy everything IN the account, keeping the account, its login and its plan. Immediate and irreversible; waits for the job to finish. |
| `harbor account delete/cancel-delete` | Schedule (and cancel) account deletion. |
| `harbor import enex <file>` / `harbor export enex` | Evernote ENEX interchange. The import uploads straight to storage in chunks (any size), then waits for the import to finish — `--no-wait` returns as soon as it is queued, and `--notify-email` asks for a completion email. `harbor import abort <job-id>` cancels an upload that never finished. |
| `harbor status` | Server health (liveness, readiness, version). |
| `harbor api-version` | Server build version. |
| `harbor openapi` | Fetch the OpenAPI 3.0 spec. |
| `harbor skill install/show/path` | Install the bundled AI-agent skill (Claude Code, Codex, Cursor — see below). |

## Using with AI agents

`harbor` is designed to be driven by automated agents and scripts:

- **`--json` everywhere.** Every command prints the API's JSON shape verbatim
  with `--json`; stdout is data only (logs and errors go to stderr), so it
  pipes cleanly into `jq` and friends.
  ```sh
  # All note titles in a notebook:
  harbor notes list --notebook <id> --json | jq -r '.data[].title'

  # Just attachment hits from a search:
  harbor search invoice --json | jq '.data[] | select(.type=="attachment")'
  ```
- **Pipe content in.** Create or append notes from stdin — ideal for generated
  content: `generate_report | harbor notes create --title Report --stdin`.
- **Stable exit codes.** `0` on success, non-zero on error (see below), so
  agents can branch on failures.
- **Structured errors.** API errors carry a stable `code`; add `--verbose` to
  surface the `request_id` for support.
- **Self-describing API.** `harbor openapi --output harbor.json` fetches the
  full OpenAPI 3.0 spec for tooling or codegen.

### Install the agent skill

`harbor` ships a self-contained **skill** that teaches an AI coding agent how to
drive the CLI — creating, **editing**, and **richly formatting** notes
(Markdown/HTML, checklists, tables, colors, embedded files, note-to-note links),
organizing notebooks and tags, searching, reminders, and sharing. Installing it
turns your agent into a capable Harbor notes assistant.

It installs in each agent's native form (`--agent`):

```sh
harbor skill install                  # Claude Code → ~/.claude/skills/harbor/ (a skill dir)
harbor skill install --agent codex    # Codex       → ~/.codex/AGENTS.md (a managed block)
harbor skill install --agent cursor   # Cursor      → .cursor/rules/harbor.mdc (a rule file)

harbor skill install --force          # reinstall after a CLI upgrade
harbor skill install --project        # the project-scoped variant
harbor skill show                     # print the skill (also: show formatting.md | reference.md)
harbor skill path --agent codex       # print where it installs
```

The skill is embedded in the binary, so a fresh `harbor` upgrade carries the
latest skill. Re-installing **backs up any existing copy** first (a timestamped
`.backup-*`), so your edits are never lost — and for Codex's shared `AGENTS.md`
only the delimited Harbor block is replaced, leaving the rest of the file intact.
For Cursor and Codex the deep-dive guides aren't copied to disk; the skill tells
the agent to fetch them on demand with `harbor skill show formatting.md` /
`reference.md`. Use `--dir <path>` to target any other directory.

## Exit codes

Every diagnostic goes to **stderr**, so stdout stays data-only and pipes stay
clean. Two failure classes a script would react to differently get their own
code; everything else is `1`.

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | An error occurred (a human-readable message is printed to stderr). |
| `3` | The API could not be reached — DNS, refused connection, TLS, or timeout. Nothing was decided by the server, so a retry may work. |
| `4` | A plan limit blocked the write: either the resource is at your plan's cap, or the account is read-only for being over its limits. Retrying will never help — free up room or upgrade. |

```sh
harbor notes create --title "Meeting" --stdin < notes.md
case $? in
  0) ;;
  4) echo "Out of room on this plan — see 'harbor usage'." >&2; exit 1 ;;
  3) echo "Harbor unreachable; will retry." >&2 ;;
  *) echo "Failed." >&2; exit 1 ;;
esac
```

With `--json`, errors are written to stderr as the API's error envelope
(`{"error": {code, message, details, request_id}}`) rather than prose, so a
script parses one shape whether the command succeeded or failed.

**A command never exits `0` for work it did not do.** That includes the cases
where the server answers `200`:

- **A subcommand that does not exist** is an error (`1`), not a help screen.
  `harbor files delete` fails, because there is no such command — only a bare
  `harbor files` prints the help. Stray positional arguments are refused the
  same way rather than silently ignored.
- **`harbor sync push`** reports each change's outcome inside a `200`, so it
  exits `4` when a refusal was a plan limit and `1` for any other refusal. The
  per-change results are still printed either way — that is how a client learns
  which USNs landed and which changes to re-resolve. A `conflict` is not a
  refusal (the server hands back the record you need to resolve it), so that
  stays `0`.
- **`harbor import enex`** waits for the import and exits `1` when it came back
  `partial`, `failed` or `aborted`, or reported failed notes; the counters are
  still printed, and `harbor import status <job-id>` has the per-note reasons.
  With `--no-wait` the import has not run yet, so a merely *enqueued* one stays
  `0` — read its outcome from `harbor import status`.
- **`harbor status`** exits `1` when the server is not ready, after printing the
  readiness table.

## Plans & limits

Harbor plans cap how many notes, notebooks, tags, files, and tasks an account
holds. When a create is refused, the CLI says which limit you hit, what it
means, and where to upgrade — and exits `4`:

```
$ harbor notebooks create --name "Q3 Planning"
Error: You've reached your plan's limit of 3 notebooks. Upgrade to add more.
  code: plan_limit_reached
  You are using 3 of 3 notebooks on the starter plan.
  Only notebooks are blocked — everything else still works.
  To free a slot, delete notebooks you no longer need with
  'harbor notebooks delete <id>' — that frees it immediately.
  Upgrade at https://app.harbor.my/settings/plan — plans are changed in the
  Harbor web app, not the CLI.
  Run 'harbor usage' to see every limit on this plan.
```

The remedy is per resource, because they do not free a slot the same way:
trashing a **note** frees nothing until it is expunged, and **files** have no
delete at all — an attachment is released only when the notes holding it are
permanently deleted.

`harbor usage` shows where you stand before you hit a wall, and `harbor plan`
shows what you are subscribed to. **Billing is never handled in the CLI** —
these commands read your plan and point you at the web app (or the App Store /
Google Play, if that is where you subscribed).

## Development

See [CLAUDE.md](CLAUDE.md) for the architecture, the "add a new endpoint"
recipe, and the testing approach.

```sh
make build         # build ./build/harbor
make test          # go test ./...
make lint          # gofmt + go vet
make cross-build   # build release binaries for all platforms
```

## License

[MIT](LICENSE) © Cloudmanic Labs, LLC.

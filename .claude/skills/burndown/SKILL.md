---
name: burndown
description: >-
  Drain the harbor-cli queue on the Harbor org board: take every open harbor-cli
  issue at one Priority level (default High; Spicer can say critical/medium/low), strictly
  one at a time — implement it via the /github-issue playbook, verify it, have a fresh-eyes
  agent review the PR, then merge WITHOUT waiting for Spicer's approval. Attended by
  default: block and ask him whenever something needs him. If he says he's leaving ("I'm
  going to bed", "walking away", etc.), go unattended: never block, skip or park blocked
  issues with an explanatory comment on the issue (and PR), and get through as many issues
  as possible. Use ONLY when Spicer explicitly invokes /burndown — never auto-invoke.
argument-hint: "[critical|high|medium|low] [optionally: 'I'm going to bed' → unattended]"
---

<!-- Created 2026-08-06 · Copyright (c) 2026 Cloudmanic Labs, LLC. All rights reserved. -->

# Burndown — drain the harbor-cli priority queue

Spicer ranks harbor-cli issues with the **Priority** field on the Harbor org board.
This skill works that queue end-to-end: pick the next issue at the chosen level, implement
it, verify it, get it reviewed by an agent with no prior context, merge it, mark it Done,
and move to the next one — until the level is drained. The fresh-eyes review **replaces
Spicer's PR approval**; that is the deal that authorizes merging without him.

## Parse the invocation

The args are free text; pull two things out of them:

- **Priority level** — one of `critical`, `high`, `medium`, `low`. Default: **high**.
  Work ONLY that level; no cascading down when it's drained. If the level is High (the
  default) and Critical harbor-cli items also sit in the queue, say so in one line up
  front — but don't work them unless told to.
- **Attendance** — default **attended**. Any statement that Spicer is leaving or
  unavailable — "I'm going to bed", "walking away", "heading out", "AFK", "see you in the
  morning", anything like that — means **unattended**. This applies to the invocation args
  AND to any message he sends mid-run: "going to bed" flips to unattended from that point;
  "I'm back" flips to attended. The words `attended` / `unattended` work too.

## Attended vs unattended

**Attended (default — he said nothing about leaving):** the moment something needs Spicer
— an ambiguous requirement, a design/parity call, a spec that doesn't fit the platform, a
login or credential problem, a destructive step, a fresh-eyes disagreement you can't
resolve — **stop and ask him, and wait.** Do not skip the issue, do not guess.

**Unattended (he said he's leaving):** the goal is throughput — get through as many
issues as possible, and leave everything that needed him waiting for his return with the
reason written down where he'll see it. Never block on a question. When an issue needs him:

- **Blocked before real work started** → comment on the issue: what you need from him, why
  you couldn't proceed without it, and what you'll do once he answers. Leave Status =
  Todo. Move to the next issue.
- **Blocked mid-work, PR already open** → push the branch, mark the PR **draft**, and
  comment on BOTH the issue and the PR: what's blocking, the state the work is in, what
  remains. Leave Status = In Progress. Move to the next issue.
- **Before skipping, read the issue's comments.** If a previous run already posted the
  same blocker and Spicer replied, his reply IS the answer — use it and proceed. If he
  hasn't replied, don't post the same question twice; just note the issue in the end-of-run
  summary.
- A genuine ambiguity you'd have asked about in attended mode is a skip, not a guess.
- **If something systemic breaks** (main is red, the API is unreachable, login fails,
  network down) so every remaining issue would fail the same way, stop the run with one
  clear summary — don't spray ten identical failure comments across the board.

## Every pause gets a TL;DR

**Whenever you stop and hand control back to Spicer, end the message with a short
plain-language TL;DR** — the same thing `/tldr` would produce, appended under a
`**TL;DR**` heading. He reads the TL;DR first and decides from there whether to scroll up
for the full detail. He should never have to type `/tldr` to get it.

- **Write the full version too.** The TL;DR is an addition, not a replacement — keep the
  detailed explanation above it.
- **Only when you're actually pausing** — asking a question, reporting a blocker, finishing
  an issue and handing back, or ending the run. Narrating progress mid-work ("running the
  tests now", "the fresh-eyes agent found two things, fixing them") is thinking out loud,
  not a pause: no TL;DR.
- **Shape:** a few bullets, non-technical, what he needs to know and what needs his
  decision. Per his global preferences — bottom line first, no drama, no jargon.

## Keep the parent context light — delegate

**Push work into subagents wherever it can go.** The parent agent is the orchestrator: it
owns the queue, the decisions, and what gets said to Spicer. It should not be the thing
that reads twelve files to find a call site or scrolls a thousand lines of test output.
A burndown run can span many issues, and a parent that fills its context on issue #1 gets
worse at issue #5.

Delegate by default:

- **Codebase reconnaissance** — "where does X live", "what calls Y", "how does this repo do
  Z" → an `Explore` or `general-purpose` agent. Ask for the conclusion and the
  `file:line` anchors, not file dumps.
- **The fresh-eyes review** (step 4 below) — already a subagent, and mandatory.
- **Self-contained implementation** of a well-specified change, when the spec is settled and
  the acceptance criteria are unambiguous. Hand over the issue number, the plan, and the
  repo conventions; get back a summary of what changed.
- **Noisy verification** — long test runs, multi-step CLI E2E walkthroughs against a test
  environment, log spelunking. Ask for a verdict plus the few lines of output worth pasting
  into the PR.
- **Research across repos** — reading the server docs in `app.harbor.my`, checking what
  another client did for parity.

Keep in the parent: the queue and board updates, choosing the next issue, the merge, every
judgment call about scope or ambiguity, and everything said to Spicer. **Never delegate a
decision that should stop and ask him** — a subagent has no way to reach him, so an
ambiguity handed to a subagent silently becomes a guess.

Run independent subagents concurrently in one message. Verify what a subagent claims before
acting on it — agents get things confidently wrong, and the parent is accountable for what
lands.

## The queue

All the IDs, so nothing needs discovering at runtime:

| Thing | Value |
|---|---|
| Project | HarborMyNotes org project **#1**, node id `PVT_kwDOEm_6zs4Behal` |
| Status field | `PVTSSF_lADOEm_6zs4BehalzhY688g` — Todo `f75ad846` · In Progress `47fc9ee4` · Done `98236657` |
| Priority field | `PVTSSF_lADOEm_6zs4BehalzhZZ0bQ` — Critical `cf150f8f` · High `ab481900` · Medium `2990a77b` · Low `a55a730c` (read-only for this skill — never set Priority) |

Build the queue (write `board.json` to the session scratchpad, never into the repo):

```bash
gh project item-list 1 --owner HarborMyNotes --limit 2000 --format json > "$SCRATCHPAD/board.json"
jq -r --arg p "High" '
  .items[]
  | select(.repository == "https://github.com/HarborMyNotes/harbor-cli"
           and .content.type == "Issue"
           and .priority == $p
           and ((.status // "Todo") == "Todo"))
  | [.id, "#\(.content.number)", .title] | @tsv' "$SCRATCHPAD/board.json"
```

- ⚠️ `.repository` in this JSON is the **full URL** (`https://github.com/HarborMyNotes/harbor-cli`),
  not `owner/repo` — the obvious filter silently matches nothing.
- `--limit 2000` matters: the board holds well over 1000 items and the default limit
  truncates.
- Only `Status = Todo` (or unset) is eligible. `In Progress` belongs to someone else —
  leave it alone and mention it in the summary. `Done`/closed is finished.
- **Take issues in the order the board returns them** (top of the column first) — that
  lets Spicer re-rank the queue by dragging cards.
- **Refresh the queue after every merge.** Priorities and statuses move while you work.
- Before starting an issue, confirm it's still open:
  `gh issue view <N> --repo HarborMyNotes/harbor-cli --json state,title`.
- Empty queue → say so and stop.

## Working one issue

Strictly **one issue at a time** — no parallel worktrees, no batching. For each:

1. **Announce it** — number and title — so an attended Spicer can redirect before work
   starts.
2. **Run the `/github-issue` playbook** (invoke the `github-issue` skill with the issue
   number; if that fails, read `~/.claude/commands/github-issue.md` and follow it). That
   covers: read the body and every comment, download and actually view attached images,
   board → In Progress (IDs above — no discovery needed), checkout main + pull, branch
   `issue-<N>-<title>`, implement per repo conventions, tests, commit, push, PR titled
   `Fix #<N>: <title>` with `Closes #<N>`. Print the PR URL — and again on every
   subsequent push.
3. **Verify like a fresh QA, not like the author.** `make lint && make test` must be
   clean, and new code needs tests in its sibling `X_test.go` (one test file per file —
   see `CLAUDE.md`). Unit tests alone are NOT verification for anything user-facing:
   `make build` and actually **run the real command** — check the table output, the
   `--json` output, the exit code, and the error text a user would see. Walk the issue's
   acceptance criteria one by one. Paste the real terminal output into the PR. Test
   against a PR-preview sprite or `dev-cli@harbor.my` (see `CLAUDE.md`) — **never sign up
   for a production account**.
4. **Fresh-eyes review — the gate that replaces Spicer's approval.** Launch a NEW
   general-purpose agent with zero implementation context. Give it only: the repo, the
   issue number, and the PR number. Have it read the issue (body + comments), read the
   full diff, and judge — does the PR actually satisfy the acceptance criteria? Bugs?
   Parity violations? Convention breaks? Fix everything real it finds, push (print the PR
   URL), then re-review with another clean agent until one comes back with no blocking
   findings. If three rounds don't converge, that's a blocker: ask (attended) or park with
   comments on issue + PR (unattended).
5. **CI green.** `gh pr checks <PR> --watch`. Never merge red. An unrelated flake is
   still a stop: rerun it once; if it stays red, treat as blocked.
6. **Merge** — no approval needed at this point. Squash, delete the branch remote and
   local, back to main:
   ```bash
   gh pr merge <PR> --squash --delete-branch
   git checkout main && git pull
   git branch -d issue-<N>-<...> 2>/dev/null || true   # local deletion is its own step — verify it's gone
   ```
7. **Confirm the issue closed** (`Closes #<N>` should have done it; close it manually if
   not) and set the board item's **Status = Done**:
   ```bash
   gh project item-edit --id <ITEM_ID> --project-id PVT_kwDOEm_6zs4Behal \
     --field-id PVTSSF_lADOEm_6zs4BehalzhY688g --single-select-option-id 98236657
   ```
8. **One short paragraph to the chat** — issue, merged PR link, what shipped — then
   refresh the queue and take the next issue.

## Hard rules

- **harbor-cli issues only.** Other repos' items never enter the queue, even when a fix
  would be "easy".
- **Never merge without all three:** verification done, a clean fresh-eyes review, CI
  green. If the merge itself is refused (branch protection, conflicts you can't cleanly
  resolve), that's a blocker — ask or park.
- **Every CLAUDE.md rule still stands** — repo and workspace: `dev-cli@harbor.my` for
  testing, never create production accounts, never touch `me@spicer.cc` or
  `harbormaster@harbor.my`, design parity ("spec doesn't fit → stop and ask" — in
  unattended mode that means comment + skip, never improvise), PR URLs printed on every
  push, Cloudmanic header + doc comments on new code.
- **`go test ./...` must pass with no network and no config.** A test that needs a live
  API or a real `~/.config/harbor` is a broken test, not a passing verification.
- **Merging is the end of an issue.** Every merge to `main` fires `release.yml`, which
  auto-bumps the patch version, cuts the GitHub release, and updates the formula in
  `HarborMyNotes/homebrew-harbor` on its own. Never hand-bump a version, cut a tag, or
  edit the formula — unless an issue explicitly asks for it.
- **Don't work around a blocker just to keep a streak going.** A skipped issue with a
  clear comment is a good outcome; a merged guess is not.

## End of run

When the queue is drained (or the run stops early), report in tight `/tldr` shape — this
is what Spicer reads when he comes back:

```markdown
**Burndown: <level> — <n> merged, <n> skipped/parked**
- ✅ #<N> <title> — <PR URL>
- ⏸ #<N> <title> — parked: <one-line reason> (comments on issue + PR)
- ⏭ #<N> <title> — skipped: <one-line reason> (comment on issue)

**What you need to know**
- <only what changes what he does next — nothing about process>

**Decisions needed from you**
- <one bullet per parked/skipped question, or "Nothing — queue is clean.">
```

<!-- Harbor agent skill — formatting cookbook • v1.1.0 -->
<!-- Copyright (c) 2026 Cloudmanic Labs, LLC. All rights reserved. Date: 2026-06-22 -->

# Formatting Harbor notes — the rich-content cookbook

Everything you need to build **richly formatted** Harbor notes from the CLI:
the storage model, the Markdown you can use, the HTML allowlist (for colors,
callouts, alignment), embedding files/images, and linking notes to each other.

> Read this alongside `SKILL.md`. The commands here assume a logged-in session
> and use `--stdin`/`--file` for multi-line bodies (never `--content` for
> multi-line — it does not interpret `\n`).

---

## 1. The storage model (know this first)

- A note body is stored as a **sanitized HTML fragment** — a documented
  *allowlist subset* of standard HTML. It is **not** Markdown and **not**
  Evernote's ENML. HTML is what is stored and what renders.
- On **write**, you pick how your input is interpreted with `--format`:
  - `--format markdown` *(the CLI default)* — CommonMark + GFM, converted to HTML
    server-side. **Raw HTML inside your Markdown is passed through and then
    sanitized**, so you can mix Markdown and the occasional HTML tag.
  - `--format html` — you supply the HTML fragment directly (still sanitized).
- On **read**, `harbor notes get <id> --format`:
  - `markdown` — the body converted **best-effort** back to Markdown (lossy for
    colors/embeds/complex structure, and it drops collapsed-outline state — see
    section 4).
  - `html` — the **exact stored HTML** (lossless; use this to round-trip rich
    notes).
  - Neither is the way to produce a Markdown **file**. `harbor notes export <id>`
    renders one on the server — higher fidelity than the read-side conversion,
    and it brings the attachments — but it is a document, not a body: it carries
    YAML front matter and the title as a heading, so it must not be written back
    with `notes update`.
- **Size cap: 5 MiB** per body (`note_too_large` if exceeded).
- **Encrypted notes** store opaque ciphertext — none of this applies to them.

**Rule of thumb:** reach for Markdown for everything it can express; switch to
`--format html` (or inline a few HTML tags inside Markdown) only for colors,
highlights, alignment, callouts, and embeds.

---

## 2. Markdown you can rely on (CommonMark + GFM)

All standard CommonMark plus GitHub-Flavored extensions. (Outer fence shown with
four backticks so the inner code fence stays intact.)

````markdown
# H1   ## H2   ### H3

**bold**  *italic*  ~~strikethrough~~  `inline code`

- bullet
- nested
  - child
1. ordered
2. list

> Blockquote — good for quotes and simple callouts.

[Link text](https://example.com)        <!-- http/https/mailto are allowed -->

---                                       <!-- horizontal rule -->

```js
// fenced code block with a language hint
const x = 1;
```
````

### Checklists (GFM task lists)

```bash
harbor notes create --title "Release checklist" --stdin --json <<'MD'
# Release checklist
- [x] Cut release branch
- [ ] QA sign-off
- [ ] Publish notes
MD
```

`- [ ]` is unchecked, `- [x]` is checked. To **toggle** an item later, use the
read-modify-write pattern from `SKILL.md` (fetch the body, flip `[ ]`↔`[x]`,
`notes update --file`).

### Tables (GFM pipe tables)

```markdown
| Quarter | Revenue | Status |
|--------:|:-------:|:-------|
| Q1      | $1.2M   | closed |
| Q2      | $1.6M   | open   |
```

Alignment is set by the `:` in the header separator (`:--` left, `:-:` center,
`--:` right).

---

## 3. Colors, highlights, alignment & callouts (needs HTML)

Markdown can't express color, so use HTML. The sanitizer keeps a **filtered
inline-`style` allowlist** — only these properties survive:

- `color`
- `background-color`
- `text-align`
- font properties (`font-weight`, `font-style`, `font-size`, `font-family`)

…plus semantic `class` attributes, the safe link schemes `http`, `https`,
`mailto`, `harbor`, and a short list of `data-` attributes Harbor uses to store
display state (`data-type="checklist"` and `data-checked` on checklists,
`data-collapsed` on folds — section 4). Everything else (scripts, `on*` handlers,
`position`, arbitrary CSS, `javascript:` URLs, disallowed tags) is **stripped**.

You can drop these tags straight into a Markdown body (they pass through), or
write the whole note with `--format html`.

### Colored / highlighted text

```html
<span style="color:#c0392b;">Red warning text</span>
<span style="background-color:#fff3cd;">Yellow highlight</span>
<strong style="color:#1e8e3e;">Bold green</strong>
```

### Centered / right-aligned blocks

```html
<p style="text-align:center;">Centered heading line</p>
<div style="text-align:right;">Right-aligned signature</div>
```

### A callout / admonition box

There's no special "callout" element — compose one from an allowed block plus a
background color:

```html
<div style="background-color:#e7f3fe;">
  <strong>ℹ️ Note:</strong> deploys are frozen until Monday.
</div>
```

(Properties outside the allowlist, like `border` or `padding`, may be filtered;
the `background-color` and text always survive, so the box still reads as a
callout. For maximum durability, lead with an emoji + bold label as above.)

### Worked example — a fully formatted note via `--format html`

```bash
harbor notes create --title "Incident report" --format html --stdin --json <<'HTML'
<h1>Incident report — API latency</h1>
<p style="background-color:#fdecea;"><strong style="color:#c0392b;">SEV-2</strong> — resolved.</p>
<h2>Timeline</h2>
<table>
  <tr><th>Time</th><th>Event</th></tr>
  <tr><td>09:14</td><td>Alerts fired</td></tr>
  <tr><td>09:40</td><td>Rollback complete</td></tr>
</table>
<h2>Action items</h2>
<ul data-type="checklist">
  <li data-checked="false">Add latency alarm</li>
  <li data-checked="true">Post-mortem scheduled</li>
</ul>
HTML
```

> Prefer Markdown when you can — it's less verbose and round-trips more cleanly.
> Use `--format html` (like above) when the note leans heavily on colors,
> highlights, alignment, or embeds.

---

## 4. Collapsible outlines (`data-collapsed`)

A heading or a list item can be **folded**: the app draws an arrow beside it and
hides what is nested underneath. The fold is stored in the note, so it syncs
between devices — as one attribute on the element itself.

```html
<h2 data-collapsed="true">Q3 plan</h2>
<p>Hidden until the reader expands the heading.</p>

<ul>
  <li data-collapsed="true">Hiring
    <ul><li>Backfill the SRE role</li></ul>
  </li>
</ul>
```

Fold a bullet and its nested list hides; fold a heading and the whole section
beneath it hides. Rules that matter:

- **Only `<li>` and `<h1>`–`<h3>` can carry it.** Anywhere else — `<h4>` and
  below, `<p>`, `<div>` — the sanitizer drops it. Checklist items
  (`<li data-checked="…">`) fold like any other list item.
- **Absent means expanded. Never write `data-collapsed="false"`.** It is accepted
  from other clients and means exactly what writing nothing means, so in a body
  you are generating it is noise.
- **An item with nothing nested under it has nothing to fold** — no arrow, and the
  marker just sits there.
- **Nothing is hidden from you.** Folding changes what the app draws and nothing
  else: every word stays in the body, comes back from `notes get`, and stays
  searchable. The terminal and every export render a folded outline in full.
- **It survives an HTML write only.** Markdown has nowhere to put an attribute, so
  `notes update --format markdown` (the default) hands the server a body with no
  folds and the note comes back flat. The CLI prints a warning on stderr when a
  Markdown write is about to do that — read *and* write with `--format html` to
  keep them.

**Pre-folding a long outline is worth doing.** A plan somebody has to read is
easier to take in when the top level is visible and the detail is tucked away:

```bash
harbor notes create --title "Migration plan" --format html --stdin --json <<'HTML'
<h1>Migration plan</h1>
<h2 data-collapsed="true">Phase 1 — inventory</h2>
<ul><li>Catalogue every service</li><li>Flag the ones holding state</li></ul>
<h2 data-collapsed="true">Phase 2 — cutover</h2>
<ul><li>Drain traffic</li><li>Flip DNS</li></ul>
HTML
```

---

## 5. Embedding files & images

Attachments are **content-addressed**: you upload bytes once (the server computes
a `sha256`) and then reference that hash from any note body via a typed
`<harbor-embed>` element. Bytes are never inlined into the note.

```bash
# 1. Upload the file → capture its sha256 hash.
HASH=$(harbor files upload ./architecture.png --json | jq -r '.data.hash // .hash')

# 2. Reference it from a note body (HTML element; works inside Markdown too).
harbor notes create --title "Architecture" --format html --stdin --json <<HTML
<h1>System architecture</h1>
<p>Current topology:</p>
<harbor-embed type="image" resource="sha256:${HASH}" title="Architecture diagram"></harbor-embed>
HTML
```

`<harbor-embed>` has a fixed attribute set:

| Attribute | Meaning |
|---|---|
| `type` | `image` · `pdf` · `audio` · `file` (content-addressed) · `video`/`bookmark` (external `src`) |
| `resource` | a `sha256:<hash>` reference to an uploaded blob (for content-addressed types) |
| `src` | an external URL (for `video`/`bookmark`) |
| `title` | optional caption / accessible label |
| `width`, `height` | optional dimensions |
| `align` | optional alignment |

A plain `<img src="https://…">` with an `http(s)` URL is also tolerated (good for
images that already live on the web):

```html
<img src="https://example.com/banner.png" alt="Banner">
```

To embed an image **inside otherwise-Markdown** content, just inline the tag:

```bash
harbor notes update "$NOTE_ID" --stdin --json <<MD
Here's the latest diagram:

<harbor-embed type="image" resource="sha256:${HASH}"></harbor-embed>

…and the notes below it.
MD
```

---

## 6. Linking notes (`harbor:note/<uuid>`)

In-app note links live in the body as anchors whose URL uses the **`harbor:` URI
scheme**: `harbor:note/<uuid>`. On every save the server parses the body, extracts
those targets, and maintains the link graph you can read with
`harbor notes links` / `harbor notes backlinks`.

- **Markdown:** `[Roadmap](harbor:note/5b1f2c9a-1a2b-3c4d-5e6f-7a8b9c0d1e2f)`
- **HTML:** `<a href="harbor:note/5b1f2c9a-…">Roadmap</a>`

Rules that matter:

- The target must be a **canonical 36-char hyphenated UUID**. Only `harbor:note/…`
  anchors become edges (notebook/tag `harbor:` targets and self-links are ignored).
- Links are **derived and server-owned** — there is no "create link" command; you
  create a link simply by writing one into a note body.
- An edge is **broken** when its target was never created or was permanently
  expunged. A merely *trashed* target is **not** broken (it's recoverable).

```bash
# Find the target's id…
TARGET=$(harbor search 'intitle:Roadmap' --json | jq -r '.data[0].id')

# …and link to it from another note's body.
harbor notes update "$NOTE_ID" --stdin --json <<MD
See the [Roadmap](harbor:note/${TARGET}) for context.
MD

# Inspect the graph
harbor notes links "$NOTE_ID" --json        # outgoing
harbor notes backlinks "$TARGET" --json     # who links here (live notes only)
```

---

## 7. Editing formatted notes without losing formatting

The cardinal rule from `SKILL.md`, restated for rich notes:

1. **Fetch the body in the format you'll edit in.**
   - Plain/Markdown-friendly note → `--format markdown`.
   - Note with colors, embeds, alignment, intricate tables, or collapsed
     outlines → **`--format html`** (markdown read-back is lossy and will drop
     those, and a Markdown write-back un-folds the whole note).
2. **Edit the fetched text.**
3. **Write the whole body back with the matching `--format`.**

```bash
# Lossless round-trip for a rich note:
harbor notes get "$NOTE_ID" --format html --json | jq -r '.content' > /tmp/note.html
# …edit /tmp/note.html…
harbor notes update "$NOTE_ID" --file /tmp/note.html --format html --json
```

To **add** to the end of a note without touching the rest, `harbor notes append`
(it accepts `--format markdown|html` too):

```bash
harbor notes append "$NOTE_ID" --format html --content '<p style="color:#1e8e3e;">✅ Done.</p>' --json
```

---

## 8. Quick reference — what survives sanitization

| You want | How | Survives? |
|---|---|---|
| Headings, lists, emphasis, code, quotes, rules | Markdown | ✅ |
| Checklists | GFM `- [ ]` / `- [x]`, or `<ul data-type="checklist"><li data-checked="…">` | ✅ |
| Collapsed outline sections | `data-collapsed="true"` on `<li>` / `<h1>`–`<h3>` | ✅ HTML writes only |
| Tables | GFM pipe tables (or `<table>`) | ✅ |
| Links | `[t](https://…)`, `mailto:`, `harbor:note/<uuid>` | ✅ |
| Text color / highlight | `<span style="color:…;background-color:…">` | ✅ |
| Alignment | `style="text-align:center\|right\|left"` | ✅ |
| Font weight/size/family | `style="font-…"` | ✅ |
| Semantic classes | `class="…"` | ✅ |
| Embedded uploaded file/image | `<harbor-embed type=… resource="sha256:…">` | ✅ |
| External image | `<img src="https://…">` | ✅ |
| Scripts / JS / event handlers | `<script>`, `onclick=…`, `javascript:` URLs | ❌ stripped |
| Arbitrary CSS (`position`, `margin`, …) | inline `style` outside the allowlist | ❌ stripped |
| Iframes / arbitrary embeds | `<iframe>` etc. | ❌ use `<harbor-embed>` |
| Real checkboxes | `<input type="checkbox">` | ❌ use the checklist markup above |

When unsure whether something survived, **read it back with `--format html`** and
check — what you see is exactly what's stored.

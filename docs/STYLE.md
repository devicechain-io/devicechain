# DeviceChain docs style guide

How pages under `docs/docs` and their Spanish mirrors in `docs/i18n/es` are written. This file is not
published — it lives beside the site, not in it.

## Who reads what

| Section | Reader | What they came for |
| --- | --- | --- |
| `intro`, `concepts/` | Evaluators and new developers | What the platform does, how it thinks, whether it fits |
| `quickstart/`, `guides/` | Device and application developers | A task done, start to finish |
| `deployment/` | Operators | What to run, what to watch, what to do when it breaks |
| `reference/` | Anyone mid-task | A fact, found fast |

Write for the reader of the section. An operator page may assume Kubernetes; a concept page may not.

## The one rule that outranks the rest

**A rewrite changes how a page says things, never what it says.** Every claim, limit, default,
number, state name, qualifier and failure mode on the old page is on the new page. Tightening prose
is exactly how "only on `Recreate`" becomes "always" and "up to a day" disappears — and those
qualifiers are the facts. If a sentence cannot be shortened without losing one, leave it long.

If you believe a claim is false, do not fix it by rewording. Record it; the fact-check decides.

## Voice

- Second person, present tense, active voice: "You publish the profile", not "The profile is
  published by the user".
- Lead with what the reader can do or rely on. The first paragraph of a page answers "what is this,
  and why would I care?" The first sentence of a section answers the section's question.
- One idea per sentence. Split sentences that carry a claim, a caveat and a cross-reference at once.
- Say it once. Cut throat-clearing ("Read this once and the rest of the page will make sense"),
  restatements of the previous sentence, and closing summaries that repeat the section.
- Explain *why* when the reason changes what the reader does. Drop it when it is only interesting to
  the people who built it.
- Confident, not breathless. No "simply", "just", "easily", "powerful", "seamless", "modern".

## Words

- **Internal vocabulary is explained on first use or replaced.** `DETECT`/`REACT` → "detection" and
  "actions" (keep the uppercase form only where it is a literal UI label or identifier).
  "Replay-correct", "fails closed", "confused deputy", "red line", "arc", "slice", "seam" — say what
  happens instead: "refuses the request when X is missing", "gives the same result when events are
  replayed".
- **No aphorisms standing in for an explanation.** "The AI proposes and the compiler disposes" is
  fine only after the sentence that says what it means.
- One name per thing. If the console calls it "Access token", the page calls it "Access token" —
  not "access key" in one paragraph and "credential secret" in the next.
- Literals are verbatim: commands, flags, GraphQL fields and types, env vars, metric and alert
  names, state names (`HELD`, `PARKED`), Helm keys, file names a user edits. Never "tidy" them.

## Emphasis and typography

- Bold marks a term at the point it is defined, or the single phrase in a warning the reader must
  not miss. At most a few per section. Never bold whole sentences.
- No emoji (no 🔴, ✅, ⚠️). No ALL-CAPS for emphasis.
- Italics sparingly, for a word's meaning or a quoted UI message.
- Em dashes are fine; a paragraph with three of them needs restructuring.

## Structure

- **Front matter does not change**, with one exception: a weak `title` may be improved. The title is
  the sidebar label and the browser tab, so keep it short (two to five words) and keep the page's
  `# H1` identical to it. Never change `slug`, `sidebar_position` or the file name — those move the
  URL or the sidebar order.
- **Anchors are part of the public contract.** Every existing `{#id}` stays, on the same section.
  If you change the text of a heading that has no explicit id, pin its **old** auto-slug on the new
  heading (`## New wording {#old-wording}`) so no inbound link, bookmark or search result breaks.
  Never rename or remove an id.
- Keep every link, with the same target. You may move it within the page.
- Headings are nouns or tasks ("Command lifecycle", "Rotate a credential"). No questions, no puns.
- Lists for three or more parallel items; prose for reasoning. A list whose items are paragraphs
  wants to be sections or a table.
- Tables for comparisons across the same attributes.
- Procedures are numbered steps, one action per step, with the expected result where it helps.
- Code blocks keep their language tag and content.

## Admonitions

- `note` for context, `tip` for a shortcut, `warning` for something that costs time or data,
  `danger` for something irreversible or unsafe.
- Short: two to five lines. Detail belongs in the body, linked from the admonition.
- A **Status** note says what is available, what is planned, and nothing else. Mechanism goes in
  the body.

## Length

Most pages come out 10–30% shorter. That is a result, not a target: never cut a fact, a warning or a
troubleshooting step to hit it. A page that is long because the subject is long stays long, and gets
better headings instead.

## What never appears on a published page

- ADR numbers or links to the private strategy repo (`hack/check-docs-adr-refs.sh` enforces this).
- Source file paths from the backend (`backend/...`), function names that are not user-facing API,
  issue or PR numbers.
- Promises of dates.

## Spanish

- The Spanish page is a translation of the English page as it stands now — same sections, same
  order, same tables with the same row counts, same admonitions, same code blocks.
- Address the reader as **tú** (`consulta`, `puedes`), neutral international Spanish.
- Every `{#id}` is copied **unchanged** from the English heading. A Spanish heading without one
  slugs from the Spanish text and breaks every link to it.
- Link targets are identical to the English page's.
- Literals stay in English (see *Words*). Established technical loanwords stay as the existing
  Spanish pages use them (`payload`, `token`, `broker`, `hypertable`); read the current Spanish
  page before translating and reuse its terminology.
- Translate UI labels only if the console itself is translated for that label; otherwise keep the
  English label, since that is what the reader will see.
- Admonition titles are translated (`:::note Estado`).

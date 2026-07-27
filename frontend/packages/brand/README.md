<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# @devicechain/brand

The canonical DeviceChain brand values, and the generated stylesheets each
consumer actually needs.

`tokens.json` is the source of truth. Everything in `css/` and
`tokens.generated.json` is generated from it and carries a do-not-hand-edit
header.

```bash
npm run generate -w @devicechain/brand         # regenerate after editing tokens.json
npm run check:generated -w @devicechain/brand  # fail if the committed output is stale
npm run verify -w @devicechain/brand           # report consumers that disagree

# the marketing site lives in a separate repo, so point at it explicitly
DC_WEBSITE=../devicechain-website npm run verify -w @devicechain/brand
```

## Why three stylesheets and not one

The three consumers do not share a vocabulary, and a single stylesheet cannot
serve them:

| consumer | file | vocabulary | value format |
| --- | --- | --- | --- |
| marketing site | `css/brand.css` | `--brand`, `--ink`, `--bg`, accents | hex |
| console | `css/shadcn.css` | `--primary`, `--ring`, `--sidebar-*` | **bare** `H S% L%` triples |
| docs | `css/infima.css` | `--ifm-color-primary*` | hex, plus a five-step ramp |

Two details that are easy to get wrong:

- The console's triples have **no `hsl()` wrapper**. shadcn composes them as
  `hsl(var(--primary) / <alpha>)`, so wrapping them breaks every alpha variant.
- Infima wants a five-step ramp either side of primary. It is generated from the
  single canonical value so the steps cannot drift out of order, which is the
  usual failure mode of a hand-written ramp.

`css/shadcn.css` and `css/infima.css` deliberately cover **brand-derived tokens
only**. The console still owns the rest of its shadcn scale (card, popover,
`chart-*`, `sidebar-*` surfaces) and the docs site still owns its own
background theming.

## Why the values are stored as hex

The brandmark artwork is hex (`branding/logo.svg`), so hex is the origin and
every other form is derived. This is not cosmetic. The console previously
declared the brand blue as the rounded triple `197 71% 42%`, which renders
`#1f8cb7` — **not** the `#208cb7` in the artwork. One channel out of 255, invisible
to the eye, and still two different colours claiming to be the same one.
Deriving removes that entire class of mismatch.

## The one rule

`core.primary` is the only colour that ever means *interactive*. The four
accents (`aqua` → `violet` → `magenta` → `amber`, in pipeline order: ingest,
detect, react, command) identify an **area**. They never mark a link, a button
or a control. Break that and a page stops being navigable.

## What `verify` is for, and what it is not

"Keep it in step with the console" was a comment in three files, not a
guarantee — and it had already failed twice by the time this package was
written. `verify` turns that comment into something a CI job can enforce.

It is deliberately **read-only**. It prints what disagrees and exits non-zero;
it never rewrites a consumer. Changing a shipped colour is a visual decision,
so migrate consumers one at a time and look at the result. Do not bulk-rewrite
to silence the script.

## Migration status

No consumer has been migrated yet — this package is currently the source of
truth plus the drift alarm. Known disagreements at time of writing:

- **console** `--primary` (light and dark) — rounded triples, as described
  above. Adopting the derived values is a no-op to the eye.
- **marketing site** `--brand-bright` — had drifted to `#3aa9d4`
  (`hsl(197 64% 53%)`), a third value that neither the console nor the docs
  used. The console and docs both use `#24a3d6` (`hsl(197 71% 49%)`), and the
  console is the documented source of truth, so it wins here. Applying it is a
  **visible** change to the site's hover states and hero eyebrow and should be
  reviewed on its own.
- **docs** — already agrees on both light and dark.

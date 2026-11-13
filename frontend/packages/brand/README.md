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

## Using it

```bash
npm install @devicechain/brand
```

```css
/* whichever vocabulary your app speaks — see the table below */
@import '@devicechain/brand/css/shadcn.css';
```

```ts
import tokens from '@devicechain/brand/tokens';
```

The public exports are `@devicechain/brand/tokens.json` (source tokens),
`@devicechain/brand/tokens` (generated tokens), and the three stylesheet paths in
the table below. There is no root `@devicechain/brand` entry; do not import it as
JavaScript or reach into `tokens.generated.json` directly.

There is no build step and no JavaScript to run: the stylesheets and the
generated token JSON are shipped as-is.

## Working on it

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

The brandmark artwork is hex (`branding/logos/logo.svg`), so hex is the origin and
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

The console and dashboard viewer import `css/shadcn.css`; the docs site imports
`css/infima.css`. These consumers use the generated values directly. The marketing
site lives in a separate repository; use `DC_WEBSITE` with `verify` to check its
current values.

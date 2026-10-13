// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Static anchor check: every anchor the recipes rely on must still exist in the console
// source. No instance, no browser, no node_modules (it runs before `npm ci` in the frontend
// CI job, on Node 26 where .ts files load natively). It catches the real risk: a refactor
// that drops an attribute a screenshot recipe selects on.
//
// recipes.ts is IMPORTED, not pattern-matched, so a recipe built by a helper is checked
// as the string the browser will actually receive, and a recipe that yields no anchor at
// all fails instead of passing vacuously. The primitives' own behaviour (a drawer really
// renders data-shot-drawer, a tab really renders data-shot-tab, ...) is pinned by the
// console unit test src/components/guideAnchors.test.tsx, not here.
//
//   node hack/guide-capture/check-anchors.mjs [console-src-dir]

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = process.argv[2] ?? join(here, '..', '..', 'frontend', 'apps', 'console', 'src');

function walk(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...walk(p));
    // Tests are excluded so a test fixture can never satisfy an anchor the product lost.
    else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

// Comment-only lines are dropped before matching, so a comment that merely MENTIONS an
// anchor cannot satisfy it. (Lines, not a tokenizer: JSX text holds apostrophes and `//`
// inside strings, which defeat a string-unaware stripper in the other direction.)
const COMMENT_LINE = /^\s*(\/\/|\*|\/\*|\{\/\*)/;
const code = (text) => text.split('\n').filter((l) => !COMMENT_LINE.test(l)).join('\n');

const files = walk(srcDir).map((p) => ({
  rel: relative(srcDir, p).split(sep).join('/'),
  text: code(readFileSync(p, 'utf8')),
}));
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
// data-table.tsx declares the data-shot-mask prop TYPE; only a JSX use counts as a use.
const JSX_PII = /\sdata-shot-pii[\s>={/]/;
const JSX_MASK =/\sdata-shot-mask[\s>={/]/;

// Anchors of the shared primitives: any file may carry them, and guideAnchors.test.tsx
// proves they render. Everything else belongs to the screen a recipe names in `sources`.
const PRIMITIVE = new Set(['page', 'drawer', 'confirm', 'sheet', 'empty', 'error', 'loading', 'combobox', 'combobox-option']);

const rules = {
  // A literal (`data-shot="x"`) or an expression that names it (`data-shot={a ? 'x' : 'y'}`).
  // A props object (`{ 'data-shot': 'x' }`, for a component that forwards extra props) counts too.
  'data-shot': (v) =>
    new RegExp(`data-shot=(?:["']${esc(v)}["']|\\{[^}]*["']${esc(v)}["'])|["']data-shot["']:\\s*["']${esc(v)}["']`),
  // A segmented-control option: its value is the `value:` of an options entry in the screen's
  // source. (That the primitive renders the attribute at all is checked globally, below.)
  'data-shot-option': (v) => new RegExp(`\\bvalue:\\s*['"]${esc(v)}['"]`),
  // An element keyed by data: only the presence of the attribute on the screen is checked.
  'data-shot-for': () => /data-shot-for=\{/,
  'data-testid': (v) => new RegExp(`data-testid=["']${esc(v)}["']`),
  // A drawer id is the `shot` prop of a FormDrawer call site (a template literal counts).
  // The lookbehind keeps `data-shot="x"` from passing as a drawer id.
  'data-shot-drawer': (v) => new RegExp(`(?<![\\w-])shot=(?:"${esc(v)}"|\\{\`${esc(v)}[\`$])`),
  // A tab id is the `value` of a TabsTrigger, or the `value:` of a registry resource's tab entry
  // (ResourceDetailPage renders those through one dynamic TabsTrigger).
  'data-shot-tab': (v) => new RegExp(`<TabsTrigger[^>]*value="${esc(v)}"|\\bvalue:\\s*['"]${esc(v)}['"]`),
};

// Every [data-shot*="v"] / [data-testid="v"] / [data-shot-mask] inside a selector string.
const SELECTOR_REF = /\[(data-shot(?:-tab|-drawer|-mask|-option|-for)?|data-testid)(?:=["']([^"'$]+)["'])?\]/g;
function refsIn(str) {
  return [...str.matchAll(SELECTOR_REF)].map((m) => ({ attr: m[1], value: m[2] }));
}

const { recipes } = await import(pathToFileURL(join(here, 'recipes.ts')).href);
if (!Array.isArray(recipes) || recipes.length === 0) {
  console.error('check-anchors: recipes.ts exported no recipes');
  process.exit(1);
}

const problems = [];
const find = (re, among) => (among ?? files).some((f) => re.test(f.text));

for (const r of recipes) {
  const sources = (r.sources ?? []).map((rel) => {
    const f = files.find((x) => x.rel === rel);
    if (!f) problems.push(`${r.id}: source file ${rel} does not exist`);
    return f;
  });
  const among = r.sources ? sources.filter(Boolean) : undefined;

  const selectors = [r.anchor, r.clip, r.minRows?.selector, r.setup?.toString(), r.then?.toString()].filter(Boolean);
  // A tab recipe's page-specific steps live in `then`; the wrapper's setup only calls it. A
  // recipe that calls it without carrying it would hide every anchor in it from this check.
  if (r.setup?.toString().includes('then?.(') && !r.then) problems.push(`${r.id}: setup calls then?.() but the recipe carries no then`);
  const refs = selectors.flatMap(refsIn);
  // A recipe that names no anchor at all would be vacuously green.
  if (!refs.some((x) => x.attr !== 'data-shot-mask' && x.value !== undefined)) {
    problems.push(`${r.id}: no data-shot*/data-testid anchor found in its anchor, clip, minRows or setup`);
  }
  for (const { attr, value } of refs) {
    if (attr === 'data-shot-mask') continue; // handled by r.mask below
    if (value === undefined) continue; // a bare [data-shot-tab] names no particular anchor
    const scoped = !(attr === 'data-shot' && PRIMITIVE.has(value));
    if (!find(rules[attr](value), scoped ? among : undefined)) {
      problems.push(`${r.id}: ${attr}="${value}" not found${scoped && r.sources ? ` in ${r.sources.join(', ')}` : ''}`);
    }
  }
  // A screen that shows the operator's address must mark it, or the harness cannot swap it.
  if (r.pii) {
    const pool = among ?? files;
    if (!pool.some((f) => JSX_PII.test(f.text))) {
      problems.push(`${r.id}: pii is set but no JSX use of data-shot-pii exists${r.sources ? ` in ${r.sources.join(', ')}` : ''}`);
    }
  }
  if (r.mask) {
    const pool = (among ?? files).filter((f) => f.rel !== 'components/ui/data-table.tsx');
    if (!pool.some((f) => JSX_MASK.test(f.text))) {
      problems.push(`${r.id}: mask is set but no JSX use of data-shot-mask exists${r.sources ? ` in ${r.sources.join(', ')}` : ''}`);
    }
  }
}

// What the harness itself selects on, whichever recipes exist.
for (const a of PRIMITIVE) {
  if (!find(rules['data-shot'](a))) problems.push(`harness: data-shot="${a}" not found anywhere`);
}

// Attributes the harness acts on without any one recipe naming them.
if (!find(/data-shot-option=\{/)) problems.push('harness: no primitive renders data-shot-option');
if (!find(JSX_PII)) problems.push('harness: no JSX use of data-shot-pii anywhere');

if (problems.length) {
  console.error(`check-anchors: ${problems.length} problem(s) in ${srcDir}:`);
  for (const p of problems) console.error(`  - ${p}`);
  process.exit(1);
}
console.log(`check-anchors: ${recipes.length} recipes, every anchor present in the console source`);

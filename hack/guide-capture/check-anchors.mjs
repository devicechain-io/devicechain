// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Static anchor check: every anchor recipes.ts relies on must still exist in the console
// source. No instance, no browser, no dependencies; it runs in the frontend CI job and
// catches the real risk, a refactor that quietly drops an attribute a screenshot recipe
// selects on. (It cannot tell that an anchor sits on the RIGHT element; the capture run
// does that.)
//
//   node hack/guide-capture/check-anchors.mjs [console-src-dir]

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

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

const recipes = readFileSync(join(here, 'recipes.ts'), 'utf8');
// Comments are stripped before matching: a comment that merely MENTIONS an anchor (this
// file's own convention is documented in tabs.tsx, sheet.tsx ...) must not satisfy it.
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');
const files = walk(srcDir).map((p) => ({ p, text: stripComments(readFileSync(p, 'utf8')) }));
const anywhere = (re) => files.some((f) => re.test(f.text));
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// What recipes.ts references: [data-shot="v"], [data-shot-tab="v"], [data-shot-drawer="v"],
// [data-testid="v"], bare [data-shot-mask], and deviceTab('v') (a tab key passed to the
// helper, which builds a dynamic data-shot-tab selector the regex below cannot see).
const refs = new Map(); // key -> {attr, value}
const add = (attr, value) => refs.set(`${attr}=${value ?? ''}`, { attr, value });
for (const m of recipes.matchAll(/\[(data-shot(?:-tab|-drawer)?|data-testid)="([^"$]+)"\]/g)) add(m[1], m[2]);
for (const m of recipes.matchAll(/\[(data-shot-mask)\]/g)) add(m[1]);
for (const m of recipes.matchAll(/deviceTab\('([\w-]+)'/g)) add('data-shot-tab', m[1]);
// The harness itself selects on these regardless of any one recipe.
for (const a of ['page', 'empty', 'error', 'loading']) add('data-shot', a);
add('data-shot-mask');

const rules = {
  'data-shot': (v) => new RegExp(`data-shot=["']${esc(v)}["']`),
  'data-testid': (v) => new RegExp(`data-testid=["']${esc(v)}["']`),
  // A drawer id is the `shot` prop of a FormDrawer call site (a template literal counts).
  'data-shot-drawer': (v) => new RegExp(`shot=(?:"${esc(v)}"|\\{\`${esc(v)}[\`$])`),
  // A tab id is the `value` of a TabsTrigger.
  'data-shot-tab': (v) => new RegExp(`<TabsTrigger[^>]*value="${esc(v)}"`),
};

const missing = [];
for (const { attr, value } of refs.values()) {
  const found = value === undefined ? anywhere(new RegExp(esc(attr))) : anywhere(rules[attr](value));
  if (!found) missing.push(value === undefined ? attr : `${attr}="${value}"`);
}
// The primitives must still render the attributes the rules above assume.
for (const [attr, file] of [
  ['data-shot-tab', 'components/ui/tabs.tsx'],
  ['data-shot-drawer', 'components/registry/FormDrawer.tsx'],
]) {
  const f = files.find((x) => x.p.replace(/\\/g, '/').endsWith(file));
  if (!f || !new RegExp(`${attr}=`).test(f.text)) missing.push(`${attr} (primitive ${file})`);
}

if (missing.length) {
  console.error(`check-anchors: ${missing.length} anchor(s) referenced by recipes.ts not found in ${srcDir}:`);
  for (const m of missing) console.error(`  - ${m}`);
  process.exit(1);
}
console.log(`check-anchors: ${refs.size} anchor(s) referenced by recipes.ts all present in the console source`);

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Negative controls for check-structure-parity.mjs. Every failure case has a
// counterweight that must PASS, so no case is satisfied by a check that always fails.
import assert from 'node:assert/strict';
import { cp, mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { execFileSync, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  checkStructureParity, compareStructure, extractStructure, headingText, slugify, validateAllowlist,
} from './check-structure-parity.mjs';

const docsRoot = fileURLToPath(new URL('..', import.meta.url));

const EN = `---
title: Page
---

# Page {#page}

Intro.

## Overview

:::note
Read this.
:::

## Install {#install}

\`\`\`bash
# a comment, not a heading
npm install
\`\`\`

### Options {#options}

\`\`\`yaml
a: 1
\`\`\`
`;

const ES = `---
title: Página
---

# Página {#page}

Introducción.

## Resumen {#overview}

:::note
Lee esto.
:::

## Instalación {#install}

\`\`\`bash
# un comentario, no un encabezado
npm install
\`\`\`

### Opciones {#options}

\`\`\`yaml
a: 1
\`\`\`
`;

const diff = (en, loc) => compareStructure(extractStructure(en), extractStructure(loc));

async function fixture(pages) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'structure-parity-'));
  for (const [rel, text] of Object.entries(pages)) {
    await mkdir(path.dirname(path.join(root, rel)), { recursive: true });
    await writeFile(path.join(root, rel), text);
  }
  return root;
}
const ES_PAGE = 'i18n/es/docusaurus-plugin-content-docs/current/guides/page.md';

test('a faithful translation passes — including a pinned implicit slug and a # inside a fence', () => {
  assert.deepEqual(diff(EN, ES), []);
  const s = extractStructure(EN);
  assert.deepEqual(s.headings, [1, 2, 1, 0, 0, 0], 'the # line inside the bash fence is not a heading');
  assert.deepEqual(s.fences, ['bash', 'yaml']);
  assert.deepEqual(s.admonitions, ['note']);
  assert.deepEqual(s.anchors, ['page', 'install', 'options']);
  assert.deepEqual(s.ids, ['page', 'overview', 'install', 'options']);
});

test('CRLF line endings do not change the structure', () => {
  assert.deepEqual(extractStructure(EN.replace(/\n/g, '\r\n')), extractStructure(EN));
});

test('a translation that DROPS an explicit English anchor fails', () => {
  const out = diff(EN, ES.replace('### Opciones {#options}', '### Opciones'));
  assert.deepEqual(out.map(d => d.check), ['anchors']);
  assert.match(out[0].detail, /missing English anchor\(s\): \{#options\}/);
});

test('a translation that TRANSLATES an anchor id fails both ways', () => {
  const out = diff(EN, ES.replace('{#install}', '{#instalacion}'));
  assert.deepEqual(out.map(d => d.detail), [
    'missing English anchor(s): {#install}',
    'anchor(s) no English heading has: {#instalacion}',
  ]);
});

test('a pinned anchor that matches no English heading (a typo) fails', () => {
  const out = diff(EN, ES.replace('{#overview}', '{#overveiw}'));
  assert.deepEqual(out.map(d => d.detail), ['anchor(s) no English heading has: {#overveiw}']);
});

test('a missing section fails on heading count', () => {
  const out = diff(EN, ES.replace(/### Opciones[^]*$/, ''));
  assert.ok(out.some(d => d.check === 'headings' && /h3 1 in English, 0 translated/.test(d.detail)), JSON.stringify(out));
});

test('a heading demoted to another level fails even though the total is unchanged', () => {
  const out = diff(EN, ES.replace('### Opciones', '## Opciones'));
  assert.deepEqual(out.map(d => d.check), ['headings']);
});

test('a dropped or relabelled code fence fails', () => {
  assert.deepEqual(diff(EN, ES.replace('```yaml', '```yml')).map(d => d.check), ['fences']);
  assert.deepEqual(diff(EN, ES.replace(/```yaml\na: 1\n```\n/, '')).map(d => d.check), ['fences']);
});

test('a dropped or retyped admonition fails', () => {
  assert.deepEqual(diff(EN, ES.replace(':::note', ':::tip')).map(d => d.check), ['admonitions']);
  assert.deepEqual(diff(EN, ES.replace(/:::note\nLee esto.\n:::\n/, '')).map(d => d.check), ['admonitions']);
});

test('heading text reduces to what github-slugger sees', () => {
  assert.equal(headingText('The `foo` [flag](../x.md) **now**'), 'The foo flag now');
  assert.equal(slugify("A tenant's settings"), 'a-tenants-settings');
  assert.equal(slugify('Event processing & alarms'), 'event-processing--alarms');
});

test('the slug mirror matches the real github-slugger on every English heading', async () => {
  // The implicit id is what accepts a translation's pinned anchor; a drifted mirror
  // would only ever raise false alarms, but it must not raise them on the real tree.
  const GithubSlugger = createRequire(path.join(docsRoot, 'package.json'))('github-slugger');
  const walk = async dir => (await readdir(dir, { withFileTypes: true })).flatMap(e => e.isDirectory()
    ? [walk(path.join(dir, e.name))] : /\.mdx?$/.test(e.name) ? [path.join(dir, e.name)] : []);
  const flat = async dir => (await Promise.all(await walk(dir))).flat(Infinity);
  let n = 0;
  for (const file of await flat(path.join(docsRoot, 'docs'))) {
    let fence = false;
    for (const line of (await readFile(file, 'utf8')).split(/\r?\n/)) {
      if (/^\s*(```|~~~)/.test(line)) { fence = !fence; continue; }
      const m = !fence && /^ {0,3}#{1,6}\s+(.*)$/.exec(line);
      if (!m) continue;
      const text = headingText(m[1].replace(/\s*\{#[^}]+\}\s*$/, ''));
      assert.equal(slugify(text), GithubSlugger.slug(text), `${file}: ${text}`);
      n++;
    }
  }
  assert.ok(n > 500, `only ${n} headings found — the walk is broken`);
});

test('the tree walk compares pages, honours an exact allowlist entry and reports a stale one', async () => {
  const root = await fixture({ 'docs/guides/page.md': EN, [ES_PAGE]: ES.replace(':::note', ':::tip') });
  try {
    const detail = 'admonitions differ: English [note], translated [tip]';
    let r = await checkStructureParity(root, []);
    assert.equal(r.compared, 1);
    assert.deepEqual(r.problems.map(p => [p.locale, p.page, p.detail]), [['es', 'guides/page.md', detail]]);

    const entry = { locale: 'es', page: 'guides/page.md', check: 'admonitions', detail, reason: 'a reason long enough to count' };
    r = await checkStructureParity(root, [entry]);
    assert.deepEqual([r.problems, r.stale], [[], []]);

    // An entry excuses ONE difference. A later, different drift on the same check
    // must still fail — and the entry, now matching nothing, is reported stale.
    await writeFile(path.join(root, ES_PAGE), ES.replace(/:::note\nLee esto.\n:::\n/, ''));
    r = await checkStructureParity(root, [entry]);
    assert.equal(r.problems.length, 1);
    assert.deepEqual(r.stale, [entry]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('a tree with nothing to compare reports compared = 0, not a clean pass', async () => {
  const root = await fixture({ 'docs/guides/page.md': EN, 'i18n/es/docusaurus-theme-classic/navbar.json': '{}' });
  try {
    const r = await checkStructureParity(root, []);
    assert.deepEqual([r.compared, r.problems], [0, []]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('the CLI refuses a run that compared nothing, and passes the real tree', async () => {
  const script = fileURLToPath(new URL('./check-structure-parity.mjs', import.meta.url));
  const root = await fixture({ 'docs/guides/page.md': EN, 'i18n/es/docusaurus-theme-classic/navbar.json': '{}' });
  try {
    const empty = spawnSync(process.execPath, [script, root], { encoding: 'utf8' });
    assert.equal(empty.status, 1);
    assert.match(empty.stderr, /compared no translated pages at all/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
  const drifted = await fixture({ 'docs/guides/page.md': EN, [ES_PAGE]: ES.replace(' {#install}', '') });
  try {
    const bad = spawnSync(process.execPath, [script, drifted], { encoding: 'utf8' });
    assert.equal(bad.status, 1);
    assert.match(bad.stdout, /es\/guides\/page\.md: missing English anchor\(s\): \{#install\}/);
  } finally {
    await rm(drifted, { recursive: true, force: true });
  }
  assert.match(execFileSync(process.execPath, [script], { encoding: 'utf8' }), /Structure parity OK: \d+ translated page/);
});

test('the allowlist refuses an entry with no exact detail or no real reason', () => {
  const ok = { locale: 'es', page: 'a.md', check: 'fences', detail: 'x', reason: 'a reason long enough to count' };
  assert.doesNotThrow(() => validateAllowlist([ok]));
  assert.throws(() => validateAllowlist([{ ...ok, detail: undefined }]));
  assert.throws(() => validateAllowlist([{ ...ok, reason: 'because' }]));
  assert.throws(() => validateAllowlist([{ ...ok, check: 'prose' }]));
});

// THE REAL TREE, both ways: it passes with the committed allowlist, and a copy of it
// with ONE anchor removed from ONE translated page fails, naming that anchor. Without
// the second half, a green real-tree run is indistinguishable from a walk that looked
// at nothing.
test('the real docs tree passes, and a planted missing anchor in a copy of it fails', async () => {
  const allowlist = validateAllowlist(JSON.parse(await readFile(new URL('./structure-parity-allowlist.json', import.meta.url), 'utf8')));
  const real = await checkStructureParity(docsRoot, allowlist);
  assert.deepEqual([real.problems, real.stale], [[], []]);
  assert.ok(real.compared > 100, `compared only ${real.compared} pages`);

  const root = await mkdtemp(path.join(os.tmpdir(), 'structure-parity-real-'));
  try {
    await cp(path.join(docsRoot, 'docs'), path.join(root, 'docs'), { recursive: true });
    await cp(path.join(docsRoot, 'i18n'), path.join(root, 'i18n'), { recursive: true });
    const page = 'user-guide/administering-the-instance.md';
    const target = path.join(root, 'i18n/es/docusaurus-plugin-content-docs/current', page);
    const text = await readFile(target, 'utf8');
    assert.ok(text.includes(' {#tiers}'), 'the planted page no longer carries {#tiers}; pick another');
    await writeFile(target, text.replace(' {#tiers}', ''));

    const planted = await checkStructureParity(root, allowlist);
    assert.equal(planted.compared, real.compared);
    assert.deepEqual(planted.problems.map(p => [p.locale, p.page, p.detail]),
      [['es', page, 'missing English anchor(s): {#tiers}']]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

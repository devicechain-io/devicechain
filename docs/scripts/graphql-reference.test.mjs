// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Tests for the generated GraphQL reference pages (generate-graphql-reference.mjs).
//
// Run with `npm run test:graphql-reference` (node --test). These need the docs
// dependencies installed (graphql, and @mdx-js/mdx + remark-gfm, which Docusaurus
// brings), unlike schema-publish.test.mjs.

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { compile, createProcessor } from '@mdx-js/mdx';
import { buildSchema } from 'graphql';
import remarkGfm from 'remark-gfm';

import {
  CHROME, LOCALES, DOCS, buildPages, assertCoverage, configuredLocales, pageName,
  escapeBlock, escapeCell, escapeInline, escapeEsm, defaultOf,
} from './generate-graphql-reference.mjs';
import { GenerateError, discover, resolve } from './generate-schema.mjs';
import { SCHEMAS, REQUIRED_OUTPUTS } from './schemas.manifest.mjs';

// The real tree, generated once: it is the slow part.
const pages = buildPages();

// ---------------------------------------------------------------------------
// Coverage: every served schema has a page, so a new service is never missed.
// ---------------------------------------------------------------------------

test('every served schema in the tree has a reference page, in every locale', () => {
  // Derived from the TREE (discover), not from the manifest the generator reads, and
  // filtered only by the manifest's explicit publish:false — so a schema that exists
  // and is served but has no page cannot pass.
  const unpublished = new Set(SCHEMAS.filter((s) => s.publish === false).map((s) => s.source));
  const served = discover().filter((src) => !unpublished.has(src));
  assert.ok(served.length >= 13, `expected the full schema set, discovered ${served.length}`);
  const want = served
    .map((src) => pageName(resolve(SCHEMAS.find((s) => s.source === src)).published))
    .sort();

  for (const locale of LOCALES) {
    const have = pages.filter((p) => p.locale === locale && p.name !== 'index').map((p) => p.name).sort();
    assert.deepEqual(have, want, `locale ${locale}`);
    assert.ok(pages.some((p) => p.locale === locale && p.name === 'index'), `locale ${locale} has an index`);
  }
});

test('user-management\'s three schemas have pages, asserted by name', () => {
  for (const out of REQUIRED_OUTPUTS) {
    for (const locale of LOCALES) {
      assert.ok(pages.some((p) => p.locale === locale && p.name === pageName(out)), `${locale}: ${out}`);
    }
  }
});

test('the floor fails, by name, when a published schema loses its page', () => {
  const published = SCHEMAS.map(resolve).filter((s) => s.publish !== false);
  assert.doesNotThrow(() => assertCoverage(pages, published));
  const dropped = pages.filter((p) => !(p.locale === 'es' && p.name === 'device-state'));
  assert.throws(() => assertCoverage(dropped, published), (err) => {
    assert.ok(err instanceof GenerateError);
    assert.match(err.message, /es: device-state/);
    return true;
  });
});

test('every locale the site builds has page chrome, and nothing else does', () => {
  assert.deepEqual([...LOCALES].sort(), [...configuredLocales()].sort());
  // And every translated tree on disk is one of them.
  const onDisk = readdirSync(join(DOCS, 'i18n'), { withFileTypes: true })
    .filter((d) => d.isDirectory()).map((d) => d.name);
  for (const l of onDisk) assert.ok(CHROME[l], `i18n/${l} has no GraphQL reference chrome`);
});

test('translated pages carry translated chrome over the identical English body', () => {
  const en = pages.find((p) => p.locale === 'en' && p.name === 'device-management').text;
  const body = en.slice(en.indexOf('\n## '));
  for (const locale of LOCALES.filter((l) => l !== 'en')) {
    const page = pages.find((p) => p.locale === locale && p.name === 'device-management').text;
    assert.ok(page.endsWith(body), `${locale} body differs from English`);
    assert.ok(page.includes(CHROME[locale].pageTitle('device-management')), `${locale} title`);
    assert.ok(page.includes(CHROME[locale].englishOnly), `${locale} English-only note`);
  }
});

// ---------------------------------------------------------------------------
// Rendering, against a fixture tree.
// ---------------------------------------------------------------------------

const UM = 'backend/services/user-management/graphql';
const FIXTURE_SCHEMAS = [
  { source: 'backend/services/alpha/graphql/schema.graphql', area: 'alpha' },
  { source: `${UM}/schema.graphql`, area: 'user-management' },
  { source: `${UM}/admin_schema.graphql`, area: 'user-management' },
  { source: `${UM}/settings_schema.graphql`, area: 'user-management' },
];
const TRIVIAL = 'type Query { ok: Boolean }\n';

function fixture(alpha) {
  const repo = mkdtempSync(join(tmpdir(), 'dc-gqlref-'));
  const put = (rel, text) => {
    mkdirSync(join(repo, rel, '..'), { recursive: true });
    writeFileSync(join(repo, rel), text);
  };
  put(FIXTURE_SCHEMAS[0].source, alpha);
  for (const s of FIXTURE_SCHEMAS.slice(1)) put(s.source, TRIVIAL);
  return repo;
}

function renderAlpha(sdl) {
  const repo = fixture(sdl);
  try {
    return buildPages(repo, FIXTURE_SCHEMAS).find((p) => p.locale === 'en' && p.name === 'alpha').text;
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
}

test('a new served schema gets a page with no change to the generator', () => {
  const repo = fixture(TRIVIAL);
  try {
    const names = buildPages(repo, FIXTURE_SCHEMAS).filter((p) => p.locale === 'en').map((p) => p.name);
    assert.ok(names.includes('alpha'));
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test('renders """ descriptions and never # comments', () => {
  const page = renderAlpha([
    '# maintainer-only note that must not be published',
    '"""The public description of Widget."""',
    'type Widget {',
    '  # a field-level maintainer note',
    '  """The widget\'s token."""',
    '  token: String!',
    '  size: Int',
    '}',
    'type Query {',
    '  """Look a widget up."""',
    '  widget("""Its token.""" token: String!): Widget',
    '}',
  ].join('\n'));
  assert.match(page, /The public description of Widget\./);
  assert.match(page, /The widget's token\./);
  assert.match(page, /Look a widget up\./);
  assert.match(page, /\| `token` \| `String!` \| Its token\. \|/);
  assert.doesNotMatch(page, /maintainer/);
  // The undescribed field says so rather than rendering an empty cell.
  assert.match(page, /\| `size` \| `Int` \| _No description yet\._ \|/);
  // Root fields first, linked to their return type's section.
  assert.ok(page.indexOf('{#query-widget}') < page.indexOf('{#Widget}'));
  assert.match(page, /\*\*Returns\*\* \[`Widget`\]\(#Widget\)/);
});

// Docusaurus strips `{#id}` heading ids before MDX sees them; plain @mdx-js/mdx does not.
const compileLikeDocusaurus = (text) => compile(
  text.replace(/^---\n[\s\S]*?\n---\n/, '').replace(/ \{#[^}\n]+\}$/gm, ''),
  { remarkPlugins: [remarkGfm] },
);

const NASTY = [
  'Points at <instance> and {"a": 1}; a & b; a | b; `code <x> {y} | z`.',
  '',
  'An example:',
  '',
  '    query { devices { token } }',
  '    # not a heading',
  '',
  '```',
  '{ "literal": <kept> }',
  '```',
  '',
  'After <the> fence.',
  '',
  // An inline span at line start is not a fence (a backtick fence's info string
  // cannot hold a backtick); its expression must still be escaped.
  '```inline``` at start {"LEAK:"+Object.keys(process.env).length}',
  '',
  'Text after the fake fence {still escaped}.',
  '',
  'export const leaked = process.env.PATH',
  'import fs from "node:fs"',
  'export the data as CSV.',
].join('\n');

// Docusaurus-equivalent parse, listing every node MDX would EXECUTE: an expression,
// ESM, or JSX. A page that compiles can still run code at build time, so compiling is
// not the check — the absence of these nodes is.
const processor = createProcessor({ remarkPlugins: [remarkGfm] });
function executableNodes(text) {
  const tree = processor.parse(
    text.replace(/^---\n[\s\S]*?\n---\n/, '').replace(/ \{#[^}\n]+\}$/gm, ''),
  );
  const found = [];
  (function walk(n) {
    // The generator's own <br />, attribute-free, is the one JSX it writes on purpose.
    const ownBreak = n.name === 'br' && !n.attributes?.length && !n.children?.length;
    if (/^mdx/.test(n.type) && !ownBreak) found.push(`${n.type}: ${String(n.value ?? n.name ?? '').slice(0, 60)}`);
    for (const c of n.children ?? []) walk(c);
  })(tree);
  return found;
}

test('every real page compiles as MDX and executes nothing', async () => {
  for (const p of pages.filter((x) => x.locale === 'en')) {
    await assert.doesNotReject(compileLikeDocusaurus(p.text), p.path);
    assert.deepEqual(executableNodes(p.text), [], p.path);
  }
});

test('MDX-hostile descriptions are escaped, in blocks and in table cells', async () => {
  const sdl = [
    `"""\n${NASTY}\n"""`,
    'type Widget {',
    `  """\n${NASTY}\n  """`,
    '  token: String!',
    '}',
    `"""\n${NASTY}\n"""`,
    'enum Shade {',
    `  """\n${NASTY}\n  """`,
    '  DARK',
    '}',
    'type Query {',
    `  """\n${NASTY}\n  """`,
    `  widget("""\n${NASTY}\n""" token: String!, shade: Shade): Widget`,
    '}',
  ].join('\n');
  const page = renderAlpha(sdl);
  await assert.doesNotReject(compileLikeDocusaurus(page));
  assert.deepEqual(executableNodes(page), []);
  // The prose survives as prose.
  assert.ok(page.includes('&#101;xport the data as CSV.'));
  assert.ok(page.includes('fake fence \\{still escaped\\}.'));
  // The indented example became a fence rather than a heading.
  assert.match(page, /```\n {4}query \{ devices \{ token \} \}\n {4}# not a heading\n```/);
});

test('NEGATIVE CONTROL: unescaped, these lines execute or break the build', async () => {
  for (const raw of [
    '```inline``` at start {1+1}',
    'export const leaked = process.env.PATH',
    'import fs from "node:fs"',
  ]) {
    assert.notDeepEqual(executableNodes(`Text\n\n${raw}\n`), [], raw);
    assert.deepEqual(executableNodes(`Text\n\n${escapeBlock(raw)}\n`), [], raw);
  }
  // Prose MDX cannot parse as ESM fails the whole site build.
  await assert.rejects(compileLikeDocusaurus('Text\n\nexport the data as CSV.\n'));
  await assert.doesNotReject(compileLikeDocusaurus(`Text\n\n${escapeBlock('export the data as CSV.')}\n`));
});

test('a leading import or export is escaped, and nothing else is', () => {
  assert.equal(escapeEsm('export const x = 1'), '&#101;xport const x = 1');
  assert.equal(escapeEsm('  import x'), '  &#105;mport x');
  assert.equal(escapeEsm('exporter of data'), 'exporter of data');
  assert.equal(escapeEsm('we export it'), 'we export it');
  assert.equal(escapeBlock('```a``` {b}'), '```a``` \\{b\\}');
});

test('defaults are printed for arguments and input fields', () => {
  const page = renderAlpha([
    'input Filter { size: Int = 10 tags: [String!] = ["a"] }',
    'type Query { find(limit: Int = 25, filter: Filter = {size: 3}): Boolean }',
  ].join('\n'));
  assert.ok(page.includes('| `limit` | `Int` = `25` |'), 'argument default');
  assert.ok(page.includes('| `filter` | [`Filter`](#Filter) = `{ size: 3 }` |'), 'object default');
  assert.ok(page.includes('| `size` | `Int` = `10` |'), 'input field default');
  assert.ok(page.includes('| `tags` | `[String!]` = `["a"]` |'), 'list default');
  const s = buildSchema('type Query { f(x: Int): Int }');
  assert.equal(defaultOf(s.getQueryType().getFields().f.args[0]), undefined);
});

test('the citation gate fires on what the generator itself writes', () => {
  // Page chrome never passes through the SDL publisher's gate, so only the page-level
  // scan can catch a citation there.
  const repo = fixture(TRIVIAL);
  try {
    const chrome = { en: { ...CHROME.en, pageIntro: 'Generated per ADR-047.' } };
    assert.throws(() => buildPages(repo, FIXTURE_SCHEMAS, chrome), (err) => {
      assert.ok(err instanceof GenerateError);
      assert.match(err.message, /unpublishable reference/);
      assert.match(err.message, /ADR-047/);
      return true;
    });
    assert.doesNotThrow(() => buildPages(repo, FIXTURE_SCHEMAS, { en: CHROME.en }));
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});

test('NEGATIVE CONTROL: the same descriptions unescaped do not compile', async () => {
  for (const raw of ['Points at <instance> here.', 'A {"json": 1} literal.']) {
    await assert.rejects(compileLikeDocusaurus(`Text\n\n${raw}\n`), raw);
    await assert.doesNotReject(compileLikeDocusaurus(`Text\n\n${escapeBlock(raw)}\n`), raw);
  }
});

test('the escapers leave code spans and fences literal', () => {
  assert.equal(escapeInline('a <b> `c <d> {e}` {f}'), 'a \\<b> `c <d> {e}` \\{f\\}');
  assert.equal(escapeInline('an ` unmatched <x>'), 'an ` unmatched \\<x>');
  assert.equal(escapeBlock('```\n<x> {y}\n```\n<z>'), '```\n<x> {y}\n```\n\\<z>');
  assert.equal(escapeCell('one\ntwo | three\n\nfour'), 'one two \\| three<br /><br />four');
});

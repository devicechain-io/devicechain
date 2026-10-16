// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Guard: every translated docs page keeps the STRUCTURE of its English original.
//
// hack/check-docs-locale-parity.sh proves a translated page EXISTS for every English
// page. check-translation-freshness.mjs reports, advisorily, which translations have
// not been re-reviewed since English changed. Neither looks inside a page, and the
// thing that breaks first inside a page is not prose — it is the skeleton:
//
//   - EXPLICIT HEADING ANCHORS (`## Título {#title}`). These are the ids other pages,
//     the release notes and the console link to. Docusaurus's broken-anchor check sees
//     links from INSIDE the site only; a console deep link or a release-notes URL to
//     /es/docs/page#title that lost its anchor lands at the top of the page, silently.
//     A translated heading WITHOUT the explicit id gets a slug of its translated text,
//     so the anchor moves the moment someone forgets to carry `{#title}` across.
//   - HEADING COUNT PER LEVEL. A section that was added to English and never
//     translated, or a translation that merged two sections, shows up here first.
//   - CODE FENCES, by language and in order. Commands and config are copied, not
//     translated; a translation that lost one is missing an instruction.
//   - ADMONITIONS, by type and in order. A dropped `:::danger` is a dropped warning.
//
// Freshness answers "was this re-reviewed?"; this answers "does it still have the
// same bones?". They are complementary on purpose: a page can be fresh and still
// missing an anchor (it was translated that way), and stale with perfect structure
// (English changed a sentence). This check BLOCKS, because a structural difference is
// never a batching question — it is a defect or a recorded, reasoned exception.
//
// 🔴 SCOPE: it compares structure, never prose. A green run means the skeletons
// match, not that the Spanish says what the English says.
//
// Exceptions live in structure-parity-allowlist.json, each with a reason. An entry
// accepts one EXACT difference (its `detail` string), not a whole check on a page, so
// excusing an added :::note cannot also excuse a :::danger dropped later. An entry
// that no longer suppresses anything is itself a failure, so the list cannot rot
// into a set of permanent blind spots.
//
//   node docs/scripts/check-structure-parity.mjs [docs-root]   # default: this docs/
//   node --test docs/scripts/structure-parity.test.mjs   # the negative controls

import { readFile, readdir, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const CHECKS = ['anchors', 'headings', 'fences', 'admonitions'];

// ---------------------------------------------------------------------------
// The id Docusaurus gives a heading with no explicit `{#id}`: github-slugger over
// the heading's plain text, with a per-page counter for repeats. The character class
// mirrors github-slugger's generated one (it KEEPS letters, marks, numbers, `_`,
// space and `-`, and removes everything else). A divergence can only ever produce a
// FALSE ALARM here, never hide a real difference: an implicit id is used solely to
// accept a translation's explicit anchor, so a wrong slug rejects, it does not pass.
// The test file pins it against the real github-slugger on every English heading.
// ---------------------------------------------------------------------------
export function slugify(value, maintainCase = false) {
  return (maintainCase ? value : value.toLowerCase())
    .replace(/[^\p{L}\p{M}\p{N}\p{Pc} -]/gu, '')
    .replace(/ /g, '-');
}

export function createSlugger() {
  const seen = new Map();
  return {
    slug(value, maintainCase = false) {
      const base = slugify(value, maintainCase);
      let s = base;
      while (seen.has(s)) {
        seen.set(base, seen.get(base) + 1);
        s = `${base}-${seen.get(base)}`;
      }
      seen.set(s, 0);
      return s;
    },
  };
}

// The plain text of a heading's inline Markdown, as the MDX tree's toString sees it.
export function headingText(raw) {
  return raw
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/<[^>]+>/g, '')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/(\*\*|__)(.+?)\1/g, '$2')
    .replace(/(^|[^\w*])[*_]([^*_]+)[*_](?=[^\w*]|$)/g, '$1$2')
    .trim();
}

// ---------------------------------------------------------------------------
// extractStructure(text) -> { anchors, headings, fences, admonitions }
//
// A deliberately small Markdown reader: it needs only to know what is inside a code
// fence (where `#` and `:::` are not structure) and what is front matter. Anything
// richer would be a second Markdown parser to keep in step with the MDX one.
// ---------------------------------------------------------------------------
export function extractStructure(text) {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const anchors = [];
  const headings = [0, 0, 0, 0, 0, 0];
  const fences = [];
  const admonitions = [];
  const ids = [];
  const slugger = createSlugger();
  let i = 0;

  if (lines[0] === '---') {
    const end = lines.indexOf('---', 1);
    if (end > 0) i = end + 1;
  }

  let fence = null; // { ch, len }
  for (; i < lines.length; i++) {
    const line = lines[i];
    if (fence) {
      const close = /^\s*(`{3,}|~{3,})\s*$/.exec(line);
      if (close && close[1][0] === fence.ch && close[1].length >= fence.len) fence = null;
      continue;
    }
    const open = /^\s*(`{3,}|~{3,})\s*([^\s`]*)/.exec(line);
    if (open) {
      fence = { ch: open[1][0], len: open[1].length };
      fences.push(open[2] || '(none)');
      continue;
    }
    const heading = /^ {0,3}(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      headings[heading[1].length - 1]++;
      const raw = heading[2].replace(/\s+#+\s*$/, '');
      const id = /\s*\{#([^}\s]+)\}\s*$/.exec(raw);
      if (id) anchors.push(id[1]);
      ids.push(slugger.slug(id ? id[1] : headingText(raw), Boolean(id)));
      continue;
    }
    const adm = /^\s*:{3,}([A-Za-z]+)/.exec(line);
    if (adm) admonitions.push(adm[1]);
  }
  return { anchors, ids, headings, fences, admonitions };
}

// compareStructure(en, loc) -> [{ check, detail }]
export function compareStructure(en, loc) {
  const out = [];
  // Anchors are compared as IDS, not as `{#…}` spellings, in both directions:
  //   - every explicit English anchor must be the id of some translated heading. In
  //     practice that means the translation carries the same `{#id}`; a heading left
  //     in English whose slug happens to equal it would also do — and would also work.
  //   - every explicit translated anchor must be an id English has, explicit or
  //     slugged. Translations routinely PIN an English heading's implicit slug
  //     (`## 概述 {#overview}`) so a link that works in English keeps working in every
  //     locale; that is correct and passes. What fails is an anchor English has under
  //     no name at all — which is what a translated id (`{#niveles}` for `{#tiers}`)
  //     or a typo looks like.
  const locIds = new Set(loc.ids);
  const enIds = new Set(en.ids);
  const missing = [...new Set(en.anchors)].filter(x => !locIds.has(x)).sort();
  const extra = [...new Set(loc.anchors)].filter(x => !enIds.has(x)).sort();
  if (missing.length) out.push({ check: 'anchors', detail: `missing English anchor(s): ${missing.map(x => `{#${x}}`).join(' ')}` });
  if (extra.length) out.push({ check: 'anchors', detail: `anchor(s) no English heading has: ${extra.map(x => `{#${x}}`).join(' ')}` });

  const levels = [];
  for (let l = 0; l < 6; l++) {
    if (en.headings[l] !== loc.headings[l]) levels.push(`h${l + 1} ${en.headings[l]} in English, ${loc.headings[l]} translated`);
  }
  if (levels.length) out.push({ check: 'headings', detail: `heading count differs: ${levels.join('; ')}` });

  if (en.fences.join('\0') !== loc.fences.join('\0')) {
    out.push({ check: 'fences', detail: `code fences differ: English [${en.fences.join(', ')}], translated [${loc.fences.join(', ')}]` });
  }
  if (en.admonitions.join('\0') !== loc.admonitions.join('\0')) {
    out.push({ check: 'admonitions', detail: `admonitions differ: English [${en.admonitions.join(', ')}], translated [${loc.admonitions.join(', ')}]` });
  }
  return out;
}

async function isDir(p) {
  try { return (await stat(p)).isDirectory(); } catch { return false; }
}

async function mdFiles(dir, prefix = '') {
  const found = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const name = prefix + entry.name;
    if (entry.isDirectory()) found.push(...await mdFiles(path.join(dir, entry.name), name + '/'));
    else if (entry.isFile() && /\.(md|mdx)$/.test(name)) found.push(name);
  }
  return found.sort();
}

function englishTreeFor(docsRoot, instance) {
  return instance === 'current'
    ? path.join(docsRoot, 'docs')
    : path.join(docsRoot, 'versioned_docs', instance);
}

// checkStructureParity(docsRoot, allowlist) -> { problems, stale, compared }
//
// Pages present on only one side are SKIPPED here, not reported: the file set is
// hack/check-docs-locale-parity.sh's job, and reporting it twice makes two guards
// disagree about whose failure it is. `compared` is returned so a caller can refuse
// a run that looked at nothing.
export async function checkStructureParity(docsRoot, allowlist = []) {
  const problems = [];
  let compared = 0;
  const used = new Set();
  const i18n = path.join(docsRoot, 'i18n');
  const locales = (await isDir(i18n))
    ? (await readdir(i18n, { withFileTypes: true })).filter(e => e.isDirectory()).map(e => e.name).sort()
    : [];

  for (const locale of locales) {
    const pluginDir = path.join(i18n, locale, 'docusaurus-plugin-content-docs');
    if (!await isDir(pluginDir)) continue;
    for (const inst of (await readdir(pluginDir, { withFileTypes: true })).filter(e => e.isDirectory())) {
      const instance = inst.name;
      const enTree = englishTreeFor(docsRoot, instance);
      const locTree = path.join(pluginDir, instance);
      if (!await isDir(enTree)) continue;
      const locSet = new Set(await mdFiles(locTree));
      for (const page of await mdFiles(enTree)) {
        if (!locSet.has(page)) continue;
        compared++;
        const [en, loc] = await Promise.all([
          readFile(path.join(enTree, page), 'utf8'),
          readFile(path.join(locTree, page), 'utf8'),
        ]);
        const label = instance === 'current' ? page : `${instance}/${page}`;
        for (const diff of compareStructure(extractStructure(en), extractStructure(loc))) {
          const idx = allowlist.findIndex(e => e.locale === locale && e.page === label && e.check === diff.check && e.detail === diff.detail);
          if (idx >= 0) { used.add(idx); continue; }
          problems.push({ locale, page: label, ...diff });
        }
      }
    }
  }
  const stale = allowlist.filter((_, idx) => !used.has(idx));
  return { problems, stale, compared };
}

export function validateAllowlist(list) {
  if (!Array.isArray(list)) throw new Error('allowlist must be a JSON array');
  for (const e of list) {
    if (!e || typeof e.locale !== 'string' || typeof e.page !== 'string' || !CHECKS.includes(e.check) ||
        typeof e.detail !== 'string' || typeof e.reason !== 'string' || e.reason.trim().length < 20) {
      throw new Error(`allowlist entry needs locale, page, check (one of ${CHECKS.join('/')}), the exact detail it accepts, and a real reason: ${JSON.stringify(e)}`);
    }
  }
  return list;
}

async function main(args) {
  const docsRoot = args[0] ? path.resolve(args[0]) : fileURLToPath(new URL('..', import.meta.url));
  const allowlistPath = fileURLToPath(new URL('./structure-parity-allowlist.json', import.meta.url));
  const allowlist = validateAllowlist(JSON.parse(await readFile(allowlistPath, 'utf8')));
  const { problems, stale, compared } = await checkStructureParity(docsRoot, allowlist);

  if (compared === 0) {
    console.error('::error::compared no translated pages at all — the enumeration is broken, not the tree');
    process.exitCode = 1;
    return;
  }
  for (const p of problems) console.log(`  ${p.locale}/${p.page}: ${p.detail}`);
  for (const e of stale) {
    console.log(`  STALE allowlist entry (suppresses nothing now — delete it): ${e.locale}/${e.page} [${e.check}]`);
  }
  if (problems.length || stale.length) {
    console.error(`
==> Translated docs have drifted structurally from English

A translation must keep English's explicit heading anchors ({#id}) — console deep
links and release notes point at them, and the site's own broken-anchor check
cannot see a link from outside the site — plus the same number of headings per
level, the same code fences and the same admonitions, in the same order.

Fix the translation. If the difference is intentional, add an entry with a reason
to docs/scripts/structure-parity-allowlist.json.`);
    process.exitCode = 1;
    return;
  }
  console.log(`==> Structure parity OK: ${compared} translated page(s) keep English's anchors, headings, fences and admonitions.`);
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  main(process.argv.slice(2)).catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

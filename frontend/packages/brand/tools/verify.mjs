// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Report where a consumer's declared brand values disagree with tokens.json.
//
//     node tools/verify.mjs
//     DC_WEBSITE=/path/to/devicechain-website node tools/verify.mjs
//
// This exists because "keep it in step with the console" was a COMMENT, not a
// guarantee, and it had already failed: the marketing site's lifted blue had
// drifted to a third value that neither the console nor the docs used, and the
// console's rounded HSL triple was not the same colour as the hex everyone else
// wrote. A comment cannot catch that. A script run in CI can.
//
// Deliberately read-only and advisory about WHICH way to fix things: it prints
// what disagrees and exits non-zero, but never rewrites a consumer, because
// changing a shipped colour is a visual decision, not a mechanical one.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { hexToHsl, hexToShadcnTriple } from './color.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = join(HERE, '..');
const REPO = join(ROOT, '..', '..', '..');
const tokens = JSON.parse(readFileSync(join(ROOT, 'tokens.json'), 'utf8'));

const PRIMARY = tokens.core.primary.value;
const BRIGHT = tokens.core.primaryBright.value;

const findings = [];
const checked = [];

function read(rel) {
  try {
    return readFileSync(join(REPO, rel), 'utf8');
  } catch {
    return null;
  }
}

/** Last declaration wins, matching the cascade, so scan all and keep the last. */
function declared(css, prop, withinSelector) {
  const scope = withinSelector
    ? (css.match(
        new RegExp(
          `${withinSelector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{([^}]*)\\}`,
          'g'
        )
      ) || []).join('\n')
    : css;
  const re = new RegExp(`--${prop}\\s*:\\s*([^;]+);`, 'g');
  let m;
  let last = null;
  while ((m = re.exec(scope))) last = m[1].trim();
  return last;
}

function compare(where, label, actual, expected, note) {
  checked.push(where + ' · ' + label);
  if (actual === null) {
    findings.push({ where, label, actual: '(not declared)', expected, note });
    return;
  }
  if (actual.toLowerCase() !== expected.toLowerCase()) {
    findings.push({ where, label, actual, expected, note });
  }
}

/* ── shadcn-triple consumers ───────────────────────────────────────────── */
//
// BOTH front-end apps, not just the console. Checking only the console was a
// real hole: apps/dashboard/src/theme.css carries the same token contract (it
// is what @devicechain/widgets reads, so a dashboard renders identically in
// either host) and had drifted to the same wrong triple — unnoticed, because
// nothing looked at it. A gate that covers one of two consumers mostly teaches
// you to trust it.
for (const rel of [
  'frontend/apps/console/src/index.css',
  'frontend/apps/dashboard/src/theme.css',
]) {
  const css = read(rel);
  if (!css) {
    console.warn(`skip: ${rel} not found`);
    continue;
  }
  if (/@import\s+['"]@devicechain\/brand\/css\/shadcn\.css['"]/.test(css)) {
    // A MIGRATED consumer declares nothing — it imports. See the docs branch
    // below for why an un-special-cased scan here is worse than useless.
    console.log(`ok: ${rel} imports the generated triples — nothing to compare`);
    continue;
  }
  // The :root block is light mode; .dark carries the lifted values.
  compare(
    rel,
    'light --primary',
    declared(css, 'primary', ':root'),
    hexToShadcnTriple(PRIMARY),
    `rounded triples do not round-trip: '197 71% 42%' renders #1f8cb7, not ${PRIMARY}`
  );
  compare(rel, 'dark --primary', declared(css, 'primary', '.dark'), hexToShadcnTriple(BRIGHT));
}

/* ── Docs: Infima hex, base AND the full ramp ──────────────────────────── */
{
  const rel = 'docs/src/css/custom.css';
  const css = read(rel);
  if (!css) {
    console.warn(`skip: ${rel} not found`);
  } else if (/@import\s+['"]@devicechain\/brand\/css\/infima\.css['"]/.test(css)) {
    // A MIGRATED consumer declares nothing — it imports. Without this branch the
    // declaration scan below finds nothing and reports 14 "(not declared)"
    // failures against a consumer that is in fact perfectly correct, which is
    // the fastest way to teach everyone to ignore this script.
    console.log(`ok: ${rel} imports the generated ramp — nothing to compare`);
  } else {
    // Checking only the base colour is NOT enough, and assuming otherwise gave
    // a false all-clear once already: the docs' base matched exactly while its
    // hand-built DARK ramp differed by up to 45/255 on --ifm-color-primary-
    // lightest. Infima renders links, hovers and admonitions out of the ramp,
    // not the base, so the ramp is the part a reader actually sees.
    const generated = readFileSync(join(ROOT, 'css/infima.css'), 'utf8');
    const STEPS = ['', '-dark', '-darker', '-darkest', '-light', '-lighter', '-lightest'];
    for (const [theme, selector] of [
      ['light', ':root'],
      ['dark', "[data-theme='dark']"],
    ]) {
      for (const step of STEPS) {
        const prop = `ifm-color-primary${step}`;
        compare(
          rel,
          `${theme} --${prop}`,
          declared(css, prop, selector),
          declared(generated, prop, selector)
        );
      }
    }
  }
}

/* ── Marketing site: a SEPARATE REPO, so opt in by path ────────────────── */
{
  const base = process.env.DC_WEBSITE;
  if (!base) {
    console.warn(
      'skip: marketing site not checked — set DC_WEBSITE=/path/to/devicechain-website'
    );
  } else {
    let css = null;
    try {
      css = readFileSync(join(base, 'brand.css'), 'utf8');
    } catch {
      console.warn(`skip: ${join(base, 'brand.css')} not found`);
    }
    if (css) {
      const rel = `${base}/brand.css`;
      compare(rel, '--brand', declared(css, 'brand', ':root'), PRIMARY);
      compare(
        rel,
        '--brand-bright',
        declared(css, 'brand-bright', ':root'),
        BRIGHT,
        'the site had drifted to a third value that neither console nor docs used'
      );
      compare(rel, '--bg-deep', declared(css, 'bg-deep', ':root'), tokens.surface.bgDeep.value);
      for (const k of ['aqua', 'violet', 'magenta', 'amber']) {
        compare(rel, `--${k}`, declared(css, k, ':root'), tokens.accents[k].value);
      }
    }
  }
}

/* ── Report ────────────────────────────────────────────────────────────── */

const hsl = hexToHsl(PRIMARY);
console.log(
  `\ncanonical primary ${PRIMARY} = hsl(${hsl.h} ${hsl.s}% ${hsl.l}%) = shadcn "${hexToShadcnTriple(
    PRIMARY
  )}"`
);
console.log(`canonical lifted  ${BRIGHT} = shadcn "${hexToShadcnTriple(BRIGHT)}"\n`);
console.log(`checked ${checked.length} declaration(s)`);

if (!findings.length) {
  console.log('no drift — every consumer agrees with tokens.json');
  process.exit(0);
}

console.log(`\n${findings.length} disagreement(s) with tokens.json:\n`);
for (const f of findings) {
  console.log(`  ${f.where}`);
  console.log(`    ${f.label}`);
  console.log(`      declared: ${f.actual}`);
  console.log(`      canonical: ${f.expected}`);
  if (f.note) console.log(`      note: ${f.note}`);
  console.log('');
}
console.log(
  'Each of these is a VISUAL decision. Migrate a consumer deliberately —\n' +
    'do not bulk-rewrite shipped colours to silence this script.'
);
process.exit(1);

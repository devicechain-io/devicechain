// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The gate over GENERATED output. Runs after the sanitizer, over every byte the
// pipeline is about to publish — schema files and the index alike.
//
// Why it exists at all: the repository's own guard, hack/check-docs-adr-refs.sh,
// scans COMMITTED sources on the assumption that the built site is a copy of what
// those sources say. Generating into docs/static/schema/ breaks that assumption —
// the files do not exist in the CI checkout, so the guard cannot see them, and a
// clean CI run would say nothing about what the site actually serves. This gate
// covers that hole, and is deliberately stricter than the guard it stands in for.
//
// 🔴 A gate over generated output is exactly the kind that goes quiet when the
// generator changes shape. It has its own planted-reference self-test — see
// sanitize.test.mjs; a build that skips the test suite is not a verified build.

// Dead citations: a pointer into a repository the reader of docs.devicechain.io
// cannot open. The first two are the repository's stated rule; the rest are the
// same thing wearing different clothes, which the stated rule does not name.
//
// Checked on EVERY line, comment or not — a citation is just as dead inside a
// generated JSON string as inside SDL prose.
const CITATIONS = [
  { name: 'decision-record number', re: /ADR-\d+/ },
  { name: 'bare "ADR"', re: /\bADR\b/ },
  { name: 'section marker', re: /§/ },
  { name: 'roadmap slice', re: /\bslice[ \t]+(?:[a-z]?\d[0-9a-z-]*|[a-z]\b)/i },
  { name: 'sub-decision marker', re: /\bSD-\d+/ },
  { name: 'numbered decision', re: /\bdecision[ \t]+\d+/i },
  { name: 'private planning workspace', re: /\.agent-os/ },
];

// Wreckage a prose-rewriting regex leaves when a rule guesses wrong. Without
// these the gate would pass "A uniform reference to an entity ()." — no citation
// survives, and the sentence is still visibly broken.
//
// Checked ONLY on lines the sanitizer actually rewrote. Running them over the
// whole file manufactures findings out of punctuation nobody touched: a JSON
// example containing `[[lon,lat],...]` trips "doubled punctuation", and the range
// `startIndex .. startIndex+count-1` trips "space before punctuation". Both are
// correct as written, and a gate that cries wolf about its own inputs is one
// people learn to wave through.
//
// Narrowing worth knowing: these catch SHAPES, not meaning. A strip that leaves a
// grammatical but wrong sentence passes here — which is why the sanitizer's
// output is also pinned by golden tests over the real corpus.
const MANGLED = [
  { name: 'empty parenthetical', re: /\([ \t]*\)/ },
  { name: 'parenthetical opening on punctuation', re: /\([ \t]*[,;/]/ },
  { name: 'parenthetical closing on punctuation', re: /[,;/][ \t]*\)/ },
  { name: 'space before punctuation', re: /[ \t]+[.,;:)]/ },
  { name: 'dangling dash before punctuation', re: /[—–][ \t]*[.,;:]/ },
  { name: 'doubled punctuation', re: /,[ \t]*\./ },
  { name: 'leaked sanitizer marker', re: /\u0001/ },
];

/**
 * Scan one generated artifact. Returns [] when clean, otherwise one finding per
 * offending line — the line number is relative to `text`, so the caller can name
 * a file and a line the author can go and fix.
 *
 * `touched` is the set of 1-based line numbers the sanitizer rewrote, as returned
 * by sanitizeSdl. Omitting it checks citations only, which is what an artifact
 * with no sanitizer pass (the index) needs.
 */
export function scan(label, text, touched = new Set()) {
  const findings = [];
  text.split('\n').forEach((line, idx) => {
    const rules = touched.has(idx + 1) ? [...CITATIONS, ...MANGLED] : CITATIONS;
    for (const { name, re } of rules) {
      const m = re.exec(line);
      if (m) findings.push({ label, line: idx + 1, rule: name, match: m[0], text: line.trim() });
    }
  });
  return findings;
}

export function formatFindings(findings) {
  return findings
    .map((f) => `  ${f.label}:${f.line}  [${f.rule}] ${JSON.stringify(f.match)}\n      ${f.text}`)
    .join('\n');
}

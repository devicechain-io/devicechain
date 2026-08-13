// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Strips private-repo citations out of GraphQL SDL comments on the way to the
// published docs site, keeping the sentence they were embedded in.
//
// The repository convention allows a decision-record citation in source comments
// and forbids it on any surface a reader of the published site sees. An SDL file
// is both: committed source, and — once this pipeline serves it — a page on
// docs.devicechain.io. Rewriting the 163 citations at the source would fight the
// convention and rot straight back, so the rewrite happens here, at the moment
// the file crosses from one surface to the other.
//
// Two properties this is built around, in order of importance:
//
//  1. A comment block carrying no citation is passed through BYTE-IDENTICAL. All
//     the collateral-damage risk in a prose-rewriting regex lives in the lines it
//     had no business touching, so it does not get to touch them.
//  2. The output is GATED, not trusted (see gate.mjs). Every rule below is a
//     guess about the shape of English; the gate is what turns a wrong guess into
//     a build failure rather than a mangled sentence nobody reads closely.

// A record number, including the compact multi-number form "-013/044".
const NUMBER = String.raw`ADR-\d+(?:/\d+)*`;

// Roadmap coordinates that ride along with a number: "§6", "S5c′", "G4", "P2",
// "SD-3", "slice 7b", "decision 3", "Zone 1", "Tier 2", "addendum 2026-07-01".
//
// A coordinate is only ever removed at a POSITION where it cannot be prose. There
// are two such positions, and nothing is stripped anywhere else — "Tier 2" and
// "Zone 1" are also ordinary English, and a list of bare tokens to delete from
// running prose would be a menace.
const COORDS = [
  String.raw`§\d+(?:\.\d+)*[a-z]?`,
  String.raw`SD-\d+`,
  // A slice coordinate always carries a number ("9e", "6c-2", "C4b" — the leading
  // letter is optional and may be capitalized) — except for the lone "slice b",
  // hence the single-letter branch. Requiring a digit or a lone letter is what
  // stops this eating "a slice of the fleet".
  String.raw`slice[ \t]+(?:[A-Za-z]?\d[0-9A-Za-z-]*|[a-z](?![a-z]))`,
  String.raw`decision[ \t]+\d+(?:['′]s)?`,
];

// The short forms. Only ever safe against the strongest anchor — directly after a
// record number — because on their own they are indistinguishable from a metric
// name or an enum value.
const WEAK_COORDS = [
  String.raw`S\d+[a-z]*['′]?`,
  String.raw`SP\d+[a-z]?`,
  String.raw`G\d+`,
  String.raw`P\d+`,
  String.raw`Zone[ \t]+\d+`,
  String.raw`Tier[ \t]+\d+`,
  String.raw`addendum[ \t]+\d{4}-\d{2}-\d{2}`,
];

// ANCHOR 1 — trailing a record number, however the two are separated. A newline,
// a slash and a comma all count: the corpus has "— the ADR-043\n# decision 3
// enqueue gate", "(ADR-061 G2 / SD-5)" and "(ADR-037 / ADR-057, slice 7c)", and a
// same-line-space-only rule orphans the coordinate half of every one of them.
// Widening the separator stays safe because the alternation only fires when a
// coordinate actually follows — "(ADR-012, a device thing)" matches nothing.
const COORD_TAIL = String.raw`(?:[ \t\n]*[/,][ \t\n]*|[ \t\n]+)(?:`
  + [...COORDS, ...WEAK_COORDS].join('|') + String.raw`)`;

// ANCHOR 2 — opening a parenthetical, with no record number in sight. "(slice
// 9a)", "(decision 4)", "(§3.2)", "(slice 9e node trace)". An aside that OPENS on
// a coordinate is a citation whether or not a number was written next to it, and
// it is just as dead a pointer. Only the unambiguous COORDS qualify here; the
// short forms would be guessing.
const COORD_PAREN = new RegExp(String.raw`(\()(?:` + COORDS.join('|') + String.raw`)`, 'g');

const CITATION = new RegExp(NUMBER + String.raw`(?:` + COORD_TAIL + String.raw`)*`, 'g');

/** True if the text carries something this module would rewrite. */
export function hasCitation(text) {
  return new RegExp(NUMBER).test(text) || new RegExp(COORD_PAREN.source).test(text);
}

// A sentinel standing in for a removed citation, so the cleanup rules below can
// see WHERE the removal happened. Markers rather than one clever regex are what
// make the article fix possible: "not an ADR-042 token" has to become "not a
// token", and only the removal site knows which article is now wrong.
const MARK = '\u0001';

// Words that cannot end a parenthetical. One left dangling on any of these lost
// its object to the strip — "(§4, re-homed by ADR-065)" collapses to "(re-homed
// by)" — so the whole aside goes rather than being propped up.
const DANGLING = 'by|via|per|see|under|and|or|in|of|from|to|with|at|for|the|a|an';

function article(nextWord, capitalized) {
  const a = /^[aeiouAEIOU]/.test(nextWord) ? 'an' : 'a';
  return capitalized ? a[0].toUpperCase() + a.slice(1) : a;
}

/**
 * Rewrite one comment block's text. `text` may span lines: a citation can break
 * across two comment lines ("ADR-053 §5 /\nADR-045"), and a line-oriented pass
 * mangles both halves. When a strip consumes a newline the block comes back with
 * fewer lines than it went in with, which is correct — the sentence rejoined.
 */
export function sanitizeText(text) {
  if (!hasCitation(text)) return text;

  let s = text.replace(CITATION, MARK);
  s = s.replace(COORD_PAREN, `$1${MARK}`);

  // Article agreement, while the markers still say where the hole was.
  s = s.replace(
    new RegExp(`\\b(an?|An?)([ \\t\\n]+)${MARK}([ \\t\\n]+)(\\S)`, 'g'),
    (_m, art, ws1, ws2, next) => {
      const gap = (ws1 + ws2).includes('\n') ? '\n' : ' ';
      return article(next, art[0] === art[0].toUpperCase()) + gap + next;
    },
  );

  // Marker with whitespace on both sides collapses to one separator; a newline in
  // the removed run survives as a newline so the block keeps its shape.
  s = s.replace(new RegExp(`([ \\t\\n]+)${MARK}([ \\t\\n]+)`, 'g'),
    (_m, a, b) => ((a + b).includes('\n') ? '\n' : ' '));
  // Whitespace on one side only, or neither: the marker takes it along.
  s = s.replace(new RegExp(`[ \\t\\n]+${MARK}`, 'g'), '');
  s = s.replace(new RegExp(`${MARK}[ \\t\\n]*`, 'g'), '');

  // --- structural cleanup of what the strip left behind ---

  // Separator debris in a now-shorter aside: "(RBAC /)", "(, re-homed by)",
  // "(— unbuilt today, so never returned yet)".
  s = s.replace(/\([ \t]*[,;/—–][ \t]*/g, '(');
  s = s.replace(/[ \t,;/]+\)/g, ')');
  s = s.replace(/[ \t]*[—–-][ \t]*\)/g, ')');

  // An aside with no letters and no digits left in it was only ever the citation.
  // The digit test is load-bearing: "(1–100)" is a real range and must survive.
  s = s.replace(/[ \t]*\([^()A-Za-z0-9]*\)/g, '');

  // An aside ending on a preposition or conjunction lost its object.
  s = s.replace(new RegExp(`[ \\t]*\\([^()]*\\b(?:${DANGLING})\\)`, 'gi'), '');

  // Punctuation left stranded at the head of a line, because the aside that ended
  // the previous sentence began that line and was removed whole. Pull it back up
  // rather than publishing a comment line that reads "# ." — and put the line
  // break back afterwards, so the block keeps its shape instead of collapsing two
  // lines into one very long one.
  //
  // Sentence-ending marks only. "!" is deliberately absent: the corpus contains
  // "— null when\n# !ok. It decodes …", where "!" is a negation operator, and
  // pulling it up produces "null when!ok".
  s = s.replace(/\n[ \t]*([.,;:])[ \t]*/g, '$1\n');

  // Leading whitespace a removal left at the head of a line — "# (ADR-038) the
  // console applies …" loses the aside and keeps the space that followed it.
  s = s.replace(/\n[ \t]+/g, '\n');

  // Punctuation left floating by a removal.
  s = s.replace(/[ \t]+([.,;:)])/g, '$1');
  s = s.replace(/[ \t]*[—–][ \t]*([.,;:])/g, '$1');
  s = s.replace(/,[ \t]*\./g, '.');
  s = s.replace(/[ \t]{2,}/g, ' ');

  // A block that opened on its citation — "# (ADR-033). Identities, …".
  // Spaces and punctuation only, never a newline: several schemas open with a
  // bare "#" above the title as a box rule, and eating it leaves half a box.
  s = s.replace(/^[ \t.,;:/—–-]+/, '');

  return s
    .split('\n')
    .map((line) => line.replace(/[ \t]+$/, ''))
    .join('\n')
    .replace(/\n+$/, '');
}

/**
 * Re-flow a block whose lines got longer than they started, and only such a
 * block. A citation broken across two comment lines takes the line break with it
 * when it goes — correct for the sentence, but it leaves one 155-column line in
 * a file wrapped at 90.
 *
 * Conditional on purpose. Re-flowing every block unconditionally would reformat
 * comments the citation never reached into, and the wrap width is the block's
 * OWN original width rather than a house number, so a narrow comment stays narrow.
 */
function rewrapToWidth(lines, width) {
  if (!lines.some((l) => l.length > width)) return lines;

  // Blank comment lines are paragraph breaks and survive; everything between two
  // of them re-flows as one paragraph. Wrapping the over-long line alone would
  // leave the ragged short line that followed it stranded mid-paragraph.
  const out = [];
  let paragraph = [];
  const flush = () => {
    if (!paragraph.length) return;
    let current = '';
    for (const word of paragraph.join(' ').split(/[ \t]+/)) {
      if (current === '') current = word;
      else if (`${current} ${word}`.length <= width) current += ` ${word}`;
      else {
        out.push(current);
        current = word;
      }
    }
    if (current !== '') out.push(current);
    paragraph = [];
  };

  for (const line of lines) {
    if (line === '') {
      flush();
      out.push('');
    } else paragraph.push(line);
  }
  flush();
  return out;
}

const COMMENT = /^([ \t]*)#[ \t]?(.*)$/;

/**
 * Rewrite the `#` comments of a GraphQL SDL document. Non-comment lines, and
 * comment blocks carrying no citation, are returned unchanged.
 *
 * A "block" is a run of consecutive comment lines at the SAME indent. Grouping is
 * what lets a citation broken across two lines be repaired; requiring equal indent
 * is what stops the grouping from reflowing a top-level comment and the field
 * comment beneath it into one.
 *
 * Returns the rewritten text along with the 1-based line numbers it WROTE. That
 * set is what scopes the gate's mangled-prose rules: run them over the whole file
 * and they report on punctuation nobody touched — a JSON example containing
 * `[[lon,lat],...]`, or the range `startIndex .. startIndex+count-1` — which is a
 * gate crying wolf about its own inputs.
 */
export function sanitizeSdl(source) {
  const lines = source.split('\n');
  const out = [];
  const touched = new Set();

  for (let i = 0; i < lines.length; ) {
    const head = COMMENT.exec(lines[i]);
    if (!head) {
      out.push(lines[i]);
      i += 1;
      continue;
    }

    const indent = head[1];
    const texts = [];
    let j = i;
    for (; j < lines.length; j += 1) {
      const c = COMMENT.exec(lines[j]);
      if (!c || c[1] !== indent) break;
      texts.push(c[2]);
    }

    const joined = texts.join('\n');
    if (!hasCitation(joined)) {
      for (let k = i; k < j; k += 1) out.push(lines[k]);
      i = j;
      continue;
    }

    const cleaned = sanitizeText(joined);
    const width = Math.max(...texts.map((t) => t.length));
    // A block that was ONLY a citation is dropped, not left as a bare "#".
    if (cleaned.trim() !== '') {
      for (const line of rewrapToWidth(cleaned.split('\n'), width)) {
        out.push(line === '' ? `${indent}#` : `${indent}# ${line}`);
        touched.add(out.length);
      }
    }
    i = j;
  }

  return { text: out.join('\n'), touched };
}

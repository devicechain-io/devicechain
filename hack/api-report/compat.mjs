// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Compare the API of a package at the last release with the working tree's, and read the
// declared breaks. Pure functions over the models model.mjs builds, so every rule here is
// tested without a compiler (compat.test.mjs).

/**
 * One difference between two models of the same package.
 *   kind: export-removed | export-changed | member-added | export-added
 *   level: breaking | dangerous | safe
 */

// A property added to an interface or class without `?`: every object a consumer builds to
// that type stops compiling. It is not a break for a consumer that only READS the type, and
// the type's role is not visible from a declaration, so it is reported and never fails.
const REQUIRED_MEMBER = /^\s{4}(?:readonly\s+)?(?:[A-Za-z_$][\w$]*|'[^']*'|"[^"]*"|\[[^\]]+\])\s*:\s/;

// A line compares without its trailing `,` or `;`: the last arm of a union, array or member
// list gains a comma the moment another is appended after it, which is not a change to it.
const stem = (l) => l.replace(/[;,]\s*$/, '');

/**
 * Each line with the lines that open the blocks it sits in, by indentation: the key a line is
 * compared by. Without it a member that MOVED from one nested object to another of the same
 * declaration (`o: { a?: string }` -> `p: { a?: string }`) leaves the multiset of lines equal,
 * and a break reads as no change at all.
 */
function keyed(lines) {
  const open = []; // { indent, key } of each enclosing block opener
  return lines.map((l) => {
    const indent = /^\s*/.exec(l)[0].length;
    while (open.length > 0 && open[open.length - 1].indent >= indent) open.pop();
    const key = [...open.map((o) => o.key), stem(l).trim()].join('\n');
    if (/[{([<]\s*$/.test(l)) open.push({ indent, key });
    return { line: l, key };
  });
}

/** Multiset difference a \ b, as an array of a's lines (order of a kept). */
function minus(a, b) {
  const counts = new Map();
  for (const { key } of keyed(b)) counts.set(key, (counts.get(key) ?? 0) + 1);
  const out = [];
  for (const { line, key } of keyed(a)) {
    const n = counts.get(key) ?? 0;
    if (n > 0) counts.set(key, n - 1);
    else out.push(line);
  }
  return out;
}

export function diffModels(base, head) {
  const changes = [];
  for (const [id, was] of base) {
    // An unexported declaration that disappeared is a refactor unless something exported
    // still reaches it, in which case the exported declaration's own lines changed too.
    const internal = id.startsWith('~');
    if (!head.has(id)) {
      if (internal) continue;
      changes.push({ id, kind: 'export-removed', level: 'breaking', detail: was[0] ?? '' });
      continue;
    }
    const now = head.get(id);
    const lost = minus(was, now);
    const gained = minus(now, was);
    if (lost.length === 0 && gained.length === 0) continue;
    if (lost.length > 0) {
      changes.push({
        id,
        kind: 'export-changed',
        level: 'breaking',
        detail: `- ${lost[0].trim()}${lost.length > 1 ? ` (and ${lost.length - 1} more)` : ''}`,
      });
      continue;
    }
    const required = gained.filter((l) => REQUIRED_MEMBER.test(l) && !/^\s+[^:]*\?\s*:/.test(l));
    if (required.length > 0) {
      changes.push({ id, kind: 'member-added', level: 'dangerous', detail: `+ ${required[0].trim()}` });
    } else {
      changes.push({ id, kind: 'member-added', level: 'safe', detail: `+ ${gained[0].trim()}` });
    }
  }
  for (const id of head.keys()) {
    if (!base.has(id) && !id.startsWith('~')) changes.push({ id, kind: 'export-added', level: 'safe', detail: '' });
  }
  return changes.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

/**
 * Parse a breaks file: `<baseline-tag> <package> <id> <kind> <reason…>` per line, `#` comments.
 * Returns { entries, problems }; a line with no reason is a problem, not an entry.
 */
export function parseBreaks(text) {
  const entries = [];
  const problems = [];
  text.split(/\r?\n/).forEach((raw, i) => {
    const line = raw.trim();
    if (line === '' || line.startsWith('#')) return;
    const m = /^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*(.*)$/.exec(line);
    if (!m) {
      problems.push(`line ${i + 1}: expected "<baseline-tag> <package> <id> <kind> <reason>", got: ${line}`);
      return;
    }
    const [, baseline, pkg, id, kind, reason] = m;
    if (reason.trim() === '') {
      problems.push(`line ${i + 1}: ${pkg} ${id} ${kind} has no reason; say why, and what a consumer uses instead`);
      return;
    }
    entries.push({ baseline, pkg, id, kind, reason: reason.trim(), line: i + 1 });
  });
  return { entries, problems };
}

/**
 * Reconcile changes with declarations.
 *   changes: [{ pkg, id, kind, level, detail }]
 * Returns { failures, declared, expired, dangerous, safe } where every failure is a line.
 *
 * - a breaking change with a live declaration passes and is `declared`;
 * - a breaking change without one fails and prints the line that would declare it;
 * - a declaration for another baseline is `expired` (never failing: a tag push must not turn
 *   the tree red) and covers nothing;
 * - a declaration for THIS baseline that matches no breaking change is stale and fails.
 */
export function reconcile(changes, entries, baseline) {
  const failures = [];
  const declared = [];
  const expired = [];
  const used = new Set();
  const live = entries.filter((e) => e.baseline === baseline);
  for (const e of entries) if (e.baseline !== baseline) expired.push(e);

  for (const c of changes.filter((x) => x.level === 'breaking')) {
    const hit = live.findIndex((e) => e.pkg === c.pkg && e.id === c.id && e.kind === c.kind);
    if (hit >= 0) {
      used.add(hit);
      declared.push({ ...c, reason: live[hit].reason });
    } else {
      failures.push(
        `FAIL ${c.pkg}: ${c.id} ${c.kind} ${c.detail}\n` +
          `     declare with: ${baseline} ${c.pkg} ${c.id} ${c.kind} <why, and what a consumer uses instead>`,
      );
    }
  }
  live.forEach((e, i) => {
    if (!used.has(i)) {
      failures.push(
        `FAIL stale declaration (line ${e.line}): ${e.baseline} ${e.pkg} ${e.id} ${e.kind} matches no breaking change; delete it`,
      );
    }
  });
  return {
    failures,
    declared,
    expired,
    dangerous: changes.filter((c) => c.level === 'dangerous'),
    safe: changes.filter((c) => c.level === 'safe'),
  };
}

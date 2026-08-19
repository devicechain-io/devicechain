// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The cross-language pin between what this console can AUTHOR and what the compiler will
// ACCEPT — and, for every kind, whether a stored rule survives being opened here.
//
// It exists because the console shipped a taxonomy one kind short of the compiler's for an
// entire release, and nothing noticed. `connectivity` has always compiled; the form's list
// simply never learned about it, so opening such a rule relabelled it a threshold on screen.
// A comment above the union said the list "mirrors rules.RuleType". The comment was the only
// thing asserting it, and a comment cannot fail.
//
// 🔴 IT READS THE ENFORCING SWITCH, NOT THE CONST BLOCK, AND THE DIFFERENCE IS THE POINT.
// A `RuleType` const with no `case` in compile.go is a value the compiler REJECTS. Gating on
// the const block would demand this console offer it — manufacturing the exact superset
// defect this file exists to prevent, by obeying the gate. The switch is what runs; the
// const block is a list of names, some of which may not be wired to anything.
//
// The direction of the link is deliberate: TS reading Go is safe, because vitest has no
// cross-module test cache to replay a stale PASS over a changed Go file (`go test` does,
// which is why the reverse would be unsound). Same reasoning as area-catalog.test.ts.

import { describe, it, expect } from 'vitest';
import COMPILE_GO from '../../../../../../backend/services/event-processing/internal/rules/compile.go?raw';
import SCHEMA_GO from '../../../../../../backend/services/event-processing/internal/rules/schema.go?raw';
import { RULE_TYPES, parseDefinition, rebuildFrom, ruleTypeOptions } from './DetectionRuleForm';
import FORM_SRC from './DetectionRuleForm.tsx?raw';
import type { TFunction } from 'i18next';
import { ruleSurvivesRoundTrip } from './rule-equal';

// ── Reading the Go ──────────────────────────────────────────────────────────

/** Every `Ident SomeType = "value"` const in a source file, as ident → wire value. */
function constValues(src: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of src.matchAll(/^\s*([A-Z][A-Za-z0-9]*)\s+[A-Za-z][A-Za-z0-9]*\s*=\s*"([^"]*)"/gm)) {
    out.set(m[1], m[2]);
  }
  return out;
}

/** The body of a top-level Go func, from its signature to the next top-level `func`. */
function funcBody(src: string, signature: string): string {
  const start = src.indexOf(signature);
  if (start < 0) throw new Error(`no func matching ${JSON.stringify(signature)}`);
  const rest = src.slice(start + signature.length);
  const end = rest.indexOf('\nfunc ');
  return end < 0 ? rest : rest.slice(0, end);
}

/**
 * The identifiers named in `case` clauses in a region, with the given prefix.
 *
 * The prefix filter is what keeps a switch that mixes vocabularies (compile.go's main switch
 * is pure, but `forbid` tables and error paths are not) from contributing noise.
 */
function caseIdents(region: string, prefix: string): string[] {
  const out = new Set<string>();
  for (const line of region.split('\n')) {
    const m = /^\s*case\s+(.+?):/.exec(line);
    if (!m) continue;
    for (const id of m[1].split(',')) {
      const name = id.trim();
      if (name.startsWith(prefix)) out.add(name);
    }
  }
  return [...out];
}

/** Resolve Go const identifiers to their wire values, failing loudly on an unknown one. */
function wireValues(idents: string[], consts: Map<string, string>): string[] {
  return idents.map((id) => {
    const v = consts.get(id);
    // A const whose value is NOT a plain string literal (an alias, a concatenation, a
    // reference to another package) lands here rather than being silently dropped — which is
    // precisely how a real kind would go missing while every count still looked right.
    if (v === undefined) throw new Error(`const ${id} has no plain string literal value in schema.go`);
    return v;
  });
}

const SCHEMA_CONSTS = constValues(SCHEMA_GO);

// ── The parse is asserted before anything is compared against it ────────────
//
// 🔴 THIS IS THE WHOLE FILE'S NEGATIVE CONTROL. Every check below is a set comparison, and a
// regex that silently matched nothing turns every one of them into "∅ equals ∅, green". A
// minimum count and a known member BY NAME is what separates "the vocabularies agree" from
// "the extractor is broken".

describe('the Go extraction itself', () => {
  it('reads plausible const values out of schema.go', () => {
    expect(SCHEMA_CONSTS.size).toBeGreaterThan(20);
    expect(SCHEMA_CONSTS.get('TypeThreshold')).toBe('threshold');
    expect(SCHEMA_CONSTS.get('TypeConnectivity')).toBe('connectivity');
    expect(SCHEMA_CONSTS.get('AggCount')).toBe('count');
  });

  it('finds a func body, and refuses a signature that is not there', () => {
    expect(funcBody(SCHEMA_GO, 'func (s Severity) Valid() bool {').length).toBeGreaterThan(20);
    expect(() => funcBody(SCHEMA_GO, 'func (s Severity) NotAThing() bool {')).toThrow();
  });

  it('refuses a const identifier it cannot resolve — the control for wireValues', () => {
    // Without this, an unresolvable ident could silently drop out of a set and the
    // comparison would pass one member short.
    expect(() => wireValues(['TypeNotReal'], SCHEMA_CONSTS)).toThrow(/TypeNotReal/);
  });
});

// ── Vocabulary lockstep, each against the switch that ENFORCES it ───────────

const VOCABULARIES: {
  name: string;
  backend: () => string[];
  console: readonly string[];
  known: string;
  min: number;
}[] = [
  {
    name: 'rule types',
    // compile.go's main dispatch: the set of kinds that actually lower to something.
    backend: () => wireValues(caseIdents(funcBody(COMPILE_GO, 'func Compile(r Rule, limits Limits) (*CompiledRule, error) {'), 'Type'), SCHEMA_CONSTS),
    console: RULE_TYPES,
    known: 'connectivity',
    min: 8,
  },
];

describe.each(VOCABULARIES)('$name', (v) => {
  it('extracts a plausible set from the enforcing switch', () => {
    const set = v.backend();
    expect(set.length).toBeGreaterThanOrEqual(v.min);
    expect(set).toContain(v.known);
  });

  // 🔴 BOTH DIRECTIONS, and neither is the boring one.
  //
  //   missing  the console cannot author a kind the compiler accepts — the feature is
  //            unreachable, which is what shipped.
  //   extra    the console offers a kind the compiler REJECTS — the author meets the failure
  //            at publish, after doing the work, instead of at the picker.
  it('agrees with the console, in both directions', () => {
    const backend = v.backend();
    expect({ missing: backend.filter((k) => !v.console.includes(k)), extra: v.console.filter((k) => !backend.includes(k)) })
      .toEqual({ missing: [], extra: [] });
  });
});

// ── The corpus: every kind, opened and re-saved, must lose nothing ──────────
//
// This is the check the vocabulary comparison above cannot make. Adding a kind to the
// console's list makes the picker offer it; it does NOT make the form able to READ one. Those
// are separate abilities and the release defect was the second one — so a gate that only
// compared vocabularies would have gone green while opening a connectivity rule still
// rewrote it.
//
// 🔴 THE KEYS ARE CHECKED AGAINST THE BACKEND, so this corpus cannot fall behind. A ninth
// rule type fails the first test here until somebody writes a fixture for it, and writing
// that fixture is what forces them to find out whether the form can express it.

const CORPUS: Record<string, string> = {
  threshold: JSON.stringify({ name: 'r', type: 'threshold', severity: 'major', when: { metric: 'tempC', op: 'gt', threshold: 30 } }),
  duration: JSON.stringify({ name: 'r', type: 'duration', when: { metric: 'tempC', op: 'gt', threshold: 30 }, hold: '5m' }),
  absence: JSON.stringify({ name: 'r', type: 'absence', timeout: '10m' }),
  repeating: JSON.stringify({ name: 'r', type: 'repeating', when: { metric: 'tempC', op: 'gt', threshold: 30 }, count: 3, window: '5m' }),
  deltaRate: JSON.stringify({ name: 'r', type: 'deltaRate', metric: 'tempC', op: 'gt', threshold: 2, rate: true }),
  aggregate: JSON.stringify({ name: 'r', type: 'aggregate', agg: 'avg', op: 'gt', threshold: 30, windowMode: 'tumbling', window: '5m', metric: 'tempC' }),
  correlation: JSON.stringify({ name: 'r', type: 'correlation', anchorType: 'area', count: 3, window: '5m' }),
  // The kind that was missing. Leaf-less and config-less: the compiler forbids every
  // authorable field, so the whole rule is its header plus its actions.
  connectivity: JSON.stringify({ name: 'r', type: 'connectivity', severity: 'critical', actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'offline' } }] }),
};

describe('the round-trip corpus', () => {
  it('covers exactly the kinds the compiler accepts', () => {
    const backend = wireValues(caseIdents(funcBody(COMPILE_GO, 'func Compile(r Rule, limits Limits) (*CompiledRule, error) {'), 'Type'), SCHEMA_CONSTS);
    expect(Object.keys(CORPUS).sort()).toEqual([...backend].sort());
  });

  it.each(Object.entries(CORPUS))('opens and re-saves a %s rule without losing anything', (kind, definition) => {
    const parsed = parseDefinition(definition);
    expect(parsed, `${kind}: the form could not parse its own corpus entry`).not.toBeNull();
    expect(parsed!.type, `${kind}: the form relabelled the rule`).toBe(kind);
    expect(ruleSurvivesRoundTrip(definition, rebuildFrom(parsed!))).toBe(true);
  });

  // 🔴 THE CONTROL. Every assertion above is "nothing was lost", and a
  // ruleSurvivesRoundTrip that always returned true would satisfy all of them. This is a
  // definition carrying a field the form genuinely does not model, and it MUST be reported.
  it('reports a definition carrying a field the form cannot express', () => {
    const exotic = JSON.stringify({ name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 }, futureKnob: 'something the form never learned' });
    const parsed = parseDefinition(exotic);
    expect(parsed).not.toBeNull();
    expect(ruleSurvivesRoundTrip(exotic, rebuildFrom(parsed!))).toBe(false);
  });
});


// ── What the operator is actually OFFERED ───────────────────────────────────
//
// 🔴 EVERY CHECK ABOVE COMPARES LISTS IN SOURCE, AND A LIST IS NOT A PICKER. The vocabulary
// can agree perfectly while the operator is shown fewer kinds than it contains — this file
// already holds the move that would do it: `orderedOps` is `allOps(t).filter(...)`, a
// perfectly reasonable thing to write. The same line applied to the rule-type picker, at the
// function or at its call site, leaves every set comparison green and the picker short.
//
// So the offered set is checked as a set, and the call site is checked for the one shape
// that could narrow it downstream. The second is a TRIPWIRE, not a proof: it can only catch
// the narrowing written where this test looks. It is here because the alternative — the
// defect that shipped — is worth a tripwire.

describe('the rule type picker', () => {
  // A TFunction stand-in that echoes its key: this asserts WHICH options exist, not how they
  // are worded, and a missing translation must not be able to fail this test.
  const echo = ((k: string) => k) as unknown as TFunction;

  it('offers exactly the taxonomy, in its order', () => {
    expect(ruleTypeOptions(echo).map((o) => o.value)).toEqual([...RULE_TYPES]);
  });

  it('names a distinct label and description key per kind', () => {
    // A Record<RuleType, string> of key stems makes the compiler demand an entry per kind,
    // but not that the entries DIFFER — two kinds sharing a stem would render two identical
    // picker rows, which is a real way to lose a kind while every count stays right.
    const opts = ruleTypeOptions(echo);
    expect(new Set(opts.map((o) => o.label)).size).toBe(opts.length);
    expect(new Set(opts.map((o) => o.description)).size).toBe(opts.length);
  });

  it('hands the picker the whole option set, unfiltered', () => {
    expect(FORM_SRC).toContain('options={ruleTypeOptions(t)}');
    // The tripwire proper. Anything between the call and the closing brace — `.filter(`,
    // `.slice(`, a conditional — is a narrowing this file cannot otherwise see.
    expect(FORM_SRC).not.toMatch(/ruleTypeOptions\(t\)\s*[.[]/);
  });
});

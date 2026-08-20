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
// The model comes from the model module; only the picker's option list — which needs
// translation and is therefore presentation — comes from the component.
import { MAX_ACTIONS_PER_RULE, RULE_TYPES, conditionForbidden, parseDefinition, rebuildFrom } from './rule-model';
import { ruleTypeOptions } from './DetectionRuleForm';
import FORM_SRC from './DetectionRuleForm.tsx?raw';
import { CONDITION_TYPES } from './canvas/model';
import EN_RULES from '@/i18n/locales/en/deviceProfiles.json';
import ES_RULES from '@/i18n/locales/es/deviceProfiles.json';
import type { TFunction } from 'i18next';
import { ruleSurvivesRoundTrip } from './rule-equal';
import { constValues, dispatchValues, funcBody, intConst } from './go-source';

// ── Reading the Go ──────────────────────────────────────────────────────────

const SCHEMA_CONSTS = constValues(SCHEMA_GO);

/**
 * The rule kinds Compile's main dispatch actually lowers — the enforced set.
 *
 * `default` is skipped because Go spells the fall-through arm that way and it names no kind;
 * it is listed explicitly rather than filtered by shape so that any OTHER unrecognised ident
 * still throws.
 */
function ruleTypesTheCompilerAccepts(): string[] {
  const region = funcBody(COMPILE_GO, 'func Compile(r Rule, limits Limits) (*CompiledRule, error) {');
  return dispatchValues(region, SCHEMA_CONSTS, 'RuleType', ['default']);
}

// ── The parse is asserted before anything is compared against it ────────────
//
// 🔴 THIS IS THE WHOLE FILE'S NEGATIVE CONTROL. Every check below is a set comparison, and a
// regex that silently matched nothing turns every one of them into "∅ equals ∅, green". A
// minimum count and a known member BY NAME is what separates "the vocabularies agree" from
// "the extractor is broken".

describe('the Go extraction itself', () => {
  it('reads plausible const values, with their declared types, out of schema.go', () => {
    expect(SCHEMA_CONSTS.size).toBeGreaterThan(20);
    expect(SCHEMA_CONSTS.get('TypeThreshold')).toEqual({ type: 'RuleType', value: 'threshold' });
    expect(SCHEMA_CONSTS.get('TypeConnectivity')).toEqual({ type: 'RuleType', value: 'connectivity' });
    // A different vocabulary in the same file, to prove the declared type is really read and
    // not assumed — this is what lets the dispatch scan tell kinds from everything else.
    expect(SCHEMA_CONSTS.get('AggCount')).toEqual({ type: 'AggFunc', value: 'count' });
  });

  it('finds a func body, and refuses a signature that is not there', () => {
    expect(funcBody(SCHEMA_GO, 'func (s Severity) Valid() bool {').length).toBeGreaterThan(20);
    expect(() => funcBody(SCHEMA_GO, 'func (s Severity) NotAThing() bool {')).toThrow();
  });

  // 🔴 THE CONTROL FOR THE BYPASS THAT WAS DEMONSTRATED AGAINST AN EARLIER VERSION OF THIS
  // FILE. It filtered case idents by the prefix `Type`, so a kind declared as
  // `KindMaintenance RuleType = "maintenance"` — with a real case in the dispatch, compiling
  // and running — was silently discarded, and the gate reported the vocabularies in
  // agreement. The scan now refuses anything it cannot account for.
  it('refuses a case ident it cannot resolve, whatever it is named', () => {
    const region = '\tswitch r.Type {\n\tcase KindMaintenance:\n\t\tbreak\n\t}';
    expect(() => dispatchValues(region, SCHEMA_CONSTS, 'RuleType')).toThrow(/KindMaintenance/);
    // ...including a conversion expression, which carries no ident to resolve at all.
    const converted = '\tswitch r.Type {\n\tcase RuleType("maintenance"):\n\t\tbreak\n\t}';
    expect(() => dispatchValues(converted, SCHEMA_CONSTS, 'RuleType')).toThrow();
  });

  it('reads a differently-NAMED const of the right type — the counterweight', () => {
    // The rule is the declared TYPE, not the spelling. A kind named `KindX` that really is a
    // RuleType must be picked up, or this gate would demand a naming convention Go does not.
    const consts = new Map([['KindMaintenance', { type: 'RuleType', value: 'maintenance' }]]);
    expect(dispatchValues('\tcase KindMaintenance:\n', consts, 'RuleType')).toEqual(['maintenance']);
  });
});

// ── Vocabulary lockstep, each against the switch that ENFORCES it ───────────

describe('rule types', () => {
  // 🔴 BOTH DIRECTIONS, AND NEITHER IS THE BORING ONE.
  //
  //   missing  the console cannot author a kind the compiler accepts — the feature is
  //            unreachable, which is what shipped.
  //   extra    the console offers a kind the compiler REJECTS — the author meets the failure
  //            at publish, after doing the work, instead of at the picker.
  //
  // No plausibility test guards this one, and that is deliberate rather than an omission: the
  // console side is a literal array, so an extraction that came back empty fails here with
  // eight `extra` entries. The ∅-equals-∅ vacuity this file warns about needs BOTH sides
  // computed, which is true of the corpus and canvas sections below and not of this.
  it('agree with the console, in both directions', () => {
    const backend = ruleTypesTheCompilerAccepts();
    expect({
      missing: backend.filter((k) => !RULE_TYPES.includes(k as never)),
      extra: RULE_TYPES.filter((k) => !backend.includes(k)),
    }).toEqual({ missing: [], extra: [] });
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
    const backend = ruleTypesTheCompilerAccepts();
    expect(Object.keys(CORPUS).sort()).toEqual([...backend].sort());
  });

  it.each(Object.entries(CORPUS))('opens and re-saves a %s rule without losing anything', (kind, definition) => {
    const parsed = parseDefinition(definition);
    expect(parsed, `${kind}: the form could not parse its own corpus entry`).not.toBeNull();
    expect(parsed!.type, `${kind}: the form relabelled the rule`).toBe(kind);
    expect(ruleSurvivesRoundTrip(definition, rebuildFrom(parsed!))).toBe(true);
  });

  // 🔴 THE KIND CORPUS ABOVE IS ONE AXIS, AND IT IS THE THIN ONE. Each entry is a minimal
  // rule of its type, so between them they exercise almost none of the FIELD surface — and a
  // round-trip check is only as sharp as the shapes it is fed. Deleting the whole
  // httpCall/publish verbatim pass-through from parseDefinition, which silently rewrites every
  // canvas-authored webhook into an empty raiseAlarm on save, left the kind corpus entirely
  // green. So the second axis: shapes that are all the same KIND and differ in what they
  // carry, aimed squarely at the fields a rebuild is most likely to drop.
  //
  // These are not hypothetical. Every one is a shape the canvas or the API produces, and the
  // pass-throughs they pin (`raw` for outbound actions, `guard` for canvas branches) exist
  // precisely because losing them corrupts a rule the form was only supposed to read.
  const SHAPES: Record<string, string> = {
    'an httpCall action carried through verbatim': JSON.stringify({
      name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [{ type: 'httpCall', httpCall: { url: 'https://example.invalid/hook', secretRef: 'h', timeoutMs: 2000 } }],
    }),
    'a publish action carried through verbatim': JSON.stringify({
      name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [{ type: 'publish', publish: { connector: 'c', payload: '"x"' } }],
    }),
    'a canvas-authored guard on an action': JSON.stringify({
      name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [{ type: 'raiseAlarm', guard: 'value > 40', raiseAlarm: { alarmKey: 'hot' } }],
    }),
    'a sendCommand action with a payload': JSON.stringify({
      name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [{ type: 'sendCommand', sendCommand: { command: 'reset', payload: '{"hard":true}' } }],
    }),
    'a raw-CEL leaf': JSON.stringify({ name: 'r', type: 'threshold', when: { cel: 'm["tempC"] > 30.0' } }),
    'a leaf bounded by a device attribute': JSON.stringify({
      name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', thresholdAttr: 'maxTemp' },
    }),
    'a description and a severity': JSON.stringify({
      name: 'r', description: 'why this exists', severity: 'minor', type: 'threshold',
      when: { metric: 'tempC', op: 'gt', threshold: 30 },
    }),
    'a session-windowed aggregate': JSON.stringify({
      name: 'r', type: 'aggregate', agg: 'max', op: 'gt', threshold: 5, windowMode: 'session', gap: '2m', metric: 'tempC',
    }),
    'a count-windowed aggregate': JSON.stringify({
      name: 'r', type: 'aggregate', agg: 'count', op: 'ge', threshold: 4, windowMode: 'count', count: 10,
    }),
    'a correlation with a member cap': JSON.stringify({
      name: 'r', type: 'correlation', anchorType: 'area', count: 3, window: '5m', memberCap: 50,
    }),
    'several actions at once': JSON.stringify({
      name: 'r', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [
        { type: 'raiseAlarm', raiseAlarm: { alarmKey: 'hot' } },
        { type: 'sendCommand', sendCommand: { command: 'cool', payload: '' } },
        { type: 'httpCall', httpCall: { url: 'https://example.invalid/h' } },
      ],
    }),
  };

  it.each(Object.entries(SHAPES))('opens and re-saves %s without losing anything', (_shape, definition) => {
    const parsed = parseDefinition(definition);
    expect(parsed).not.toBeNull();
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

  // 🔴 THE FAILURE THIS CATCHES IS THE ONE AN OPERATOR SEES. The picker's keys are computed
  // from the kind name, so a kind can join the taxonomy, appear in the picker, and render
  // `ruleTypeConnectivityLabel` as its own label — i18next falls back to the key, nothing
  // errors, and the row looks like a bug in the feature. Every other check in this file is
  // about which kinds EXIST; this is the one about whether they can be read.
  it.each(['en', 'es'])('resolves every picker key in %s', (locale) => {
    const catalog = (locale === 'en' ? EN_RULES : ES_RULES) as Record<string, string>;
    const missing = RULE_TYPES.flatMap((v) => {
      const stem = `ruleType${v[0].toUpperCase()}${v.slice(1)}`;
      return [`${stem}Label`, `${stem}Description`].filter((k) => !catalog[k]);
    });
    expect({ locale, missing }).toEqual({ locale, missing: [] });
  });

  it('offers a distinct label per kind', () => {
    // Two kinds resolving to one label would render two identical picker rows — a way to
    // lose a kind while every count stays right.
    const opts = ruleTypeOptions(echo);
    expect(new Set(opts.map((o) => o.label)).size).toBe(opts.length);
  });

  it('hands the picker the whole option set, unfiltered', () => {
    expect(FORM_SRC).toContain('options={ruleTypeOptions(t)}');
    // The tripwire proper. Anything between the call and the closing brace — `.filter(`,
    // `.slice(`, a conditional — is a narrowing this file cannot otherwise see.
    expect(FORM_SRC).not.toMatch(/ruleTypeOptions\(t\)\s*[.[]/);
  });
});


// ── The SECOND authoring surface ────────────────────────────────────────────
//
// 🔴 THE FORM IS NOT THE WHOLE CONSOLE. The canvas carries its own condition vocabulary, and
// leaving it ungated would have reproduced this file's own subject one directory down: a kind
// added to the compiler and to the form passes every check above while the canvas silently
// cannot express it. The canvas fails CLOSED on a kind it does not know (roundtrip.ts returns
// `canvasErrorUnsupportedType`), so the cost is a refusal rather than a rewrite — but its
// documented fallback is "open it in the form", and the form is where the rewrite lived.
//
// The canvas is allowed to offer FEWER kinds than the compiler accepts. What it is not allowed
// to do is drift without anyone deciding: each omission is named here with its reason, and the
// omission is ASSERTED to still be real. A gap list that is not checked against reality is how
// a fixed gap turns into a free slot for the next one.

describe('the canvas condition vocabulary', () => {
  // Kinds the compiler accepts that the canvas deliberately does not offer as a node.
  const DECLARED_GAPS: Record<string, string> = {
    connectivity:
      'no canvas node yet — it is a leaf-less edge trigger with no ports to wire, so it needs a ' +
      'node shape of its own rather than a copy of an existing condition. Authorable in the form.',
  };

  it('offers no kind the compiler would refuse', () => {
    const backend = ruleTypesTheCompilerAccepts();
    const extra = CONDITION_TYPES.filter((c) => !backend.includes(c));
    expect({ extra }).toEqual({ extra: [] });
  });

  // 🔴 SET EQUALITY, WHICH KEEPS THE GAP LIST HONEST IN BOTH DIRECTIONS AT ONCE. A new gap
  // fails because `missing` grows; a gap that gets CLOSED fails because `missing` shrinks
  // while its entry stays — so a stale entry cannot survive as a free slot for the next one.
  // (A separate "no stale entry" test used to sit here and could not fail while this passed.)
  it('omits only what is declared, with a reason', () => {
    const backend = ruleTypesTheCompilerAccepts();
    const missing = backend.filter((k) => !CONDITION_TYPES.includes(k as never));
    expect({ missing: missing.sort() }).toEqual({ missing: Object.keys(DECLARED_GAPS).sort() });
  });

  it('derives its condition set from the node catalog', () => {
    // The control for all three checks above: a CONDITION_TYPES that came back empty would
    // satisfy "offers nothing the compiler refuses" perfectly.
    expect(CONDITION_TYPES.length).toBeGreaterThanOrEqual(7);
    expect(CONDITION_TYPES).toContain('threshold');
    expect(CONDITION_TYPES).not.toContain('action'); // not a condition
    expect(CONDITION_TYPES).not.toContain('source');
  });
});


// ── Mirrored ceilings ───────────────────────────────────────────────────────
//
// A number the console repeats from the compiler is the same class of drift as a vocabulary it
// repeats, and it is easier to miss because a stale number still looks like a number. These are
// hints, not gates — the server re-checks and wins — but a hint that says 8 when the compiler
// allows 4 lets an author write work the publish then throws away.

describe('ceilings mirrored from the compiler', () => {
  it('reads the const, and refuses one that is not there', () => {
    expect(intConst(SCHEMA_GO, 'MaxActionsPerRule')).toBeGreaterThan(0);
    expect(() => intConst(SCHEMA_GO, 'MaxNotAThing')).toThrow();
  });

  it('caps the action chain where the compiler does', () => {
    expect(MAX_ACTIONS_PER_RULE).toBe(intConst(SCHEMA_GO, 'MaxActionsPerRule'));
  });
});


// ── What parseDefinition does with input that is not a rule ─────────────────
//
// 🔴 ALSO WRITTEN BECAUSE A MUTATION SURVIVED. Deleting the non-object guard broke nothing:
// the crash it prevents had been FIXED and never PINNED, which leaves the fix one refactor
// from being undone silently.
//
// `null`, `true` and `[…]` are all well-formed JSON, so a definition containing one gets past
// `JSON.parse` and then dies on the first property read — the drawer throwing instead of
// showing the warning built for exactly this case.

describe('parseDefinition on input that is not a rule object', () => {
  it.each([['null'], ['true'], ['42'], ['"a string"'], ['[1,2,3]']])(
    'returns null rather than throwing for %s',
    (raw) => {
      expect(() => parseDefinition(raw)).not.toThrow();
      expect(parseDefinition(raw)).toBeNull();
    },
  );

  it('returns null for malformed JSON', () => {
    expect(parseDefinition('{not json')).toBeNull();
  });

  // The counterweight: a real definition must still parse, or "returns null" would be a
  // trivially correct implementation of this whole function.
  it('still parses an actual rule', () => {
    expect(parseDefinition(CORPUS.threshold)).not.toBeNull();
  });
});


// ── The per-type authoring SHAPE, not just the kind list ────────────────────
//
// 🔴 THE GAP EVERY OTHER CHECK IN THIS FILE LEAVES OPEN. They all ask WHICH KINDS exist. None
// asks what a kind may CARRY — and the compiler has a precise answer per kind, in the
// `forbid(r, "<kind>", forbidden{…})` table. The console mirrors one slice of that table by
// hand (`conditionForbidden`), and a hand-mirrored table is the same drift the taxonomy list
// was, one level down: a kind whose leaf the compiler forbids but the form still offers gets a
// condition editor an author fills in, saves, and has refused at publish.
//
// A mutation removing `connectivity` from `conditionForbidden` survived every other check in
// this file, which is what this section exists to fix.

describe('the per-type authoring shape', () => {
  /** Kinds whose `forbid` entry sets `leaf: true` — the compiler refuses a condition on them. */
  function kindsForbiddingALeaf(): string[] {
    const out: string[] = [];
    for (const m of COMPILE_GO.matchAll(/forbid\(r, "([a-zA-Z]+)", forbidden\{([^}]*)\}/g)) {
      if (/\bleaf:\s*true\b/.test(m[2])) out.push(m[1]);
    }
    return out;
  }

  it('reads a plausible forbid table', () => {
    // The control: a regex that matched nothing would make the comparison below ∅ = ∅.
    const all = [...COMPILE_GO.matchAll(/forbid\(r, "([a-zA-Z]+)", forbidden\{/g)].map((m) => m[1]);
    expect(all.length).toBeGreaterThanOrEqual(7);
    expect(all).toContain('threshold');
    // ...and that not every kind forbids a leaf, or the comparison would be trivially wide.
    expect(kindsForbiddingALeaf().length).toBeLessThan(all.length);
  });

  it('offers a condition editor for exactly the kinds the compiler allows one on', () => {
    const compilerForbids = kindsForbiddingALeaf().sort();
    const consoleForbids = RULE_TYPES.filter((t) => conditionForbidden(t)).sort();
    expect({ consoleForbids }).toEqual({ consoleForbids: compilerForbids });
  });
});

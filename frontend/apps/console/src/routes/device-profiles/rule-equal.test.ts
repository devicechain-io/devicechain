// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { ruleSurvivesRoundTrip, sameLogicalRule, stableStringify } from './rule-equal';

describe('stableStringify', () => {
  it('is key-order independent', () => {
    expect(stableStringify({ b: 1, a: 2 })).toBe(stableStringify({ a: 2, b: 1 }));
    expect(stableStringify({ a: { y: 1, x: 2 } })).toBe(stableStringify({ a: { x: 2, y: 1 } }));
  });
  it('preserves array order', () => {
    expect(stableStringify([1, 2])).not.toBe(stableStringify([2, 1]));
  });
});

describe('sameLogicalRule', () => {
  it('treats two key-orderings of the same rule as equal', () => {
    // The canvas emits Go-canonical order (name, type, …, when); the form emits its own order.
    const canvas = '{"name":"hot","type":"threshold","when":{"metric":"tempC","op":"gt","threshold":30}}';
    const form = '{"type":"threshold","name":"hot","when":{"op":"gt","threshold":30,"metric":"tempC"}}';
    expect(sameLogicalRule(canvas, form)).toBe(true);
  });
  it('detects a real definition change (a rename lives in the definition)', () => {
    const a = '{"name":"hot","type":"threshold"}';
    const b = '{"name":"warm","type":"threshold"}';
    expect(sameLogicalRule(a, b)).toBe(false);
  });
  it('is false when either side is unparseable', () => {
    expect(sameLogicalRule('{bad', '{}')).toBe(false);
    expect(sameLogicalRule('{}', '{bad')).toBe(false);
  });
  it('treats an empty when-object as equal to an absent when (canvas vs form for absence)', () => {
    // The canvas stores the canonical marshal (always `"when":{}`); the form omits it.
    const canvas = '{"name":"silent","type":"absence","severity":"major","when":{},"timeout":"5m0s"}';
    const form = '{"name":"silent","type":"absence","severity":"major","timeout":"5m0s"}';
    expect(sameLogicalRule(canvas, form)).toBe(true);
  });
});


// ── ruleSurvivesRoundTrip ───────────────────────────────────────────────────
//
// The open-time check: does what the form WOULD save still carry what was stored? It is
// one-directional where sameLogicalRule is symmetric, and both halves of that are load-bearing.

describe('ruleSurvivesRoundTrip', () => {
  const stored = JSON.stringify({ name: 'r', type: 'threshold', when: { metric: 'm', op: 'gt', threshold: 1 } });

  it('accepts a rebuild that says everything the stored rule said', () => {
    expect(ruleSurvivesRoundTrip(stored, stored)).toBe(true);
  });

  it('reports a rebuild that dropped a field', () => {
    expect(ruleSurvivesRoundTrip(stored, JSON.stringify({ name: 'r', type: 'threshold' }))).toBe(false);
  });

  it('reports a rebuild that CHANGED a value, not only one that dropped it', () => {
    const relabelled = JSON.stringify({ name: 'r', type: 'absence', when: { metric: 'm', op: 'gt', threshold: 1 } });
    expect(ruleSurvivesRoundTrip(stored, relabelled)).toBe(false);
  });

  // 🔴 THE DIRECTION. The form always emits `name`, so a stored rule that omitted it gets one
  // back — and that is not data loss. A symmetric comparison would warn here, on a rule that
  // is perfectly healthy, and a warning that fires on healthy rules is one nobody reads.
  it('does not report a rebuild that added a field the stored rule omitted', () => {
    const withoutName = JSON.stringify({ type: 'threshold', when: { metric: 'm', op: 'gt', threshold: 1 } });
    const withName = JSON.stringify({ name: '', type: 'threshold', when: { metric: 'm', op: 'gt', threshold: 1 } });
    expect(ruleSurvivesRoundTrip(withoutName, withName)).toBe(true);
  });

  // 🔴 THE `omitempty` EQUIVALENCE CLASS. A client whose serializer writes empty collections
  // and strings produces a definition Go decodes to exactly the same Rule as one that omits
  // them — so the form must not claim it is about to lose a description that was never there.
  it.each([
    ['an empty description', { description: '' }],
    ['a null description', { description: null }],
    ['a false rate', { rate: false }],
    ['an empty action list', { actions: [] }],
    ['a null action list', { actions: null }],
    ['an empty severity', { severity: '' }],
  ])('does not report %s as lost', (_name, extra) => {
    const verbose = JSON.stringify({ name: 'r', type: 'threshold', when: { metric: 'm', op: 'gt', threshold: 1 }, ...extra });
    expect(ruleSurvivesRoundTrip(verbose, stored)).toBe(true);
  });

  // 🔴 THE COUNTERWEIGHT, AND THE REASON ZERO IS NOT IN THE LIST ABOVE. `Threshold` is a
  // *float64 on the Go side, so omitempty elides only nil — a zero threshold is a real value
  // that survives the server's own round-trip. If zero were treated as elidable, a rebuild
  // that DROPPED it would compare equal and the loss would go unreported.
  it('reports a dropped zero, because zero is a value', () => {
    const withZero = JSON.stringify({ name: 'r', type: 'deltaRate', metric: 'm', op: 'gt', threshold: 0 });
    const without = JSON.stringify({ name: 'r', type: 'deltaRate', metric: 'm', op: 'gt' });
    expect(ruleSurvivesRoundTrip(withZero, without)).toBe(false);
  });

  it('treats an unreadable definition as lossy on either side', () => {
    expect(ruleSurvivesRoundTrip('{not json', stored)).toBe(false);
    expect(ruleSurvivesRoundTrip(stored, '{not json')).toBe(false);
  });
});

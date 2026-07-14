// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { buildSelector, MAX_SELECTOR_LEAVES, type FacetCondition } from './selector';

function cond(c: Partial<FacetCondition>): FacetCondition {
  return { key: 'climate', valueType: 'STRING', operator: 'eq', values: [], ...c };
}

describe('buildSelector', () => {
  it('returns a null selector when nothing usable is picked', () => {
    expect(buildSelector([]).selector).toBeNull();
    expect(buildSelector([cond({ values: [] })]).selector).toBeNull();
    // Whitespace-only values contribute nothing.
    expect(buildSelector([cond({ values: ['  '] })]).selector).toBeNull();
  });

  it('composes a single string equality', () => {
    expect(buildSelector([cond({ values: ['arid'] })]).selector).toBe('attr["climate"] == "arid"');
  });

  it('fans a multi-value equality out to an OR', () => {
    const built = buildSelector([cond({ values: ['arid', 'temperate'] })]);
    expect(built.selector).toBe('(attr["climate"] == "arid" || attr["climate"] == "temperate")');
  });

  it('ANDs multiple facet conditions', () => {
    const built = buildSelector([
      cond({ key: 'climate', values: ['arid'] }),
      cond({ key: 'country', values: ['US'] }),
    ]);
    expect(built.selector).toBe('attr["climate"] == "arid" && attr["country"] == "US"');
  });

  it('renders present, neq, numeric, and boolean forms', () => {
    expect(buildSelector([cond({ operator: 'present' })]).selector).toBe('"climate" in attr');
    expect(buildSelector([cond({ operator: 'neq', values: ['arid'] })]).selector).toBe(
      'attr["climate"] != "arid"',
    );
    expect(
      buildSelector([cond({ key: 'population', valueType: 'LONG', operator: 'gte', values: ['1000'] })])
        .selector,
    ).toBe('attr["population"] >= 1000');
    expect(
      buildSelector([cond({ key: 'active', valueType: 'BOOLEAN', operator: 'eq', values: ['true'] })])
        .selector,
    ).toBe('attr["active"] == true');
  });

  it('escapes a value that would break out of the CEL string literal', () => {
    const built = buildSelector([cond({ values: ['a"b\\c'] })]);
    expect(built.selector).toBe('attr["climate"] == "a\\"b\\\\c"');
  });

  it('drops and explains an invalid numeric value', () => {
    const built = buildSelector([
      cond({ key: 'population', valueType: 'LONG', operator: 'gt', values: ['not-a-number'] }),
    ]);
    expect(built.selector).toBeNull();
    expect(built.issues).toHaveLength(1);
    expect(built.issues[0]).toContain('population');
  });

  it('rejects a numeric operator on a non-numeric facet with an issue', () => {
    const built = buildSelector([cond({ operator: 'gt', values: ['5'] })]); // STRING facet
    expect(built.selector).toBeNull();
    expect(built.issues[0]).toContain('LONG or DOUBLE');
  });

  it('rejects a multi-value "is not" as ambiguous', () => {
    const built = buildSelector([cond({ operator: 'neq', values: ['arid', 'temperate'] })]);
    expect(built.selector).toBeNull();
    expect(built.issues[0]).toContain('single value');
  });

  it('rejects numeric literals CEL cannot parse (unary +, trailing dot)', () => {
    // CEL has no unary plus and NUM_FLOAT requires digits after the dot; the value is
    // interpolated as a raw CEL literal, so these must be dropped, not emitted.
    for (const bad of ['+5', '5.', '5.e3']) {
      const built = buildSelector([
        cond({ key: 'population', valueType: 'DOUBLE', operator: 'gt', values: [bad] }),
      ]);
      expect(built.selector, `expected ${bad} rejected`).toBeNull();
      expect(built.issues).toHaveLength(1);
    }
    // Valid decimal forms still compose.
    expect(
      buildSelector([cond({ key: 'p', valueType: 'DOUBLE', operator: 'gt', values: ['.5'] })]).selector,
    ).toBe('attr["p"] > .5');
    expect(
      buildSelector([cond({ key: 'p', valueType: 'DOUBLE', operator: 'gt', values: ['-2.5'] })])
        .selector,
    ).toBe('attr["p"] > -2.5');
  });

  it('requires an integer literal for a LONG facet', () => {
    expect(
      buildSelector([cond({ key: 'n', valueType: 'LONG', operator: 'eq', values: ['1.5'] })]).selector,
    ).toBeNull();
    expect(
      buildSelector([cond({ key: 'n', valueType: 'LONG', operator: 'eq', values: ['42'] })]).selector,
    ).toBe('attr["n"] == 42');
  });

  it('fails closed with an issue when the leaf cap is exceeded', () => {
    const many = Array.from({ length: MAX_SELECTOR_LEAVES + 1 }, (_, i) => `v${i}`);
    const built = buildSelector([cond({ operator: 'eq', values: many })]);
    expect(built.selector).toBeNull();
    expect(built.issues.some((i) => i.includes('Too many'))).toBe(true);
    // Exactly at the cap still composes.
    const atCap = Array.from({ length: MAX_SELECTOR_LEAVES }, (_, i) => `v${i}`);
    expect(buildSelector([cond({ operator: 'eq', values: atCap })]).selector).not.toBeNull();
  });

  it('keeps usable conditions and drops the broken one', () => {
    const built = buildSelector([
      cond({ key: 'climate', values: ['arid'] }),
      cond({ key: 'population', valueType: 'LONG', operator: 'gt', values: ['x'] }),
    ]);
    expect(built.selector).toBe('attr["climate"] == "arid"');
    expect(built.issues).toHaveLength(1);
  });
});

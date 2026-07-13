// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { sameLogicalRule, stableStringify } from './rule-equal';

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
});

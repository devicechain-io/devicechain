// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { applyOptionPatch, type WidgetOptionSpecs } from './option-schema';

const specs: WidgetOptionSpecs = {
  title: { kind: 'string', doc: 'Heading.' },
  bands: { kind: 'number', integer: true, min: 1, max: 10, doc: 'Bands.' },
  rim: { kind: 'boolean', doc: 'Rim.' },
  trend: { kind: 'enum', values: ['rising', 'falling'], doc: 'Trend.' },
};

describe('applyOptionPatch', () => {
  it('writes declared keys and carries everything else through untouched', () => {
    const before = { title: 'A', legacy: 'kept' };
    const next = applyOptionPatch(specs, before, { bands: 4, trend: 'rising' });
    expect(next).toEqual({ title: 'A', legacy: 'kept', bands: 4, trend: 'rising' });
    expect(before).toEqual({ title: 'A', legacy: 'kept' }); // the input is not mutated
  });

  it('refuses a key the schema does not declare', () => {
    expect(() => applyOptionPatch(specs, {}, { nope: 1 }, 'acme/tank')).toThrow(
      'acme/tank: "nope" is not a declared option',
    );
    // an inherited name is not a declared option either
    expect(() => applyOptionPatch(specs, {}, { constructor: 1 })).toThrow('not a declared option');
  });

  it('refuses a value of the wrong kind or outside an enum, and a non-finite number', () => {
    expect(() => applyOptionPatch(specs, {}, { title: 3 })).toThrow('takes a string');
    expect(() => applyOptionPatch(specs, {}, { rim: 'yes' })).toThrow('takes a boolean');
    expect(() => applyOptionPatch(specs, {}, { bands: '4' })).toThrow('takes a finite number');
    expect(() => applyOptionPatch(specs, {}, { bands: Number.NaN })).toThrow('takes a finite number');
    expect(() => applyOptionPatch(specs, {}, { trend: 'sideways' })).toThrow('one of "rising", "falling"');
  });

  it('refuses a json-format string that does not parse, and accepts one that does', () => {
    const json: WidgetOptionSpecs = { schema: { kind: 'string', format: 'json', doc: 'Params.' } };
    expect(() => applyOptionPatch(json, {}, { schema: '[{"name":' })).toThrow('takes a JSON string');
    expect(applyOptionPatch(json, {}, { schema: '[]' })).toEqual({ schema: '[]' });
  });

  it('folds a number into its bounds', () => {
    expect(applyOptionPatch(specs, {}, { bands: 99 }).bands).toBe(10);
    expect(applyOptionPatch(specs, {}, { bands: 0 }).bands).toBe(1);
    expect(applyOptionPatch(specs, {}, { bands: 2.9 }).bands).toBe(2);
  });

  it('drops a key given undefined, an empty string, or false on a flag', () => {
    const before = { title: 'A', bands: 3, rim: true, trend: 'rising' };
    expect(applyOptionPatch(specs, before, { title: '', bands: undefined, rim: false, trend: '' })).toEqual({});
  });
});

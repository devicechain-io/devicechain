// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The `list` option kind: validation of a stored list, the validated write an editor is given,
// and the descriptor check that a list declares its item fields soundly.

import { describe, expect, it } from 'vitest';

import { applyOptionPatch, validateOptions, type WidgetOptionSpecs } from './option-schema';
import { validateWidgetDescriptor } from './widget-catalog';

const specs = {
  bands: {
    kind: 'list',
    maxItems: 2,
    doc: 'bands',
    item: {
      at: { kind: 'number', required: true, doc: 'start' },
      level: { kind: 'enum', required: true, values: ['low', 'high'], doc: 'level' },
      note: { kind: 'string', doc: 'note' },
    },
  },
} as const satisfies WidgetOptionSpecs;

const check = (bag: Record<string, unknown>) =>
  validateOptions(specs, bag, { label: 'w' }).map((i) => `${i.code}:${i.key}`);

describe('validateOptions on a list', () => {
  it('accepts a well-formed list, and an absent one', () => {
    expect(check({ bands: [{ at: 1, level: 'low', note: 'x' }, { at: 2, level: 'high' }] })).toEqual([]);
    expect(check({})).toEqual([]);
  });

  it('points at the offending cell', () => {
    expect(check({ bands: [{ at: 1, level: 'low' }, { at: 'x', level: 'mid' }] })).toEqual([
      'type:bands[1].at',
      'enum:bands[1].level',
    ]);
  });

  it('reports a missing required field, an undeclared field, a non-list, a non-object entry and an overlong list', () => {
    expect(check({ bands: [{ level: 'low' }] })).toEqual(['missing:bands[0].at']);
    expect(check({ bands: [{ at: 1, level: 'low', color: '#f00' }] })).toEqual(['unknown:bands[0].color']);
    expect(check({ bands: {} })).toEqual(['type:bands']);
    expect(check({ bands: [7] })).toEqual(['type:bands[0]']);
    expect(check({ bands: [{ at: 1, level: 'low' }, { at: 2, level: 'low' }, { at: 3, level: 'low' }] })).toEqual([
      'range:bands',
    ]);
  });
});

describe('applyOptionPatch on a list', () => {
  it('stores a valid list and refuses an invalid one, naming the cell', () => {
    expect(applyOptionPatch(specs, {}, { bands: [{ at: 1, level: 'low' }] })).toEqual({ bands: [{ at: 1, level: 'low' }] });
    expect(() => applyOptionPatch(specs, {}, { bands: [{ at: 1, level: 'nope' }] })).toThrow(/bands\[0\]\.level/);
    expect(() => applyOptionPatch(specs, {}, { bands: [{ at: 1, level: 'low', color: '#f00' }] })).toThrow(/color/);
    expect(() => applyOptionPatch(specs, {}, { bands: 'x' })).toThrow();
  });

  it('drops the key for an empty list', () => {
    expect(applyOptionPatch(specs, { bands: [{ at: 1, level: 'low' }], other: 1 }, { bands: [] })).toEqual({ other: 1 });
  });
});

describe('a descriptor that declares a list', () => {
  const base = {
    id: 'acme/x',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'X' },
    channel: 'none',
    datasource: { mode: 'none' },
    binding: {},
    defaults: {},
    interactions: {},
  };
  const issues = (options: unknown) =>
    validateWidgetDescriptor({ ...base, options } as never).map((i) => `${i.path}: ${i.message}`);

  it('is accepted when its item fields are sound', () => {
    expect(issues(specs)).toEqual([]);
  });

  it('rejects an empty item, a nested list and a bad item field', () => {
    expect(issues({ l: { kind: 'list', doc: 'd', item: {} } }).join('\n')).toMatch(/item/);
    expect(
      issues({ l: { kind: 'list', doc: 'd', item: { inner: { kind: 'list', doc: 'd', item: { a: { kind: 'string', doc: 'd' } } } } } }).join('\n'),
    ).toMatch(/cannot itself be a list/);
    expect(issues({ l: { kind: 'list', doc: 'd', item: { a: { kind: 'enum', doc: 'd', values: [] } } } }).join('\n')).toMatch(
      /options\.l\.item\.a\.values/,
    );
    expect(issues({ l: { kind: 'list', doc: 'd', maxItems: 0, item: { a: { kind: 'string', doc: 'd' } } } }).join('\n')).toMatch(
      /maxItems/,
    );
    expect(issues({ l: { kind: 'list', doc: 'd', ordered: 'yes', item: { a: { kind: 'string', doc: 'd' } } } }).join('\n')).toMatch(
      /options\.l\.ordered/,
    );
  });

  it('accepts ordered as true or false', () => {
    for (const ordered of [true, false]) {
      expect(issues({ l: { kind: 'list', doc: 'd', ordered, item: { a: { kind: 'string', doc: 'd' } } } })).toEqual([]);
    }
  });
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { buildPayload, defaultValues, isScalar, parseParameterSchema, validateParams } from './command-params';

describe('parseParameterSchema', () => {
  it('parses a JSON array of descriptors', () => {
    const params = parseParameterSchema('[{"name":"level","dataType":"INT","required":true}]');
    expect(params).toHaveLength(1);
    expect(params[0].name).toBe('level');
    expect(params[0].dataType).toBe('INT');
  });

  it('returns [] for null / empty / malformed / non-array input', () => {
    expect(parseParameterSchema(null)).toEqual([]);
    expect(parseParameterSchema(undefined)).toEqual([]);
    expect(parseParameterSchema('')).toEqual([]);
    expect(parseParameterSchema('not json')).toEqual([]);
    expect(parseParameterSchema('{"name":"x"}')).toEqual([]); // object root, not an array
  });

  it('drops malformed entries (missing a string name) rather than crashing', () => {
    const params = parseParameterSchema('[{"name":"ok"},{"nope":1},42,null]');
    expect(params).toHaveLength(1);
    expect(params[0].name).toBe('ok');
  });
});

describe('isScalar', () => {
  it('treats absent kind as SCALAR and only OBJECT as non-scalar', () => {
    expect(isScalar({ name: 'a' })).toBe(true);
    expect(isScalar({ name: 'a', kind: 'SCALAR' })).toBe(true);
    expect(isScalar({ name: 'a', kind: 'OBJECT' })).toBe(false);
  });
});

describe('defaultValues', () => {
  it('seeds from scalar defaults, skipping params without one', () => {
    const values = defaultValues([
      { name: 'a', default: '5' },
      { name: 'b' },
      { name: 'c', kind: 'OBJECT', default: 'x' }, // object → skipped
    ]);
    expect(values).toEqual({ a: '5' });
  });
});

describe('validateParams', () => {
  it('flags a missing required value', () => {
    const errors = validateParams([{ name: 'a', required: true }], {});
    expect(errors.a).toBe('Required');
  });

  it('allows an empty optional value', () => {
    expect(validateParams([{ name: 'a' }], {})).toEqual({});
  });

  it('rejects a non-numeric INT/DOUBLE and enforces integer-ness', () => {
    expect(validateParams([{ name: 'n', dataType: 'DOUBLE' }], { n: 'abc' }).n).toBe('Must be a number');
    expect(validateParams([{ name: 'n', dataType: 'INT' }], { n: '3.5' }).n).toBe('Must be a whole number');
    expect(validateParams([{ name: 'n', dataType: 'INT' }], { n: '3' })).toEqual({});
  });

  it('enforces min/max bounds', () => {
    expect(validateParams([{ name: 'n', dataType: 'INT', minValue: 1 }], { n: '0' }).n).toContain('≥ 1');
    expect(validateParams([{ name: 'n', dataType: 'INT', maxValue: 10 }], { n: '11' }).n).toContain('≤ 10');
    expect(validateParams([{ name: 'n', dataType: 'INT', minValue: 1, maxValue: 10 }], { n: '5' })).toEqual({});
  });
});

describe('buildPayload', () => {
  it('coerces each scalar to its declared type', () => {
    const payload = buildPayload(
      [
        { name: 'count', dataType: 'INT' },
        { name: 'ratio', dataType: 'DOUBLE' },
        { name: 'on', dataType: 'BOOLEAN' },
        { name: 'label', dataType: 'STRING' },
      ],
      { count: '5', ratio: '1.5', on: 'true', label: 'hi' },
    );
    expect(JSON.parse(payload!)).toEqual({ count: 5, ratio: 1.5, on: true, label: 'hi' });
  });

  it('omits empty optional values and returns undefined when nothing contributes', () => {
    expect(buildPayload([{ name: 'a' }, { name: 'b' }], {})).toBeUndefined();
    const payload = buildPayload([{ name: 'a' }, { name: 'b' }], { a: 'x' });
    expect(JSON.parse(payload!)).toEqual({ a: 'x' });
  });

  it('skips OBJECT parameters (not form-editable yet)', () => {
    const payload = buildPayload(
      [
        { name: 'a', dataType: 'STRING' },
        { name: 'nested', kind: 'OBJECT', parameters: [{ name: 'inner' }] },
      ],
      { a: 'x', nested: 'ignored' },
    );
    expect(JSON.parse(payload!)).toEqual({ a: 'x' });
  });

  it('treats BOOLEAN as false unless the value is exactly "true"', () => {
    const payload = buildPayload([{ name: 'on', dataType: 'BOOLEAN' }], { on: 'false' });
    expect(JSON.parse(payload!)).toEqual({ on: false });
  });
});

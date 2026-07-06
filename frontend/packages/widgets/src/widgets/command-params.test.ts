// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { buildPayload, defaultValues, isScalar, parseBool, parseParameterSchema, validateParams } from './command-params';

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

  it('sanitizes wrong-typed fields so a hand-edited schema can never crash the form', () => {
    // enum as a string (has .length but not an array), default as a number, bounds NaN,
    // unknown dataType — all coerced/dropped rather than trusted.
    const [p] = parseParameterSchema(
      '[{"name":"x","enum":"on","default":5,"minValue":"lo","dataType":"WEIRD","kind":"NOPE"}]',
    );
    expect(p.enum).toBeUndefined();
    expect(p.default).toBeUndefined();
    expect(p.minValue).toBeUndefined();
    expect(p.dataType).toBeUndefined();
    expect(p.kind).toBeUndefined(); // unknown kind → treated as SCALAR (isScalar true)
    expect(isScalar(p)).toBe(true);
  });

  it('keeps only string members of a valid enum array', () => {
    const [p] = parseParameterSchema('[{"name":"mode","enum":["a",1,"b",null]}]');
    expect(p.enum).toEqual(['a', 'b']);
  });
});

describe('parseBool', () => {
  it('accepts the strconv.ParseBool truthy spellings', () => {
    for (const t of ['true', 'True', 'TRUE', '1', 't', 'yes', 'on']) expect(parseBool(t)).toBe(true);
    for (const f of ['false', 'False', '0', 'f', 'no', 'off', '']) expect(parseBool(f)).toBe(false);
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

  it('always seeds a BOOLEAN to a concrete true/false, normalizing the default spelling', () => {
    expect(defaultValues([{ name: 'on', dataType: 'BOOLEAN', default: 'True' }])).toEqual({ on: 'true' });
    expect(defaultValues([{ name: 'on', dataType: 'BOOLEAN' }])).toEqual({ on: 'false' }); // no default → false
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

  it('never flags a BOOLEAN as required-missing (a checkbox always has a value)', () => {
    expect(validateParams([{ name: 'on', dataType: 'BOOLEAN', required: true }], {})).toEqual({});
  });

  it('blocks a required OBJECT parameter (unsatisfiable in the typed form)', () => {
    const errors = validateParams([{ name: 'config', kind: 'OBJECT', required: true }], {});
    expect(errors.config).toContain('not supported');
    // a non-required OBJECT is allowed (silently omitted)
    expect(validateParams([{ name: 'config', kind: 'OBJECT' }], {})).toEqual({});
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

  it('always includes a BOOLEAN and interprets truthy spellings', () => {
    expect(JSON.parse(buildPayload([{ name: 'on', dataType: 'BOOLEAN' }], { on: 'false' })!)).toEqual({ on: false });
    expect(JSON.parse(buildPayload([{ name: 'on', dataType: 'BOOLEAN' }], { on: 'True' })!)).toEqual({ on: true });
    // even with an empty value map, a boolean sends its (false) state rather than being omitted
    expect(JSON.parse(buildPayload([{ name: 'on', dataType: 'BOOLEAN' }], {})!)).toEqual({ on: false });
  });
});

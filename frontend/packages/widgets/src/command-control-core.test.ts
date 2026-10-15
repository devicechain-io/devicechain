// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { CommandRow } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { bindControl, clampToRange, deliveryGroup, lastSent, payloadFor, type Binding, type Range } from './command-control-core';

const schema = (...params: object[]) => JSON.stringify(params);
const bound = (b: Binding) => {
  if (!b.ok) throw new Error(`refused: ${JSON.stringify(b.refusal)}`);
  return b;
};

describe('clampToRange', () => {
  const r = (min: number, max: number, step: number, integer = false): Range => ({ min, max, step, integer });

  it('holds a value inside the range', () => {
    expect(clampToRange(r(15, 35, 0.5), 99)).toBe(35);
    expect(clampToRange(r(15, 35, 0.5), -4)).toBe(15);
  });

  it('snaps to a whole step from the minimum, without float noise', () => {
    expect(clampToRange(r(0, 1, 0.1), 0.30000000000000004)).toBe(0.3);
    expect(clampToRange(r(0, 1, 0.1), 0.7)).toBe(0.7);
    expect(clampToRange(r(15, 35, 0.5), 22.3)).toBe(22.5);
    // The grid starts at min, not at zero: 16 is on it for min 1 step 5, 15 is not.
    expect(clampToRange(r(1, 21, 5), 15)).toBe(16);
  });

  it('never leaves the range when the last step does not land on max', () => {
    // 0..10 in steps of 4: the grid is 0, 4, 8; 10 rounds to 8, not to a 12 above the range.
    expect(clampToRange(r(0, 10, 4), 10)).toBe(8);
    expect(clampToRange(r(0, 10, 4), 11)).toBe(8);
  });

  it('rounds a whole-number parameter and survives NaN', () => {
    expect(clampToRange(r(0, 10, 1, true), 4.6)).toBe(5);
    expect(clampToRange(r(3, 10, 1, true), Number.NaN)).toBe(3);
  });
});

describe('bindControl', () => {
  const base = { commandName: 'setSetpoint', parameter: 'target' };

  it('takes range, integer-ness and step from the command definition', () => {
    const b = bound(bindControl('number', { ...base, parameterSchema: schema({ name: 'target', dataType: 'INT', minValue: 2, maxValue: 8 }) }));
    expect(b.range).toEqual({ min: 2, max: 8, step: 1, integer: true });
  });

  it('gives a continuous parameter a hundred notches unless the author set a step', () => {
    const p = schema({ name: 'target', dataType: 'DOUBLE', minValue: 0, maxValue: 50 });
    expect(bound(bindControl('number', { ...base, parameterSchema: p })).range?.step).toBe(0.5);
    expect(bound(bindControl('number', { ...base, parameterSchema: p, step: 5 })).range?.step).toBe(5);
    // A step of zero or below is not a step.
    expect(bound(bindControl('number', { ...base, parameterSchema: p, step: 0 })).range?.step).toBe(0.5);
  });

  it('lets the definition win over an authored range', () => {
    const b = bound(
      bindControl('number', {
        ...base,
        parameterSchema: schema({ name: 'target', dataType: 'DOUBLE', minValue: 15, maxValue: 35 }),
        min: 0,
        max: 100,
      }),
    );
    expect(b.range).toMatchObject({ min: 15, max: 35 });
  });

  it('refuses an inverted or empty range', () => {
    const p = schema({ name: 'target', dataType: 'DOUBLE' });
    expect(bindControl('number', { ...base, parameterSchema: p, min: 5, max: 5 })).toEqual({ ok: false, refusal: { code: 'badRange' } });
    expect(bindControl('number', { ...base, parameterSchema: p, min: 9, max: 5 })).toEqual({ ok: false, refusal: { code: 'badRange' } });
  });

  it('refuses an enum-constrained parameter: a slider would offer values between the choices', () => {
    const b = bindControl('number', {
      ...base,
      parameterSchema: schema({ name: 'target', dataType: 'INT', enum: ['1', '2'], minValue: 1, maxValue: 2 }),
    });
    expect(b).toEqual({ ok: false, refusal: { code: 'fixedChoices', name: 'target' } });
  });

  it('names every other required parameter that has no default', () => {
    const b = bindControl('number', {
      ...base,
      parameterSchema: schema(
        { name: 'target', dataType: 'DOUBLE', minValue: 0, maxValue: 1 },
        { name: 'zone', dataType: 'STRING', required: true },
        { name: 'ramp', dataType: 'INT', required: true, default: '3' },
        { name: 'cfg', kind: 'OBJECT', required: true },
      ),
    });
    expect(b).toEqual({ ok: false, refusal: { code: 'otherRequired', names: ['zone', 'cfg'] } });
  });

  it('refuses a default the definition itself would reject, rather than send it on every command', () => {
    const b = bindControl('number', {
      ...base,
      parameterSchema: schema(
        { name: 'target', dataType: 'DOUBLE', minValue: 0, maxValue: 1 },
        { name: 'ramp', dataType: 'INT', default: '2.5' },
        { name: 'gain', dataType: 'DOUBLE', default: '9', maxValue: 5 },
        { name: 'mode', dataType: 'STRING', enum: ['heat', 'cool'], default: 'auto' },
        { name: 'ok', dataType: 'INT', default: '3' },
      ),
    });
    expect(b).toEqual({ ok: false, refusal: { code: 'badDefault', names: ['ramp', 'gain', 'mode'] } });
    // The counterweight: valid defaults bind and are sent.
    const good = bindControl('number', {
      ...base,
      parameterSchema: schema(
        { name: 'target', dataType: 'DOUBLE', minValue: 0, maxValue: 1 },
        { name: 'mode', dataType: 'STRING', enum: ['heat', 'cool'], default: 'cool' },
      ),
    });
    expect(bound(good).sent.map((p) => p.name)).toEqual(['target', 'mode']);
  });

  it('picks the one parameter of the right type when none is named, and only then', () => {
    const one = schema({ name: 'power', dataType: 'BOOLEAN' }, { name: 'ramp', dataType: 'INT' });
    expect(bound(bindControl('boolean', { commandName: 'c', parameterSchema: one, parameter: undefined })).param.name).toBe('power');
    const two = schema({ name: 'a', dataType: 'BOOLEAN' }, { name: 'b', dataType: 'BOOLEAN' });
    expect(bindControl('boolean', { commandName: 'c', parameterSchema: two, parameter: undefined })).toEqual({
      ok: false,
      refusal: { code: 'setParameter' },
    });
  });

  it('refuses the wrong type instead of coercing it', () => {
    const p = schema({ name: 'target', dataType: 'STRING' });
    expect(bindControl('number', { ...base, parameterSchema: p })).toEqual({ ok: false, refusal: { code: 'wrongType', name: 'target', kind: 'number' } });
    expect(bindControl('boolean', { ...base, parameterSchema: schema({ name: 'target', dataType: 'DOUBLE' }) })).toEqual({
      ok: false,
      refusal: { code: 'wrongType', name: 'target', kind: 'boolean' },
    });
  });

  it('refuses a missing or malformed schema', () => {
    expect(bindControl('boolean', { commandName: 'c', parameterSchema: null, parameter: 'on' })).toEqual({ ok: false, refusal: { code: 'parameterMissing', name: 'on' } });
    expect(bindControl('boolean', { commandName: 'c', parameterSchema: '{not json', parameter: 'on' })).toEqual({ ok: false, refusal: { code: 'parameterMissing', name: 'on' } });
  });
});

describe('payloadFor', () => {
  it('sends the bound value as its real type, the defaults, and nothing else', () => {
    const b = bound(
      bindControl('boolean', {
        commandName: 'c',
        parameter: 'on',
        parameterSchema: schema(
          { name: 'on', dataType: 'BOOLEAN', required: true },
          { name: 'ramp', dataType: 'INT', default: '5' },
          { name: 'quiet', dataType: 'BOOLEAN' },
          { name: 'note', dataType: 'STRING' },
        ),
      }),
    );
    expect(JSON.parse(payloadFor(b, true))).toEqual({ on: true, ramp: 5 });
    expect(JSON.parse(payloadFor(b, false))).toEqual({ on: false, ramp: 5 });
  });

  it('keeps a number a number, to the digit', () => {
    const b = bound(bindControl('number', { commandName: 'c', parameter: 't', parameterSchema: schema({ name: 't', dataType: 'DOUBLE', minValue: 0, maxValue: 9 }) }));
    expect(payloadFor(b, 22.5)).toBe('{"t":22.5}');
  });
});

describe('deliveryGroup', () => {
  it('groups every lifecycle status, and does not call an unknown one pending', () => {
    for (const s of ['QUEUED', 'HELD', 'SENT', 'PARKED']) expect(deliveryGroup(s), s).toBe('pending');
    expect(deliveryGroup('SUCCESSFUL')).toBe('delivered');
    for (const s of ['FAILED', 'TIMEOUT', 'EXPIRED', 'CANCELLED']) expect(deliveryGroup(s), s).toBe('failed');
    expect(deliveryGroup('SOMETHING_NEW')).toBe('unknown');
  });
});

describe('lastSent', () => {
  const row = (over: Partial<CommandRow>): CommandRow => ({
    token: 't',
    name: 'c',
    status: 'SENT',
    payload: '{"p":1}',
    responsePayload: null,
    error: null,
    queuedTime: '2026-01-01T00:00:00Z',
    sentTime: null,
    respondedTime: null,
    ...over,
  });

  it('reports the newest command even when it carries no readable value', () => {
    // The newest was sent from elsewhere without this parameter. Reporting the older 1 would be
    // reporting a value that has since been superseded.
    const rows = [row({ token: 'a', queuedTime: '2026-01-01T00:00:00Z' }), row({ token: 'b', payload: '{}', queuedTime: '2026-01-02T00:00:00Z' })];
    expect(lastSent('number', rows, 'c', 'p', null)).toEqual({ token: 'b', value: undefined, status: 'SENT' });
  });

  it('reads a value only as the type the control needs', () => {
    expect(lastSent('boolean', [row({ payload: '{"p":"true"}' })], 'c', 'p', null)?.value).toBeUndefined();
    expect(lastSent('number', [row({ payload: '{"p":"1"}' })], 'c', 'p', null)?.value).toBeUndefined();
    expect(lastSent('boolean', [row({ payload: '{"p":true}' })], 'c', 'p', null)?.value).toBe(true);
    expect(lastSent('number', [row({ payload: 'not json' })], 'c', 'p', null)?.value).toBeUndefined();
  });

  it('stands an accepted send in for the history until the history has it', () => {
    expect(lastSent('number', [], 'c', 'p', { token: 'x', value: 7 })).toEqual({ token: 'x', value: 7, status: 'QUEUED' });
    expect(lastSent('number', [row({ token: 'x', status: 'SUCCESSFUL', payload: '{"p":7}' })], 'c', 'p', { token: 'x', value: 7 })?.status).toBe('SUCCESSFUL');
  });
});

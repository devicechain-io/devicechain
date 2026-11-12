// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { buildExpectation, parseDevices, type ExpectationRow } from './expectations';

const row: ExpectationRow = { id: '1', window: 'INCIDENT', shape: 'raised', name: 'Alarm', rule: 'R', device: 'D', command: 'C', minutes: '5' };
describe('expectation JSON', () => {
  it.each([
    ['raised', { raised: { rule: 'R', device: 'D', within: '5m' } }],
    ['neverRaised', { neverRaised: { rule: 'R', device: 'D' } }],
    ['intent', { intent: { kind: 'sendCommand', command: 'C', device: 'D', within: '5m' } }],
    ['neverIntent', { neverIntent: { kind: 'sendCommand', command: 'C', device: 'D' } }],
  ] as const)('builds %s exactly', (shape, expected) => {
    const result = buildExpectation({ ...row, shape, window: 'NORMAL' });
    expect(result.window).toBe('NORMAL'); expect(result.name).toBe('Alarm');
    expect(JSON.parse(result.expect)).toEqual(expected);
  });
  it('rejects a nonpositive or invalid duration', () => {
    for (const minutes of ['0', '-1', '', 'abc', 'Infinity']) expect(() => buildExpectation({ ...row, minutes })).toThrow();
  });
  it('accepts fractional minutes and trims tokens', () => {
    expect(JSON.parse(buildExpectation({ ...row, device: ' D ', minutes: '1.5' }).expect)).toEqual({ raised: { rule: 'R', device: 'D', within: '1.5m' } });
  });
  it('separates comma/newline device tokens and removes duplicates', () => {
    expect(parseDevices(' D, E\nD\n\n F ')).toEqual(['D', 'E', 'F']);
  });
});

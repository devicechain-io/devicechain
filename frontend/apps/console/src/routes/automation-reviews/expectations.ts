// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

export type ExpectationShape = 'raised' | 'neverRaised' | 'intent' | 'neverIntent';
export interface ExpectationRow {
  id: string;
  window: 'INCIDENT' | 'NORMAL';
  shape: ExpectationShape;
  name: string;
  rule: string;
  device: string;
  command: string;
  minutes: string;
}

// The ruletest decoder accepts Go duration strings, including fractional minutes.
export function buildExpectation(row: ExpectationRow) {
  const positive = row.shape === 'raised' || row.shape === 'intent';
  const command = row.shape === 'intent' || row.shape === 'neverIntent';
  if (!row.device.trim() || (command && !row.command.trim()) ||
      (positive && (!/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(row.minutes.trim()) || !Number.isFinite(Number(row.minutes)) || Number(row.minutes) <= 0))) {
    throw new Error('invalidExpectation');
  }
  const match = {
    ...(command ? { kind: 'sendCommand', command: row.command.trim() } : { rule: row.rule.trim() }),
    device: row.device.trim(),
    ...(positive ? { within: `${row.minutes.trim()}m` } : {}),
  };
  return { window: row.window, name: row.name.trim() || undefined, expect: JSON.stringify({ [row.shape]: match }) };
}

export function parseDevices(value: string): string[] {
  return [...new Set(value.split(/[,\n]/).map(token => token.trim()).filter(Boolean))];
}

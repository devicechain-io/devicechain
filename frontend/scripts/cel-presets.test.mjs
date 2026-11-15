// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildCelPreset } from '../apps/console/src/routes/device-profiles/canvas/cel-presets.ts';

test('Filter guards missing readings and emits CEL doubles', () => {
  assert.equal(buildCelPreset('filter', 'event', 'temp', '>', '20'), '"temp" in m && m["temp"] > 20.0');
  assert.equal(buildCelPreset('filter', 'detection', '', '<=', '-2.5'), 'hasValue && value <= -2.5');
});

test('Exists uses the correct vocabulary and safely quotes arbitrary metric keys', () => {
  assert.equal(buildCelPreset('exists', 'event', 'a"\\b', '', ''), '"a\\"\\\\b" in m');
  assert.equal(buildCelPreset('exists', 'detection', '', '', ''), 'hasValue');
});

test('Math keeps the scalar source and decimal operand', () => {
  assert.equal(buildCelPreset('math', 'event', 'temp', '*', '1.8'), 'm["temp"] * 1.8');
  assert.equal(buildCelPreset('math', 'detection', '', '+', '32'), 'value + 32.0');
});

test('Incomplete, non-finite, unsupported and zero-divisor inputs cannot be applied', () => {
  for (const number of ['', ' ', 'NaN', 'Infinity', '1e999']) {
    assert.equal(buildCelPreset('filter', 'event', 'temp', '>', number), null);
  }
  assert.equal(buildCelPreset('exists', 'event', ' ', '', ''), null);
  assert.equal(buildCelPreset('filter', 'event', 'temp', '|| true', '2'), null);
  assert.equal(buildCelPreset('math', 'event', 'temp', '**', '2'), null);
  assert.equal(buildCelPreset('math', 'event', 'temp', '/', '-0'), null);
});

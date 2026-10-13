// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parseLock, validateManifest } from './fetch-guide-assets.mjs';

test('parseLock accepts one full sha and rejects anything else', () => {
  const sha = 'a'.repeat(40);
  assert.equal(parseLock(`${sha}\n`), sha);
  for (const bad of ['', 'main', 'abc123', `${sha}\n${sha}`, 'A'.repeat(40)]) {
    assert.throws(() => parseLock(bad), bad);
  }
});

test('validateManifest requires schemaVersion 1 and a shots object', () => {
  validateManifest({ schemaVersion: 1, shots: {} });
  for (const bad of [null, {}, { schemaVersion: 2, shots: {} }, { schemaVersion: 1 }, { schemaVersion: 1, shots: [] }]) {
    assert.throws(() => validateManifest(bad));
  }
});

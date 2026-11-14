// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');

test('the public roadmap does not advertise shipped profile decoders as future work', () => {
  const guide = read('../docs/guides/payload-decoders.md');
  assert.match(guide, /runs on the live uplinks/);
  assert.doesNotMatch(read('../../ROADMAP.md'), /\*\*Payload decoders you write yourself\.\*\*/);
});

test('the decoder guide locates script execution outside DeviceChain', () => {
  const guide = read('../docs/guides/payload-decoders.md');
  assert.match(guide, /DeviceChain does not execute JavaScript, Python or other user-supplied scripts/);
  assert.match(guide, /JavaScript codec runs in ChirpStack/);
  assert.match(guide, /network server's codec or in an external bridge/);
});

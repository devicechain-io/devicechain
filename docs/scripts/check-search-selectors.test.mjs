// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// The postbuild selector check must be able to FAIL: a check that passes on an empty or
// renamed bundle is the failure it exists to catch.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { missingSelectors } from './check-search-selectors.mjs';

function build(css) {
  const dir = mkdtempSync(join(tmpdir(), 'search-selectors-'));
  mkdirSync(join(dir, 'assets', 'css'), { recursive: true });
  writeFileSync(join(dir, 'assets', 'css', 'styles.css'), css);
  return dir;
}

test('passes when every class the wrapper reads is defined', () => {
  const dir = build('.suggestion_fB_2{a:b}.noResults_l6Q3{a:b}.x .cursor_eG29{a:b}');
  try {
    assert.deepEqual(missingSelectors(dir), []);
  } finally {
    rmSync(dir, { recursive: true });
  }
});

test('fails naming the renamed classes', () => {
  const dir = build('.item_fB_2{a:b}.noResults_l6Q3{a:b}.cursor_eG29{a:b}');
  try {
    assert.deepEqual(missingSelectors(dir), ['suggestion_']);
  } finally {
    rmSync(dir, { recursive: true });
  }
});

test('fails loudly when there is no CSS at all', () => {
  const dir = mkdtempSync(join(tmpdir(), 'search-selectors-'));
  try {
    assert.throws(() => missingSelectors(dir), /no CSS found/);
  } finally {
    rmSync(dir, { recursive: true });
  }
});

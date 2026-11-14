// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { createWidget } from './create-widget.mjs';
import { nodeCommand, runSync } from './launch.mjs';

function fixture(t) {
  const root = mkdtempSync(path.join(tmpdir(), 'create-widget-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

test('creates a portable typed widget package with shared SDK peers', (t) => {
  const target = path.join(fixture(t), 'widget');
  createWidget('acme/tank-level', target);
  const manifest = JSON.parse(readFileSync(path.join(target, 'package.json'), 'utf8'));
  assert.equal(manifest.name, '@acme/widget-tank-level');
  assert.equal(manifest.private, true);
  assert.equal(manifest.dependencies, undefined);
  for (const peer of ['react', '@devicechain/widget-sdk', '@devicechain/dashboards']) {
    assert.ok(manifest.peerDependencies[peer], peer);
  }
  assert.equal(manifest.exports['.'].import, './dist/index.js');
  assert.equal(manifest.exports['.'].types, './dist/index.d.ts');
  const config = JSON.parse(readFileSync(path.join(target, 'tsconfig.json'), 'utf8'));
  assert.equal(config.compilerOptions.strict, true);
  assert.equal(config.compilerOptions.declaration, true);
  const source = readFileSync(path.join(target, 'src/index.tsx'), 'utf8');
  assert.match(source, /id: 'acme\/tank-level'/);
  assert.match(source, /defineWidget/);
  assert.doesNotMatch(source, /echarts|maplibre|fetch\(|as any/);
});

test('rejects invalid ids before creating anything', (t) => {
  const root = fixture(t);
  for (const id of ['plain', '../escape', 'a/b/c', 'Acme/tank', "acme/a';oops", '', 'acme/-tank']) {
    assert.throws(() => createWidget(id, path.join(root, 'output')), /scope\/name/);
  }
  assert.deepEqual(readdirSync(root), []);
});

test('refuses an existing directory or file without changing their contents', (t) => {
  const root = fixture(t);
  const marker = path.join(root, 'keep.txt');
  writeFileSync(marker, 'keep me');
  assert.throws(() => createWidget('acme/tank', root), /EEXIST/);
  assert.throws(() => createWidget('acme/tank', marker), /EEXIST/);
  assert.deepEqual(readdirSync(root), ['keep.txt']);
  assert.equal(readFileSync(marker, 'utf8'), 'keep me');
});

test('CLI creates the package and reports argument errors with a failing exit status', (t) => {
  const target = path.join(fixture(t), 'widget');
  const script = fileURLToPath(new URL('./create-widget.mjs', import.meta.url));
  const run = (args) => runSync(nodeCommand(script, args), { encoding: 'utf8' });
  const missing = run([]);
  assert.equal(missing.status, 1);
  assert.match(missing.stderr, /Usage:/);
  assert.equal(run(['acme/tank', target, 'extra']).status, 1);
  const created = run(['acme/tank', target]);
  assert.equal(created.status, 0, created.stderr);
  assert.match(created.stdout, /Created acme\/tank/);
  assert.equal(JSON.parse(readFileSync(path.join(target, 'package.json'), 'utf8')).name, '@acme/widget-tank');
  assert.equal(run(['acme/tank', target]).status, 1);
});

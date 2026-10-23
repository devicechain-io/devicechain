// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { diffModels, parseBreaks, reconcile } from './compat.mjs';

const m = (o) => new Map(Object.entries(o));

test('a removed export is a break, a new one is safe', () => {
  const c = diffModels(m({ '.:A': ['export declare const A: 1;'] }), m({ '.:B': ['export declare const B: 1;'] }));
  assert.deepEqual(c.map((x) => [x.id, x.kind, x.level]), [
    ['.:A', 'export-removed', 'breaking'],
    ['.:B', 'export-added', 'safe'],
  ]);
});

test('a removed line of a kept declaration is a break; an added optional member is safe', () => {
  const was = ['export interface I {', '    a: string;', '}'];
  assert.equal(diffModels(m({ '.:I': was }), m({ '.:I': ['export interface I {', '}'] }))[0].level, 'breaking');
  const safe = diffModels(m({ '.:I': was }), m({ '.:I': ['export interface I {', '    a: string;', '    b?: number;', '}'] }));
  assert.equal(safe[0].level, 'safe');
});

test('an added REQUIRED member is reported, never failing', () => {
  const c = diffModels(
    m({ '.:I': ['export interface I {', '    a: string;', '}'] }),
    m({ '.:I': ['export interface I {', '    a: string;', '    b: number;', '}'] }),
  );
  assert.equal(c[0].level, 'dangerous');
});

test('appending after the last union arm is not a change to that arm', () => {
  const c = diffModels(
    m({ '.:T': ['export declare const T: readonly [', "    'a'", '];'] }),
    m({ '.:T': ['export declare const T: readonly [', "    'a',", "    'b'", '];'] }),
  );
  assert.equal(c[0].level, 'safe');
});

test('an unexported declaration that vanished is not an export removal', () => {
  assert.deepEqual(diffModels(m({ '~x:Gone': ['interface Gone {}'] }), m({})), []);
});

test('declarations: live ones cover, others expire, unmatched live ones are stale', () => {
  const change = { pkg: 'p', id: '.:A', kind: 'export-removed', level: 'breaking', detail: '' };
  const { entries } = parseBreaks('v1 p .:A export-removed gone, use B\nv0 p .:Z export-removed old\nv1 p .:Y export-removed stale one');
  const r = reconcile([change], entries, 'v1');
  assert.equal(r.declared.length, 1);
  assert.equal(r.expired.length, 1);
  assert.equal(r.failures.length, 1);
  assert.match(r.failures[0], /stale declaration.*\.:Y/);
});

test('an undeclared break fails and prints the line that would declare it', () => {
  const r = reconcile([{ pkg: 'p', id: '.:A', kind: 'export-removed', level: 'breaking', detail: '' }], [], 'v1');
  assert.match(r.failures[0], /declare with: v1 p \.:A export-removed/);
});

test('a declaration without a reason is refused', () => {
  const { entries, problems } = parseBreaks('v1 p .:A export-removed');
  assert.equal(entries.length, 0);
  assert.match(problems[0], /no reason/);
});

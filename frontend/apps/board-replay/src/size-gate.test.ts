// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { afterEach, describe, expect, it } from 'vitest';

import { check, initialFiles, measure, type Budget } from '../scripts/check-bundle-size.mjs';

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

// A fake dist: an entry that statically imports one chunk and lazily imports another.
function fakeDist(): string {
  const dist = mkdtempSync(path.join(tmpdir(), 'size-gate-'));
  dirs.push(dist);
  mkdirSync(path.join(dist, 'chunks'));
  writeFileSync(path.join(dist, 'main.js'), 'import{a}from"./chunks/static-1.js";const m=()=>import("./chunks/lazy-1.js");');
  writeFileSync(path.join(dist, 'chunks/static-1.js'), randomBytes(5000));
  writeFileSync(path.join(dist, 'chunks/lazy-1.js'), randomBytes(20000));
  return dist;
}

const budget: Budget = { maxInitial: 100_000, maxTotal: 100_000, recordingGzip: 1000, hardInitial: 400_000, hardTotal: 1_200_000 };

describe('bundle size gate', () => {
  it('counts statically imported chunks in the initial load and not lazily imported ones', () => {
    const dist = fakeDist();
    const names = initialFiles(dist).map((f) => path.basename(f)).sort();
    expect(names).toEqual(['main.js', 'static-1.js']);
    const m = measure(dist);
    expect(m.initial).toBeGreaterThan(5000);
    expect(m.initial).toBeLessThan(6000);
    expect(m.total).toBeGreaterThan(25_000);
  });

  it('passes inside the budget', () => {
    expect(check({ initial: 90_000, total: 90_000 }, budget)).toEqual([]);
  });

  it('fails when the initial load or the total grows past the budget', () => {
    expect(check({ initial: 100_001, total: 10 }, budget)).toHaveLength(1);
    expect(check({ initial: 10, total: 100_001 }, budget)).toHaveLength(1);
  });

  it('fails a budget set above the product targets, and a build past them', () => {
    expect(check({ initial: 10, total: 10 }, { ...budget, maxInitial: 400_001 }).length).toBeGreaterThan(0);
    expect(check({ initial: 10, total: 10 }, { ...budget, maxTotal: 1_200_000 }).length).toBeGreaterThan(0);
  });

  it('the committed budget is within the product targets and is positive', () => {
    const committed = JSON.parse(readFileSync(path.resolve(import.meta.dirname, '../size-budget.json'), 'utf8')) as Budget;
    expect(committed.maxInitial).toBeGreaterThan(0);
    expect(committed.maxTotal).toBeGreaterThan(0);
    expect(check({ initial: 0, total: 0 }, committed)).toEqual([]);
  });
});

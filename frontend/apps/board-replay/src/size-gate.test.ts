// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { afterEach, describe, expect, it } from 'vitest';

import { absentBoundaries, check, checkAbsent, checkShape, initialFiles, lazyBoundaries, measure, type Budget } from '../scripts/check-bundle-size.mjs';

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

// A fake dist: an entry that statically imports one chunk and lazily imports another.
function fakeDist(): string {
  const dist = mkdtempSync(path.join(tmpdir(), 'size-gate-'));
  dirs.push(dist);
  mkdirSync(path.join(dist, 'chunks'));
  // A named static import, a BARE side-effect import (`import"./x.js"`, which has no
  // `from`), and a lazy dynamic import.
  writeFileSync(
    path.join(dist, 'main.js'),
    'import{a}from"./chunks/static-1.js";import"./chunks/bare-1.js";const m=()=>import("./chunks/lazy-1.js");',
  );
  writeFileSync(path.join(dist, 'chunks/static-1.js'), randomBytes(5000));
  writeFileSync(path.join(dist, 'chunks/bare-1.js'), randomBytes(7000));
  writeFileSync(path.join(dist, 'chunks/lazy-1.js'), randomBytes(20000));
  return dist;
}

const budget: Budget = { maxInitial: 100_000, maxTotal: 100_000, recordingGzip: 1000, hardInitial: 400_000, hardTotal: 1_200_000 };

describe('bundle size gate', () => {
  it('counts statically imported chunks in the initial load and not lazily imported ones', () => {
    const dist = fakeDist();
    const names = initialFiles(dist).map((f) => path.basename(f)).sort();
    expect(names).toEqual(['bare-1.js', 'main.js', 'static-1.js']);
    const m = measure(dist);
    expect(m.initial).toBeGreaterThan(12_000);
    expect(m.initial).toBeLessThan(13_000);
    expect(m.total).toBeGreaterThan(32_000);
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

  it('the showcase recording fits the allowance the budget holds for the recording the page plays', () => {
    const committed = JSON.parse(readFileSync(path.resolve(import.meta.dirname, '../size-budget.json'), 'utf8')) as Budget;
    const recording = readFileSync(path.resolve(import.meta.dirname, '../../../testdata/board-replay/sitepulse-showcase.json'));
    expect(gzipSync(recording, { level: 9 }).length).toBeLessThanOrEqual(committed.recordingGzip);
  });
});

describe('lazy boundary check', () => {
  // A dist whose entry statically imports `shared-1.js` and lazily imports `widget-1.js`.
  function shapedDist(files: { main?: string; shared?: string; widget?: string }): string {
    const dist = mkdtempSync(path.join(tmpdir(), 'shape-gate-'));
    dirs.push(dist);
    mkdirSync(path.join(dist, 'chunks'));
    writeFileSync(
      path.join(dist, 'main.js'),
      `import{a}from"./chunks/shared-1.js";const m=()=>import("./chunks/widget-1.js");${files.main ?? ''}`,
    );
    writeFileSync(path.join(dist, 'chunks/shared-1.js'), files.shared ?? 'export const a=1;');
    writeFileSync(path.join(dist, 'chunks/widget-1.js'), files.widget ?? 'export const w=1;');
    return dist;
  }
  const boundary = [{ what: 'the widget', chunk: 'widget', markers: ['MARKER'] }];

  it('passes when the marker is only in the lazy chunk', () => {
    expect(checkShape(shapedDist({ widget: '"MARKER"' }), boundary)).toEqual([]);
  });

  it('fails when the marker is in the entry, or in a chunk the entry imports statically', () => {
    expect(checkShape(shapedDist({ widget: '"MARKER"', main: '"MARKER"' }), boundary)).toHaveLength(1);
    expect(checkShape(shapedDist({ widget: '"MARKER"', shared: '"MARKER"' }), boundary)).toHaveLength(1);
  });

  it('fails when no lazy chunk of that name holds the marker (the reach control)', () => {
    expect(checkShape(shapedDist({}), boundary)).toHaveLength(1);
    // Folded into the entry and gone from the chunk: both halves report.
    expect(checkShape(shapedDist({ main: '"MARKER"' }), boundary)).toHaveLength(2);
  });

  it('reads the catalog markers from the catalogs, and none of them is English', () => {
    const en = JSON.parse(readFileSync(path.resolve(import.meta.dirname, 'i18n/locales/en/chrome.json'), 'utf8')) as Record<string, string>;
    const markers = lazyBoundaries().flatMap((b) => b.markers);
    expect(markers.length).toBeGreaterThan(5);
    for (const marker of markers) {
      expect(marker.length).toBeGreaterThan(5);
      expect(Object.values(en).some((v) => v.includes(marker)), `${marker} is English chrome`).toBe(false);
    }
  });
});

describe('absent-from-the-build check', () => {
  function absentDist(files: { main?: string; widget?: string }): { dist: string; owner: string } {
    const dist = mkdtempSync(path.join(tmpdir(), 'absent-gate-'));
    dirs.push(dist);
    mkdirSync(path.join(dist, 'chunks'));
    writeFileSync(path.join(dist, 'main.js'), files.main ?? 'export const a=1;');
    writeFileSync(path.join(dist, 'chunks/widget-1.js'), files.widget ?? 'export const w=1;');
    // The owning package lives OUTSIDE dist, as the real one does.
    const home = mkdtempSync(path.join(tmpdir(), 'absent-owner-'));
    dirs.push(home);
    const owner = path.join(home, 'owner.src.js');
    writeFileSync(owner, 'const docs = "MARKER";');
    return { dist, owner };
  }
  const boundary = (owner: string) => [{ what: 'the docs', reach: owner, markers: ['MARKER'] }];

  it('passes when no file of the build holds the marker', () => {
    const { dist, owner } = absentDist({});
    expect(checkAbsent(dist, boundary(owner))).toEqual([]);
  });

  it('fails when the entry or a lazy chunk holds it -- initial or not', () => {
    const a = absentDist({ main: '"MARKER"' });
    expect(checkAbsent(a.dist, boundary(a.owner))).toHaveLength(1);
    const b = absentDist({ widget: '"MARKER"' });
    expect(checkAbsent(b.dist, boundary(b.owner))).toHaveLength(1);
  });

  it('fails when the owning package no longer holds the marker (the reach control)', () => {
    const { dist, owner } = absentDist({});
    writeFileSync(owner, 'const docs = "reworded";');
    expect(checkAbsent(dist, boundary(owner))).toHaveLength(1);
  });

  it('names markers the widgets package really holds', () => {
    for (const { reach, markers } of absentBoundaries()) {
      const owner = readFileSync(reach, 'utf8');
      for (const marker of markers) expect(owner.includes(marker), marker).toBe(true);
    }
  });
});

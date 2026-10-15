// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A seeded property test of the conversion of a definition stored before schemaVersion 2. Every
// stored dashboard in every tenant goes through it on its first load after the upgrade, and the
// parser it feeds REFUSES two overlapping grid widgets, so a conversion that leaves one overlap
// behind is a dashboard that no longer opens. The generator draws old-format boards the old
// parser accepted (any column count, gap and row height; boxes past the edge, fractional,
// negative or missing fields; z-stacked overlap; breakpoint overrides; widgets of an unknown
// type; junk breakpoint entries) and asserts, for every one:
//   - it parses, and keeps every widget, in order, with its id;
//   - nothing overlaps at any breakpoint, and every footprint is inside the 12 columns;
//   - it is a fixed point: saving and re-reading changes nothing but the conversion mark;
//   - stacking order is kept: of two widgets that shared a column, the one that started strictly
//     higher on the old grid still starts higher.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition, withoutConversionMark } from './definition';
import { gridFootprintsOverlap, layoutBreakpoints, resolvePlacement } from './grid';
import type { GridPlacement } from './types';

// mulberry32: small, fast and deterministic, so a failure names a seed that reproduces it.
function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

type Rand = () => number;
const int = (r: Rand, lo: number, hi: number) => lo + Math.floor(r() * (hi - lo + 1));
const pick = <T,>(r: Rand, xs: readonly T[]): T => xs[Math.floor(r() * xs.length)];
const chance = (r: Rand, p: number) => r() < p;

interface OldBox {
  col?: unknown;
  colSpan?: unknown;
  row?: unknown;
  rowSpan?: unknown;
  z?: unknown;
  offset?: unknown;
}

function oldBox(r: Rand, columns: number): OldBox {
  const box: OldBox = {};
  // Mostly well-formed, sometimes past the edge, fractional, negative or missing.
  const col = chance(r, 0.05) ? -int(r, 1, 3) : chance(r, 0.05) ? int(r, columns, columns + 6) : int(r, 0, columns - 1);
  const colSpan = chance(r, 0.05) ? int(r, columns, columns * 2) : int(r, 1, Math.max(1, columns - Math.max(0, col)));
  const row = chance(r, 0.05) ? -int(r, 1, 3) : int(r, 0, 40);
  const rowSpan = int(r, 1, 12);
  if (!chance(r, 0.03)) box.col = chance(r, 0.1) ? col + 0.4 : col;
  if (!chance(r, 0.03)) box.colSpan = chance(r, 0.1) ? colSpan + 0.6 : colSpan;
  if (!chance(r, 0.03)) box.row = row;
  if (!chance(r, 0.03)) box.rowSpan = chance(r, 0.05) ? 0 : rowSpan;
  if (chance(r, 0.03)) box.col = 'x';
  box.z = chance(r, 0.2) ? int(r, -3, 9) : 0;
  if (chance(r, 0.1)) box.offset = { x: int(r, -40, 40), y: int(r, -40, 40) };
  return box;
}

interface Generated {
  doc: Record<string, unknown>;
  // The old placement each widget showed at, per breakpoint, for the stacking-order check.
  oldColumns: number;
}

function oldBoard(seed: number): Generated {
  const r = rng(seed);
  const columns = pick(r, [24, 24, 24, 12, 16, 36, 48, 7, 1]);
  const grid: Record<string, unknown> = { columns };
  if (chance(r, 0.8)) grid.rowHeight = pick(r, [40, 40, 20, 32, 64, 120, 8]);
  if (chance(r, 0.6)) grid.gap = chance(r, 0.8) ? pick(r, [0, 4, 8, 16]) : { row: int(r, 0, 20), col: int(r, 0, 20) };
  const bpNames = ['md', 'lg', 'xl'].filter(() => chance(r, 0.3));
  const breakpoints: Record<string, unknown> = { base: 0 };
  for (const name of bpNames) breakpoints[name] = int(r, 600, 2400);
  if (chance(r, 0.1)) breakpoints.junk = 'not a width';

  // Half the boards are TIDY, as the boards real tools wrote are: packed left to right in bands,
  // touching but never overlapping, so rounding is the only thing that can make them collide.
  // The rest are anything the old parser accepted, z-stacked overlap included.
  const tidy = chance(r, 0.5);
  let x = 0;
  let y = 0;
  let band = 1;
  const tidyBox = (): OldBox => {
    const colSpan = int(r, 1, Math.max(1, Math.min(columns, 12)));
    if (x + colSpan > columns) {
      x = 0;
      y += band;
      band = 1;
    }
    const rowSpan = int(r, 1, 8);
    band = Math.max(band, rowSpan);
    const box = { col: x, colSpan, row: y, rowSpan, z: 0 };
    x += colSpan;
    return box;
  };
  const widgets: unknown[] = [];
  const n = int(r, 0, 30);
  for (let i = 0; i < n; i++) {
    const layout: Record<string, unknown> = { base: tidy ? tidyBox() : oldBox(r, columns) };
    // A tidy board's overrides are not tidy among themselves; they exercise the push, not order.
    for (const name of bpNames) if (chance(r, tidy ? 0.1 : 0.4)) layout[name] = oldBox(r, columns);
    // An override for a breakpoint the canvas does not declare: never shown, still checked.
    if (chance(r, 0.05)) layout.orphan = oldBox(r, columns);
    // A breakpoint entry the old parser skipped as not-a-box.
    if (chance(r, 0.05)) layout[pick(r, ['md', 'lg', 'orphan'])] = pick(r, [null, 'x', 3, []]);
    const known = chance(r, 0.8);
    widgets.push(
      known
        ? { id: `w${i}`, type: 'label', layout, options: { text: `w${i}` } }
        : { id: `w${i}`, type: 'acme/radar', layout, options: { k: i } },
    );
  }
  const doc: Record<string, unknown> = {
    title: `fuzz ${seed}`,
    canvas: { grid, sizing: 'fill', breakpoints },
    widgets,
  };
  // Stated 1, or unstated (read by its shape).
  if (chance(r, 0.8)) doc.schemaVersion = 1;
  return { doc, oldColumns: columns };
}

function num(v: unknown, fallback: number): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

// The old placement a widget showed at a breakpoint, as the old parser read it.
function oldAt(w: { layout: Record<string, unknown> }, bp: string) {
  const own = w.layout[bp];
  const box = (own !== null && typeof own === 'object' && !Array.isArray(own) ? own : w.layout.base) as OldBox;
  return {
    col: Math.max(0, Math.round(num(box.col, 0))),
    cols: Math.max(1, Math.round(num(box.colSpan, 1))),
    row: Math.max(0, Math.round(num(box.row, 0))),
    rows: Math.max(1, Math.round(num(box.rowSpan, 1))),
  };
}

// 1000 boards run in a few seconds; the converter was also run at 20000 when this was written.
const SEEDS = 1000;

describe('converting old-format boards (seeded property test)', () => {
  it(`parses, keeps every widget and leaves no overlap, for ${SEEDS} generated boards`, () => {
    let orderChecked = 0;
    for (let seed = 1; seed <= SEEDS; seed++) {
      const { doc, oldColumns } = oldBoard(seed);
      const before = JSON.stringify(doc);
      let def;
      try {
        def = parseDashboardDefinition(doc);
      } catch (err) {
        throw new Error(`seed ${seed}: ${(err as Error).message}\n${before}`);
      }
      const ctx = `seed ${seed}`;
      expect(JSON.stringify(doc), ctx).toBe(before);
      const oldWidgets = doc.widgets as { id: string; layout: Record<string, unknown> }[];
      expect(def.widgets.map((w) => w.id), ctx).toEqual(oldWidgets.map((w) => w.id));
      expect(def.schemaVersion, ctx).toBe(3);
      if (oldWidgets.length > 0) expect(def.convertedFrom, ctx).toBe(1);

      for (const bp of layoutBreakpoints(def.canvas.breakpoints, def.widgets)) {
        const grid = def.widgets.map((w) => resolvePlacement(w.layout, bp) as GridPlacement);
        for (const p of grid) {
          expect(p.mode, ctx).toBe('grid');
          expect(p.col + p.cols, `${ctx} ${bp}`).toBeLessThanOrEqual(def.canvas.grid.columns);
          expect(Number.isInteger(p.row) && p.row >= 0 && p.rows >= 1 && p.cols >= 1, ctx).toBe(true);
        }
        for (let i = 0; i < grid.length; i++) {
          for (let j = i + 1; j < grid.length; j++) {
            expect(gridFootprintsOverlap(grid[i], grid[j]), `${ctx} ${bp} ${i}/${j}`).toBe(false);
          }
        }
        // Stacking order: two widgets that shared a column, one strictly above the other on the
        // old grid, keep that order. (Old boxes are clamped to the old column count first, as the
        // old renderer drew them.)
        const olds = oldWidgets.map((w) => oldAt(w, bp));
        // Only where the old layout itself did not overlap: a widget that was layered under
        // another is pushed below it, past whatever was below both, and cannot keep its order.
        const clampOld = (o: (typeof olds)[number]) => {
          const c0 = Math.min(o.col, oldColumns - 1);
          return { mode: 'grid' as const, col: c0, cols: Math.max(1, Math.min(o.col + o.cols, oldColumns) - c0), row: o.row, rows: o.rows };
        };
        const oldOverlaps = olds.some((a, i) => olds.some((b, j) => i < j && gridFootprintsOverlap(clampOld(a), clampOld(b))));
        if (oldOverlaps) continue;
        orderChecked++;
        for (let i = 0; i < olds.length; i++) {
          for (let j = 0; j < olds.length; j++) {
            const a = olds[i];
            const b = olds[j];
            const aEnd = Math.min(a.col + a.cols, oldColumns);
            const bEnd = Math.min(b.col + b.cols, oldColumns);
            const share = Math.min(a.col, oldColumns - 1) < bEnd && Math.min(b.col, oldColumns - 1) < aEnd;
            // ...and still share one after the columns were scaled (rounding can part two that
            // shared a sliver of a column; those no longer stack, so they have no order).
            const shareNow = grid[i].col < grid[j].col + grid[j].cols && grid[j].col < grid[i].col + grid[i].cols;
            if (share && shareNow && a.row + a.rows <= b.row) {
              expect(grid[i].row, `${ctx} ${bp}: ${oldWidgets[i].id} was above ${oldWidgets[j].id}`).toBeLessThan(grid[j].row);
            }
          }
        }
      }

      // A fixed point: what a save writes reads back as the same board, with no mark.
      const saved = JSON.parse(serializeDefinition(def));
      const again = parseDashboardDefinition(saved);
      expect(again.convertedFrom, ctx).toBeUndefined();
      expect(again, ctx).toEqual(withoutConversionMark(def));
      // And reading the converted document twice is the same as once.
      expect(parseDashboardDefinition(doc), ctx).toEqual(def);
    }
    // The order property is vacuous if no board reached it.
    expect(orderChecked).toBeGreaterThan(SEEDS / 3);
  }, 60_000);
});

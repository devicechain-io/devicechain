// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The one-way conversion of a definition stored before schemaVersion 2 onto the square-cell grid.
//
// Before 2, a widget was a box on a grid of fluid columns (24 by default) and FIXED-PX rows (40
// by default), layered by `z` and nudged by a pixel `offset`. Now cells are square, every
// distance is in cells, and two grid widgets cannot overlap. There is no exact mapping between
// the two (a fixed-px row is a different fraction of a square cell at every width), so this is
// an APPROXIMATION, and it says so: the parser marks the result `convertedFrom`, and an editor
// shows "converted from the previous grid -- review the layout" until the next save writes the
// current format.
//
// The mapping:
//   columns  start and end lines scale by newColumns / oldColumns and round, so two boxes that
//            met at a line still meet at one;
//   rows     start and end lines go to px on the old grid (row * (rowHeight + gap)) and back to
//            cells at the reference width (1366 px, the common laptop), where a new cell is
//            (1366 - (columns-1) * gutter) / columns;
//   gutter   the old gap, unchanged;
//   z, offset  dropped: there is no layering on the grid, and a pixel nudge has no meaning in
//            cells. Overlap was the only thing either was for;
//   overlap  whatever rounding (or the old z-stacking) leaves overlapping is pushed DOWN until it
//            fits, keeping every widget below the ones it was below (restack), so the converted
//            definition parses: the overlap refusal is not relaxed for converted documents.
//
// It works on the RAW document, before the parser sees it, so every widget is converted the same
// way, a widget of a type this build does not know included: its stored object is kept verbatim
// and written back on save, so its layout must already be in the current format.

import {
  BASE_BREAKPOINT,
  DEFAULT_GRID,
  cellSizeAt,
  gridFootprintsOverlap,
  gutterPx,
} from './grid';
import { CURRENT_SCHEMA_VERSION, type CanvasGrid, type GridPlacement } from './types';

// The format this conversion produces: the square-cell grid. Later formats are layered on by
// their own conversions (./legacy-window), never folded into this one.
export const GRID_SCHEMA_VERSION = 2;

// The width the old fixed-px rows are read at.
export const LEGACY_REFERENCE_WIDTH = 1366;

const LEGACY_DEFAULTS = { columns: 24, gap: 8, rowHeight: 40 };

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function num(rec: Record<string, unknown>, key: string, fallback: number): number {
  const v = rec[key];
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

// The format a stored document is in: its schemaVersion when it states one. A document that
// states none (no producer has written one without it, so it is hand-written or very old) is
// read by its SHAPE: an old grid (`rowHeight`, `gap`), an old box (`colSpan`, `rowSpan`, `z`), or
// a box with neither a `mode` nor `cols` anywhere makes it 1, and anything else is the current
// format. Guessing 1 for every unversioned document would put a hand-written current one
// through the converter and mangle it.
export function storedSchemaVersion(raw: Record<string, unknown>): number {
  if (typeof raw.schemaVersion === 'number' && Number.isFinite(raw.schemaVersion)) return raw.schemaVersion;
  const grid = isRecord(raw.canvas) && isRecord(raw.canvas.grid) ? raw.canvas.grid : {};
  if ('rowHeight' in grid || 'gap' in grid) return 1;
  const widgets = Array.isArray(raw.widgets) ? raw.widgets : [];
  for (const w of widgets) {
    if (!isRecord(w) || !isRecord(w.layout)) continue;
    for (const box of Object.values(w.layout)) {
      if (!isRecord(box)) continue;
      if ('colSpan' in box || 'rowSpan' in box || 'z' in box) return 1;
      // A box with neither a mode nor a span in cells is not a current placement (one with no
      // mode is a grid placement, and a grid placement needs `cols`): the old parser read it,
      // filling every field it left out, so the old format is the reading that opens.
      if (!('mode' in box) && !('cols' in box)) return 1;
    }
  }
  return CURRENT_SCHEMA_VERSION;
}

interface LegacyGrid {
  columns: number;
  rowPitch: number;
  gutter: CanvasGrid['gutter'];
}

function legacyGrid(raw: unknown): LegacyGrid {
  const rec = isRecord(raw) ? raw : {};
  const columns = Math.max(1, Math.round(num(rec, 'columns', LEGACY_DEFAULTS.columns)));
  const rowHeight = Math.max(1, num(rec, 'rowHeight', LEGACY_DEFAULTS.rowHeight));
  let gapRow = LEGACY_DEFAULTS.gap;
  let gapCol = LEGACY_DEFAULTS.gap;
  if (typeof rec.gap === 'number' && Number.isFinite(rec.gap)) {
    gapRow = gapCol = Math.max(0, rec.gap);
  } else if (isRecord(rec.gap)) {
    gapRow = Math.max(0, num(rec.gap, 'row', LEGACY_DEFAULTS.gap));
    gapCol = Math.max(0, num(rec.gap, 'col', LEGACY_DEFAULTS.gap));
  }
  return {
    columns,
    rowPitch: rowHeight + gapRow,
    gutter: gapRow === gapCol ? gapCol : { x: gapCol, y: gapRow },
  };
}

// A widget's rows on the OLD grid (start line, end line) and its old start column.
interface OldSpan {
  row: number;
  end: number;
  col: number;
}

interface StackItem {
  index: number;
  placement: GridPlacement;
  old: OldSpan;
}

// restack places the converted boxes of one breakpoint so that none overlaps and the old stacking
// survives, returning only the ones that moved. In the old grid's reading order (row, column,
// then document order), each widget goes at its converted row, but never above the bottom of a
// widget it was BELOW on the old grid and still shares a column with, and then further down past
// anything it would overlap. The first rule is what a plain push-down lacks: rounding collapses
// short old rows onto one new row, the push then moves a widget down a long way, and a widget
// that was under it -- placed later, colliding with nothing -- would stay where it was and end
// up ABOVE it. Old boxes that overlapped (layered with z) have no above/below, so only the
// collision rule applies to them.
function restack(items: StackItem[]): Map<number, GridPlacement> {
  const order = [...items].sort((a, b) => a.old.row - b.old.row || a.old.col - b.old.col || a.index - b.index);
  const placed: StackItem[] = [];
  const moved = new Map<number, GridPlacement>();
  for (const item of order) {
    let p = item.placement;
    const sharesColumn = (q: GridPlacement) => p.col < q.col + q.cols && q.col < p.col + p.cols;
    const floor = placed
      .filter((q) => q.old.end <= item.old.row && sharesColumn(q.placement))
      .reduce((m, q) => Math.max(m, q.placement.row + q.placement.rows), 0);
    if (floor > p.row) p = { ...p, row: floor };
    for (;;) {
      const hits = placed.filter((q) => gridFootprintsOverlap(p, q.placement));
      if (hits.length === 0) break;
      p = { ...p, row: Math.max(...hits.map((q) => q.placement.row + q.placement.rows)) };
    }
    placed.push({ ...item, placement: p });
    if (p !== item.placement) moved.set(item.index, p);
  }
  return moved;
}

// convertLegacyDefinition returns the document in the current format. The input is not modified.
export function convertLegacyDefinition(raw: Record<string, unknown>): Record<string, unknown> {
  const canvasIn = isRecord(raw.canvas) ? raw.canvas : {};
  const old = legacyGrid(canvasIn.grid);
  const grid: CanvasGrid = { ...DEFAULT_GRID, gutter: old.gutter };
  const columns = grid.columns;
  const pitch = cellSizeAt(grid, LEGACY_REFERENCE_WIDTH) + gutterPx(grid).y;

  const toCols = (line: number) => Math.round((line * columns) / old.columns);
  const toRows = (line: number) => Math.round((line * old.rowPitch) / pitch);
  const convertBox = (box: Record<string, unknown>): GridPlacement => {
    const col = Math.max(0, Math.round(num(box, 'col', 0)));
    const colSpan = Math.max(1, Math.round(num(box, 'colSpan', 1)));
    const row = Math.max(0, Math.round(num(box, 'row', 0)));
    const rowSpan = Math.max(1, Math.round(num(box, 'rowSpan', 1)));
    const c0 = Math.min(toCols(col), columns - 1);
    const c1 = Math.min(Math.max(toCols(col + colSpan), c0 + 1), columns);
    const r0 = toRows(row);
    const r1 = Math.max(toRows(row + rowSpan), r0 + 1);
    return { mode: 'grid', col: c0, row: r0, cols: c1 - c0, rows: r1 - r0 };
  };

  // Each widget's old box per breakpoint is kept beside its conversion, because the push below
  // works from where a widget was on the OLD grid, not where rounding put it.
  const oldBoxes = new Map<number, Record<string, OldSpan>>();
  const widgetsIn = Array.isArray(raw.widgets) ? raw.widgets : [];
  const widgets = widgetsIn.map((w, i) => {
    if (!isRecord(w) || !isRecord(w.layout)) return w;
    const layout: Record<string, unknown> = {};
    const olds: Record<string, OldSpan> = {};
    for (const [bp, box] of Object.entries(w.layout)) {
      // An entry that is not a box (null, a number) was skipped by the old parser and is skipped
      // by the current one, which shows the base placement there instead. It is dropped here so
      // the push below sees the placement the parser checks.
      if (!isRecord(box) || bp === '__proto__') continue;
      layout[bp] = convertBox(box);
      const row = Math.max(0, Math.round(num(box, 'row', 0)));
      olds[bp] = { row, end: row + Math.max(1, Math.round(num(box, 'rowSpan', 1))), col: Math.max(0, Math.round(num(box, 'col', 0))) };
    }
    oldBoxes.set(i, olds);
    return { ...w, layout };
  });

  // Push apart whatever the rounding, or the old layering, left overlapping: base first, since
  // every other breakpoint inherits it, then each other breakpoint over what it shows. A widget
  // shows its own entry at a breakpoint, or its base one (resolvePlacement's rule, which is the
  // one the parser checks overlap by).
  const byBreakpoint = new Set<string>([BASE_BREAKPOINT]);
  for (const w of widgets) if (isRecord(w) && isRecord(w.layout)) for (const bp of Object.keys(w.layout)) byBreakpoint.add(bp);
  for (const bp of byBreakpoint) {
    const items: StackItem[] = [];
    widgets.forEach((w, i) => {
      if (!isRecord(w) || !isRecord(w.layout)) return;
      const layout = w.layout as Record<string, unknown>;
      const own = Object.prototype.hasOwnProperty.call(layout, bp);
      const p = (own ? layout[bp] : layout[BASE_BREAKPOINT]) as GridPlacement | undefined;
      const old = oldBoxes.get(i)?.[own ? bp : BASE_BREAKPOINT];
      if (p && isRecord(p) && p.mode === 'grid' && old) items.push({ index: i, placement: p, old });
    });
    for (const [index, moved] of restack(items)) {
      const w = widgets[index] as Record<string, unknown>;
      (w.layout as Record<string, unknown>)[bp] = moved;
    }
  }

  const breakpoints: Record<string, number> = {};
  if (isRecord(canvasIn.breakpoints)) {
    for (const [name, width] of Object.entries(canvasIn.breakpoints)) {
      if (typeof width === 'number' && Number.isFinite(width)) breakpoints[name] = width;
    }
  }
  return {
    ...raw,
    schemaVersion: GRID_SCHEMA_VERSION,
    canvas: { ...canvasIn, grid, breakpoints },
    widgets,
  };
}

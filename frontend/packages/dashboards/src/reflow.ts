// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Adaptive columns and reflow, as pure functions.
//
// Cells keep a roughly constant size: a board authored in N columns renders as authored while a
// cell at N columns is at least minCellSize wide (the cells stretch to fill the width, so a wall
// gets bigger cells and bigger text, not more widgets). Narrower than that the column count
// drops to what fits, and the base layout is poured into it in reading order. A span in cells
// therefore means a readable size at every width.
//
// The container's width is what is measured, never the window's: an embedded board answers to the
// space it is actually given.

import {
  BASE_BREAKPOINT,
  PHONE_MIN_COLUMNS,
  activeBreakpoint,
  breakpointColumns,
  cellSizeAt,
  gridFootprintsOverlap,
  gutterPx,
  resolvePlacement,
} from './grid';
import type { SpanLimits } from './layout-lint';
import type { CanvasGrid, DashboardDefinition, GridPlacement, WidgetInstance } from './types';

// The column count a container width gets: the authored count while its cells would be at least
// minCellSize (scaled by the density), otherwise as many columns as fit at minCellSize, no fewer
// than PHONE_MIN_COLUMNS (a board authored in fewer keeps its own count) and never more than the
// authored count.
export function liveColumns(grid: CanvasGrid, containerWidth: number, authored: number, scale = 1): number {
  const minCell = grid.minCellSize * scale;
  if (cellSizeAt(grid, containerWidth, authored, scale) >= minCell) return authored;
  const gx = gutterPx(grid, scale).x;
  // The inverse of cellSizeAt: the most columns whose cells are still minCell wide.
  const room = cellSizeAt(grid, containerWidth, 1, scale);
  const fit = Math.floor((room + gx) / (minCell + gx));
  return Math.min(authored, Math.max(PHONE_MIN_COLUMNS, fit));
}

export interface ReflowItem {
  id: string;
  placement: GridPlacement;
  // The widget's declared limits; a span is clamped into them. Absent = unbounded.
  limits?: SpanLimits;
}

const clampInto = (n: number, min: number | undefined, max: number | undefined) =>
  Math.min(max ?? Infinity, Math.max(min ?? 1, n));

// reflowGrid pours grid footprints into `columns` columns. Widgets go in reading order (row, then
// column, then their order in the input), each keeping its span clamped into its declared limits
// and then into the columns (a widget wider than the whole grid takes the full width, below its
// minSpan if it has to), at the first free slot at or below the row the previous widget landed in.
// The row never moves back up, so the order a viewer reads is the order authored. Nothing
// overlaps. Deterministic: the same input gives the same output.
export function reflowGrid(items: readonly ReflowItem[], columns: number): Map<string, GridPlacement> {
  const order = items
    .map((item, index) => ({ item, index }))
    .sort(
      (a, b) =>
        a.item.placement.row - b.item.placement.row ||
        a.item.placement.col - b.item.placement.col ||
        a.index - b.index,
    );
  const placed: GridPlacement[] = [];
  const out = new Map<string, GridPlacement>();
  let floor = 0;
  for (const { item } of order) {
    const { placement: p, limits } = item;
    const cols = Math.min(columns, clampInto(p.cols, limits?.minSpan?.cols, limits?.maxSpan?.cols));
    const rows = clampInto(p.rows, limits?.minSpan?.rows, limits?.maxSpan?.rows);
    let spot: GridPlacement | undefined;
    for (let row = floor; !spot; row++) {
      for (let col = 0; col + cols <= columns; col++) {
        const probe: GridPlacement = { mode: 'grid', col, row, cols, rows };
        if (!placed.some((q) => gridFootprintsOverlap(probe, q))) {
          spot = probe;
          break;
        }
      }
    }
    placed.push(spot);
    out.set(item.id, spot);
    floor = spot.row;
  }
  return out;
}

export interface EffectiveLayout {
  // The breakpoint whose placements are shown: chosen from the container's width.
  breakpoint: string;
  // The column count actually drawn.
  columns: number;
  // True when the board was poured into fewer columns than that breakpoint authored.
  reflowed: boolean;
  // The grid footprint of every grid widget as drawn (the authored one when not reflowed).
  grid: Map<string, GridPlacement>;
}

// effectiveLayout is what a renderer draws for a container width. A width of 0 or less means "not
// measured yet" and draws the authored base layout. A breakpoint the author defined for this
// width wins over the automatic reflow: it is drawn as authored while its cells fit, and is itself
// poured into fewer columns only if even its own count does not fit. Relative widgets are not in
// `grid`: they follow their host.
export function effectiveLayout(
  def: DashboardDefinition,
  containerWidth: number,
  options: { scale?: number; limitsOf?: (w: WidgetInstance) => SpanLimits | undefined } = {},
): EffectiveLayout {
  const scale = options.scale ?? 1;
  const measured = containerWidth > 0;
  const breakpoint = measured ? activeBreakpoint(def.canvas.breakpoints, containerWidth) : BASE_BREAKPOINT;
  const authored = breakpointColumns(def.canvas, breakpoint);
  const columns = measured ? liveColumns(def.canvas.grid, containerWidth, authored, scale) : authored;
  const items: ReflowItem[] = [];
  for (const w of def.widgets) {
    const p = resolvePlacement(w.layout, breakpoint);
    if (p.mode === 'grid') items.push({ id: w.id, placement: p, limits: options.limitsOf?.(w) });
  }
  const reflowed = columns < authored;
  const grid = reflowed ? reflowGrid(items, columns) : new Map(items.map((i) => [i.id, i.placement]));
  return { breakpoint, columns, reflowed, grid };
}

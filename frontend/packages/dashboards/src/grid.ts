// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The square-cell grid's geometry, as pure functions: lengths, cell size, where a placement
// lands, collisions between grid footprints, and the effective frame of a widget.
//
// One copy of the arithmetic, shared by everything that has to agree about where a widget is:
// the parser (overlap), the legacy converter (mapping old boxes, then pushing collisions
// apart), the editor (pixels <-> cells), the layout lint (a relative widget off the canvas) and
// the renderers. Nothing here knows about React or the DOM.

import {
  SPACE_TOKEN_PX,
  type Breakpoints,
  type BreakpointSpec,
  type Canvas,
  type CanvasGrid,
  type GridLength,
  type GridPlacement,
  type Placement,
  type PlacementAnchor,
  type RelativePlacement,
  type ResolvedFrame,
  type SpaceTokenName,
  type WidgetFrameSpec,
  type WidgetInstance,
  type WidgetLayout,
} from './types';

export const BASE_BREAKPOINT = 'base';

// The grid a definition gets when it states none, and the values a partial grid is filled
// from. 12 columns: it divides by 2, 3, 4 and 6, and at the 960 px a half-screen board gets, a
// 12-column cell is ~73 px, comfortably above minCellSize, where 16 columns would already be
// below it (52 px) and reflow. 56 px is the smallest cell a single-value widget reads in.
export const DEFAULT_GRID: Readonly<CanvasGrid> = Object.freeze({
  columns: 12,
  minCellSize: 56,
  gutter: 8,
  padding: 0,
});

// The narrowest a board reflows to: below this many live columns a widget would be a sliver.
export const PHONE_MIN_COLUMNS = 4;

export function isSpaceToken(v: unknown): v is SpaceTokenName {
  return typeof v === 'string' && Object.prototype.hasOwnProperty.call(SPACE_TOKEN_PX, v);
}

// The px a length stands for at a density scale.
export function gridLengthPx(len: GridLength, scale = 1): number {
  const base = typeof len === 'number' ? len : SPACE_TOKEN_PX[len];
  return base * scale;
}

export interface Gutter {
  x: number;
  y: number;
}

export interface Insets {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

export function gutterPx(grid: CanvasGrid, scale = 1): Gutter {
  const g = grid.gutter;
  if (typeof g === 'object') return { x: gridLengthPx(g.x, scale), y: gridLengthPx(g.y, scale) };
  const v = gridLengthPx(g, scale);
  return { x: v, y: v };
}

export function paddingPx(grid: CanvasGrid, scale = 1): Insets {
  const p = grid.padding;
  if (typeof p === 'object') {
    return {
      top: gridLengthPx(p.top, scale),
      right: gridLengthPx(p.right, scale),
      bottom: gridLengthPx(p.bottom, scale),
      left: gridLengthPx(p.left, scale),
    };
  }
  const v = gridLengthPx(p, scale);
  return { top: v, right: v, bottom: v, left: v };
}

// The live cell size for a container width at a column count: the width left after the padding
// and the gutters, shared evenly. The gutters come out of the cell. Never below 0.
export function cellSizeAt(grid: CanvasGrid, containerWidth: number, columns = grid.columns, scale = 1): number {
  const pad = paddingPx(grid, scale);
  const g = gutterPx(grid, scale);
  return Math.max(0, (containerWidth - pad.left - pad.right - (columns - 1) * g.x) / columns);
}

// The container width at which a column count's cells are exactly `cell` px: the inverse of
// cellSizeAt.
export function containerWidthFor(grid: CanvasGrid, cell: number, columns = grid.columns, scale = 1): number {
  const pad = paddingPx(grid, scale);
  return columns * cell + (columns - 1) * gutterPx(grid, scale).x + pad.left + pad.right;
}

// ---- Breakpoints -------------------------------------------------------------------

export function breakpointMinWidth(spec: BreakpointSpec): number {
  return typeof spec === 'number' ? spec : spec.minWidth;
}

// The column count a breakpoint's placements are authored in: its declared count, or the grid's.
export function breakpointColumns(canvas: Pick<Canvas, 'grid' | 'breakpoints'>, breakpoint: string): number {
  if (breakpoint === BASE_BREAKPOINT) return canvas.grid.columns;
  const spec = Object.prototype.hasOwnProperty.call(canvas.breakpoints, breakpoint)
    ? canvas.breakpoints[breakpoint]
    : undefined;
  return spec !== undefined && typeof spec === 'object' ? spec.columns : canvas.grid.columns;
}

// activeBreakpoint picks the breakpoint whose min width is the largest one that still fits the
// width (falling back to 'base'). Deterministic given equal widths by preferring the wider min.
export function activeBreakpoint(breakpoints: Breakpoints, width: number): string {
  let best = BASE_BREAKPOINT;
  let bestWidth = -1;
  for (const [name, spec] of Object.entries(breakpoints)) {
    const minWidth = breakpointMinWidth(spec);
    if (width >= minWidth && minWidth > bestWidth) {
      best = name;
      bestWidth = minWidth;
    }
  }
  return best;
}

// The placement a widget shows at a breakpoint: its own entry, or its base one.
export function resolvePlacement(layout: WidgetLayout, breakpoint: string): Placement {
  return Object.prototype.hasOwnProperty.call(layout, breakpoint) ? layout[breakpoint] : layout[BASE_BREAKPOINT];
}

// Every breakpoint a set of widgets can be shown at: the declared ones, and any a widget carries
// an entry for (an entry for an undeclared breakpoint is never shown, but it is still checked).
export function layoutBreakpoints(breakpoints: Breakpoints, widgets: readonly { layout: WidgetLayout }[]): string[] {
  const names = new Set<string>([BASE_BREAKPOINT, ...Object.keys(breakpoints)]);
  for (const w of widgets) for (const bp of Object.keys(w.layout)) names.add(bp);
  return [...names];
}

// ---- Grid footprints ---------------------------------------------------------------

export function gridFootprintsOverlap(a: GridPlacement, b: GridPlacement): boolean {
  return a.col < b.col + b.cols && b.col < a.col + a.cols && a.row < b.row + b.rows && b.row < a.row + a.rows;
}

export interface PlacedGrid {
  id: string;
  placement: GridPlacement;
}

// pushDown resolves every collision among grid footprints by moving widgets DOWN, never sideways,
// so each keeps its column and its span: the `first` widgets are placed where they are (in the
// order given), then the rest in reading order (row, then column, then their order in the input),
// each at its own row or, if that collides, at the first row below where it no longer does. This
// is react-grid-layout's collision rule without the upward compaction, so nothing that was not in
// the way moves. Returns the same placement object for every widget that did not move.
export function pushDown(items: readonly PlacedGrid[], first: readonly string[] = []): Map<string, GridPlacement> {
  const order = items.map((item, index) => ({ ...item, index }));
  const priority = new Map(first.map((id, i) => [id, i]));
  order.sort((a, b) => {
    const pa = priority.get(a.id);
    const pb = priority.get(b.id);
    if (pa !== undefined || pb !== undefined) return (pa ?? Infinity) - (pb ?? Infinity);
    return a.placement.row - b.placement.row || a.placement.col - b.placement.col || a.index - b.index;
  });
  const placed: GridPlacement[] = [];
  const out = new Map<string, GridPlacement>();
  for (const item of order) {
    let p = item.placement;
    for (;;) {
      const hits = placed.filter((q) => gridFootprintsOverlap(p, q));
      if (hits.length === 0) break;
      p = { ...p, row: Math.max(...hits.map((q) => q.row + q.rows)) };
    }
    placed.push(p);
    out.set(item.id, p);
  }
  return out;
}

// compactUp is pushDown followed by gravity: after the collisions are resolved, every widget except
// the `first` ones (which stay exactly where they were put) slides UP one row at a time while the
// row above is free, stopping at the first footprint in its way, so nothing ever jumps over
// another widget and the reading order is the order it had. Widgets are slid in reading order of
// their pushed positions, so each one settles against the ones above it. This is what an Android
// launcher does with the icons below one you pick up: the gap closes. Deterministic: the same
// input gives the same output. Returns the same placement object for every widget that did not move.
export function compactUp(items: readonly PlacedGrid[], first: readonly string[] = []): Map<string, GridPlacement> {
  const pushed = pushDown(items, first);
  const fixed = new Set(first);
  const order = items
    .map((item, index) => ({ id: item.id, index, p: pushed.get(item.id)! }))
    .filter((x) => !fixed.has(x.id))
    .sort((a, b) => a.p.row - b.p.row || a.p.col - b.p.col || a.index - b.index);
  const out = new Map<string, GridPlacement>();
  const placed: GridPlacement[] = [];
  for (const id of first) {
    const p = pushed.get(id);
    if (p) {
      out.set(id, p);
      placed.push(p);
    }
  }
  for (const { id, p } of order) {
    let q = p;
    while (q.row > 0) {
      const above: GridPlacement = { ...q, row: q.row - 1 };
      if (placed.some((o) => gridFootprintsOverlap(above, o))) break;
      q = above;
    }
    const settled = q.row === p.row ? p : q;
    out.set(id, settled);
    placed.push(settled);
  }
  return out;
}

// firstFreeSlot is the first position, scanning rows top to bottom and columns left to right, at
// which a cols x rows footprint fits inside `columns` without touching any of `occupied`. A
// footprint wider than the grid is placed at column 0 (it overruns; the lint reports it).
export function firstFreeSlot(
  occupied: readonly GridPlacement[],
  cols: number,
  rows: number,
  columns: number,
): { col: number; row: number } {
  const lastCol = Math.max(0, columns - cols);
  const bottom = occupied.reduce((m, p) => Math.max(m, p.row + p.rows), 0);
  for (let row = 0; row <= bottom; row++) {
    for (let col = 0; col <= lastCol; col++) {
      const probe: GridPlacement = { mode: 'grid', col, row, cols, rows };
      if (!occupied.some((q) => gridFootprintsOverlap(probe, q))) return { col, row };
    }
  }
  return { col: 0, row: bottom };
}

// ---- Rectangles --------------------------------------------------------------------

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

// The grid's rule in px: n cells cover n*cell + (n-1)*gutter.
export function spanPx(n: number, cell: number, gutter: number): number {
  return n * cell + (n - 1) * gutter;
}

// Where a grid footprint lands, relative to the grid's top-left corner (inside the padding).
export function gridRect(p: GridPlacement, cell: number, gutter: Gutter): Rect {
  return {
    x: p.col * (cell + gutter.x),
    y: p.row * (cell + gutter.y),
    w: spanPx(p.cols, cell, gutter.x),
    h: spanPx(p.rows, cell, gutter.y),
  };
}

// The fraction across (fx) and down (fy) a box an anchor point sits at.
export function anchorFraction(anchor: PlacementAnchor): { fx: number; fy: number } {
  const fx = anchor.endsWith('left') ? 0 : anchor.endsWith('right') ? 1 : 0.5;
  const fy = anchor.startsWith('top') ? 0 : anchor.startsWith('bottom') ? 1 : 0.5;
  return { fx, fy };
}

// Where a relative widget lands, given its host's box: the host's `anchor` point, minus this
// widget's `self` point, plus the offset in cell pitches.
export function relativeRect(p: RelativePlacement, host: Rect, cell: number, gutter: Gutter): Rect {
  const w = 'fill' in p.size ? host.w : spanPx(p.size.cols, cell, gutter.x);
  const h = 'fill' in p.size ? host.h : spanPx(p.size.rows, cell, gutter.y);
  const a = anchorFraction(p.anchor);
  const s = anchorFraction(p.self ?? p.anchor);
  return {
    x: host.x + a.fx * host.w - s.fx * w + (p.offset?.x ?? 0) * (cell + gutter.x),
    y: host.y + a.fy * host.h - s.fy * h + (p.offset?.y ?? 0) * (cell + gutter.y),
    w,
    h,
  };
}

// The box of every widget at a breakpoint, for a cell size: grid footprints first, then each
// relative widget against its host (a host that is itself relative is resolved first; the parser
// guarantees the chain is at most one level deep and that every host exists). A widget whose
// host is missing (an unparsed definition) is left out rather than guessed.
export function layoutRects(
  widgets: readonly WidgetInstance[],
  breakpoint: string,
  cell: number,
  gutter: Gutter,
): Map<string, Rect> {
  const rects = new Map<string, Rect>();
  const relatives: { id: string; p: RelativePlacement }[] = [];
  for (const w of widgets) {
    const p = resolvePlacement(w.layout, breakpoint);
    if (p.mode === 'grid') rects.set(w.id, gridRect(p, cell, gutter));
    else relatives.push({ id: w.id, p });
  }
  // Two passes reach one level of relative-on-relative.
  for (let pass = 0; pass < 2; pass++) {
    for (const { id, p } of relatives) {
      const host = rects.get(p.host);
      if (host && !rects.has(id)) rects.set(id, relativeRect(p, host, cell, gutter));
    }
  }
  return rects;
}

// ---- Frames ------------------------------------------------------------------------

// What each variant looks like before any field is set.
const FRAME_PRESETS: Readonly<Record<ResolvedFrame['variant'], ResolvedFrame>> = {
  card: {
    variant: 'card',
    border: { width: 1, radius: 'md', color: 'border' },
    padding: 'space-3',
    background: 'card',
    header: 'auto',
  },
  flat: {
    variant: 'flat',
    border: { width: 0, radius: 'md', color: 'border' },
    padding: 'space-3',
    background: 'card',
    header: 'auto',
  },
  none: {
    variant: 'none',
    border: { width: 0, radius: 'none', color: 'border' },
    padding: 'space-0',
    background: 'transparent',
    header: 'hide',
  },
};

// resolveFrame decides every frame field for one widget. The canvas default and the widget's own
// frame merge field by field (a widget that sets only `border.color` keeps the canvas's border
// width); the merged variant picks the preset the remaining fields fall back to.
export function resolveFrame(canvasFrame: WidgetFrameSpec | undefined, widgetFrame: WidgetFrameSpec | undefined): ResolvedFrame {
  const c = canvasFrame ?? {};
  const w = widgetFrame ?? {};
  const variant = w.variant ?? c.variant ?? 'card';
  const preset = FRAME_PRESETS[variant];
  return {
    variant,
    border: { ...preset.border, ...c.border, ...w.border },
    padding: w.padding ?? c.padding ?? preset.padding,
    background: w.background ?? c.background ?? preset.background,
    header: w.header ?? c.header ?? preset.header,
  };
}

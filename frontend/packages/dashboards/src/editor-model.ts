// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure state transforms for the canvas editor. Kept free of React/DOM so the
// editing logic (move, resize, delete, retitle, add) is unit-testable; the editor UI
// (the console) wires these to its drag/resize and the save mutation. Lives here —
// next to parse/serialize — because it is the definition's edit lifecycle, not
// app-specific glue (ADR-039 authoring in the console).
//
// The editor edits the 'base' placements only; per-breakpoint editing is deferred. Every
// transform keeps the one invariant the parser enforces: no two grid footprints overlap. A move,
// a resize, an add or a narrower grid that would collide pushes the widgets in the way DOWN
// (pushDown), so the editor can never produce a definition that fails to load.

import { BASE_BREAKPOINT, generateWidgetId } from './definition';
import {
  breakpointColumns,
  firstFreeSlot,
  layoutBreakpoints,
  pushDown,
  resolvePlacement,
  spanPx,
  type Gutter,
  type PlacedGrid,
} from './grid';
import { DEFAULT_CANVAS_DENSITY } from './types';
import type {
  CanvasDensity,
  CanvasGrid,
  CanvasSizing,
  DashboardDefinition,
  GridPlacement,
  Placement,
  ResolvedWidgetInstance,
  WidgetInstance,
} from './types';
import { FALLBACK_DEFAULT_SPAN, type WidgetDescriptor } from './widget-descriptor';

// The base placement a widget is edited by.
export function basePlacement(widget: WidgetInstance): Placement {
  return widget.layout[BASE_BREAKPOINT];
}

// resolveCollisions pushes apart the grid footprints that overlap at one breakpoint, the `first`
// widgets keeping their place. At a non-base breakpoint a widget that has no entry of its own
// shows its base placement; if that one has to move, the move is written as an entry for the
// breakpoint, so the base layout is not disturbed by an edit to another one.
function resolveCollisions(def: DashboardDefinition, breakpoint: string, first: readonly string[]): DashboardDefinition {
  const placed: PlacedGrid[] = [];
  for (const w of def.widgets) {
    const p = resolvePlacement(w.layout, breakpoint);
    if (p.mode === 'grid') placed.push({ id: w.id, placement: p });
  }
  const resolved = pushDown(placed, first);
  let changed = false;
  const widgets = def.widgets.map((w) => {
    const before = resolvePlacement(w.layout, breakpoint);
    const after = resolved.get(w.id);
    if (!after || after === before) return w;
    changed = true;
    return { ...w, layout: { ...w.layout, [breakpoint]: after } };
  });
  return changed ? { ...def, widgets } : def;
}

// setWidgetPlacement replaces a widget's base placement. A grid placement pushes whatever it now
// covers down out of its way.
export function setWidgetPlacement(def: DashboardDefinition, id: string, placement: Placement): DashboardDefinition {
  const next = {
    ...def,
    widgets: def.widgets.map((w) => (w.id === id ? { ...w, layout: { ...w.layout, [BASE_BREAKPOINT]: placement } } : w)),
  };
  return placement.mode === 'grid' ? resolveCollisions(next, BASE_BREAKPOINT, [id]) : next;
}

// deleteWidget removes a widget by id. A widget that was placed relative to it would be left
// with no host, which the parser refuses, so each one is put back on the grid instead, at its
// own size (a `fill` one at its host's), in the first free slot.
export function deleteWidget(def: DashboardDefinition, id: string): DashboardDefinition {
  const gone = def.widgets.find((w) => w.id === id);
  let next: DashboardDefinition = { ...def, widgets: def.widgets.filter((w) => w.id !== id) };
  if (!gone) return next;
  for (const bp of layoutBreakpoints(def.canvas.breakpoints, def.widgets)) {
    const hostPlacement = resolvePlacement(gone.layout, bp);
    const columns = breakpointColumns(def.canvas, bp);
    for (const w of next.widgets) {
      const p = resolvePlacement(w.layout, bp);
      if (p.mode !== 'relative' || p.host !== id) continue;
      const size = 'fill' in p.size ? hostSpan(hostPlacement, next, bp) : p.size;
      const occupied = next.widgets
        .map((o) => resolvePlacement(o.layout, bp))
        .filter((o): o is GridPlacement => o.mode === 'grid');
      const cols = Math.min(size.cols, columns);
      const slot = firstFreeSlot(occupied, cols, size.rows, columns);
      const grid: GridPlacement = { mode: 'grid', col: slot.col, row: slot.row, cols, rows: size.rows };
      next = { ...next, widgets: next.widgets.map((o) => (o.id === w.id ? { ...o, layout: { ...o.layout, [bp]: grid } } : o)) };
    }
  }
  return next;
}

// The span a (possibly relative) placement covers, for re-homing a `fill` widget.
function hostSpan(p: Placement, def: DashboardDefinition, bp: string): { cols: number; rows: number } {
  if (p.mode === 'grid') return { cols: p.cols, rows: p.rows };
  if (!('fill' in p.size)) return p.size;
  const host = def.widgets.find((w) => w.id === p.host);
  return host ? hostSpan(resolvePlacement(host.layout, bp), def, bp) : { cols: 1, rows: 1 };
}

// setTitle updates the dashboard title.
export function setTitle(def: DashboardDefinition, title: string): DashboardDefinition {
  return { ...def, title };
}

// setCanvasGrid merges a partial grid change into the canvas, returning a new definition. It
// owns the grid invariants (a live edit never round-trips through parse): columns floors to an
// integer >= 1 and minCellSize to >= 1. When columns SHRINKS, every placement in the grid's own
// column count is clamped back inside it, and anything the clamp makes collide is pushed down.
export function setCanvasGrid(def: DashboardDefinition, patch: Partial<CanvasGrid>): DashboardDefinition {
  const grid = { ...def.canvas.grid, ...patch };
  if (patch.columns !== undefined) grid.columns = Math.max(1, Math.round(patch.columns));
  if (patch.minCellSize !== undefined) grid.minCellSize = Math.max(1, patch.minCellSize);
  let next: DashboardDefinition = { ...def, canvas: { ...def.canvas, grid } };
  if (grid.columns >= def.canvas.grid.columns) return next;
  // Breakpoints that declare their own column count are unaffected.
  const affected = layoutBreakpoints(def.canvas.breakpoints, def.widgets).filter(
    (bp) => breakpointColumns(next.canvas, bp) === grid.columns,
  );
  for (const bp of affected) {
    next = {
      ...next,
      widgets: next.widgets.map((w) => {
        const p = resolvePlacement(w.layout, bp);
        if (p.mode !== 'grid' || p.col + p.cols <= grid.columns) return w;
        const col = Math.min(p.col, grid.columns - 1);
        return { ...w, layout: { ...w.layout, [bp]: { ...p, col, cols: Math.min(p.cols, grid.columns - col) } } };
      }),
    };
    next = resolveCollisions(next, bp, []);
  }
  return next;
}

// setCanvasSizing replaces the container-sizing knob (fill / fixed width / fixed height).
export function setCanvasSizing(def: DashboardDefinition, sizing: CanvasSizing): DashboardDefinition {
  return { ...def, canvas: { ...def.canvas, sizing } };
}

// setCanvasDensity sets the authored density. `comfortable` (the default) is stored as absent so
// a board nobody densified serializes exactly as before.
export function setCanvasDensity(def: DashboardDefinition, density: CanvasDensity): DashboardDefinition {
  const { density: _prev, ...rest } = def.canvas;
  return { ...def, canvas: density === DEFAULT_CANVAS_DENSITY ? rest : { ...rest, density } };
}

// updateWidget replaces the widget with the matching id, returning a new definition.
export function updateWidget(def: DashboardDefinition, id: string, next: WidgetInstance): DashboardDefinition {
  return { ...def, widgets: def.widgets.map((w) => (w.id === id ? next : w)) };
}

// addWidget appends a new widget of the described kind at the descriptor's default span (no
// wider than the grid), in the first free slot scanning rows top to bottom, so it never lands on
// top of another widget. Datasource left undefined. Its starting options are the descriptor's own
// `defaults` — the widget's declaration, not a table in the editor keyed by widget id. Returns
// the new definition and the new widget's id so the caller can select it.
//
// The new widget records the registration it was authored against: its release, its settings
// version and its SDK generation (`widget: { version, optionsVersion, sdkApiVersion }`). For
// an extension that record is what lets the next load match it to the installed package at
// all — a registered extension stored without it is incomplete and parses to a placeholder.
// `optionsVersion` and `sdkApiVersion` are the compatibility key (with the id), so both are
// recorded: a reference without the SDK generation could never be refused by a host that has
// moved to a newer one. `version` is the provenance the editor's version notice compares.
export function addWidget(
  def: DashboardDefinition,
  descriptor: Pick<WidgetDescriptor, 'id' | 'defaults' | 'version' | 'optionsVersion' | 'sdkApiVersion' | 'layout'>,
): { definition: DashboardDefinition; id: string } {
  const id = generateWidgetId();
  const span = descriptor.layout?.defaultSpan ?? FALLBACK_DEFAULT_SPAN;
  const columns = def.canvas.grid.columns;
  const cols = Math.min(span.cols, columns);
  const occupied = def.widgets.map(basePlacement).filter((p): p is GridPlacement => p.mode === 'grid');
  const slot = firstFreeSlot(occupied, cols, span.rows, columns);
  const placement: GridPlacement = { mode: 'grid', col: slot.col, row: slot.row, cols, rows: span.rows };
  const common = {
    id,
    widget: {
      version: descriptor.version,
      optionsVersion: descriptor.optionsVersion,
      sdkApiVersion: descriptor.sdkApiVersion,
    },
    layout: { [BASE_BREAKPOINT]: placement },
    options: { ...descriptor.defaults },
  };
  // The id came from a registered descriptor, so it names either a built-in or a namespaced
  // extension; which one is the registry's knowledge, and this transform does not branch on it.
  const widget = { ...common, type: descriptor.id } as ResolvedWidgetInstance;
  return { definition: { ...def, widgets: [...def.widgets, widget] }, id };
}

// A pixel rectangle a drag/resize reports.
export interface PixelRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

// The rendered geometry of one grid, in pixels: the live (square) cell and the gutters. What the
// canvas measures so it can map placements to and from the pixel rects its drag/resize works in.
export interface GridGeometry {
  cell: number;
  gutter: Gutter;
}

// gridPlacementToPx renders a grid placement to a pixel rect, relative to the grid's top-left
// corner. The stride between cell starts is (cell + gutter); a span of N covers N cells and the
// N-1 gutters between them.
export function gridPlacementToPx(p: GridPlacement, geom: GridGeometry): PixelRect {
  return {
    x: p.col * (geom.cell + geom.gutter.x),
    y: p.row * (geom.cell + geom.gutter.y),
    w: spanPx(p.cols, geom.cell, geom.gutter.x),
    h: spanPx(p.rows, geom.cell, geom.gutter.y),
  };
}

// pxToGridPlacement snaps a pixel rect back to a grid placement. Clamped so a widget can't leave
// the canvas (col,row >= 0) or vanish (spans >= 1); when `columns` is given, also clamped to the
// RIGHT edge (col <= columns-1, cols <= columns-col) so a boundary drag can't commit a footprint
// that overruns the grid.
export function pxToGridPlacement(px: PixelRect, geom: GridGeometry, columns?: number): GridPlacement {
  const colStride = Math.max(1, geom.cell + geom.gutter.x);
  const rowStride = Math.max(1, geom.cell + geom.gutter.y);
  let col = Math.max(0, Math.round(px.x / colStride));
  let cols = Math.max(1, Math.round((px.w + geom.gutter.x) / colStride));
  if (columns !== undefined) {
    col = Math.min(col, columns - 1);
    cols = Math.min(cols, columns - col);
  }
  return {
    mode: 'grid',
    col,
    row: Math.max(0, Math.round(px.y / rowStride)),
    cols,
    rows: Math.max(1, Math.round((px.h + geom.gutter.y) / rowStride)),
  };
}

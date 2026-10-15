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
  gutterPx,
  layoutBreakpoints,
  pushAside,
  pushDown,
  relativeRect,
  resolvePlacement,
  layoutRects,
  spanPx,
  type Gutter,
  type PlacedGrid,
} from './grid';
import { lintLayout, RELATIVE_PROBE_CELLS, type SpanLimits } from './layout-lint';
import { DEFAULT_CANVAS_DENSITY } from './types';
import type {
  CanvasDensity,
  CanvasGrid,
  CanvasSizing,
  DashboardDefinition,
  GridPlacement,
  Placement,
  PlacementAnchor,
  RelativePlacement,
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
function resolveCollisions(
  def: DashboardDefinition,
  breakpoint: string,
  first: readonly string[],
  aside = false,
): DashboardDefinition {
  const placed: PlacedGrid[] = [];
  for (const w of def.widgets) {
    const p = resolvePlacement(w.layout, breakpoint);
    if (p.mode === 'grid') placed.push({ id: w.id, placement: p });
  }
  const resolved = aside ? pushAside(placed, first) : pushDown(placed, first);
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

// resolveEverywhere resolves collisions at every breakpoint the parser checks, base first. An
// edit to a base placement changes what every breakpoint without its own entry for that widget
// shows, so a move that is clear at base can still land on another widget's override at a
// breakpoint (and the parser refuses an overlap at any of them).
function resolveEverywhere(def: DashboardDefinition, first: readonly string[], aside = false): DashboardDefinition {
  let next = def;
  // The swap is an edit of the base layout: a breakpoint arrangement the edit reaches only through
  // the inherited base placement is pushed apart, never rearranged.
  for (const bp of layoutBreakpoints(def.canvas.breakpoints, def.widgets))
    next = resolveCollisions(next, bp, first, aside && bp === BASE_BREAKPOINT);
  return next;
}

// How an edit settles the widgets around the one it changed. Either way the edited widget lands
// exactly where it was put, only the widgets its new footprint covers move, and every other widget
// keeps its place, so a gap the author left (to align or to group) is never closed by an
// unrelated edit. By default a covered widget is pushed DOWN out of the way. With `nearest` it
// settles at the free row nearest the one it had, up or down (pushAside), so dropping a widget
// onto a neighbour swaps the two instead of shoving the neighbour further down the board: what an
// Android launcher does. The editor's pointer and keyboard edits use `nearest`.
export interface EditOptions {
  nearest?: boolean;
}

// setWidgetPlacement replaces a widget's base placement. A grid placement pushes whatever it now
// covers down out of its way, at base and at every breakpoint that shows it.
export function setWidgetPlacement(
  def: DashboardDefinition,
  id: string,
  placement: Placement,
  options: EditOptions = {},
): DashboardDefinition {
  const next = {
    ...def,
    widgets: def.widgets.map((w) => (w.id === id ? { ...w, layout: { ...w.layout, [BASE_BREAKPOINT]: placement } } : w)),
  };
  return placement.mode === 'grid' ? resolveEverywhere(next, [id], options.nearest) : next;
}

// clampSpan holds a span inside a widget's declared limits and inside the columns that remain to
// the right of `col`. Where the two disagree the grid wins (a widget never leaves the canvas), so
// a minSpan wider than what is left to the right of `col` yields to the edge: the caller moves
// `col` back first when it can (see resizeWidget).
export function clampSpan(
  span: { cols: number; rows: number },
  limits: SpanLimits | undefined,
  room: number,
): { cols: number; rows: number } {
  const cols = Math.min(room, Math.max(limits?.minSpan?.cols ?? 1, Math.min(limits?.maxSpan?.cols ?? Infinity, span.cols)));
  const rows = Math.max(limits?.minSpan?.rows ?? 1, Math.min(limits?.maxSpan?.rows ?? Infinity, span.rows));
  return { cols: Math.max(1, Math.round(cols)), rows: Math.max(1, Math.round(rows)) };
}

// moveWidget drags a grid widget so its top-left cell is (col, row): the span is kept, the
// footprint is held inside the columns and above row 0, and whatever it now covers is pushed
// down out of its way (setWidgetPlacement). A relative widget has no cell to drag, so it is
// returned unchanged.
export function moveWidget(
  def: DashboardDefinition,
  id: string,
  to: { col: number; row: number },
  columns: number = def.canvas.grid.columns,
  options: EditOptions = {},
): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  if (!w) return def;
  const p = basePlacement(w);
  if (p.mode !== 'grid') return def;
  const col = Math.min(Math.max(0, Math.round(to.col)), Math.max(0, columns - p.cols));
  const row = Math.max(0, Math.round(to.row));
  if (col === p.col && row === p.row) return def;
  return setWidgetPlacement(def, id, { ...p, col, row }, options);
}

// resizeWidget resizes a grid widget to `target` (a footprint, e.g. a snapped drag of a handle),
// stopping at the widget's minSpan and maxSpan and at the canvas edge. `fromLeft` / `fromTop`
// say which edge the handle moved: that edge follows the pointer and the opposite one stays put,
// so a widget clamped at its minimum does not slide away from the edge it was held by.
// Neighbours are pushed down.
export function resizeWidget(
  def: DashboardDefinition,
  id: string,
  target: { col: number; row: number; cols: number; rows: number },
  limits: SpanLimits | undefined,
  edges: { fromLeft?: boolean; fromTop?: boolean } = {},
  columns: number = def.canvas.grid.columns,
  options: EditOptions = {},
): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  if (!w) return def;
  const p = basePlacement(w);
  if (p.mode !== 'grid') return def;
  const right = target.col + target.cols;
  const bottom = target.row + target.rows;
  const room = edges.fromLeft ? Math.max(1, right) : Math.max(1, columns - Math.max(0, target.col));
  const { cols, rows } = clampSpan(target, limits, Math.min(columns, room));
  const col = edges.fromLeft ? Math.max(0, right - cols) : Math.max(0, target.col);
  const row = edges.fromTop ? Math.max(0, bottom - rows) : Math.max(0, target.row);
  if (col === p.col && row === p.row && cols === p.cols && rows === p.rows) return def;
  return setWidgetPlacement(def, id, { mode: 'grid', col, row, cols, rows }, options);
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
  // A slot free at one breakpoint can be taken at another that inherits it.
  return resolveEverywhere(next, []);
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
  // The slot is free at base; a breakpoint override elsewhere may cover it, so resolve there too
  // (the new widget keeps its place, and whatever it covers moves).
  return { definition: resolveEverywhere({ ...def, widgets: [...def.widgets, widget] }, [id]), id };
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

// ---- Size readout -------------------------------------------------------------------

// formatSpan is the "2×3" readout shown while a widget is resized, and the footprint the add-widget
// picker lists: columns by rows, with the multiplication sign.
export function formatSpan(span: { cols: number; rows: number }): string {
  return `${span.cols}×${span.rows}`;
}

// ---- Placement mode -----------------------------------------------------------------

// The step a relative widget's offset moves in: whole cells, or quarter cells for fine placement.
export const OFFSET_STEP = 1;
export const OFFSET_STEP_FINE = 0.25;

// snapOffset rounds an offset (in cell pitches) to the step: whole cells, or quarter cells with
// `fine` (the Shift key). Never returns -0.
export function snapOffset(value: number, fine = false): number {
  const step = fine ? OFFSET_STEP_FINE : OFFSET_STEP;
  const snapped = Math.round(value / step) * step;
  return snapped === 0 ? 0 : snapped;
}

// What a relative widget's box is measured in, to keep it on the canvas as it is dragged.
export interface RelativeBounds {
  cell: number;
  gutter: Gutter;
  columns: number;
}

function replaceBase(def: DashboardDefinition, id: string, placement: Placement): DashboardDefinition {
  return {
    ...def,
    widgets: def.widgets.map((w) => (w.id === id ? { ...w, layout: { ...w.layout, [BASE_BREAKPOINT]: placement } } : w)),
  };
}

// setRelativeOffset moves a relative widget by setting its offset, snapped to whole cells or, with
// `fine`, quarter cells. When `bounds` is given the offset is then pulled back, in whole steps, until
// the box is inside the canvas at that cell size (the editor cannot produce an off-canvas widget by
// dragging; the lint still checks the extremes of cell size). A grid widget has no offset, so it is
// returned unchanged. A zero offset is stored as absent.
export function setRelativeOffset(
  def: DashboardDefinition,
  id: string,
  offset: { x: number; y: number },
  options: { fine?: boolean; bounds?: RelativeBounds } = {},
): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  if (!w) return def;
  const p = basePlacement(w);
  if (p.mode !== 'relative') return def;
  const step = options.fine ? OFFSET_STEP_FINE : OFFSET_STEP;
  let x = snapOffset(offset.x, options.fine);
  let y = snapOffset(offset.y, options.fine);
  const { bounds } = options;
  if (bounds) {
    const host = layoutRects(def.widgets, BASE_BREAKPOINT, bounds.cell, bounds.gutter).get(p.host);
    if (host) {
      const r = relativeRect({ ...p, offset: { x, y } }, host, bounds.cell, bounds.gutter);
      const px = (bounds.cell + bounds.gutter.x) * step;
      const py = (bounds.cell + bounds.gutter.y) * step;
      const width = spanPx(bounds.columns, bounds.cell, bounds.gutter.x);
      if (r.x < 0) x += Math.ceil(-r.x / px) * step;
      else if (r.x + r.w > width) x -= Math.ceil((r.x + r.w - width) / px) * step;
      if (r.y < 0) y += Math.ceil(-r.y / py) * step;
    }
  }
  const next: RelativePlacement = { ...p };
  if (x === 0 && y === 0) delete next.offset;
  else next.offset = { x, y };
  if ((next.offset?.x ?? 0) === (p.offset?.x ?? 0) && (next.offset?.y ?? 0) === (p.offset?.y ?? 0)) return def;
  return replaceBase(def, id, next);
}

// A widget can host another when it is a grid widget at every breakpoint it is shown at (the
// parser allows one level of chaining only, and a host that is itself relative could close a
// chain), and is not the widget being placed.
function canHost(def: DashboardDefinition, host: WidgetInstance | undefined, id: string): host is WidgetInstance {
  if (!host || host.id === id) return false;
  return layoutBreakpoints(def.canvas.breakpoints, def.widgets).every(
    (bp) => resolvePlacement(host.layout, bp).mode === 'grid',
  );
}

// canHostRelative says whether `hostId` is a valid host for the widget `id` (what placeRelative
// would accept), so an editor can offer only the widgets that will work.
export function canHostRelative(def: DashboardDefinition, id: string, hostId: string): boolean {
  return canHost(def, def.widgets.find((x) => x.id === hostId), id);
}

// placeRelative puts a widget against a HOST: it keeps its current size, sits at the host's
// `anchor` point (the same point of itself, so a badge at `top-right` sits in that corner), and
// leaves the grid, so it no longer pushes anything. Placing an already-relative widget re-homes
// it (a new host and anchor, the size kept, the offset dropped). A host that is missing, is the
// widget itself, or is not a grid widget at every breakpoint is refused: the definition comes back
// unchanged. Only the base placement is edited; a breakpoint with its own entry for the widget
// keeps it.
export function placeRelative(
  def: DashboardDefinition,
  id: string,
  hostId: string,
  anchor: PlacementAnchor,
): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  if (!w) return def;
  if (!canHost(def, def.widgets.find((x) => x.id === hostId), id)) return def;
  const p = basePlacement(w);
  const size = p.mode === 'grid' ? { cols: p.cols, rows: p.rows } : p.size;
  return replaceBase(def, id, { mode: 'relative', host: hostId, anchor, size });
}

// setRelativeAnchor changes the host point a relative widget hangs from, keeping the rest.
export function setRelativeAnchor(def: DashboardDefinition, id: string, anchor: PlacementAnchor): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  const p = w && basePlacement(w);
  if (!p || p.mode !== 'relative' || p.anchor === anchor) return def;
  return replaceBase(def, id, { ...p, anchor });
}

// placeOnGrid is the way back: a relative widget takes its own size (a `fill` one its host's) in
// the first free slot of the grid, pushing nothing. A grid widget is returned unchanged.
export function placeOnGrid(def: DashboardDefinition, id: string): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  if (!w) return def;
  const p = basePlacement(w);
  if (p.mode !== 'relative') return def;
  const columns = def.canvas.grid.columns;
  const size = 'fill' in p.size ? hostSpan(p, def, BASE_BREAKPOINT) : p.size;
  const cols = Math.min(size.cols, columns);
  const occupied = def.widgets
    .filter((o) => o.id !== id)
    .map(basePlacement)
    .filter((o): o is GridPlacement => o.mode === 'grid');
  const slot = firstFreeSlot(occupied, cols, size.rows, columns);
  const grid: GridPlacement = { mode: 'grid', col: slot.col, row: slot.row, cols, rows: size.rows };
  return resolveEverywhere(replaceBase(def, id, grid), [id]);
}

// ---- Fit to limits ------------------------------------------------------------------

// fitToLimits is the one-click repair for a board whose widgets are outside what they declare they
// can be (a converted legacy board, an import, a definition the API wrote), and the way out of
// every finding that blocks a publish:
//   - every placement, at every breakpoint, has its span clamped into the widget's limits and into
//     the columns, and a footprint that overran the right edge is pulled back inside it;
//   - a relative widget that `fill`s a host its own limits do not allow is given an explicit size,
//     the host's span clamped into those limits;
//   - a relative widget off the canvas is moved back onto it, keeping as much of its offset as the
//     canvas allows, at every cell size the lint measures (fitRelativeOnCanvas). At a breakpoint
//     where it shows its base placement, the fix is written as an entry for that breakpoint, so
//     base is not disturbed;
//   - any collision a grown span causes is then pushed apart through the model. Nothing is pulled
//     up into a gap: a board the author spaced on purpose keeps its spacing, and a widget that
//     shrank leaves the room it gave up.
// Returns the same object when nothing needed fixing, so a caller can tell "already fits" from
// "fixed".
export function fitToLimits(
  def: DashboardDefinition,
  spanLimitsOf: (widget: WidgetInstance) => SpanLimits | undefined,
): DashboardDefinition {
  let changed = false;
  const columnsAt = (bp: string) => breakpointColumns(def.canvas, bp);
  const clamped: DashboardDefinition = {
    ...def,
    widgets: def.widgets.map((w) => {
      const limits = spanLimitsOf(w);
      const layout: Record<string, Placement> = {};
      let touched = false;
      for (const [bp, p] of Object.entries(w.layout)) {
        const columns = columnsAt(bp);
        let q: Placement = p;
        if (p.mode === 'grid') {
          const span = clampSpan(p, limits, columns);
          const col = Math.max(0, Math.min(p.col, columns - span.cols));
          if (span.cols !== p.cols || span.rows !== p.rows || col !== p.col) q = { ...p, ...span, col };
        } else if (!('fill' in p.size)) {
          const size = clampSpan(p.size, limits, columns);
          if (size.cols !== p.size.cols || size.rows !== p.size.rows) q = { ...p, size };
        }
        if (q !== p) touched = true;
        layout[bp] = q;
      }
      if (!touched) return w;
      changed = true;
      return { ...w, layout };
    }),
  };
  // A `fill` widget's span is its host's, read once the hosts are clamped.
  let next: DashboardDefinition = {
    ...clamped,
    widgets: clamped.widgets.map((w) => {
      const limits = spanLimitsOf(w);
      if (!limits) return w;
      let layout: WidgetInstance['layout'] | undefined;
      for (const [bp, p] of Object.entries(w.layout)) {
        if (p.mode !== 'relative' || !('fill' in p.size)) continue;
        const span = hostSpan(p, clamped, bp);
        const size = clampSpan(span, limits, columnsAt(bp));
        if (size.cols === span.cols && size.rows === span.rows) continue;
        layout = { ...(layout ?? w.layout), [bp]: { ...p, size } };
      }
      if (!layout) return w;
      changed = true;
      return { ...w, layout };
    }),
  };
  // A relative widget off the canvas is moved back onto it. Its host may itself be a relative
  // widget being moved, so a second pass sees the host where it ends up.
  for (let pass = 0; pass < 3; pass++) {
    const off = lintLayout(next, { columns: def.canvas.grid.columns, spanLimitsOf }).filter(
      (f) => f.code === 'relative-off-canvas',
    );
    if (off.length === 0) break;
    for (const f of off) {
      const fixed = fitRelativeOnCanvas(next, f.widgetId, f.breakpoint ?? BASE_BREAKPOINT);
      if (fixed !== next) changed = true;
      next = fixed;
    }
  }
  if (!changed) return def;
  return resolveEverywhere(next, []);
}

// fitRelativeOnCanvas moves the relative widget `id`, as it is shown at `bp`, back inside the
// canvas at every cell size the lint measures. A box's position is a straight line in the cell
// size and in its offset, so the offsets that keep it inside are, per axis, a range at each probed
// cell; the one kept is the widget's own offset clamped into the intersection of those ranges, in
// quarter cells. When no quarter-cell offset fits every cell size (a box as wide as the canvas
// whose `self` point differs from its anchor), the widget is put on the grid at that breakpoint,
// in the first free slot, at its own size: the one placement that is always on the canvas.
function fitRelativeOnCanvas(def: DashboardDefinition, id: string, bp: string): DashboardDefinition {
  const w = def.widgets.find((x) => x.id === id);
  if (!w) return def;
  const p = resolvePlacement(w.layout, bp);
  if (p.mode !== 'relative') return def;
  const columns = breakpointColumns(def.canvas, bp);
  const gutter = gutterPx(def.canvas.grid);
  const { offset, ...bare } = p;
  let loX = -Infinity;
  let hiX = Infinity;
  let loY = -Infinity;
  for (const cell of RELATIVE_PROBE_CELLS(def.canvas.grid.minCellSize)) {
    const host = layoutRects(def.widgets, bp, cell, gutter).get(p.host);
    if (!host) return def;
    const r = relativeRect(bare, host, cell, gutter);
    const width = spanPx(columns, cell, gutter.x);
    loX = Math.max(loX, -r.x / (cell + gutter.x));
    hiX = Math.min(hiX, (width - r.x - r.w) / (cell + gutter.x));
    loY = Math.max(loY, -r.y / (cell + gutter.y));
  }
  const step = OFFSET_STEP_FINE;
  const up = (v: number) => Math.ceil(v / step - 1e-9) * step;
  const down = (v: number) => Math.floor(v / step + 1e-9) * step;
  let placement: Placement;
  if (up(loX) <= down(hiX)) {
    const x = Math.min(Math.max(offset?.x ?? 0, up(loX)), down(hiX)) || 0;
    const y = Math.max(offset?.y ?? 0, up(loY)) || 0;
    placement = x === 0 && y === 0 ? bare : { ...bare, offset: { x, y } };
  } else {
    const size = 'fill' in p.size ? hostSpan(p, def, bp) : p.size;
    const cols = Math.min(size.cols, columns);
    const occupied = def.widgets
      .filter((o) => o.id !== id)
      .map((o) => resolvePlacement(o.layout, bp))
      .filter((o): o is GridPlacement => o.mode === 'grid');
    const slot = firstFreeSlot(occupied, cols, size.rows, columns);
    placement = { mode: 'grid', col: slot.col, row: slot.row, cols, rows: size.rows };
  }
  return {
    ...def,
    widgets: def.widgets.map((x) => (x.id === id ? { ...x, layout: { ...x.layout, [bp]: placement } } : x)),
  };
}

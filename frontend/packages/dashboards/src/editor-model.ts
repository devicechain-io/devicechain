// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure state transforms for the canvas editor. Kept free of React/DOM so the
// editing logic (move, resize, delete, reorder, retitle, add) is unit-testable;
// the editor UI (console + the reference /dash app) wires these to react-rnd and
// the save mutation. Lives here — next to parse/serialize — because it is the
// definition's edit lifecycle, not app-specific glue (ADR-039 authoring in the
// console).
//
// The editor edits the 'base' breakpoint boxes only; per-breakpoint responsive
// editing is deferred. Boxes are stored as CSS-Grid span placements (col/colSpan/
// row/rowSpan); the canvas measures its rendered column width and maps boxes to/from
// pixels (gridBoxToPx / pxToGridBox) so react-rnd drag+resize snap to real grid lines.

import { BASE_BREAKPOINT, generateWidgetId } from './definition';
import type {
  CanvasGrid,
  CanvasSizing,
  DashboardDefinition,
  ResolvedWidgetInstance,
  WidgetBox,
  WidgetInstance,
} from './types';
import type { WidgetDescriptor } from './widget-descriptor';

// The base box a widget is placed by in the editor.
export function baseBox(widget: WidgetInstance): WidgetBox {
  return widget.layout[BASE_BREAKPOINT];
}

// setWidgetBox replaces a widget's base box, returning a new definition.
export function setWidgetBox(def: DashboardDefinition, id: string, box: WidgetBox): DashboardDefinition {
  return {
    ...def,
    widgets: def.widgets.map((w) =>
      w.id === id ? { ...w, layout: { ...w.layout, [BASE_BREAKPOINT]: box } } : w,
    ),
  };
}

// deleteWidget removes a widget by id.
export function deleteWidget(def: DashboardDefinition, id: string): DashboardDefinition {
  return { ...def, widgets: def.widgets.filter((w) => w.id !== id) };
}

// bringToFront raises a widget above the others (z = current max + 1).
export function bringToFront(def: DashboardDefinition, id: string): DashboardDefinition {
  const widget = def.widgets.find((w) => w.id === id);
  if (!widget) return def;
  const box = baseBox(widget);
  // The highest z among the OTHER widgets. Strictly above them all → already on
  // top (no bump — this also covers a lone widget at z=0). On a tie, bump past it.
  const maxOther = Math.max(-Infinity, ...def.widgets.filter((w) => w.id !== id).map((w) => baseBox(w).z));
  if (box.z > maxOther) return def;
  return setWidgetBox(def, id, { ...box, z: maxOther + 1 });
}

// setTitle updates the dashboard title.
export function setTitle(def: DashboardDefinition, title: string): DashboardDefinition {
  return { ...def, title };
}

// setCanvasGrid merges a partial grid change (columns / gap / rowHeight) into the
// canvas, returning a new definition. It owns the grid invariants (parse-time
// clamping is asymmetric — a live edit never round-trips through parse): columns and
// rowHeight floor to >=1 integers so the renderer never emits `repeat(0,…)` or a
// fractional/`repeat(12.5,…)` template (which CSS drops wholesale). When columns
// SHRINKS, existing widgets that now overrun the grid are clamped back inside it —
// otherwise they'd land in implicit `auto` tracks past the fluid columns (0-width /
// horizontal overflow in the viewer).
export function setCanvasGrid(def: DashboardDefinition, patch: Partial<CanvasGrid>): DashboardDefinition {
  const grid = { ...def.canvas.grid, ...patch };
  if (patch.columns !== undefined) grid.columns = Math.max(1, Math.round(patch.columns));
  if (patch.rowHeight !== undefined) grid.rowHeight = Math.max(1, Math.round(patch.rowHeight));
  const widgets =
    grid.columns < def.canvas.grid.columns ? def.widgets.map((w) => clampWidgetColumns(w, grid.columns)) : def.widgets;
  return { ...def, widgets, canvas: { ...def.canvas, grid } };
}

// clampWidgetColumns pulls every breakpoint box of a widget back inside `columns`
// (col <= columns-1, colSpan <= columns-col), leaving rows untouched.
function clampWidgetColumns(widget: WidgetInstance, columns: number): WidgetInstance {
  const layout: WidgetInstance['layout'] = {};
  for (const [bp, box] of Object.entries(widget.layout)) {
    const col = Math.min(box.col, columns - 1);
    layout[bp] = { ...box, col, colSpan: Math.min(box.colSpan, columns - col) };
  }
  return { ...widget, layout };
}

// setCanvasSizing replaces the container-sizing knob (fill / fixed width / fixed height).
export function setCanvasSizing(def: DashboardDefinition, sizing: CanvasSizing): DashboardDefinition {
  return { ...def, canvas: { ...def.canvas, sizing } };
}

// updateWidget replaces the widget with the matching id, returning a new definition.
export function updateWidget(def: DashboardDefinition, id: string, next: WidgetInstance): DashboardDefinition {
  return { ...def, widgets: def.widgets.map((w) => (w.id === id ? next : w)) };
}

// addWidget appends a new widget of the described kind, placed on top (z = max existing
// z + 1) at a sensible default base box, datasource left undefined. Its starting options
// are the descriptor's own `defaults` — the widget's declaration, not a table in the editor
// keyed by widget id. Returns the new definition and the new widget's id so the caller can
// select it.
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
  descriptor: Pick<WidgetDescriptor, 'id' | 'defaults' | 'version' | 'optionsVersion' | 'sdkApiVersion'>,
): { definition: DashboardDefinition; id: string } {
  const maxZ = def.widgets.reduce((m, w) => Math.max(m, baseBox(w).z), 0);
  const id = generateWidgetId();
  // A sensible starter tile on a high-res grid: a third of a 24-col canvas, 4 rows.
  const box: WidgetBox = { col: 0, colSpan: 8, row: 0, rowSpan: 4, z: maxZ + 1 };
  const common = {
    id,
    widget: {
      version: descriptor.version,
      optionsVersion: descriptor.optionsVersion,
      sdkApiVersion: descriptor.sdkApiVersion,
    },
    layout: { [BASE_BREAKPOINT]: box },
    options: { ...descriptor.defaults },
  };
  // The id came from a registered descriptor, so it names either a built-in or a namespaced
  // extension; which one is the registry's knowledge, and this transform does not branch on it.
  const widget = { ...common, type: descriptor.id } as ResolvedWidgetInstance;
  return { definition: { ...def, widgets: [...def.widgets, widget] }, id };
}

// A pixel rectangle react-rnd reports after a drag/resize.
export interface PixelRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

// The rendered geometry of one grid, in pixels — what the canvas measures so it can
// map span boxes to/from the pixel rects react-rnd works in. `colWidth` is the width
// of one fractional column at the current container width; the gaps are the gutters.
export interface GridGeometry {
  colWidth: number;
  colGap: number;
  rowHeight: number;
  rowGap: number;
}

// gridBoxToPx renders a span box to a pixel rect at the current grid geometry. The
// stride between track starts is (track + gap); a span of N covers N tracks and the
// N-1 gaps between them. The signed `offset` (overlap escape hatch) is added last.
export function gridBoxToPx(box: WidgetBox, geom: GridGeometry): PixelRect {
  const colStride = geom.colWidth + geom.colGap;
  const rowStride = geom.rowHeight + geom.rowGap;
  const dx = box.offset?.x ?? 0;
  const dy = box.offset?.y ?? 0;
  return {
    x: box.col * colStride + dx,
    y: box.row * rowStride + dy,
    w: box.colSpan * geom.colWidth + (box.colSpan - 1) * geom.colGap,
    h: box.rowSpan * geom.rowHeight + (box.rowSpan - 1) * geom.rowGap,
  };
}

// pxToGridBox snaps a pixel rect back to a span box, preserving z and any existing
// offset (drag/resize move on the grid; offset is a hand-edited fine nudge the editor
// doesn't clobber). Clamped so a widget can't leave the canvas (col,row >= 0) or
// vanish (spans >= 1); when `columns` is given, also clamped to the RIGHT edge
// (col <= columns-1, colSpan <= columns-col) so a boundary/offset drag can't commit a
// box that overruns the grid into implicit tracks. The pixel rect is un-offset first
// so the snap is grid-relative.
export function pxToGridBox(
  px: PixelRect,
  geom: GridGeometry,
  z: number,
  offset?: { x: number; y: number },
  columns?: number,
): WidgetBox {
  const colStride = Math.max(1, geom.colWidth + geom.colGap);
  const rowStride = Math.max(1, geom.rowHeight + geom.rowGap);
  const x = px.x - (offset?.x ?? 0);
  const y = px.y - (offset?.y ?? 0);
  let col = Math.max(0, Math.round(x / colStride));
  let colSpan = Math.max(1, Math.round((px.w + geom.colGap) / colStride));
  if (columns !== undefined) {
    col = Math.min(col, columns - 1);
    colSpan = Math.min(colSpan, columns - col);
  }
  const box: WidgetBox = {
    col,
    colSpan,
    row: Math.max(0, Math.round(y / rowStride)),
    rowSpan: Math.max(1, Math.round((px.h + geom.rowGap) / rowStride)),
    z,
  };
  if (offset) box.offset = offset;
  return box;
}

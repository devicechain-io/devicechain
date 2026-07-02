// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure state transforms for the canvas editor. Kept free of React/DOM so the
// editing logic (move, resize, delete, reorder, retitle, dirty-check, serialize)
// is unit-testable; DashboardEditor wires these to react-rnd + the save mutation.
//
// The editor edits the 'base' breakpoint boxes only (D2 scope); per-breakpoint
// responsive editing is deferred. Boxes are stored in grid cells — the canvas
// multiplies by canvas.grid.size for pixels, so snapping is exact.

import { BASE_BREAKPOINT, type DashboardDefinition, type WidgetBox, type WidgetInstance } from '@devicechain/dashboards';

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
  const maxZ = def.widgets.reduce((m, w) => Math.max(m, baseBox(w).z), 0);
  const box = baseBox(widget);
  if (box.z === maxZ && maxZ > 0) return def; // already on top
  return setWidgetBox(def, id, { ...box, z: maxZ + 1 });
}

// setTitle updates the dashboard title.
export function setTitle(def: DashboardDefinition, title: string): DashboardDefinition {
  return { ...def, title };
}

// (serializeDefinition + isDirty live in @devicechain/dashboards alongside
// parseDashboardDefinition — they are the definition's serialize/compare lifecycle,
// not editor-specific transforms.)

// A pixel rectangle react-rnd reports after a drag/resize.
export interface PixelRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

// pxToCellBox snaps a pixel rectangle back to a grid-cell box, preserving z.
// Clamped so a widget can't leave the canvas (x,y >= 0) or vanish (w,h >= 1).
export function pxToCellBox(px: PixelRect, cell: number, z: number): WidgetBox {
  const c = Math.max(1, cell);
  return {
    x: Math.max(0, Math.round(px.x / c)),
    y: Math.max(0, Math.round(px.y / c)),
    w: Math.max(1, Math.round(px.w / c)),
    h: Math.max(1, Math.round(px.h / c)),
    z,
  };
}

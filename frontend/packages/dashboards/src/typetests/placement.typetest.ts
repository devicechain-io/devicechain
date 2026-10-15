// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Type-level tests for placements and frames. Compiled by the package's `tsc --noEmit`; vitest
// never runs it. Every NEGATIVE case is a `// @ts-expect-error` directly above the offending
// property: if it ever starts compiling, tsc reports the directive as unused and the typecheck
// fails. Delete a directive to see its case fail.

import type { CanvasGrid, Placement, WidgetFrameSpec, WidgetLayout } from '../types';

// ---- Positive ---------------------------------------------------------------

export const grid: Placement = { mode: 'grid', col: 0, row: 0, cols: 4, rows: 2 };
export const badge: Placement = {
  mode: 'relative',
  host: 'map',
  anchor: 'top-right',
  self: 'top-right',
  offset: { x: -0.25, y: 0.25 },
  size: { cols: 1, rows: 1 },
  layer: 1,
};
export const filler: Placement = { mode: 'relative', host: 'map', anchor: 'center', size: { fill: 'host' } };
export const layout: WidgetLayout = { base: grid, phone: badge };
export const gridSpec: CanvasGrid = {
  columns: 12,
  minCellSize: 56,
  gutter: { x: 8, y: 'space-2' },
  padding: { top: 'space-4', right: 0, bottom: 0, left: 0 },
};
export const frame: WidgetFrameSpec = {
  variant: 'flat',
  border: { width: 2, radius: 'sm', color: 'sev-critical' },
  padding: 'space-0',
  background: 'transparent',
  header: 'hide',
};

// Narrowing on the mode reaches each member's own fields.
export function spanOf(p: Placement): number {
  if (p.mode === 'grid') return p.cols;
  return 'fill' in p.size ? 0 : p.size.cols;
}

// ---- Negative ---------------------------------------------------------------

// The removed layering fields.
export const noZ: Placement = {
  mode: 'grid',
  col: 0,
  row: 0,
  cols: 1,
  rows: 1,
  // @ts-expect-error z-order is gone: layering is the relative mode
  z: 1,
};

// A relative placement names its host.
// @ts-expect-error host is required
export const noHost: Placement = { mode: 'relative', anchor: 'top', size: { cols: 1, rows: 1 } };

export const badAnchor: Placement = {
  mode: 'relative',
  host: 'a',
  // @ts-expect-error not one of the nine anchor points
  anchor: 'middle',
  size: { cols: 1, rows: 1 },
};

export const badFill: Placement = {
  mode: 'relative',
  host: 'a',
  anchor: 'top',
  // @ts-expect-error a relative widget fills its host or takes a span, nothing else
  size: { fill: 'canvas' },
};

// @ts-expect-error a reserved mode is not a member of the union this release builds
export const pinned: Placement = { mode: 'pinned', col: 0, row: 0, cols: 1, rows: 1 };

export const freeColour: WidgetFrameSpec = {
  // @ts-expect-error colours are tokens, never free values
  border: { color: '#f00' },
};

export const pxPadding: WidgetFrameSpec = {
  // @ts-expect-error frame padding is a spacing token
  padding: 12,
};

export const wideBorder: WidgetFrameSpec = {
  // @ts-expect-error a border is 0 to 3 px
  border: { width: 4 },
};

export const badGutter: CanvasGrid = {
  columns: 12,
  minCellSize: 56,
  // @ts-expect-error a length is a number or a space-0..space-4 token
  gutter: 'space-9',
  padding: 0,
};

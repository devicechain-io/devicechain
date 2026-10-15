// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The canvas's pointer arithmetic, free of React and the DOM so it is unit-testable: how far a
// pointer has moved in cells, what footprint a drag of a handle asks for, and where the dashed
// link from a relative widget to its host runs.

import { anchorFraction, type GridPlacement, type PlacementAnchor, type Rect } from '@devicechain/dashboards';

// The eight resize handles, named for the compass edge or corner they sit on.
export type Handle = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw';

export const HANDLES: readonly Handle[] = ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'];

export interface Strides {
  col: number;
  row: number;
}

// moveTarget is the cell a dragged widget's top-left corner is over after the pointer has moved
// (dx, dy) px: the origin plus the whole cells nearest the movement.
export function moveTarget(origin: GridPlacement, dx: number, dy: number, s: Strides): { col: number; row: number } {
  return { col: origin.col + Math.round(dx / s.col), row: origin.row + Math.round(dy / s.row) };
}

// resizeTarget is the footprint a drag of `handle` by (dx, dy) px asks for, snapped to cells. The
// edge under the handle follows the pointer and the opposite edge stays where it was; a west or
// north edge is held at the canvas edge rather than going negative (the model clamps the span to
// the widget's limits). Spans are at least one cell.
export function resizeTarget(
  origin: GridPlacement,
  handle: Handle,
  dx: number,
  dy: number,
  s: Strides,
): { col: number; row: number; cols: number; rows: number } {
  const dc = Math.round(dx / s.col);
  const dr = Math.round(dy / s.row);
  let { col, row, cols, rows } = origin;
  if (handle.includes('e')) cols = Math.max(1, origin.cols + dc);
  if (handle.includes('w')) {
    const left = Math.min(origin.col + origin.cols - 1, Math.max(0, origin.col + dc));
    cols = origin.col + origin.cols - left;
    col = left;
  }
  if (handle.includes('s')) rows = Math.max(1, origin.rows + dr);
  if (handle.includes('n')) {
    const top = Math.min(origin.row + origin.rows - 1, Math.max(0, origin.row + dr));
    rows = origin.row + origin.rows - top;
    row = top;
  }
  return { col, row, cols, rows };
}

// Which resize edges a handle moves, in the shape resizeWidget takes.
export function handleEdges(handle: Handle): { fromLeft: boolean; fromTop: boolean } {
  return { fromLeft: handle.includes('w'), fromTop: handle.includes('n') };
}

// The handles an axis restriction leaves: a pinned axis has none on it, and a corner needs both.
export function enabledHandles(pinnedCols: boolean, pinnedRows: boolean): Handle[] {
  return HANDLES.filter((h) => {
    const horizontal = h.includes('e') || h.includes('w');
    const vertical = h.includes('n') || h.includes('s');
    return !(horizontal && pinnedCols) && !(vertical && pinnedRows);
  });
}

// The dashed link between a relative widget and its host: from the host's anchor point to the same
// kind of point on the widget (its `self`, else its anchor).
export function guideLine(
  host: Rect,
  box: Rect,
  anchor: PlacementAnchor,
  self: PlacementAnchor | undefined,
): { x1: number; y1: number; x2: number; y2: number } {
  const a = anchorFraction(anchor);
  const b = anchorFraction(self ?? anchor);
  return {
    x1: host.x + a.fx * host.w,
    y1: host.y + a.fy * host.h,
    x2: box.x + b.fx * box.w,
    y2: box.y + b.fy * box.h,
  };
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import type { GridPlacement } from '@devicechain/dashboards';

import { enabledHandles, guideLine, handleEdges, moveTarget, resizeTarget } from './canvasGeometry';

const origin: GridPlacement = { mode: 'grid', col: 4, row: 2, cols: 3, rows: 2 };
const s = { col: 100, row: 100 };

describe('moveTarget', () => {
  it('moves by the whole cells nearest the pointer movement', () => {
    expect(moveTarget(origin, 149, -151, s)).toEqual({ col: 5, row: 0 });
    expect(moveTarget(origin, 151, 49, s)).toEqual({ col: 6, row: 2 });
  });
});

describe('resizeTarget', () => {
  it('east and south grow the far edges and leave the origin corner', () => {
    expect(resizeTarget(origin, 'se', 200, 100, s)).toEqual({ col: 4, row: 2, cols: 5, rows: 3 });
  });

  it('west and north keep the opposite edge fixed', () => {
    // Dragging the west edge left two cells: the right edge (col 7) stays.
    expect(resizeTarget(origin, 'w', -200, 0, s)).toEqual({ col: 2, row: 2, cols: 5, rows: 2 });
    expect(resizeTarget(origin, 'n', 0, -100, s)).toEqual({ col: 4, row: 1, cols: 3, rows: 3 });
  });

  it('never goes below one cell, and never past the canvas edge on the west or north', () => {
    expect(resizeTarget(origin, 'e', -900, 0, s).cols).toBe(1);
    expect(resizeTarget(origin, 'w', 900, 0, s)).toEqual({ col: 6, row: 2, cols: 1, rows: 2 });
    expect(resizeTarget(origin, 'w', -9000, 0, s)).toMatchObject({ col: 0, cols: 7 });
    expect(resizeTarget(origin, 'n', 0, -9000, s)).toMatchObject({ row: 0, rows: 4 });
  });
});

describe('handles', () => {
  it('names the edges a handle moves', () => {
    expect(handleEdges('nw')).toEqual({ fromLeft: true, fromTop: true });
    expect(handleEdges('se')).toEqual({ fromLeft: false, fromTop: false });
  });

  it('offers a handle on an axis only while that axis can be resized', () => {
    expect(enabledHandles(false, false)).toHaveLength(8);
    // A pinned row count leaves east and west only: no corner needs the pinned axis.
    expect(enabledHandles(false, true).sort()).toEqual(['e', 'w']);
    expect(enabledHandles(true, false).sort()).toEqual(['n', 's']);
    expect(enabledHandles(true, true)).toEqual([]);
  });
});

describe('guideLine', () => {
  it('runs from the host anchor to the same point of the widget, or its own self point', () => {
    const host = { x: 100, y: 100, w: 200, h: 100 };
    const box = { x: 250, y: 90, w: 60, h: 40 };
    expect(guideLine(host, box, 'top-right', undefined)).toEqual({ x1: 300, y1: 100, x2: 310, y2: 90 });
    expect(guideLine(host, box, 'center', 'bottom-left')).toEqual({ x1: 200, y1: 150, x2: 250, y2: 130 });
  });
});

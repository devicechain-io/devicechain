// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { isDirty, type DashboardDefinition } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  baseBox,
  bringToFront,
  deleteWidget,
  pxToCellBox,
  setTitle,
  setWidgetBox,
} from './editor-model';

const box = (over = {}) => ({ x: 0, y: 0, w: 4, h: 3, z: 0, ...over });

function def(): DashboardDefinition {
  return {
    schemaVersion: 1,
    title: 'Test',
    canvas: { grid: { snap: true, size: 8 }, breakpoints: { base: 0 } },
    widgets: [
      { id: 'a', type: 'gauge', layout: { base: box({ z: 1 }) } },
      { id: 'b', type: 'label', layout: { base: box({ x: 10, z: 2 }) } },
    ],
  };
}

describe('setWidgetBox', () => {
  it('replaces only the target widget’s base box', () => {
    const next = setWidgetBox(def(), 'a', box({ x: 5, y: 6, w: 8, h: 9, z: 1 }));
    expect(baseBox(next.widgets[0])).toEqual({ x: 5, y: 6, w: 8, h: 9, z: 1 });
    expect(baseBox(next.widgets[1])).toEqual(box({ x: 10, z: 2 })); // untouched
  });
});

describe('deleteWidget', () => {
  it('removes the widget by id', () => {
    const next = deleteWidget(def(), 'a');
    expect(next.widgets.map((w) => w.id)).toEqual(['b']);
  });
});

describe('bringToFront', () => {
  it('raises a widget above the current max z', () => {
    const next = bringToFront(def(), 'a'); // a.z=1, max=2 → a.z=3
    expect(baseBox(next.widgets[0]).z).toBe(3);
  });
  it('is a no-op when already on top', () => {
    const d = def();
    expect(bringToFront(d, 'b')).toBe(d); // b already has max z
  });
});

describe('setTitle', () => {
  it('updates the title', () => {
    expect(setTitle(def(), 'Renamed').title).toBe('Renamed');
  });
});

describe('isDirty', () => {
  it('is false for an unchanged copy and true after an edit', () => {
    const d = def();
    expect(isDirty(d, def())).toBe(false);
    expect(isDirty(setTitle(d, 'x'), d)).toBe(true);
  });
});

describe('pxToCellBox', () => {
  it('snaps pixels to grid cells and clamps', () => {
    // cell=8: 41px → 5 cells (rounded), width 63px → 8 cells.
    expect(pxToCellBox({ x: 41, y: 17, w: 63, h: 25 }, 8, 2)).toEqual({ x: 5, y: 2, w: 8, h: 3, z: 2 });
    // negative/zero clamps to x,y>=0 and w,h>=1.
    expect(pxToCellBox({ x: -20, y: -1, w: 2, h: 0 }, 8, 0)).toEqual({ x: 0, y: 0, w: 1, h: 1, z: 0 });
  });
});

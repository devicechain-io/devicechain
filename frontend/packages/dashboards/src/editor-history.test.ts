// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { isDirty, serializeDefinition } from './definition';
import { moveWidget, setTitle, updateWidget } from './editor-model';
import { pruneSlots } from './slots';
import {
  canRedo,
  canUndo,
  commitEdit,
  createEditHistory,
  DEFAULT_HISTORY_LIMIT,
  rebaseHistory,
  redoEdit,
  sealHistory,
  undoEdit,
  type EditHistory,
} from './editor-history';
import type { DashboardDefinition, WidgetInstance } from './types';

function board(): DashboardDefinition {
  return {
    schemaVersion: 2,
    title: 'History',
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [
      { id: 'a', type: 'gauge', layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } } },
      { id: 'b', type: 'label', layout: { base: { mode: 'grid', col: 3, row: 0, cols: 3, rows: 3 } } },
    ],
  };
}

const same = (a: DashboardDefinition, b: DashboardDefinition) => serializeDefinition(a) === serializeDefinition(b);
const json = (d: DashboardDefinition) => serializeDefinition(d);

describe('undo and redo', () => {
  it('walk back and forward through the exact definitions that were committed', () => {
    const d0 = board();
    const d1 = moveWidget(d0, 'a', { col: 6, row: 0 }, 12, { nearest: true });
    const d2 = setTitle(d1, 'Renamed');
    const d3 = moveWidget(d2, 'b', { col: 0, row: 4 }, 12, { nearest: true });
    let h: EditHistory<DashboardDefinition, string> = createEditHistory(d0);
    h = commitEdit(h, d1, 'move a');
    h = commitEdit(h, d2, 'title');
    h = commitEdit(h, d3, 'move b');

    let r = undoEdit(h);
    expect(r.label).toBe('move b');
    expect(json(r.history.present)).toBe(json(d2));
    r = undoEdit(r.history);
    expect(r.label).toBe('title');
    expect(json(r.history.present)).toBe(json(d1));
    r = undoEdit(r.history);
    expect(r.label).toBe('move a');
    expect(json(r.history.present)).toBe(json(d0));
    expect(canUndo(r.history)).toBe(false);
    // Nothing further back: the history is returned unchanged and no edit is named.
    const stuck = undoEdit(r.history);
    expect(stuck.history).toBe(r.history);
    expect(stuck.label).toBeUndefined();

    r = redoEdit(r.history);
    expect(r.label).toBe('move a');
    expect(json(r.history.present)).toBe(json(d1));
    r = redoEdit(r.history);
    expect(json(r.history.present)).toBe(json(d2));
    r = redoEdit(r.history);
    expect(r.label).toBe('move b');
    expect(json(r.history.present)).toBe(json(d3));
    expect(canRedo(r.history)).toBe(false);
  });

  it('a new edit after an undo drops the redo branch', () => {
    const d0 = board();
    const d1 = setTitle(d0, 'one');
    const d2 = setTitle(d0, 'two');
    let h = commitEdit(createEditHistory(d0), d1, 'title');
    h = undoEdit(h).history;
    expect(canRedo(h)).toBe(true);
    h = commitEdit(h, d2, 'title');
    expect(canRedo(h)).toBe(false);
    expect(h.present.title).toBe('two');
    expect(undoEdit(h).history.present.title).toBe('History');
  });

  it('records nothing for a commit that changes nothing', () => {
    const d0 = board();
    const h0 = createEditHistory(d0);
    expect(commitEdit(h0, d0, 'noop')).toBe(h0);
    // An equal copy, under an equality the caller supplies, is not an edit either.
    expect(commitEdit(h0, structuredClone(d0), 'noop', { equals: same })).toBe(h0);
  });
});

describe('coalescing', () => {
  it('typing into one field is one entry; another field starts a new one', () => {
    const d0 = board();
    let h = createEditHistory(d0);
    for (const title of ['T', 'Te', 'Tem', 'Temp']) h = commitEdit(h, setTitle(h.present, title), 'title', { key: 'title' });
    expect(h.past).toHaveLength(1);
    h = commitEdit(h, moveWidget(h.present, 'a', { col: 6, row: 0 }), 'move', { key: 'canvas:columns' });
    expect(h.past).toHaveLength(2);
    expect(undoEdit(h).history.present.title).toBe('Temp');
    expect(undoEdit(undoEdit(h).history).history.present.title).toBe('History');
  });

  it('a sealed key starts a new entry even for the same field', () => {
    let h = createEditHistory(board());
    h = commitEdit(h, setTitle(h.present, 'A'), 'title', { key: 'title' });
    h = sealHistory(h);
    h = commitEdit(h, setTitle(h.present, 'AB'), 'title', { key: 'title' });
    expect(h.past).toHaveLength(2);
  });

  it('an undo seals: typing again after it is a new entry', () => {
    let h = createEditHistory(board());
    h = commitEdit(h, setTitle(h.present, 'A'), 'title', { key: 'title' });
    h = commitEdit(h, setTitle(h.present, 'AB'), 'title', { key: 'title' });
    h = undoEdit(h).history;
    h = redoEdit(h).history;
    h = commitEdit(h, setTitle(h.present, 'ABC'), 'title', { key: 'title' });
    expect(h.past).toHaveLength(2);
  });

  it('typing a field back to where it started leaves no entry behind', () => {
    let h = createEditHistory(board());
    h = commitEdit(h, setTitle(h.present, 'HistoryX'), 'title', { key: 'title', equals: same });
    h = commitEdit(h, setTitle(h.present, 'History'), 'title', { key: 'title', equals: same });
    expect(canUndo(h)).toBe(false);
  });
});

describe('depth', () => {
  it('is bounded: the oldest entries fall off', () => {
    const d0 = board();
    let h = createEditHistory(d0, { limit: 5 });
    for (let i = 1; i <= 8; i++) h = commitEdit(h, setTitle(h.present, `t${i}`), 'title');
    expect(h.past).toHaveLength(5);
    let undone = 0;
    while (canUndo(h)) {
      h = undoEdit(h).history;
      undone++;
    }
    expect(undone).toBe(5);
    expect(h.present.title).toBe('t3');
  });

  it('defaults to a hundred entries', () => {
    expect(DEFAULT_HISTORY_LIMIT).toBe(100);
    let h = createEditHistory(board());
    for (let i = 1; i <= 130; i++) h = commitEdit(h, setTitle(h.present, `t${i}`), 'title');
    expect(h.past).toHaveLength(100);
  });
});

describe('rebase', () => {
  it('applies a non-edit change to every state, so undo cannot bring the old form back', () => {
    const d0 = { ...board(), convertedFrom: 1 };
    let h = commitEdit(createEditHistory<DashboardDefinition, string>(d0), setTitle(d0, 'x'), 'title');
    h = rebaseHistory(h, ({ convertedFrom: _drop, ...rest }) => rest);
    expect(h.present.convertedFrom).toBeUndefined();
    expect(undoEdit(h).history.present.convertedFrom).toBeUndefined();
    expect(undoEdit(h).history.present.title).toBe('History');
  });
});

// The history holds whole snapshots, and that is affordable only because the editor's transforms
// share every widget an edit does not touch. Measured on a board of 150 widgets with real options
// (about 95 KB serialized): a hundred edits hold about 250 distinct widget objects, not the 15,150
// a deep copy per snapshot would. A transform that started cloning the board would multiply the
// editor's memory by the depth, and this is where it shows.
describe('memory', () => {
  it('a hundred edits to a large board share every widget they did not touch', () => {
    const widgets: WidgetInstance[] = Array.from({ length: 150 }, (_, i) => ({
      id: `w${i}`,
      type: 'gauge',
      layout: { base: { mode: 'grid', col: (i % 4) * 3, row: Math.floor(i / 4) * 3, cols: 3, rows: 3 } },
      options: { title: `Widget ${i}`, min: 0, max: 100, thresholds: Array.from({ length: 20 }, (_, k) => ({ value: k, color: '#c00' })) },
    }));
    let h = createEditHistory<DashboardDefinition, string>({ ...board(), widgets });
    for (let i = 0; i < 100; i++) {
      const id = `w${i % 150}`;
      const w = h.present.widgets.find((x) => x.id === id)!;
      const next =
        i % 2 === 0
          ? updateWidget(h.present, id, { ...w, options: { ...w.options, title: `edit ${i}` } } as WidgetInstance)
          : moveWidget(h.present, id, { col: (i * 3) % 12, row: 200 + i * 3 }, 12, { nearest: true });
      h = commitEdit(h, pruneSlots(next), 'edit', { equals: (a, b) => !isDirty(a, b) });
    }
    expect(h.past).toHaveLength(100);
    const distinct = new Set<object>();
    for (const state of [...h.past.map((e) => e.state), h.present]) for (const w of state.widgets) distinct.add(w);
    // 150 originals plus at most one new object per edited widget per edit (a move may also push a
    // neighbour aside); a deep copy per snapshot would be 101 x 150.
    expect(distinct.size).toBeLessThanOrEqual(150 + 2 * 100);
  });
});

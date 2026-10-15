// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The conversion of a definition stored before schemaVersion 3 onto the time span. The rule is
// stated in ./legacy-window and pinned here: a sample count has no span, so it is dropped and
// the chart follows the board, flagged; nothing else is touched.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition, storedDefinition } from './definition';
import { convertSampleWindows } from './legacy-window';
import { CURRENT_SCHEMA_VERSION } from './types';

const base = { mode: 'grid', col: 0, row: 0, cols: 4, rows: 2 };
const chart = (id: string, options?: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({
  id,
  type: 'timeseries-chart',
  layout: { base: { ...base, row: Number(id.replace(/\D/g, '')) * 2 } },
  datasource: { kind: 'device', deviceToken: 'd-1', measurements: ['t'] },
  ...(options ? { options } : {}),
  ...extra,
});
const card = (id: string, options: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({
  id,
  type: 'latest-card',
  layout: { base: { ...base, col: 8 } },
  datasource: { kind: 'device', deviceToken: 'd-1', measurements: ['t'] },
  options,
  ...extra,
});
const board = (widgets: unknown[], schemaVersion = 2) => ({ schemaVersion, title: 'b', canvas: {}, widgets });
const optionsOf = (w: unknown) => (w as { options?: Record<string, unknown> }).options;

describe('convertSampleWindows', () => {
  it('drops the sample count from a timeseries chart, whatever its value, and flags the chart', () => {
    for (const count of [1, 60, 300, 100000]) {
      const { doc, flagged } = convertSampleWindows(board([chart('c1', { title: 'T', window: count })]));
      expect(optionsOf((doc.widgets as unknown[])[0]), String(count)).toEqual({ title: 'T' });
      expect(flagged, String(count)).toEqual([0]);
    }
  });

  it('does NOT turn a count into a span: no windowSeconds appears, so the chart follows the board', () => {
    const { doc } = convertSampleWindows(board([chart('c1', { window: 300 })]));
    expect(JSON.stringify(doc)).not.toContain('windowSeconds');
  });

  it('leaves a chart that never had a window, and every other widget (a card keeps its window), alone', () => {
    const input = board([chart('c1', { title: 'T' }), card('k1', { window: 120 }), chart('c2')]);
    const { doc, flagged } = convertSampleWindows(input);
    expect(flagged).toEqual([]);
    expect(doc.widgets).toEqual(input.widgets);
  });

  it('moves the chart settings generation from 1 to 2 so it does not load as a placeholder, and no other', () => {
    const ref = (optionsVersion: number) => ({ widget: { version: '1.0.0', optionsVersion, sdkApiVersion: 1 } });
    const { doc } = convertSampleWindows(
      board([chart('c1', { window: 5 }, ref(1)), chart('c2', {}, ref(2)), card('k1', {}, ref(1))]),
    );
    const ws = doc.widgets as Array<{ widget?: { optionsVersion: number } }>;
    expect(ws[0].widget?.optionsVersion).toBe(2);
    expect(ws[1].widget?.optionsVersion).toBe(2);
    expect(ws[2].widget?.optionsVersion).toBe(1); // a card's options did not change meaning
  });

  it('keeps every widget, in order, and does not mutate its input', () => {
    const input = board([chart('c1', { window: 300 }), card('k1', { window: 300 }), chart('c2', { window: 9 })]);
    const frozen = JSON.stringify(input);
    const { doc } = convertSampleWindows(input);
    expect(JSON.stringify(input)).toBe(frozen);
    expect((doc.widgets as Array<{ id: string }>).map((w) => w.id)).toEqual(['c1', 'k1', 'c2']);
    expect(doc.schemaVersion).toBe(CURRENT_SCHEMA_VERSION);
  });

  it('is a fixed point: converting its own output changes and flags nothing', () => {
    const once = convertSampleWindows(
      board([chart('c1', { window: 300 }, { widget: { version: '1.0.0', optionsVersion: 1 } }), card('k1', { window: 3 })]),
    );
    const twice = convertSampleWindows(once.doc);
    expect(twice.doc).toEqual(once.doc);
    expect(twice.flagged).toEqual([]);
  });

  it('tolerates a document with no widgets array, or widgets that are not objects', () => {
    expect(convertSampleWindows({ schemaVersion: 2 }).flagged).toEqual([]);
    expect(convertSampleWindows({ widgets: [null, 7, 'x'] }).doc.widgets).toEqual([null, 7, 'x']);
  });
});

describe('loading a board stored before the time span', () => {
  it('a PUBLISHED version (immutable, never re-saved) converts on every load, identically, and is never written to', () => {
    // A published version is read again on every open and never saved back, so the conversion
    // runs each time: it must be deterministic and must not touch the stored document.
    const stored = board([chart('c1', { title: 'Temp', window: 20 }, { widget: { optionsVersion: 1 } }), card('k1', { window: 90 })]);
    const bytes = JSON.stringify(stored);
    const first = parseDashboardDefinition(JSON.parse(bytes));
    const second = parseDashboardDefinition(stored);
    const third = parseDashboardDefinition(stored);
    expect(JSON.stringify(stored)).toBe(bytes);
    expect(serializeDefinition(second)).toBe(serializeDefinition(first));
    expect(serializeDefinition(third)).toBe(serializeDefinition(first));
    expect(second.convertedWindows).toEqual(['c1']);
    expect(third.convertedWindows).toEqual(['c1']);
    // And what it converts to is itself a fixed point of the parser.
    const reread = parseDashboardDefinition(JSON.parse(serializeDefinition(first)));
    expect(serializeDefinition(reread)).toBe(serializeDefinition(first));
    expect(reread.convertedWindows).toBeUndefined();
  });

  it('flags the converted charts by id and marks the board, keeping every widget', () => {
    const def = parseDashboardDefinition(
      board([chart('c1', { title: 'Temp', window: 300 }), chart('c2', { title: 'Hum' }), card('k1', { window: 90 })]),
    );
    expect(def.widgets.map((w) => w.id)).toEqual(['c1', 'c2', 'k1']);
    expect(def.convertedWindows).toEqual(['c1']);
    expect(def.convertedFrom).toBe(2);
    expect(def.schemaVersion).toBe(3);
    expect(optionsOf(def.widgets[0])).toEqual({ title: 'Temp' });
    expect(optionsOf(def.widgets[2])).toEqual({ window: 90 });
  });

  it('converts a version-1 board in two steps: the grid first, then the span', () => {
    const v1 = {
      schemaVersion: 1,
      title: 'old',
      canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 } },
      widgets: [
        { id: 'c1', type: 'timeseries-chart', layout: { base: { col: 0, colSpan: 12, row: 0, rowSpan: 6 } }, options: { window: 50 } },
      ],
    };
    const def = parseDashboardDefinition(v1);
    expect(def.schemaVersion).toBe(3);
    expect(def.convertedFrom).toBe(1);
    expect(def.convertedWindows).toEqual(['c1']);
    expect(def.widgets[0].layout.base.mode).toBe('grid');
    expect(optionsOf(def.widgets[0])).toEqual({});
  });

  it('converts a version-2 board with nothing to flag SILENTLY: nothing was lost, so nothing to review', () => {
    const def = parseDashboardDefinition(board([chart('c1', { title: 'T' }), card('k1', { window: 90 })]));
    expect(def.schemaVersion).toBe(3);
    expect(def.convertedFrom).toBeUndefined();
    expect(def.convertedWindows).toBeUndefined();
  });

  it('a chart that carried a v1 settings reference still renders (not a placeholder) after conversion', () => {
    const def = parseDashboardDefinition(
      board([chart('c1', { window: 300 }, { widget: { version: '1.0.0', optionsVersion: 1, sdkApiVersion: 1 } })]),
    );
    expect(def.widgets[0].type).toBe('timeseries-chart');
    expect((def.widgets[0] as { widget?: { optionsVersion: number } }).widget?.optionsVersion).toBe(2);
  });

  it('a save writes the current format with neither mark, and reading that back converts nothing', () => {
    const def = parseDashboardDefinition(board([chart('c1', { window: 300 })]));
    const saved = JSON.parse(serializeDefinition(def));
    expect(saved.schemaVersion).toBe(3);
    expect('convertedFrom' in saved).toBe(false);
    expect('convertedWindows' in saved).toBe(false);
    expect(JSON.stringify(saved)).not.toContain('"window"');
    const again = parseDashboardDefinition(saved);
    expect(again.convertedFrom).toBeUndefined();
    expect(again.convertedWindows).toBeUndefined();
    expect(storedDefinition(again)).toEqual(storedDefinition(parseDashboardDefinition(saved)));
  });

  it('an already-current board that still carries a window is NOT converted (the option check reports it instead)', () => {
    const def = parseDashboardDefinition(board([chart('c1', { window: 300 })], 3));
    expect(def.convertedWindows).toBeUndefined();
    expect(optionsOf(def.widgets[0])).toEqual({ window: 300 });
  });
});

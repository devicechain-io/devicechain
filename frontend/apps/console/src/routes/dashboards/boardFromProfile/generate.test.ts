// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The generator's output for the shapes of profile that matter, and the proof that every one of
// them is a board the console's own import pipeline (parser, option validation, layout lint)
// accepts. A generator that wrote a plausible board the editor then refuses would be worse than
// none, so the validity check runs on every case.

import { describe, expect, it } from 'vitest';
import { lintLayout, spanLimitsOfLayout, UNKNOWN_WIDGET_TYPE } from '@devicechain/dashboards';
import { defaultWidgetRegistry } from '../editor/widgetRegistry';
import { prepareImport } from '../import/importDefinition';
import {
  generateBoard,
  MAX_CARDS,
  MAX_CHART_SERIES,
  MAX_CHARTS,
  type BoardCommand,
  type BoardInput,
  type BoardMetric,
} from './generate';

const labels: BoardInput['labels'] = {
  device: 'Device',
  chart: 'Telemetry',
  alarms: 'Alarms',
  calculated: 'Calculated',
  measurementsLeftOut: (shown, total) => `Showing ${shown} of ${total}`,
  commandsLeftOut: (count) => `${count} commands left out`,
};

const metric = (key: string, extra: Partial<BoardMetric> = {}): BoardMetric => ({
  key,
  dataType: 'DOUBLE',
  calculated: false,
  ...extra,
});
const command = (key: string, impact: string | null = 'NORMAL', extra: Partial<BoardCommand> = {}): BoardCommand => ({
  key,
  impact,
  ...extra,
});
const input = (over: Partial<BoardInput> = {}): BoardInput => ({
  title: 'Boiler board',
  metrics: [metric('temperature', { unit: 'C' }), metric('humidity', { unit: '%' })],
  commands: [],
  labels,
  ...over,
});

function generated(over: Partial<BoardInput> = {}) {
  const board = generateBoard(input(over));
  if (!board.ok) throw new Error(`refused: ${board.reason}`);
  return board;
}

// The real pipeline: parse against the host catalog, validate every option, lint the layout.
function assertValid(definition: Record<string, unknown>) {
  const registry = defaultWidgetRegistry();
  const prepared = prepareImport(JSON.stringify(definition), registry.catalog);
  expect(prepared.unresolved).toEqual([]);
  expect(prepared.optionIssues).toEqual([]);
  const findings = lintLayout(prepared.definition, {
    columns: prepared.definition.canvas.grid.columns,
    spanLimitsOf: (w) =>
      w.type === UNKNOWN_WIDGET_TYPE ? undefined : spanLimitsOfLayout(registry.descriptor(w.type)?.layout),
  }).filter((f) => f.severity === 'error');
  expect(findings).toEqual([]);
  return prepared.definition;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Loose = Record<string, any>;
const widgetsOf = (b: ReturnType<typeof generated>, type: string) =>
  (b.definition.widgets as Loose[]).filter((w) => w.type === type);
// The notes about what was left out.
const notesOf = (b: ReturnType<typeof generated>) => widgetsOf(b, 'label');

describe('a small profile', () => {
  it('gets a selector, a card per measurement with its unit, a chart and an alarm table', () => {
    const b = generated({ defaultDevice: 'boiler-1' });
    const def = assertValid(b.definition);
    expect(def.title).toBe('Boiler board');
    expect(widgetsOf(b, 'entity-selector')).toHaveLength(1);
    expect(widgetsOf(b, 'latest-card').map((w) => [w.options.measurement, w.options.unit])).toEqual([
      ['temperature', 'C'],
      ['humidity', '%'],
    ]);
    // Different units, different scales: one chart (and one axis) each.
    expect(widgetsOf(b, 'timeseries-chart').map((w) => [w.datasource.measurements, w.options.unit])).toEqual([
      [['temperature'], 'C'],
      [['humidity'], '%'],
    ]);
    expect(widgetsOf(b, 'alarm-table')).toHaveLength(1);
    expect(notesOf(b)).toHaveLength(0);
    expect(b.summary.leftOut).toEqual([]);
  });

  it('binds the device slot to a device of the type, or leaves it unbound when there is none', () => {
    const bound = generated({ defaultDevice: 'boiler-1' }).definition.slots as Loose;
    expect(bound.device.defaultBinding).toEqual({ kind: 'device', deviceToken: 'boiler-1' });
    const unbound = generated({ defaultDevice: null }).definition.slots as Loose;
    expect(unbound.device.defaultBinding).toBeUndefined();
    // Every data widget reads the one slot the selector re-points.
    for (const w of widgetsOf(generated(), 'latest-card')) expect(w.datasource.slot).toBe('device');
    expect(widgetsOf(generated(), 'entity-selector')[0].options.selectionTarget).toBe('device');
  });

  it('narrows the selector to devices of the type, so it cannot land on another type', () => {
    const b = generated({ deviceType: 'boiler', defaultDevice: 'boiler-1' });
    assertValid(b.definition);
    const slots = b.definition.slots as Loose;
    expect(slots.device.candidates).toEqual({ deviceType: 'boiler' });
    expect(slots.device.defaultBinding).toEqual({ kind: 'device', deviceToken: 'boiler-1' });
    // Still narrowed when the type has no device yet: the selector then offers none, not another type's.
    const empty = generated({ deviceType: 'boiler', defaultDevice: null }).definition.slots as Loose;
    expect(empty.device.candidates).toEqual({ deviceType: 'boiler' });
    expect((generated().definition.slots as Loose).device.candidates).toBeUndefined();
  });

  it('is deterministic', () => {
    expect(JSON.stringify(generated().definition)).toBe(JSON.stringify(generated().definition));
  });

  it('keeps INT readings whole and does not chart a boolean', () => {
    const b = generated({
      metrics: [metric('cycles', { dataType: 'INT' }), metric('door', { dataType: 'BOOLEAN' })],
    });
    assertValid(b.definition);
    expect(widgetsOf(b, 'latest-card')[0].options.precision).toBe(0);
    expect(widgetsOf(b, 'timeseries-chart')[0].datasource.measurements).toEqual(['cycles']);
  });

  it('puts measurements sharing a unit on one chart, a unit apart on its own axis', () => {
    const b = generated({
      metrics: [
        metric('temperature', { unit: 'C' }),
        metric('humidity', { unit: '%' }),
        metric('dewPoint', { unit: 'C' }),
      ],
    });
    assertValid(b.definition);
    const charts = widgetsOf(b, 'timeseries-chart');
    expect(charts.map((w) => w.datasource.measurements)).toEqual([['temperature', 'dewPoint'], ['humidity']]);
    expect(charts.map((w) => w.options.title)).toEqual(['Telemetry (C)', 'Telemetry (%)']);
    expect(b.summary.charts.map((c) => c.unit)).toEqual(['C', '%']);
  });

  it('gives a measurement with no unit a chart of its own', () => {
    const b = generated({ metrics: [metric('a'), metric('b'), metric('c', { unit: '' })] });
    assertValid(b.definition);
    const charts = widgetsOf(b, 'timeseries-chart');
    expect(charts.map((w) => w.datasource.measurements)).toEqual([['a'], ['b'], ['c']]);
    expect(charts[0].options).not.toHaveProperty('unit');
  });

  it('lays charts two to a row and tucks the alarm table into an odd last row', () => {
    const three = generated({ metrics: [metric('a'), metric('b'), metric('c')] });
    assertValid(three.definition);
    const alarms = three.summary.widgets.find((w) => w.id === 'alarms')!;
    const last = three.summary.widgets.find((w) => w.id === 'chart-3')!;
    expect([alarms.row, alarms.col, alarms.cols]).toEqual([last.row, 6, 6]);
    const two = generated({ metrics: [metric('a'), metric('b')] });
    assertValid(two.definition);
    const full = two.summary.widgets.find((w) => w.id === 'alarms')!;
    expect(full.cols).toBe(12);
    expect(full.row).toBeGreaterThan(two.summary.widgets.find((w) => w.id === 'chart-2')!.row);
  });

  it('has no chart when nothing is numeric, and the alarm table takes the full row', () => {
    const b = generated({ metrics: [metric('door', { dataType: 'BOOLEAN' })] });
    assertValid(b.definition);
    expect(widgetsOf(b, 'timeseries-chart')).toHaveLength(0);
    expect(b.summary.widgets.find((w) => w.id === 'alarms')?.cols).toBe(12);
  });
});

describe('a large profile', () => {
  const sixty = Array.from({ length: 60 }, (_, i) => metric(`m${String(i + 1).padStart(2, '0')}`));

  it('caps the cards in declared order and says how many were left out', () => {
    const b = generated({ metrics: sixty });
    assertValid(b.definition);
    const cards = widgetsOf(b, 'latest-card');
    expect(cards).toHaveLength(MAX_CARDS);
    expect(cards.map((w) => w.options.measurement)).toEqual(sixty.slice(0, MAX_CARDS).map((m) => m.key));
    expect(b.summary.leftOut).toHaveLength(60 - MAX_CARDS);
    expect(b.summary.leftOut[0]).toBe(sixty[MAX_CARDS].key);
    // Also on the board itself, as a text widget.
    expect(notesOf(b)[0].options.text).toBe(`Showing ${MAX_CARDS} of 60`);
  });

  it('draws at most a few charts, and a few series on each', () => {
    const sameUnit = sixty.map((m) => ({ ...m, unit: 'C' }));
    const one = widgetsOf(generated({ metrics: sameUnit }), 'timeseries-chart');
    expect(one).toHaveLength(1);
    expect(one[0].datasource.measurements).toHaveLength(MAX_CHART_SERIES);
    const b = generated({ metrics: sixty });
    assertValid(b.definition);
    expect(widgetsOf(b, 'timeseries-chart')).toHaveLength(MAX_CHARTS);
  });

  it('stays valid at exactly the cap and one past it', () => {
    const atCap = generated({ metrics: sixty.slice(0, MAX_CARDS) });
    assertValid(atCap.definition);
    expect(notesOf(atCap)).toHaveLength(0);
    const over = generated({ metrics: sixty.slice(0, MAX_CARDS + 1) });
    assertValid(over.definition);
    expect(over.summary.leftOut).toHaveLength(1);
  });
});

describe('a profile with no measurements', () => {
  it('is refused explicitly rather than given a near-empty board', () => {
    expect(generateBoard(input({ metrics: [], commands: [command('reboot')] }))).toEqual({
      ok: false,
      reason: 'no-measurements',
    });
  });

  it('ignores blank and repeated keys', () => {
    expect(generateBoard(input({ metrics: [metric('')] })).ok).toBe(false);
    const b = generated({ metrics: [metric('t'), metric('t')] });
    expect(widgetsOf(b, 'latest-card')).toHaveLength(1);
  });
});

describe('calculated metrics', () => {
  it('are included, marked in the card title, and still read by their own key', () => {
    const b = generated({ metrics: [metric('temperature'), metric('heatIndex', { calculated: true })] });
    assertValid(b.definition);
    const cards = widgetsOf(b, 'latest-card');
    expect(cards[0].options.title).toBe('temperature');
    expect(cards[1].options.title).toBe('heatIndex (Calculated)');
    expect(cards[1].options.measurement).toBe('heatIndex');
  });
});

describe('commands', () => {
  it('gives a button to a NORMAL command, with its parameter schema', () => {
    const schema = JSON.stringify([{ name: 'seconds', type: 'INT' }]);
    const b = generated({ commands: [command('flush', 'NORMAL', { name: 'Flush', parameterSchema: schema })] });
    assertValid(b.definition);
    const [btn] = widgetsOf(b, 'command-button');
    expect(btn.options).toMatchObject({ commandName: 'flush', commandLabel: 'Flush', parameterSchema: schema });
    expect(btn.datasource.slot).toBe('device');
  });

  it('skips disruptive, destructive and unknown-impact commands and notes it on the board', () => {
    const b = generated({
      commands: [
        command('flush'),
        command('reboot', 'DISRUPTIVE'),
        command('wipe', 'DESTRUCTIVE'),
        command('mystery', null),
      ],
    });
    assertValid(b.definition);
    expect(widgetsOf(b, 'command-button').map((w) => w.options.commandName)).toEqual(['flush']);
    expect(b.summary.skippedCommands).toEqual(['reboot', 'wipe', 'mystery']);
    expect(notesOf(b)[0].options.text).toBe('3 commands left out');
  });

  it('lays out many buttons without overlap', () => {
    const b = generated({ commands: Array.from({ length: 9 }, (_, i) => command(`c${i}`)) });
    assertValid(b.definition);
    expect(widgetsOf(b, 'command-button')).toHaveLength(9);
  });
});

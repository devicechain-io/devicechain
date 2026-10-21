// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Turns what a device type's profile declares into a dashboard definition made ONLY of widgets the
// console already ships: a device selector, one card per measurement, a chart of the key numeric
// measurements, an alarm table, and a button for each command that is safe to send from a board.
//
// Pure: no I/O, no i18n lookups (the caller passes the words), so the same input always gives the
// same board and the tests can assert it exactly. The output is a plain JSON-able object in the
// current definition format; the flow runs it through the SAME import pipeline as a pasted file
// (parser, option validation, layout lint) before anything is saved, so nothing here is trusted.

export const SCHEMA_VERSION = 3;

// Three rows of four cards. A profile can declare dozens of measurements (60 is real); a board of
// 60 cards is a wall nobody reads, and the editor is one click away for the rest.
export const MAX_CARDS = 12;
// More lines than this on one chart are unreadable; the first few declared are the key ones.
export const MAX_CHART_SERIES = 4;

const COLUMNS = 12;
const SELECTOR_SLOT = 'device';
const NUMERIC = new Set(['DOUBLE', 'INT']);

export interface BoardMetric {
  key: string;
  dataType: string;
  unit?: string | null;
  calculated: boolean;
}

export interface BoardCommand {
  key: string;
  name?: string | null;
  description?: string | null;
  // The parameter descriptors as JSON text, as a command definition stores them.
  parameterSchema?: string | null;
  // NORMAL, DISRUPTIVE or DESTRUCTIVE. Anything else, or none, is treated as NOT safe.
  impact?: string | null;
}

// Every word the board contains, supplied by the caller (translated in the app, literal in tests).
export interface BoardLabels {
  device: string;
  // The selector offers every device of the tenant, not only this type's: a slot cannot yet be
  // narrowed to a device type, so the board says so beside the selector.
  selectorScope: string;
  chart: string;
  alarms: string;
  calculated: string;
  measurementsLeftOut: (shown: number, total: number) => string;
  commandsLeftOut: (count: number) => string;
}

export interface BoardInput {
  title: string;
  metrics: readonly BoardMetric[];
  commands: readonly BoardCommand[];
  // A device of this type to preselect, or none when the type has no devices yet.
  defaultDevice?: string | null;
  labels: BoardLabels;
}

export interface PlannedWidget {
  id: string;
  type: string;
  title: string;
  col: number;
  row: number;
  cols: number;
  rows: number;
}

export interface BoardSummary {
  widgets: PlannedWidget[];
  cards: number;
  // The declared measurements that got no card, in declared order.
  leftOut: string[];
  chartMeasurements: string[];
  commands: string[];
  // Commands not given a button because sending them from a board is not safe.
  skippedCommands: string[];
}

export type GeneratedBoard =
  | { ok: true; definition: Record<string, unknown>; summary: BoardSummary }
  | { ok: false; reason: 'no-measurements' };

type Json = Record<string, unknown>;

function place(col: number, row: number, cols: number, rows: number): Json {
  return { base: { mode: 'grid', col, row, cols, rows } };
}

export function generateBoard(input: BoardInput): GeneratedBoard {
  const { labels } = input;
  // Keys are unique on a profile; a repeat here would make two cards read the same stream.
  const seen = new Set<string>();
  const metrics = input.metrics.filter((m) => m.key !== '' && !seen.has(m.key) && !!seen.add(m.key));
  // A board of nothing but a selector and an alarm table is not what "from this profile" promises,
  // and a silent near-empty board is the worse answer. Refuse, and let the caller say why.
  if (metrics.length === 0) return { ok: false, reason: 'no-measurements' };

  const shown = metrics.slice(0, MAX_CARDS);
  const leftOut = metrics.slice(MAX_CARDS).map((m) => m.key);
  const safe = input.commands.filter((c) => c.impact === 'NORMAL');
  const skippedCommands = input.commands.filter((c) => c.impact !== 'NORMAL').map((c) => c.key);

  const widgets: Json[] = [];
  const planned: PlannedWidget[] = [];
  const add = (w: { id: string; type: string; title: string }, box: [number, number, number, number], rest: Json) => {
    widgets.push({ id: w.id, type: w.type, layout: place(...box), ...rest });
    planned.push({ ...w, col: box[0], row: box[1], cols: box[2], rows: box[3] });
  };
  const slotSource = (measurements: string[]): Json => ({ kind: 'slot', slot: SELECTOR_SLOT, measurements });

  let row = 0;
  add({ id: 'selector', type: 'entity-selector', title: labels.device }, [0, row, 6, 1], {
    options: { title: labels.device, selectionTarget: SELECTOR_SLOT },
  });
  add({ id: 'note-selector', type: 'label', title: labels.selectorScope }, [6, row, 6, 1], {
    options: { text: labels.selectorScope, align: 'left' },
  });
  row += 1;

  const note = (id: string, text: string) => {
    add({ id, type: 'label', title: text }, [0, row, COLUMNS, 1], { options: { text, align: 'left' } });
    row += 1;
  };
  if (leftOut.length > 0) note('note-measurements', labels.measurementsLeftOut(shown.length, metrics.length));

  shown.forEach((m, i) => {
    const title = m.calculated ? `${m.key} (${labels.calculated})` : m.key;
    const options: Json = { title, measurement: m.key };
    if (m.unit) options.unit = m.unit;
    if (m.dataType === 'INT') options.precision = 0;
    add(
      { id: `card-${i + 1}`, type: 'latest-card', title },
      [(i % 4) * 3, row + Math.floor(i / 4) * 2, 3, 2],
      { datasource: slotSource([m.key]), options },
    );
  });
  row += Math.ceil(shown.length / 4) * 2;

  const chartMeasurements = metrics
    .filter((m) => NUMERIC.has(m.dataType))
    .slice(0, MAX_CHART_SERIES)
    .map((m) => m.key);
  if (chartMeasurements.length > 0) {
    add({ id: 'chart', type: 'timeseries-chart', title: labels.chart }, [0, row, 8, 4], {
      datasource: slotSource(chartMeasurements),
      options: { title: labels.chart },
    });
  }
  // Beside the chart when there is one; the whole row otherwise.
  const alarmCols = chartMeasurements.length > 0 ? 4 : COLUMNS;
  add({ id: 'alarms', type: 'alarm-table', title: labels.alarms }, [COLUMNS - alarmCols, row, alarmCols, 4], {
    datasource: slotSource([]),
    options: { title: labels.alarms, state: 'ACTIVE' },
  });
  row += 4;

  if (skippedCommands.length > 0) note('note-commands', labels.commandsLeftOut(skippedCommands.length));
  safe.forEach((c, i) => {
    const title = c.name?.trim() || c.key;
    const options: Json = { title, commandName: c.key, commandLabel: title };
    if (c.parameterSchema) options.parameterSchema = c.parameterSchema;
    add(
      { id: `command-${i + 1}`, type: 'command-button', title },
      [(i % 4) * 3, row + Math.floor(i / 4) * 3, 3, 3],
      { datasource: slotSource([]), options },
    );
  });

  const definition: Json = {
    schemaVersion: SCHEMA_VERSION,
    title: input.title,
    canvas: {
      breakpoints: { base: 0 },
      grid: { columns: COLUMNS, gutter: 8, minCellSize: 56, padding: 0 },
      sizing: 'fill',
    },
    slots: {
      [SELECTOR_SLOT]: {
        label: labels.device,
        type: 'device',
        ...(input.defaultDevice ? { defaultBinding: { kind: 'device', deviceToken: input.defaultDevice } } : {}),
      },
    },
    widgets,
  };

  return {
    ok: true,
    definition,
    summary: {
      widgets: planned,
      cards: shown.length,
      leftOut,
      chartMeasurements,
      commands: safe.map((c) => c.key),
      skippedCommands,
    },
  };
}

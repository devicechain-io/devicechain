// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Turns what a device type's profile declares into a dashboard definition made ONLY of widgets the
// console already ships: a device selector, one card per measurement, a chart per unit of the
// numeric measurements, an alarm table, and a button for each command that is safe to send from a board.
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
// Charts are grouped by unit so every y-axis has ONE scale (humidity near 65 next to a temperature
// near 3 on a shared axis flattens the temperature into a line). A measurement with no unit is
// not known to share a scale with anything, so it gets a chart of its own. More charts than this
// is a wall; the cards still show every measurement.
export const MAX_CHARTS = 4;

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
  // The chart's title stem; the unit (or the measurement, for one with no unit) follows it.
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
  // The device type's token. The selector offers only devices of this type, so a board made for
  // one type never lands on (or lets the viewer wander to) a device of another.
  deviceType?: string | null;
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
  // One entry per chart: the unit it is scaled in (none for a unitless measurement) and its series.
  charts: Array<{ unit: string | null; measurements: string[] }>;
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

// Groups the numeric measurements into charts, each sharing one unit (and so one axis scale).
function planCharts(metrics: readonly BoardMetric[]): Array<{ unit: string | null; measurements: string[] }> {
  const byUnit = new Map<string, { unit: string | null; measurements: string[] }>();
  const charts: Array<{ unit: string | null; measurements: string[] }> = [];
  for (const m of metrics) {
    if (!NUMERIC.has(m.dataType)) continue;
    const unit = m.unit?.trim() || null;
    if (unit === null) {
      charts.push({ unit: null, measurements: [m.key] });
      continue;
    }
    let group = byUnit.get(unit);
    if (!group) {
      group = { unit, measurements: [] };
      byUnit.set(unit, group);
      charts.push(group);
    }
    if (group.measurements.length < MAX_CHART_SERIES) group.measurements.push(m.key);
  }
  return charts.slice(0, MAX_CHARTS);
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
  add({ id: 'selector', type: 'entity-selector', title: labels.device }, [0, row, COLUMNS, 1], {
    options: { title: labels.device, selectionTarget: SELECTOR_SLOT },
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

  const charts = planCharts(metrics);
  charts.forEach((c, i) => {
    const title = `${labels.chart} (${c.unit ?? c.measurements[0]})`;
    const options: Json = { title };
    if (c.unit) options.unit = c.unit;
    add(
      { id: `chart-${i + 1}`, type: 'timeseries-chart', title },
      [(i % 2) * 6, row + Math.floor(i / 2) * 4, 6, 4],
      { datasource: slotSource(c.measurements), options },
    );
  });
  // Two charts per row. The alarm table takes the empty half of an odd last row; otherwise a row
  // of its own.
  const chartRows = Math.ceil(charts.length / 2);
  const beside = charts.length % 2 === 1;
  const alarmCols = beside ? 6 : COLUMNS;
  const alarmRow = beside ? row + (chartRows - 1) * 4 : row + chartRows * 4;
  add({ id: 'alarms', type: 'alarm-table', title: labels.alarms }, [COLUMNS - alarmCols, alarmRow, alarmCols, 4], {
    datasource: slotSource([]),
    options: { title: labels.alarms, state: 'ACTIVE' },
  });
  row = alarmRow + 4;

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
        ...(input.deviceType ? { candidates: { deviceType: input.deviceType } } : {}),
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
      chartMeasurements: charts.flatMap((c) => c.measurements),
      charts,
      commands: safe.map((c) => c.key),
      skippedCommands,
    },
  };
}

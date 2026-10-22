// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the timeseries chart and the fleet table hand their frame to export: the rows they are
// showing, labelled as what they are (a bucket average or a reading), with the unit in the heading, an
// ISO-8601 instant with its zone, and a file name that says which span and which bucket.

import type { FleetMember, KnownWidgetInstance, MeasurementSample } from '@devicechain/dashboards';
import { WidgetSubjectProvider, WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The chart engine needs a canvas; the export reads the samples, not the drawing.
vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return { ...actual, EChart: () => <div data-testid="echart" />, loadChartEngine: async () => undefined };
});

import { fleetExport, samplesExport } from './export-rows';
import { TimeSeriesChart } from './widgets/time-series-chart';
import { FleetTable } from './widgets/fleet-table';

const sample = (name: string, value: number | null, time: string | null, bucketSeconds?: number): MeasurementSample => ({
  id: `${name}-${time}`,
  deviceToken: 'pump-3',
  eventType: 0,
  occurredTime: time,
  name,
  value,
  classifier: null,
  ...(bucketSeconds !== undefined ? { bucketSeconds } : {}),
});

describe('samplesExport', () => {
  const samples = [
    sample('temperature', 22.456, '2026-10-22T14:02:00Z'),
    sample('temperature', 21.04, '2026-10-22T13:00:00Z', 60),
    sample('humidity', 55, '2026-10-22T13:00:00Z', 60),
    sample('temperature', null, '2026-10-22T13:30:00Z'),
    sample('temperature', 99, null),
  ];

  it('is long, oldest first, a row per point, with the unit in the heading and the basis of each point', () => {
    const t = samplesExport({ subject: 'Pump 3', samples, unit: '°C', precision: 1, seriesIs: 'measurement', spanSeconds: 3600 });
    expect(t.columns).toEqual(['Time (UTC)', 'Measurement', 'Value (°C)', 'Basis']);
    expect(t.rows).toEqual([
      ['2026-10-22T13:00:00.000Z', 'humidity', 55, 'avg per 1m'],
      ['2026-10-22T13:00:00.000Z', 'temperature', 21, 'avg per 1m'],
      ['2026-10-22T14:02:00.000Z', 'temperature', 22.5, 'reading'],
    ]);
  });

  it('leaves out a point with no value or no readable time: it is not on the chart', () => {
    const t = samplesExport({ subject: 's', samples, seriesIs: 'measurement' });
    expect(t.rows).toHaveLength(3);
  });

  it('says the span, the window it holds and what the points are, in the file name', () => {
    const t = samplesExport({ subject: 'Pump 3', samples, seriesIs: 'measurement', spanSeconds: 3600 });
    expect(t.name).toBe('Pump 3-1h-20261022T1300Z-20261022T1402Z-avg per 1m-reading');
  });

  it('names no span it does not know, and no basis for an empty chart', () => {
    expect(samplesExport({ subject: 's', samples: [], seriesIs: 'device' })).toEqual({
      name: 's',
      columns: ['Time (UTC)', 'Series', 'Value', 'Basis'],
      rows: [],
    });
  });

  it('writes in the viewer’s language', () => {
    document.documentElement.lang = 'es';
    const t = samplesExport({ subject: 's', samples: samples.slice(1, 2), seriesIs: 'measurement' });
    document.documentElement.lang = 'en';
    expect(t.columns).toEqual(['Hora (UTC)', 'Medición', 'Valor', 'Base']);
    expect(t.rows[0][3]).toBe('prom. por 1m');
  });
});

describe('fleetExport', () => {
  const m = (token: string, name: string | null, temperature?: number): FleetMember => ({
    token,
    name,
    latest: temperature === undefined ? {} : { temperature: { ...sample('temperature', temperature, '2026-10-22T14:00:00Z'), deviceToken: token } },
    seed: 'ready',
    observed: temperature === undefined ? [] : ['temperature'],
  });

  it('is a row per device in the order on screen, the reading and its instant side by side, empty when there is none', () => {
    const t = fleetExport({
      subject: 'Plant',
      members: [m('d-1', 'Unit 1', 21.456), m('d-2', 'Unit 2'), m('d-3', 'A unit', 30)],
      total: 3,
      columns: [{ name: 'temperature', heading: 'Temp (°C)', precision: 1 }],
      sort: { by: null, descending: false },
    });
    expect(t.columns).toEqual(['Device', 'Device token', 'Temp (°C) (latest)', 'Temp (°C) reading time (UTC)']);
    expect(t.rows).toEqual([
      ['A unit', 'd-3', 30, '2026-10-22T14:00:00.000Z'],
      ['Unit 1', 'd-1', 21.5, '2026-10-22T14:00:00.000Z'],
      ['Unit 2', 'd-2', null, null],
    ]);
  });

  it('says in its name when the group is bigger than the devices it holds', () => {
    const t = fleetExport({ subject: 'Plant', members: [m('d-1', 'Unit 1', 1)], total: 812, columns: [], sort: { by: null, descending: false } });
    expect(t.name).toMatch(/^Plant-latest-first-1-of-812-\d{8}T\d{4}Z$/);
  });
});

// ---- Through the widgets' frames -------------------------------------------------------------

let saved: Blob[];
let names: string[];
beforeEach(() => {
  document.documentElement.lang = 'en';
  saved = [];
  names = [];
  vi.spyOn(URL, 'createObjectURL').mockImplementation((b) => (saved.push(b as Blob), 'blob:csv'));
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined);
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    names.push(this.download);
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

async function exportCsv(): Promise<string[]> {
  fireEvent.click(await screen.findByRole('button', { name: 'Export data' }));
  fireEvent.click(await screen.findByRole('menuitem', { name: 'Download CSV' }));
  await vi.waitFor(() => expect(saved).toHaveLength(1));
  const bytes = new Uint8Array(await saved[0].arrayBuffer());
  return new TextDecoder().decode(bytes.slice(3)).trimEnd().split(String.fromCharCode(13, 10));
}

describe('the timeseries chart’s frame', () => {
  const widget = (options: Record<string, unknown>): KnownWidgetInstance =>
    ({
      id: 'ts',
      type: 'timeseries-chart',
      layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
      datasource: { kind: 'device', deviceToken: 'pump-3', measurements: ['temperature'] },
      options,
    }) as unknown as KnownWidgetInstance;
  const data = (samples: MeasurementSample[]) => ({ latest: {}, samples, error: null });

  it('exports the samples it draws, over the board’s span', async () => {
    render(
      <WidgetTimeRangeProvider range={{ spanSeconds: 86_400, paused: false }}>
        <TimeSeriesChart
          widget={widget({ title: 'Temperature', unit: '°C' })}
          data={data([sample('temperature', 21, '2026-10-22T13:00:00Z', 360), sample('temperature', 22, '2026-10-22T14:00:00Z')])}
        />
      </WidgetTimeRangeProvider>,
    );
    expect(await exportCsv()).toEqual([
      'Time (UTC),Measurement,Value (°C),Basis',
      '2026-10-22T13:00:00.000Z,temperature,21,avg per 6m',
      '2026-10-22T14:00:00.000Z,temperature,22,reading',
    ]);
    expect(names[0]).toBe('Temperature-1d-20261022T1300Z-20261022T1400Z-avg-per-6m-reading.csv');
  });

  it('names the widget’s own pinned span, not the board’s, when it has one', async () => {
    render(
      <WidgetTimeRangeProvider range={{ spanSeconds: 86_400, paused: false }}>
        <TimeSeriesChart widget={widget({ title: 'Temperature', windowSeconds: 900 })} data={data([sample('temperature', 21, '2026-10-22T13:00:00Z')])} />
      </WidgetTimeRangeProvider>,
    );
    await exportCsv();
    expect(names[0]).toMatch(/^Temperature-15m-/);
  });

  it('names the device when the widget has no title, and no span off a board', async () => {
    // (a widget with no title still has a title bar while its subject line shows)
    render(
      <WidgetSubjectProvider label="Pump 3">
        <TimeSeriesChart widget={widget({})} data={data([sample('temperature', 21, '2026-10-22T13:00:00Z')])} />
      </WidgetSubjectProvider>,
    );
    await exportCsv();
    expect(names[0]).toBe('pump-3-20261022T1300Z-20261022T1300Z-reading.csv');
  });

  it('has no menu without a title bar to put it in', () => {
    render(<TimeSeriesChart widget={widget({})} data={data([sample('temperature', 21, '2026-10-22T13:00:00Z')])} />);
    expect(screen.queryByRole('button', { name: 'Export data' })).toBeNull();
  });
});

describe('the fleet table’s frame', () => {
  it('exports every device in the order on screen, not the page', async () => {
    const members = Array.from({ length: 30 }, (_, i) => ({
      token: `d-${String(i).padStart(2, '0')}`,
      name: `Unit ${String(i).padStart(2, '0')}`,
      latest: { temperature: { ...sample('temperature', 20 + i, '2026-10-22T14:00:00Z'), deviceToken: `d-${i}` } },
      seed: 'ready' as const,
      observed: ['temperature'],
    }));
    const w = {
      id: 'ft',
      type: 'fleet-table',
      layout: { base: { mode: 'grid', col: 0, row: 0, cols: 12, rows: 6 } },
      datasource: { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' }, measurements: ['temperature'] },
      options: { title: 'Plant', pageSize: 10, sortBy: 'temperature', sortDescending: true, columns: [{ measurement: 'temperature', label: 'Temp', unit: '°C' }] },
    } as unknown as KnownWidgetInstance;
    render(<FleetTable widget={w} data={{ latest: {}, samples: [], error: null, members: { status: 'ready', members, total: 30, truncated: false } }} />);
    expect(document.querySelectorAll('tbody tr')).toHaveLength(10);
    const lines = await exportCsv();
    expect(lines[0]).toBe('Device,Device token,Temp (°C) (latest),Temp (°C) reading time (UTC)');
    expect(lines).toHaveLength(31);
    expect(lines[1]).toBe('Unit 29,d-29,49,2026-10-22T14:00:00.000Z');
    expect(lines[30]).toBe('Unit 00,d-00,20,2026-10-22T14:00:00.000Z');
  });
});

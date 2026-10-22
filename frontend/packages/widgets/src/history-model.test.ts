// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementBucket } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  bucketLabel,
  clampPage,
  columnHeading,
  historyColumns,
  historyExport,
  historyRefreshMs,
  historyRows,
  historyWindow,
  observedNames,
  roundTo,
  spansDays,
  stamp,
} from './history-model';

const b = (bucketStart: string, name: string, avg: number | null): MeasurementBucket => ({ bucketStart, name, avg, min: avg, max: avg, sum: avg, count: avg === null ? 0 : 1 });

describe('the window a history table reads', () => {
  it('is whole minutes ending at the last boundary, in the buckets of the span’s chart', () => {
    const at = Date.parse('2026-10-22T14:05:42Z');
    expect(historyWindow(3600, at)).toEqual({
      startTime: '2026-10-22T13:05:00.000Z',
      endTime: '2026-10-22T14:04:59.999Z',
      intervalSeconds: 60,
    });
    // A day is 240 buckets or fewer: six minutes.
    expect(historyWindow(86_400, at).intervalSeconds).toBe(360);
    expect(bucketLabel(360)).toBe('6m');
    expect(bucketLabel(3600)).toBe('1h');
  });

  it('is read again once per bucket, never more often than a minute', () => {
    expect(historyRefreshMs(900)).toBe(60_000);
    expect(historyRefreshMs(86_400)).toBe(360_000);
  });
});

describe('the columns', () => {
  it('are the author’s list if it names any', () => {
    expect(historyColumns([{ name: 'humidity', unit: '%' }], ['temperature'], ['pressure'])).toEqual([{ name: 'humidity', unit: '%' }]);
  });

  it('else the datasource’s measurements, else what the device reported, sorted', () => {
    expect(historyColumns([], ['temperature', 'humidity'], []).map((c) => c.name)).toEqual(['temperature', 'humidity']);
    expect(historyColumns([], [], ['pressure', 'humidity', 'pressure']).map((c) => c.name)).toEqual(['humidity', 'pressure']);
  });

  it('carry their unit in the heading', () => {
    expect(columnHeading({ name: 'temperature' })).toBe('temperature');
    expect(columnHeading({ name: 'temperature', label: 'Temp', unit: '°C' })).toBe('Temp (°C)');
  });
});

describe('the rows', () => {
  const buckets = [
    b('2026-10-22T13:00:00Z', 'temperature', 20),
    b('2026-10-22T13:00:00Z', 'humidity', 50),
    b('2026-10-22T13:05:00Z', 'temperature', 22),
    b('2026-10-22T13:10:00Z', 'humidity', 52),
    b('2026-10-22T13:15:00Z', 'temperature', null),
    b('2026-10-22T13:20:00Z', 'pressure', 1000),
  ];

  it('are one per bucket start, newest first, holding only the shown columns', () => {
    const rows = historyRows(buckets, ['temperature', 'humidity'], false);
    expect(rows.map((r) => r.bucketStart)).toEqual(['2026-10-22T13:10:00Z', '2026-10-22T13:05:00Z', '2026-10-22T13:00:00Z']);
    expect(Object.fromEntries(rows[2].averages)).toEqual({ temperature: 20, humidity: 50 });
  });

  it('leave a measurement with no readings out of its row, never as zero', () => {
    const rows = historyRows(buckets, ['temperature', 'humidity'], true);
    expect(rows.map((r) => r.bucketStart)[0]).toBe('2026-10-22T13:00:00Z');
    expect(rows[1].averages.has('humidity')).toBe(false);
    expect(rows[1].averages.get('temperature')).toBe(22);
  });

  it('drop a bucket that holds no average for any shown column', () => {
    expect(historyRows([b('2026-10-22T13:15:00Z', 'temperature', null)], ['temperature'], false)).toEqual([]);
  });

  it('drop a bucket whose start cannot be read, and a non-finite average', () => {
    expect(historyRows([b('nonsense', 'temperature', 1), b('2026-10-22T13:00:00Z', 'temperature', Number.NaN)], ['temperature'], false)).toEqual([]);
  });

  it('name the measurements read, for a table with no columns of its own', () => {
    expect(observedNames(buckets).sort()).toEqual(['humidity', 'pressure', 'temperature']);
  });

  it('say when they span days', () => {
    const rows = historyRows([b('2026-10-22T13:00:00Z', 'a', 1), b('2026-10-22T13:05:00Z', 'a', 1)], ['a'], false);
    expect(spansDays(rows, 'UTC')).toBe(false);
    const across = historyRows([b('2026-10-22T23:55:00Z', 'a', 1), b('2026-10-23T00:05:00Z', 'a', 1)], ['a'], false);
    expect(spansDays(across, 'UTC')).toBe(true);
    // The same two instants are one day in a zone where both fall before midnight.
    expect(spansDays(across, 'America/New_York')).toBe(false);
  });
});

describe('paging', () => {
  it('holds the page inside the pages that exist', () => {
    expect(clampPage(5, 25, 10)).toBe(2);
    expect(clampPage(-1, 25, 10)).toBe(0);
    expect(clampPage(3, 0, 10)).toBe(0);
  });
});

describe('rounding for display and export', () => {
  it('rounds to the precision, and leaves a value exact without one', () => {
    expect(roundTo(21.4567, 1)).toBe(21.5);
    expect(roundTo(21.4567, 0)).toBe(21);
    expect(roundTo(21.4567, undefined)).toBe(21.4567);
  });
});

describe('the export', () => {
  const rows = historyRows(
    [b('2026-10-22T13:00:00Z', 'temperature', 20.04), b('2026-10-22T13:00:00Z', 'humidity', 50.5), b('2026-10-22T13:05:00Z', 'temperature', 22)],
    ['temperature', 'humidity'],
    false,
  );
  const table = historyExport({
    subject: 'Pump 3',
    columns: [{ name: 'temperature', label: 'Temp', unit: '°C', precision: 1 }, { name: 'humidity', unit: '%' }],
    rows,
    window: { startTime: '2026-10-22T13:00:00.000Z', endTime: '2026-10-22T13:59:59.999Z', intervalSeconds: 300 },
    precision: undefined,
    heading: { time: 'Bucket start (UTC)', average: (bucket) => `avg per ${bucket}` },
  });

  it('says in every heading that it is an average, per which bucket, with the unit', () => {
    expect(table.columns).toEqual(['Bucket start (UTC)', 'Temp (°C) avg per 5m', 'humidity (%) avg per 5m']);
  });

  it('holds every row in the order on screen, the time an ISO-8601 instant with its zone', () => {
    expect(table.rows).toEqual([
      ['2026-10-22T13:05:00.000Z', 22, null],
      ['2026-10-22T13:00:00.000Z', 20, 50.5],
    ]);
  });

  it('says the span and the bucket in its name', () => {
    expect(table.name).toBe('Pump 3-avg-per-5m-20261022T1300Z-20261022T1400Z');
  });

  it('rounds to the precision shown: the column’s, else the table’s, else none', () => {
    const wide = historyExport({
      subject: 's',
      columns: [{ name: 'temperature' }, { name: 'humidity', precision: 0 }],
      rows,
      window: { startTime: '2026-10-22T13:00:00.000Z', endTime: '2026-10-22T13:59:59.999Z', intervalSeconds: 60 },
      precision: 1,
      heading: { time: 't', average: (x) => x },
    });
    expect(wide.rows[1]).toEqual(['2026-10-22T13:00:00.000Z', 20, 51]);
  });
});

describe('file name stamps', () => {
  it('are UTC to the nearest minute, so a window’s end names the boundary it ends at', () => {
    expect(stamp('2026-10-22T14:04:59.999Z')).toBe('20261022T1405Z');
    expect(stamp('2026-10-22T13:05:00.000Z')).toBe('20261022T1305Z');
  });
});

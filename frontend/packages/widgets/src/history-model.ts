// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The history table's state, decided apart from the drawing and from the hub: which window it reads,
// how the store's buckets become rows, how the rows are ordered and paged, and the CSV it exports.
// Pure, so each rule is a test.
//
// 🔴 EVERY NUMBER HERE IS A BUCKET AVERAGE. The store answers a history read with buckets (a count, a
// minimum, a maximum and a sum of the readings in a time slot) and this table shows each bucket's
// average. It is never a reading and never the device's state at that time, so everything that names
// it (the footer, the column headings, the export's headings and file name) says "avg per <bucket>".

import { bucketSecondsFor, spanLabel, type HistoryWindow, type MeasurementBucket } from '@devicechain/dashboards';
import type { WidgetExportTable } from '@devicechain/widget-sdk';

import { clampPage, pageCount, seedWindow } from './fleet-model';

export { clampPage, pageCount };

export const DEFAULT_PAGE_SIZE = 10;

// What the feed hands the table: the buckets it read for a window, or why it has none.
export type HistoryView =
  | { status: 'loading' | 'unbound' | 'forbidden' | 'unsupported' | 'failed' }
  | { status: 'ready'; buckets: readonly MeasurementBucket[]; window: HistoryWindow };

// How a column is presented: its heading, its unit, and its precision. By measurement name.
export interface HistoryColumn {
  name: string;
  label?: string;
  unit?: string;
  precision?: number;
}

// One row: a bucket's start and the average of each measurement in it. A measurement with no readings
// in the bucket has no entry (not zero, not the previous value).
export interface HistoryRow {
  // RFC 3339, as the store returned it.
  bucketStart: string;
  at: number;
  averages: ReadonlyMap<string, number>;
}

// The window a history table over `spanSeconds` reads at `at` (epoch ms): whole minutes, in the
// buckets of the span's chart (bucketSecondsFor). Delegates to the fleet seed window, which every
// board widget reading a span uses, so the table reads exactly the buckets a chart over the same span
// draws.
export function historyWindow(spanSeconds: number, at: number): HistoryWindow {
  return seedWindow(spanSeconds, at);
}

// How often a live table reads again: one bucket of its span, never more often than once a minute. A
// bucket cannot change between two reads inside it by more than the readings of that slice, and a
// week's span re-read every few seconds would scan a week of summaries to move by a few seconds.
export function historyRefreshMs(spanSeconds: number): number {
  return bucketSecondsFor(spanSeconds) * 1000;
}

// The bucket width as the sentence's unit: `5m`, `1h`, `42m`. Always exact (spanLabel).
export function bucketLabel(bucketSeconds: number): string {
  return spanLabel(bucketSeconds);
}

// The columns to show: the author's list if it names any, else the datasource's measurements, else
// whatever the buckets hold (sorted, so the order is stable between reads).
export function historyColumns(
  authored: readonly HistoryColumn[],
  datasource: readonly string[],
  observed: Iterable<string>,
): HistoryColumn[] {
  if (authored.length > 0) return [...authored];
  const names = datasource.length > 0 ? datasource : [...new Set(observed)].sort();
  return names.map((name) => ({ name }));
}

// Pivots the store's buckets into rows, one per bucket start. A bucket without an average (it holds
// no readings) contributes nothing, and a start with no average for any column is no row. Newest
// first unless `oldestFirst`.
export function historyRows(buckets: readonly MeasurementBucket[], columns: readonly string[], oldestFirst: boolean): HistoryRow[] {
  const shown = new Set(columns);
  const byStart = new Map<string, Map<string, number>>();
  for (const b of buckets) {
    if (!shown.has(b.name) || b.avg == null || !Number.isFinite(b.avg)) continue;
    let row = byStart.get(b.bucketStart);
    if (!row) byStart.set(b.bucketStart, (row = new Map()));
    row.set(b.name, b.avg);
  }
  const rows: HistoryRow[] = [];
  for (const [bucketStart, averages] of byStart) {
    const at = Date.parse(bucketStart);
    if (Number.isNaN(at)) continue;
    rows.push({ bucketStart, at, averages });
  }
  rows.sort((a, b) => (oldestFirst ? a.at - b.at : b.at - a.at));
  return rows;
}

// Every measurement name the buckets hold (for a table whose columns come from what was read).
export function observedNames(buckets: readonly MeasurementBucket[]): string[] {
  return [...new Set(buckets.filter((b) => b.avg != null).map((b) => b.name))];
}

// The heading of a column: its label (else the measurement's name) and its unit.
export function columnHeading(c: HistoryColumn): string {
  return `${c.label ?? c.name}${c.unit ? ` (${c.unit})` : ''}`;
}

// A stored value at the column's precision, or at the board-wide one: what the cell prints, as a
// number, so the export carries what the table shows. With neither set the value is exact.
export function roundTo(value: number, precision: number | undefined): number {
  if (precision === undefined) return value;
  const places = Math.min(100, Math.max(0, Math.trunc(precision)));
  return Number(value.toFixed(places));
}

// `2026-10-22T14:05:00.000Z` -> `20261022T1405Z`, the way a file name carries a time: UTC, to the
// nearest minute (a window's end is the instant just before its boundary, and names the boundary).
export function stamp(iso: string): string {
  const minute = Math.round(Date.parse(iso) / 60_000) * 60_000;
  return new Date(minute).toISOString().replace(/[-:]/g, '').replace(/\d\d\.\d+Z$/, 'Z');
}

export interface HistoryExportInputs {
  // The widget's title, else its device: what the file is called after.
  subject: string;
  columns: readonly HistoryColumn[];
  rows: readonly HistoryRow[];
  window: Pick<HistoryWindow, 'startTime' | 'endTime' | 'intervalSeconds'>;
  precision: number | undefined;
  heading: { time: string; average: (bucket: string) => string };
}

// The CSV of what the table holds: EVERY row (not the page on screen), in the order on screen, one
// column per measurement. The time is the bucket's start as an ISO-8601 instant in UTC (`Z`), and each
// heading carries the unit and says it is an average per bucket. The file name carries the subject,
// the bucket and the window it covers, so a file found later says which span and which bucket it holds.
export function historyExport(i: HistoryExportInputs): WidgetExportTable {
  const bucket = bucketLabel(i.window.intervalSeconds);
  return {
    name: `${i.subject}-avg-per-${bucket}-${stamp(i.window.startTime)}-${stamp(i.window.endTime)}`,
    columns: [i.heading.time, ...i.columns.map((c) => `${columnHeading(c)} ${i.heading.average(bucket)}`)],
    rows: i.rows.map((r) => [
      new Date(r.at).toISOString(),
      ...i.columns.map((c) => {
        const v = r.averages.get(c.name);
        return v === undefined ? null : roundTo(v, c.precision ?? i.precision);
      }),
    ]),
  };
}

// A date is printed only when it tells the rows apart: a table whose buckets all fall on one day
// (in the zone shown) prints times; one that crosses midnight prints dates and times.
export function spansDays(rows: readonly HistoryRow[], timeZone: string | undefined): boolean {
  if (rows.length < 2) return false;
  const day = (at: number) => new Date(at).toLocaleDateString(undefined, timeZone ? { timeZone } : undefined);
  const first = day(rows[0].at);
  return rows.some((r) => day(r.at) !== first);
}

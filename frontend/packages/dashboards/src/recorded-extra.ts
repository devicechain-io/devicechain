// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The parts of RecordedDataSource only some boards use, loaded on first use (recorded.ts
// imports this module dynamically): a board with no map and no stat widget never downloads
// them. Pure functions over the recording's columns; the source owns the cursor and the shift.

import type { MeasurementBucket } from './queries';
import type { BoardRecording } from './recording';
import type { LocationSample } from './types';

// First index whose value is >= x (the array is non-decreasing).
function lowerBound(a: readonly number[], x: number): number {
  let lo = 0;
  let hi = a.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (a[mid] < x) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

// First index whose value is > x (the array is non-decreasing).
function upperBound(a: readonly number[], x: number): number {
  let lo = 0;
  let hi = a.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (a[mid] <= x) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

// One series: its name and its rows (indices into the measurement columns), with their times.
export interface SeriesRows {
  name: string;
  rows: number[];
  times: number[];
}

// foldBuckets groups each series' rows with times in [fromMs, toMs] (recording offsets) into
// buckets `widthMs` wide, aligned on the epoch at `originMs + t` (the source's own clock),
// each with its exact count, minimum, maximum and sum. A bucket that holds no row is not
// returned. Ordered by bucket start and then name, as the event store orders them.
export function foldBuckets(
  rec: BoardRecording,
  series: readonly SeriesRows[],
  fromMs: number,
  toMs: number,
  originMs: number,
  widthMs: number,
): MeasurementBucket[] {
  const { t, v } = rec.measurements;
  const out: MeasurementBucket[] = [];
  for (const s of series) {
    const byStart = new Map<number, { bucketStart: string; name: string; avg: number; min: number; max: number; sum: number; count: number }>();
    for (let i = lowerBound(s.times, fromMs), end = upperBound(s.times, toMs); i < end; i++) {
      const row = s.rows[i];
      const start = Math.floor((originMs + t[row]) / widthMs) * widthMs;
      const value = v[row];
      const b = byStart.get(start);
      if (!b) {
        byStart.set(start, { bucketStart: new Date(start).toISOString(), name: s.name, avg: value, min: value, max: value, sum: value, count: 1 });
      } else {
        b.count += 1;
        b.sum += value;
        if (value < b.min) b.min = value;
        if (value > b.max) b.max = value;
        b.avg = b.sum / b.count;
      }
    }
    out.push(...byStart.values());
  }
  out.sort((a, b) => (a.bucketStart < b.bucketStart ? -1 : a.bucketStart > b.bucketStart ? 1 : a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return out;
}

// lastPositions is each device's last position at `cursor` (absent for a device not yet
// located), and a signature that changes exactly when one of them moves on to a new fix.
export function lastPositions(
  rec: BoardRecording,
  positions: ReadonlyMap<string, { rows: number[]; times: number[] }>,
  tokens: readonly string[],
  cursor: number,
  iso: (offsetMs: number) => string,
): { signature: string; locations: LocationSample[] } {
  const loc = rec.locations;
  const rows: Array<[string, number]> = [];
  for (const token of tokens) {
    const p = positions.get(token);
    if (!p) continue;
    const at = upperBound(p.times, cursor) - 1;
    if (at >= 0) rows.push([token, p.rows[at]]);
  }
  return {
    signature: rows.map(([, row]) => row).join(','),
    locations: rows.map(([token, row]) => ({
      id: `${token}|location|${row}`,
      deviceToken: token,
      latitude: loc.lat[row],
      longitude: loc.lon[row],
      elevation: null,
      accuracy: null,
      speed: loc.spd[row],
      heading: loc.hdg[row],
      occurredTime: iso(loc.t[row]),
    })),
  };
}

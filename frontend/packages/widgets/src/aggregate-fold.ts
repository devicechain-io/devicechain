// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Aggregates over a span, folded from the backend's time buckets. Each bucket carries the exact
// count, minimum, maximum and sum of the readings in it, so the span's aggregates are exact too:
// the min of the mins, the max of the maxes, the sum of the sums, the count of the counts, and a
// mean that is the total sum over the total count (which is the count-weighted mean of the
// buckets' averages, without ever reading an average).
//
// 🔴 A MINIMUM OR MAXIMUM IS NEVER DERIVED FROM AVERAGES. A bucket's average hides its extremes, so
// the "max of the avgs" is a plausible number that is not the maximum. A bucket that holds
// readings but lacks a field the aggregate needs is therefore a loud failure here, never a
// quiet substitution.

import {
  bucketSecondsFor,
  type AggregateTrendPoint,
  type HistoryWindow,
  type MeasurementAggregate,
  type MeasurementBucket,
} from '@devicechain/dashboards';

export class AggregateFieldsError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'AggregateFieldsError';
  }
}

// aggregateBuckets folds the buckets (any number of devices) into one aggregate per measurement
// name. A name with no readings in any bucket is absent from the result, not zero.
export function aggregateBuckets(buckets: readonly MeasurementBucket[]): Record<string, MeasurementAggregate> {
  const out: Record<string, MeasurementAggregate> = {};
  for (const b of buckets) {
    if (typeof b.count !== 'number') {
      throw new AggregateFieldsError(`the bucket at ${b.bucketStart} for "${b.name}" carries no reading count`);
    }
    if (b.count === 0) continue;
    const { min, max, sum } = b;
    if (min == null || max == null || sum == null) {
      const missing = [min == null && 'min', max == null && 'max', sum == null && 'sum'].filter(Boolean);
      throw new AggregateFieldsError(
        `the bucket at ${b.bucketStart} for "${b.name}" holds ${b.count} readings but no ${missing.join(', ')}`,
      );
    }
    const a = out[b.name];
    if (!a) out[b.name] = { count: b.count, min, max, sum, avg: sum / b.count };
    else {
      a.count += b.count;
      a.sum += sum;
      a.min = Math.min(a.min, min);
      a.max = Math.max(a.max, max);
      a.avg = a.sum / a.count;
    }
  }
  return out;
}

// aggregateSeries lays the same buckets out as a trend: per measurement name, one point per bucket
// that holds readings, oldest first. Buckets of several devices at one instant merge exactly as
// aggregateBuckets merges them. Call it only on buckets aggregateBuckets accepted (it does not
// re-validate), so a bucket that cannot be trusted has already failed the read.
export function aggregateSeries(buckets: readonly MeasurementBucket[]): Record<string, AggregateTrendPoint[]> {
  const byName = new Map<string, Map<string, AggregateTrendPoint>>();
  for (const b of buckets) {
    if (!b.count || b.min == null || b.max == null || b.sum == null) continue;
    let points = byName.get(b.name);
    if (!points) byName.set(b.name, (points = new Map()));
    const at = points.get(b.bucketStart);
    if (!at) {
      points.set(b.bucketStart, { bucketStart: b.bucketStart, count: b.count, min: b.min, max: b.max, sum: b.sum, avg: b.sum / b.count });
    } else {
      at.count += b.count;
      at.sum += b.sum;
      at.min = Math.min(at.min, b.min);
      at.max = Math.max(at.max, b.max);
      at.avg = at.sum / at.count;
    }
  }
  const out: Record<string, AggregateTrendPoint[]> = {};
  for (const [name, points] of byName) {
    out[name] = [...points.values()].sort((a, b) => Date.parse(a.bucketStart) - Date.parse(b.bucketStart));
  }
  return out;
}

// ---- The window a stat reads ------------------------------------------------------------------

const MINUTE_MS = 60_000;

// aggregateRefreshMs is how often a live stat reads again: one bucket of the span's chart
// (bucketSecondsFor), so never more often than once a minute. Finer is wasted -- the window it
// reads moves in whole minutes (aggregateWindow) -- and a 7-day figure re-read every few seconds
// costs the event store a scan of a week of summaries to move by a few seconds' readings.
export function aggregateRefreshMs(spanSeconds: number): number {
  return bucketSecondsFor(spanSeconds) * 1000;
}

export interface AggregateWindow {
  window: HistoryWindow;
  // The length the window actually covers, in seconds: the span, or less when the span is not a
  // whole number of minutes. This, not the span asked for, is what the card says it covers.
  spanSeconds: number;
}

// aggregateWindow is the window a stat over `spanSeconds` reads at `at` (epoch ms): whole minutes,
// ending at the last minute boundary at or before `at`.
//
// 🔴 Whole minutes because the event store answers from one-minute summaries, and a summary is
// read whole: a window starting mid-minute would also count the readings from that minute's
// start, BEFORE the span, so "Maximum · 1h" could be the maximum of an hour and a minute. Both
// edges fall on minute boundaries instead, and the end is asked for as the instant just before
// its boundary (the store's end is inclusive), so the summaries read are exactly the window's.
// The start rounds UP, so a span that is not a whole number of minutes covers less, never more,
// and says so (`spanSeconds`).
//
// Live cards read again on the boundaries of aggregateRefreshMs (a whole number of minutes), so
// every card of one span ends its window on the same boundary: cards that read the same
// measurement ask the same question at the same moment, and the hub answers them with one read.
// One bucket wide: the card needs only the totals, so the store returns one or two rows per
// measurement rather than a chart's worth.
//
// `bucketSeconds` asks for the window in finer buckets (a trend needs a point per bucket); the
// totals fold identically from any number of buckets, so the figure is the same either way. Unset
// the window is one bucket wide, as above.
export function aggregateWindow(spanSeconds: number, at: number, bucketSeconds?: number): AggregateWindow {
  const end = Math.floor(at / MINUTE_MS) * MINUTE_MS;
  const start = Math.ceil((end - spanSeconds * 1000) / MINUTE_MS) * MINUTE_MS;
  const covered = (end - start) / 1000;
  return {
    window: {
      startTime: new Date(start).toISOString(),
      endTime: new Date(end - 1).toISOString(),
      intervalSeconds: Math.max(60, bucketSeconds === undefined ? covered : Math.min(covered, bucketSeconds)),
    },
    spanSeconds: covered,
  };
}

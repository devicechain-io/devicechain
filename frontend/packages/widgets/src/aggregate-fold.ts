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

import type { MeasurementAggregate, MeasurementBucket } from '@devicechain/dashboards';

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

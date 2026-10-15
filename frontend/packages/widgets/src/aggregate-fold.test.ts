// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementBucket } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { AggregateFieldsError, aggregateBuckets } from './aggregate-fold';

const bucket = (over: Partial<MeasurementBucket> & Pick<MeasurementBucket, 'avg'>): MeasurementBucket => ({
  bucketStart: '2026-10-14T12:00:00Z',
  name: 'temperature',
  ...over,
});

// Three buckets with deliberately unequal counts, so a mean taken over the buckets' averages
// (rather than over the readings) lands somewhere else.
//   bucket A: 1 reading  of 10
//   bucket B: 1 reading  of 20
//   bucket C: 8 readings between 40 and 60 averaging 50
const A = bucket({ avg: 10, min: 10, max: 10, sum: 10, count: 1 });
const B = bucket({ avg: 20, min: 20, max: 20, sum: 20, count: 1 });
const C = bucket({ avg: 50, min: 40, max: 60, sum: 400, count: 8 });

describe('aggregateBuckets', () => {
  it('takes the min of the mins, the max of the maxes, the sum of the sums and the total count', () => {
    expect(aggregateBuckets([A, B, C]).temperature).toMatchObject({ min: 10, max: 60, sum: 430, count: 10 });
  });

  it('weights the mean by the readings, not by the buckets', () => {
    const a = aggregateBuckets([A, B, C]).temperature;
    expect(a.avg).toBe(43);
    // The count-weighted mean of the bucket averages is the same figure: (10*1 + 20*1 + 50*8) / 10.
    expect(a.avg).toBe((10 * 1 + 20 * 1 + 50 * 8) / 10);
    // ...and the plain mean of the three averages is the wrong answer this guards against.
    expect((10 + 20 + 50) / 3).not.toBe(a.avg);
  });

  it('never derives an extreme from the averages: the max is above every bucket average', () => {
    const a = aggregateBuckets([A, B, C]).temperature;
    expect(Math.max(...[A, B, C].map((b) => b.avg as number))).toBe(50);
    expect(a.max).toBe(60);
    expect(a.min).toBe(10);
  });

  it('keeps each measurement apart', () => {
    const out = aggregateBuckets([A, bucket({ name: 'humidity', avg: 70, min: 65, max: 75, sum: 140, count: 2 })]);
    expect(Object.keys(out).sort()).toEqual(['humidity', 'temperature']);
    expect(out.humidity).toMatchObject({ min: 65, max: 75, avg: 70, count: 2 });
  });

  it('leaves out a measurement with no readings rather than reporting zeros', () => {
    expect(aggregateBuckets([bucket({ avg: null, min: null, max: null, sum: null, count: 0 })])).toEqual({});
    expect(aggregateBuckets([])).toEqual({});
  });

  it('skips an empty bucket among full ones', () => {
    const empty = bucket({ avg: null, min: null, max: null, sum: null, count: 0 });
    expect(aggregateBuckets([A, empty, B]).temperature).toMatchObject({ min: 10, max: 20, count: 2, avg: 15 });
  });

  it('refuses a bucket that holds readings but no extremes, naming what is missing', () => {
    const noMax = bucket({ avg: 5, min: 1, max: null, sum: 10, count: 2 });
    expect(() => aggregateBuckets([A, noMax])).toThrow(AggregateFieldsError);
    expect(() => aggregateBuckets([A, noMax])).toThrow(/holds 2 readings but no max/);
    expect(() => aggregateBuckets([bucket({ avg: 5, count: 2 })])).toThrow(/no min, max, sum/);
  });

  it('refuses a bucket from a server that does not send the count', () => {
    expect(() => aggregateBuckets([bucket({ avg: 5, min: 5, max: 5, sum: 5 })])).toThrow(/no reading count/);
  });
});

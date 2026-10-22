// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The preview source answers a bucketed read the way the store does: one bucket per interval when the
// window is wider than the interval (a history table, a stat card's trend), one bucket for the window
// when it is not (a stat card's figure).

import { describe, expect, it } from 'vitest';

import { SyntheticDataSource } from './synthetic';
import type { DatasourceSelector } from './types';

const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature', 'humidity'] };
const HOUR = { startTime: '2026-10-22T13:00:00.000Z', endTime: '2026-10-22T13:59:59.999Z' };

describe('SyntheticDataSource.bucketMeasurements', () => {
  it('answers one bucket per interval, per measurement, aligned on the interval', async () => {
    const buckets = await new SyntheticDataSource().bucketMeasurements(ds, { ...HOUR, intervalSeconds: 600 });
    expect(buckets).toHaveLength(12);
    const temperature = buckets.filter((b) => b.name === 'temperature');
    expect(temperature.map((b) => b.bucketStart)[0]).toBe('2026-10-22T13:00:00.000Z');
    expect(temperature.map((b) => b.bucketStart)[5]).toBe('2026-10-22T13:50:00.000Z');
    for (const b of buckets) {
      expect(b.count).toBeGreaterThan(0);
      expect(b.min!).toBeLessThanOrEqual(b.avg!);
      expect(b.avg!).toBeLessThanOrEqual(b.max!);
    }
  });

  it('answers one bucket for the window when the interval is as wide as it', async () => {
    const buckets = await new SyntheticDataSource().bucketMeasurements(ds, { ...HOUR, intervalSeconds: 3600 });
    expect(buckets.map((b) => [b.name, b.bucketStart])).toEqual([
      ['temperature', HOUR.startTime],
      ['humidity', HOUR.startTime],
    ]);
  });

  it('bounds the preview, whatever the window and interval', async () => {
    const buckets = await new SyntheticDataSource().bucketMeasurements(
      { ...ds, measurements: ['temperature'] },
      { startTime: '2026-01-01T00:00:00.000Z', endTime: '2026-10-22T00:00:00.000Z', intervalSeconds: 60 },
    );
    expect(buckets.length).toBeLessThanOrEqual(500);
    // The cap drops the OLDEST buckets: the preview still ends where the window ends.
    expect(buckets[buckets.length - 1].bucketStart).toBe('2026-10-21T23:59:00.000Z');
    expect(buckets[0].bucketStart).toBe(new Date(Date.parse('2026-10-21T23:59:00.000Z') - 499 * 60_000).toISOString());
  });

  it('counts a bucket that starts exactly at the window end out, and one that holds the window start in', async () => {
    const at = (startTime: string, endTime: string) =>
      new SyntheticDataSource().bucketMeasurements({ ...ds, measurements: ['t'] }, { startTime, endTime, intervalSeconds: 600 });
    // [13:00, 14:00): six buckets, 13:00 .. 13:50.
    const exact = await at('2026-10-22T13:00:00.000Z', '2026-10-22T14:00:00.000Z');
    expect(exact.map((b) => b.bucketStart)).toHaveLength(6);
    expect(exact[5].bucketStart).toBe('2026-10-22T13:50:00.000Z');
    // A window starting mid-bucket includes the bucket it starts in, aligned on the interval.
    const mid = await at('2026-10-22T13:05:00.000Z', '2026-10-22T13:59:59.999Z');
    expect(mid[0].bucketStart).toBe('2026-10-22T13:00:00.000Z');
    expect(mid).toHaveLength(6);
  });
});

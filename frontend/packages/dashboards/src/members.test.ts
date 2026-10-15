// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The two pieces of the fleet read that carry no I/O: folding a device's buckets into its seed, and
// the limiter that bounds how many reads are in flight.

import { describe, expect, it } from 'vitest';

import { createLimiter, foldMemberSeed, SEED_CANCELLED } from './members';
import type { MeasurementBucket } from './queries';

const b = (name: string, bucketStart: string, avg: number | null, count: number | undefined = 5): MeasurementBucket => ({
  bucketStart,
  name,
  avg,
  count,
});

describe('foldMemberSeed', () => {
  it('keeps the NEWEST bucket per measurement, whatever order the store returned them in', () => {
    const seed = foldMemberSeed('d-1', [
      b('t', '2026-10-14T11:02:00Z', 22),
      b('t', '2026-10-14T11:03:00Z', 23),
      b('t', '2026-10-14T11:01:00Z', 21),
    ]);
    expect(seed.latest.t.value).toBe(23);
    expect(seed.latest.t.occurredTime).toBe('2026-10-14T11:03:00Z');
    expect(seed.latest.t.deviceToken).toBe('d-1');
  });

  it('keeps each measurement apart, and records every name the device reported as observed', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T11:00:00Z', 20), b('h', '2026-10-14T11:00:00Z', 55)]);
    expect(Object.keys(seed.latest).sort()).toEqual(['h', 't']);
    expect(seed.observed.sort()).toEqual(['h', 't']);
  });

  it('skips a bucket with no mean when choosing the latest, yet still counts its name as observed', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T11:00:00Z', 20), b('t', '2026-10-14T11:05:00Z', null, 3)]);
    expect(seed.latest.t.value).toBe(20);
    expect(seed.observed).toEqual(['t']);
  });

  it('does not count an empty bucket as the device reporting that measurement', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T11:00:00Z', null, 0)]);
    expect(seed.observed).toEqual([]);
    expect(seed.latest).toEqual({});
  });

  it('is empty for a device with no buckets at all', () => {
    expect(foldMemberSeed('d-1', [])).toEqual({ latest: {}, observed: [] });
  });
});

describe('createLimiter', () => {
  it('never runs more than the limit at once, and runs everything', async () => {
    const run = createLimiter(3);
    let active = 0;
    let peak = 0;
    const task = async () => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise((r) => setTimeout(r, 2));
      active -= 1;
      return 1;
    };
    const results = await Promise.all(Array.from({ length: 20 }, () => run(task)));
    expect(results.length).toBe(20);
    expect(peak).toBe(3);
  });

  it('drops a task whose asker went away before its turn, without running it', async () => {
    const run = createLimiter(1);
    let cancelled = false;
    let ran = 0;
    const first = run(async () => {
      await new Promise((r) => setTimeout(r, 5));
      ran += 1;
    });
    const second = run(
      async () => void (ran += 1),
      () => cancelled,
    );
    cancelled = true;
    await first;
    await expect(second).rejects.toBe(SEED_CANCELLED);
    expect(ran).toBe(1);
  });

  it('keeps going after a task fails', async () => {
    const run = createLimiter(1);
    await expect(run(async () => Promise.reject(new Error('x')))).rejects.toThrow('x');
    await expect(run(async () => 7)).resolves.toBe(7);
  });
});

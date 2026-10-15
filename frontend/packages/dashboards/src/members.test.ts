// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The piece of the fleet read that carries no I/O: the limiter that bounds how many reads are in
// flight. (Folding a device's buckets into its seed is the widget's plan: fleet-seed.test.ts.)

import { describe, expect, it } from 'vitest';

import { createLimiter, SEED_CANCELLED } from './members';

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

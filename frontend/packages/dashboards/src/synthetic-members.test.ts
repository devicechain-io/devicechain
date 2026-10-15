// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The preview source answers a fleet widget the way the live hub does, so a fleet table authored in
// the editor shows a fleet: a roster of out-of-phase devices, each with its own samples, one that
// never reports its second measurement, and an empty roster for an unbound slot.

import { afterEach, describe, expect, it, vi } from 'vitest';

import type { MemberRoster } from './members';
import { SyntheticDataSource } from './synthetic';
import type { DatasourceSelector, MeasurementSample } from './types';

afterEach(() => vi.useRealTimers());

const ds: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'r', targetType: 'area', targetToken: 'z' },
  measurements: ['temperature', 'humidity'],
};

describe('SyntheticDataSource.subscribeMembers', () => {
  it('delivers a roster before any sample, with names and a total equal to its size', () => {
    const order: string[] = [];
    let roster: MemberRoster | undefined;
    const dispose = new SyntheticDataSource().subscribeMembers(ds, {
      roster: (r) => {
        order.push('roster');
        roster = r;
      },
      next: () => order.push('sample'),
    });
    dispose();
    expect(order[0]).toBe('roster');
    expect(roster!.members.length).toBeGreaterThan(1);
    expect(roster!.total).toBe(roster!.members.length);
    expect(roster!.members.every((m) => m.name)).toBe(true);
  });

  it('gives every device its own values for the same measurement name', () => {
    const got: MeasurementSample[] = [];
    new SyntheticDataSource().subscribeMembers(ds, { roster: () => {}, next: (s) => got.push(s) })();
    const temperature = got.filter((s) => s.name === 'temperature');
    expect(new Set(temperature.map((s) => s.deviceToken)).size).toBe(temperature.length);
    expect(new Set(temperature.map((s) => s.value)).size).toBeGreaterThan(1);
  });

  it('has one device that never reports its second measurement, to preview that empty cell', () => {
    const got: MeasurementSample[] = [];
    new SyntheticDataSource().subscribeMembers(ds, { roster: () => {}, next: (s) => got.push(s) })();
    const humidity = new Set(got.filter((s) => s.name === 'humidity').map((s) => s.deviceToken));
    const temperature = new Set(got.filter((s) => s.name === 'temperature').map((s) => s.deviceToken));
    expect([...temperature].filter((t) => !humidity.has(t))).toEqual(['synthetic-4']);
  });

  it('keeps reporting on its interval until disposed', () => {
    vi.useFakeTimers();
    let n = 0;
    const dispose = new SyntheticDataSource({ intervalMs: 1000 }).subscribeMembers(ds, { roster: () => {}, next: () => n++ });
    const first = n;
    vi.advanceTimersByTime(3000);
    expect(n).toBeGreaterThan(first);
    dispose();
    const after = n;
    vi.advanceTimersByTime(3000);
    expect(n).toBe(after);
  });

  it('has no members for an unbound slot, and says so', () => {
    let roster: MemberRoster | undefined;
    new SyntheticDataSource().subscribeMembers({ kind: 'unbound', measurements: ['t'] }, { roster: (r) => (roster = r), next: () => {} })();
    expect(roster).toEqual({ members: [], total: 0, unbound: true });
  });

  it('settles every seed empty: its history is its live feed', async () => {
    const seen: string[] = [];
    const plan = { seed: () => Promise.reject(new Error('a preview reads no history')) };
    await new SyntheticDataSource().readMemberSeeds(['a', 'b'], plan, (t, seed) => {
      expect(seed).toEqual({ latest: {}, observed: [] });
      seen.push(t);
    });
    expect(seen).toEqual(['a', 'b']);
  });
});

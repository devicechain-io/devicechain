// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The preview source answers a presence widget the way the live hub does: the roster first, then a
// snapshot of device-state in which every state a presence widget draws appears (online, offline, and
// a device with no row at all), and nothing for an unbound slot.

import { afterEach, describe, expect, it, vi } from 'vitest';

import type { MemberRoster } from './members';
import { SyntheticDataSource } from './synthetic';
import type { DatasourceSelector, DevicePresence } from './types';

afterEach(() => vi.useRealTimers());

const ds: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'r', targetType: 'area', targetToken: 'z' },
  measurements: [],
};

function run(selector: DatasourceSelector) {
  const order: string[] = [];
  let roster: MemberRoster | undefined;
  const snapshots: DevicePresence[][] = [];
  const dispose = new SyntheticDataSource().subscribePresence(selector, {
    roster: (r) => {
      order.push('roster');
      roster = r;
    },
    next: (s) => {
      order.push('snapshot');
      snapshots.push([...s]);
    },
  });
  dispose();
  return { order, roster: roster!, snapshots };
}

describe('SyntheticDataSource.subscribePresence', () => {
  it('delivers the roster before any snapshot', () => {
    const { order, roster } = run(ds);
    expect(order.slice(0, 2)).toEqual(['roster', 'snapshot']);
    expect(roster.members.length).toBeGreaterThan(1);
  });

  it('previews every state: some devices online, one offline, one with no state row', () => {
    const { roster, snapshots } = run(ds);
    const states = new Map(snapshots[0].map((s) => [s.deviceToken, s]));
    const kinds = roster.members.map((m) => {
      const s = states.get(m.token);
      return s === undefined ? 'unknown' : s.active ? 'online' : 'offline';
    });
    expect(new Set(kinds)).toEqual(new Set(['online', 'offline', 'unknown']));
  });

  it('rosters a bound device as itself, online and just seen', () => {
    const { roster, snapshots } = run({ kind: 'device', deviceToken: 'd-9', measurements: [] });
    expect(roster.members.map((m) => m.token)).toEqual(['d-9']);
    expect(snapshots[0]).toMatchObject([{ deviceToken: 'd-9', active: true }]);
  });

  it('delivers an empty roster for an unbound slot', () => {
    const { roster, snapshots } = run({ kind: 'unbound', measurements: [] } as unknown as DatasourceSelector);
    expect(roster.members).toEqual([]);
    expect(snapshots[0]).toEqual([]);
  });

  it('stops its timer when disposed', () => {
    vi.useFakeTimers();
    const source = new SyntheticDataSource();
    let snapshots = 0;
    const dispose = source.subscribePresence(ds, { roster: () => {}, next: () => snapshots++ });
    dispose();
    vi.advanceTimersByTime(60_000);
    expect(snapshots).toBe(1);
  });
});

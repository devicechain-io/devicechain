// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver } from './hub';
import type { MemberRef, PresenceSink } from './members';
import type { DatasourceSelector, DevicePresence } from './types';

// Controllable fakes of the SDK's WIRE only (see hub-locations.test.ts): gql() is a vi.fn a test
// drives, and subscribe() is recorded so the test can assert the presence channel opens NO socket
// (device-state has no presence subscription). isForbiddenError stays the real classifier, because it
// decides whether a refusal becomes a state or an error.
const h = vi.hoisted(() => ({
  subs: [] as Array<{ closed: boolean }>,
  gql: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    subscribe: () => {
      const entry = { closed: false };
      h.subs.push(entry);
      return () => {
        entry.closed = true;
      };
    },
    gql: (...args: unknown[]) => h.gql(...args),
  };
});

import { GraphQLRequestError } from '@devicechain/client';

const state = (deviceToken: string, over: Partial<DevicePresence> = {}): DevicePresence => ({
  deviceToken,
  active: true,
  lastActivityTime: '2026-10-21T11:59:00Z',
  inactivityTimeout: 600,
  ...over,
});
const result = (rows: DevicePresence[]) => ({ deviceStatesByDeviceToken: rows });

function newResolver(devices: string[] = [], members?: MemberRef[]): DeviceResolver {
  return {
    devicesForAnchor: vi.fn(async () => devices),
    deviceExists: vi.fn(async () => true),
    ...(members ? { anchorMembers: vi.fn(async () => ({ members, total: members.length })) } : {}),
  };
}

const ONE: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: [] };
const GROUP: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' },
  measurements: [],
};

function recorder() {
  const rosters: Array<{ tokens: string[]; total: number }> = [];
  const snapshots: DevicePresence[][] = [];
  const sink: Required<PresenceSink> & { forbiddenCount: number; errors: unknown[] } = {
    forbiddenCount: 0,
    errors: [],
    roster: (r) => rosters.push({ tokens: r.members.map((m) => m.token), total: r.total }),
    next: (s) => snapshots.push([...s]),
    forbidden() {
      sink.forbiddenCount++;
    },
    error: (e) => sink.errors.push(e),
  };
  return { sink, rosters, snapshots };
}

// A tick for the async resolution + query chain; no timer fires under the 15 s poll.
const settle = () => vi.advanceTimersByTimeAsync(5);
const tokensOf = (call: number) => (h.gql.mock.calls[call][2] as { deviceTokens: string[] }).deviceTokens;

beforeEach(() => {
  h.subs.length = 0;
  h.gql.mockReset();
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('DashboardHub presence channel', () => {
  it('delivers the roster, then the device-state of its members, from device-state and over no socket', async () => {
    h.gql.mockResolvedValue(result([state('d-1')]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink);
    await settle();
    expect(r.rosters).toEqual([{ tokens: ['d-1'], total: 1 }]);
    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(h.gql.mock.calls[0][0]).toBe('device-state');
    expect(tokensOf(0)).toEqual(['d-1']);
    expect(r.snapshots).toEqual([[state('d-1')]]);
    expect(h.subs).toHaveLength(0);
  });

  it('reads an anchor through its member roster, whole in ONE query however many members', async () => {
    const members = Array.from({ length: 40 }, (_, i) => ({ token: `d-${i}`, name: `D${i}` }));
    h.gql.mockResolvedValue(result(members.map((m) => state(m.token))));
    const hub = new DashboardHub({ resolver: newResolver([], members) });
    const r = recorder();
    hub.subscribePresence(GROUP, r.sink);
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(tokensOf(0)).toHaveLength(40);
    expect(r.snapshots[0]).toHaveLength(40);
  });

  it('splits a roster past the service limit of 1000 tokens into several reads and joins them', async () => {
    const members = Array.from({ length: 1500 }, (_, i) => ({ token: `d-${i}`, name: null }));
    h.gql.mockImplementation(async (_area: string, _doc: unknown, vars: { deviceTokens: string[] }) =>
      result(vars.deviceTokens.map((t) => state(t))),
    );
    const hub = new DashboardHub({ resolver: newResolver([], members) });
    const r = recorder();
    hub.subscribePresence(GROUP, r.sink);
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(2);
    expect(tokensOf(0)).toHaveLength(1000);
    expect(tokensOf(1)).toHaveLength(500);
    expect(r.snapshots[0]).toHaveLength(1500);
  });

  it('passes through a device the service omits: no row is not an error', async () => {
    h.gql.mockResolvedValue(result([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink);
    await settle();
    expect(r.snapshots).toEqual([[]]);
    expect(r.sink.errors).toEqual([]);
  });

  it('re-reads on an interval, and a poll read changes what is delivered', async () => {
    h.gql.mockResolvedValueOnce(result([state('d-1')])).mockResolvedValueOnce(result([state('d-1', { active: false })]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink);
    await settle();
    await vi.advanceTimersByTimeAsync(15_000);
    expect(h.gql).toHaveBeenCalledTimes(2);
    expect(r.snapshots.map((s) => s[0].active)).toEqual([true, false]);
  });

  it('reads nothing while the tab is hidden, and reads at once when it is shown again', async () => {
    const doc = Object.assign(new EventTarget(), { hidden: false });
    vi.stubGlobal('document', doc);
    try {
      h.gql.mockResolvedValue(result([state('d-1')]));
      const hub = new DashboardHub({ resolver: newResolver() });
      const r = recorder();
      const dispose = hub.subscribePresence(ONE, r.sink);
      await settle();
      expect(h.gql).toHaveBeenCalledTimes(1);
      doc.hidden = true;
      doc.dispatchEvent(new Event('visibilitychange'));
      await vi.advanceTimersByTimeAsync(60_000);
      expect(h.gql).toHaveBeenCalledTimes(1);
      doc.hidden = false;
      doc.dispatchEvent(new Event('visibilitychange'));
      await settle();
      expect(h.gql).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(15_000);
      expect(h.gql).toHaveBeenCalledTimes(3);
      dispose();
      doc.dispatchEvent(new Event('visibilitychange'));
      await vi.advanceTimersByTimeAsync(60_000);
      expect(h.gql).toHaveBeenCalledTimes(3);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('opens no poll for an unbound slot, and says so with an empty snapshot', async () => {
    const hub = new DashboardHub({ resolver: newResolver([]) });
    const r = recorder();
    hub.subscribePresence({ kind: 'unbound', measurements: [] } as unknown as DatasourceSelector, r.sink);
    await settle();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(r.snapshots).toEqual([[]]);
    expect(r.rosters[0].tokens).toEqual([]);
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('reports a refusal as forbidden, not as an error', async () => {
    h.gql.mockRejectedValue(new GraphQLRequestError('GraphQL error', 200, [{ message: 'forbidden: missing required authority' }]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink);
    await settle();
    expect(r.sink.forbiddenCount).toBe(1);
    expect(r.sink.errors).toEqual([]);
  });

  it('reports any other failure as an error', async () => {
    h.gql.mockRejectedValue(new Error('device-state is down'));
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink);
    await settle();
    expect(r.sink.errors).toHaveLength(1);
    expect(r.sink.forbiddenCount).toBe(0);
  });

  it('drops a slow read that lands after a newer one', async () => {
    let releaseFirst: (v: unknown) => void = () => {};
    h.gql
      .mockImplementationOnce(() => new Promise((resolve) => (releaseFirst = resolve)))
      .mockResolvedValueOnce(result([state('d-1', { active: false })]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink);
    await settle();
    await vi.advanceTimersByTimeAsync(15_000);
    expect(r.snapshots.map((s) => s[0].active)).toEqual([false]);
    releaseFirst(result([state('d-1', { active: true })]));
    await settle();
    expect(r.snapshots.map((s) => s[0].active)).toEqual([false]);
  });

  it('stops polling and delivers nothing more once disposed, including from disposeAll', async () => {
    h.gql.mockResolvedValue(result([state('d-1')]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const a = recorder();
    const dispose = hub.subscribePresence(ONE, a.sink);
    await settle();
    dispose();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.gql).toHaveBeenCalledTimes(1);

    const b = recorder();
    hub.subscribePresence(ONE, b.sink);
    await settle();
    hub.disposeAll();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('delivers nothing when disposed before the roster resolves', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const r = recorder();
    hub.subscribePresence(ONE, r.sink)();
    await settle();
    expect(r.rosters).toEqual([]);
    expect(h.gql).not.toHaveBeenCalled();
  });
});

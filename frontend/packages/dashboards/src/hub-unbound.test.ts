// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The hub holds no slot bindings. The renderer resolves every `slot` selector through the
// settled bindings before the hub sees it, and a slot with no binding arrives as an
// explicit `{kind:'unbound'}`. These tests pin both halves of that contract on every
// channel the hub serves:
//
//   - `unbound` is ZERO devices everywhere — never tenant-wide, never an error, never a
//     backend round trip, never an availability check;
//   - a `slot` that reaches the hub unresolved FAILS LOUDLY, because a quiet empty pane
//     would hide a host that forgot to resolve it.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  DashboardHub,
  type AlarmSnapshot,
  type CommandSnapshot,
  type DeviceResolver,
  type LocationSnapshot,
} from './hub';
import type { DatasourceSelector } from './types';

const h = vi.hoisted(() => ({
  subscribes: 0,
  gql: vi.fn(),
}));

vi.mock('@devicechain/client', () => ({
  subscribe: () => {
    h.subscribes += 1;
    return () => {};
  },
  gql: (...args: unknown[]) => h.gql(...args),
  isForbiddenError: () => false,
}));

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

const UNBOUND: DatasourceSelector = { kind: 'unbound', measurements: ['temperature'] };
const UNBOUND_LOCATED: DatasourceSelector = {
  kind: 'unbound',
  measurements: [],
  location: { series: 'latest' },
};
const RAW_SLOT: DatasourceSelector = { kind: 'slot', slot: 'machine', measurements: ['temperature'] };

function newResolver(): DeviceResolver & {
  devicesForAnchor: ReturnType<typeof vi.fn>;
  deviceExists: ReturnType<typeof vi.fn>;
} {
  return {
    devicesForAnchor: vi.fn(async () => ['should-never-be-read']),
    deviceExists: vi.fn(async () => false),
  };
}

let hub: DashboardHub;
let resolver: ReturnType<typeof newResolver>;

beforeEach(() => {
  h.subscribes = 0;
  h.gql.mockReset();
  // Any query that DOES go out answers with a tenant-sized figure, so a leak is a wrong
  // number on the snapshot and not just a call count.
  h.gql.mockResolvedValue({
    alarms: { results: [], pagination: { totalRecords: 42 } },
    commands: { results: [], pagination: { totalRecords: 42 } },
    latestLocations: [],
  });
  resolver = newResolver();
  hub = new DashboardHub({ resolver, authorities: ['*'] });
});

afterEach(() => {
  hub.disposeAll();
});

describe('an unbound selector is zero devices on every channel', () => {
  it('measurement: no stream, no error, no sample', async () => {
    const sink = { next: vi.fn(), error: vi.fn() };
    hub.subscribeWidget(UNBOUND, sink);
    await flush();

    expect(hub.openStreamCount).toBe(0);
    expect(h.subscribes).toBe(0);
    expect(sink.error).not.toHaveBeenCalled();
    expect(sink.next).not.toHaveBeenCalled();
  });

  it('alarm: an empty snapshot, never the tenant-wide total', async () => {
    const snaps: AlarmSnapshot[] = [];
    const error = vi.fn();
    hub.subscribeAlarms({ datasource: UNBOUND, pageSize: 10 }, { next: (s) => snaps.push(s), error });
    await flush();

    expect(error).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ alarms: [], total: 0 }]);
    expect(h.gql).not.toHaveBeenCalled();
    expect(h.subscribes).toBe(0);
  });

  it('alarm: the counterweight — no datasource at all IS tenant-wide', async () => {
    // Without this, the test above would also pass for a hub that never queries alarms.
    const snaps: AlarmSnapshot[] = [];
    hub.subscribeAlarms({ datasource: undefined, pageSize: 10 }, { next: (s) => snaps.push(s) });
    await flush();

    expect(snaps[snaps.length - 1]?.total).toBe(42);
  });

  it('command: no target device and no poll', async () => {
    const snaps: CommandSnapshot[] = [];
    const error = vi.fn();
    hub.subscribeCommands({ datasource: UNBOUND, pageSize: 20 }, { next: (s) => snaps.push(s), error });
    await flush();

    expect(error).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ deviceToken: null, commands: [], total: 0 }]);
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('location: empty positions, never a refusal', async () => {
    const snaps: LocationSnapshot[] = [];
    const error = vi.fn();
    hub.subscribeLocations({ datasource: UNBOUND_LOCATED }, { next: (s) => snaps.push(s), error });
    await flush();

    expect(error).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ kind: 'positions', deviceTokens: [], locations: [] }]);
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('availability: available, and the existence check is never asked', async () => {
    await expect(hub.isDatasourceAvailable(UNBOUND)).resolves.toBe(true);
    expect(resolver.deviceExists).not.toHaveBeenCalled();
    expect(resolver.devicesForAnchor).not.toHaveBeenCalled();
  });
});

describe('a slot selector that reaches the hub unresolved fails loudly', () => {
  // The hub keeps no bindings, so it cannot tell a slot that is unbound from one the host
  // forgot to resolve. Answering "empty" would make the second look like the first.
  it('measurement: reports an error', async () => {
    const sink = { next: vi.fn(), error: vi.fn() };
    hub.subscribeWidget(RAW_SLOT, sink);
    await flush();

    expect(sink.error).toHaveBeenCalledTimes(1);
    expect(String(sink.error.mock.calls[0][0])).toMatch(/slot 'machine'/);
    expect(hub.openStreamCount).toBe(0);
  });

  it('alarm: reports an error rather than an empty or tenant-wide snapshot', async () => {
    const next = vi.fn();
    const error = vi.fn();
    hub.subscribeAlarms({ datasource: RAW_SLOT, pageSize: 10 }, { next, error });
    await flush();

    expect(error).toHaveBeenCalledTimes(1);
    expect(next).not.toHaveBeenCalled();
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('command: reports an error', async () => {
    const next = vi.fn();
    const error = vi.fn();
    hub.subscribeCommands({ datasource: RAW_SLOT, pageSize: 20 }, { next, error });
    await flush();

    expect(error).toHaveBeenCalledTimes(1);
    expect(next).not.toHaveBeenCalled();
  });

  it('location: reports an error', async () => {
    const next = vi.fn();
    const error = vi.fn();
    hub.subscribeLocations(
      { datasource: { ...RAW_SLOT, location: { series: 'latest' } } },
      { next, error },
    );
    await flush();

    expect(error).toHaveBeenCalledTimes(1);
    expect(next).not.toHaveBeenCalled();
  });

  it('the hub no longer accepts bindings at all', () => {
    // A decisive cutover: there is no second place a slot could be resolved.
    expect('setBindings' in hub).toBe(false);
  });
});

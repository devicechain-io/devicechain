// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type CommandSnapshot, type DeviceResolver } from './hub';
import type { CommandRow } from './types';

// Controllable fakes of the SDK. The control channel is poll-only (no subscribe stream),
// so only gql() matters here; subscribe() is stubbed to a no-op disposer.
const h = vi.hoisted(() => ({
  gql: vi.fn(),
}));

vi.mock('@devicechain/client', () => ({
  subscribe: () => () => {},
  gql: (...args: unknown[]) => h.gql(...args),
}));

function commandRow(over: Partial<CommandRow> = {}): CommandRow {
  return {
    token: 'cmd-1',
    name: 'reboot',
    status: 'QUEUED',
    payload: null,
    responsePayload: null,
    error: null,
    queuedTime: '2026-07-06T00:00:00Z',
    sentTime: null,
    respondedTime: null,
    ...over,
  };
}

// The commands-query result shape gql resolves to.
function page(rows: CommandRow[], total = rows.length) {
  return { commands: { results: rows, pagination: { totalRecords: total } } };
}

function newResolver(devices: string[] = []): DeviceResolver {
  return { devicesForAnchor: vi.fn(async () => devices), deviceExists: vi.fn(async () => true) };
}

// The criteria of the Nth gql() call (call args are [area, doc, { criteria }]).
function criteriaOf(call: number): Record<string, unknown> {
  return (h.gql.mock.calls[call][2] as { criteria: Record<string, unknown> }).criteria;
}

const settle = () => vi.advanceTimersByTimeAsync(5);

const deviceSel = (deviceToken: string) => ({ kind: 'device' as const, deviceToken, measurements: [] });

beforeEach(() => {
  h.gql.mockReset();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('DashboardHub control channel', () => {
  it('polls the target device commands and reports rows + total + resolved device', async () => {
    h.gql.mockResolvedValue(page([commandRow({ token: 'c' })], 3));
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: CommandSnapshot[] = [];

    hub.subscribeCommands({ datasource: deviceSel('therm-1'), pageSize: 20 }, { next: (s) => snaps.push(s) });
    await settle();

    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(criteriaOf(0).deviceToken).toBe('therm-1');
    expect(snaps[snaps.length - 1]).toEqual({
      deviceToken: 'therm-1',
      commands: [expect.objectContaining({ token: 'c' })],
      total: 3,
    });
  });

  it('an unscoped command widget renders empty (a command needs one target device)', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: CommandSnapshot[] = [];

    hub.subscribeCommands({ pageSize: 20 }, { next: (s) => snaps.push(s) });
    await settle();

    expect(h.gql).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ deviceToken: null, commands: [], total: 0 }]);
  });

  it('an unbound slot renders empty — never a tenant-wide command list', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const snaps: CommandSnapshot[] = [];

    hub.subscribeCommands(
      { datasource: { kind: 'slot', slot: 'unbound', measurements: [] }, pageSize: 20 },
      { next: (s) => snaps.push(s) },
    );
    await settle();

    expect(h.gql).not.toHaveBeenCalled();
    expect(snaps).toEqual([{ deviceToken: null, commands: [], total: 0 }]);
  });

  it('an anchor scope targets the first resolved device (defensive fallback)', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver(['d1', 'd2']) });

    hub.subscribeCommands(
      {
        datasource: {
          kind: 'anchor',
          anchor: { relationship: 'controls', targetType: 'asset', targetToken: 'a1' },
          measurements: [],
        },
        pageSize: 20,
      },
      { next: () => {} },
    );
    await settle();

    expect(criteriaOf(0).deviceToken).toBe('d1');
  });

  it('polls on the command cadence as a lifecycle backstop', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeCommands({ datasource: deviceSel('d1'), pageSize: 20 }, { next: () => {} });
    await settle();
    expect(h.gql).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(4_000);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('dispose stops polling', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    const dispose = hub.subscribeCommands({ datasource: deviceSel('d1'), pageSize: 20 }, { next: () => {} });
    await settle();
    dispose();

    await vi.advanceTimersByTimeAsync(20_000);
    expect(h.gql).toHaveBeenCalledTimes(1); // no further polls
  });

  it('disposeAll tears down a command subscription poll', async () => {
    h.gql.mockResolvedValue(page([]));
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.subscribeCommands({ datasource: deviceSel('d1'), pageSize: 20 }, { next: () => {} });
    await settle();

    hub.disposeAll();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(h.gql).toHaveBeenCalledTimes(1);
  });

  it('surfaces a query error to the sink', async () => {
    h.gql.mockRejectedValue(new Error('boom'));
    const hub = new DashboardHub({ resolver: newResolver() });
    const errors: unknown[] = [];

    hub.subscribeCommands({ datasource: deviceSel('d1'), pageSize: 20 }, { next: () => {}, error: (e) => errors.push(e) });
    await settle();

    expect(errors).toHaveLength(1);
    expect((errors[0] as Error).message).toBe('boom');
  });

  describe('sendCommand', () => {
    it('mints a dispatch token, issues createCommand, and returns the token', async () => {
      h.gql.mockResolvedValue({ createCommand: { token: 'ignored', status: 'QUEUED' } });
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      const result = await hub.sendCommand('therm-1', 'reboot', '{"delaySeconds":5}');

      expect(typeof result.token).toBe('string');
      expect(result.token.length).toBeGreaterThan(0);
      const req = (h.gql.mock.calls[0][2] as { request: Record<string, unknown> }).request;
      expect(req.deviceToken).toBe('therm-1');
      expect(req.name).toBe('reboot');
      expect(req.payload).toBe('{"delaySeconds":5}');
      expect(req.token).toBe(result.token); // the returned token is the one issued
    });

    it('passes a null payload when none is given', async () => {
      h.gql.mockResolvedValue({ createCommand: { token: 't', status: 'QUEUED' } });
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      await hub.sendCommand('therm-1', 'ping');

      const req = (h.gql.mock.calls[0][2] as { request: Record<string, unknown> }).request;
      expect(req.payload).toBeNull();
    });

    it('reconciles open command widgets after an issue', async () => {
      h.gql.mockResolvedValue(page([]));
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });
      hub.subscribeCommands({ datasource: deviceSel('therm-1'), pageSize: 20 }, { next: () => {} });
      await settle();
      expect(h.gql).toHaveBeenCalledTimes(1); // initial poll

      h.gql.mockResolvedValue({ createCommand: { token: 't', status: 'QUEUED' } });
      await hub.sendCommand('therm-1', 'reboot');
      await settle();

      // initial poll (1) + createCommand (2) + reconcile re-poll (3).
      expect(h.gql).toHaveBeenCalledTimes(3);
    });

    it('a disposed command widget is not reconciled by a later issue', async () => {
      h.gql.mockResolvedValue(page([]));
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });
      const dispose = hub.subscribeCommands({ datasource: deviceSel('therm-1'), pageSize: 20 }, { next: () => {} });
      await settle();
      dispose();

      await hub.sendCommand('therm-1', 'reboot');
      await settle();

      // initial poll (1) + createCommand (2) only — no reconcile, the reconciler was removed.
      expect(h.gql).toHaveBeenCalledTimes(2);
    });
  });
});

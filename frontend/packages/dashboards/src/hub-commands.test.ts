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

// The createCommand mutation's two arms, exactly as the service returns them: one field
// populated, the other null. Written as helpers so a fixture can never accidentally
// carry both (which the server never does) or the pre-split shape (which it no longer
// does).
function enqueued(status = 'QUEUED') {
  return { createCommand: { command: { token: 'server-token', status }, rejection: null } };
}

function refused(code: string, reason: string) {
  return { createCommand: { command: null, rejection: { code, reason } } };
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
      h.gql.mockResolvedValue(enqueued());
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      const result = await hub.sendCommand('therm-1', 'reboot', '{"delaySeconds":5}');

      expect(result.status).toBe('sent');
      const token = result.status === 'sent' ? result.token : '';
      expect(token.length).toBeGreaterThan(0);
      const req = (h.gql.mock.calls[0][2] as { request: Record<string, unknown> }).request;
      expect(req.deviceToken).toBe('therm-1');
      expect(req.name).toBe('reboot');
      expect(req.payload).toBe('{"delaySeconds":5}');
      expect(req.token).toBe(token); // the returned token is the one issued
    });

    // 🔴 THE ARM THAT USED NOT TO EXIST. A refused enqueue arrives as a RESULT — the
    // server decided the request and named the reason — so it must resolve, carrying the
    // machine-readable code and the operator-readable reason, and must NOT be thrown:
    // every rejection used to arrive as an error string, indistinguishable from the
    // service being down.
    it('resolves a refused enqueue as a rejection carrying its code and reason', async () => {
      h.gql.mockResolvedValue(
        refused('COMMAND_NOT_IN_VOCABULARY', 'command "reboot" is not published for device "therm-1"'),
      );
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      const result = await hub.sendCommand('therm-1', 'reboot');

      expect(result).toEqual({
        status: 'rejected',
        code: 'COMMAND_NOT_IN_VOCABULARY',
        reason: 'command "reboot" is not published for device "therm-1"',
      });
    });

    // Nothing was created, so nothing changed: re-polling would redraw the same history
    // and make a refusal look like it did something.
    it('does not reconcile open command widgets when the enqueue was refused', async () => {
      h.gql.mockResolvedValue(page([]));
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });
      hub.subscribeCommands({ datasource: deviceSel('therm-1'), pageSize: 20 }, { next: () => {} });
      await settle();
      expect(h.gql).toHaveBeenCalledTimes(1); // initial poll

      h.gql.mockResolvedValue(refused('HELD_CEILING_EXCEEDED', 'the tenant is holding its limit of commands'));
      await hub.sendCommand('therm-1', 'reboot');
      await settle();

      // initial poll (1) + createCommand (2) — and NO reconcile re-poll.
      expect(h.gql).toHaveBeenCalledTimes(2);
    });

    // 🔴 The dispatch token is minted client-side, so nothing forces sendCommand to
    // read the server's answer at all. Without an explicit guard, a response carrying
    // NEITHER arm falls straight through to "sent" with a token that names no command,
    // and every open command widget is reconciled as though something were created —
    // the operator is told their command went out and handed a handle that cancels
    // nothing. The REACT sink and the console both refuse this shape; the hub is the
    // surface most likely to be embedded outside the console, so it is the last one
    // that should be lenient.
    it('refuses a response carrying neither arm rather than reporting it as sent', async () => {
      h.gql.mockResolvedValue({ createCommand: { command: null, rejection: null } });
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      await expect(hub.sendCommand('therm-1', 'reboot')).rejects.toThrow(/no answer/i);
    });

    it('refuses an absent createCommand rather than reporting it as sent', async () => {
      h.gql.mockResolvedValue({});
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      await expect(hub.sendCommand('therm-1', 'reboot')).rejects.toThrow(/no answer/i);
    });

    it('does not reconcile open command widgets when neither arm came back', async () => {
      h.gql.mockResolvedValue(page([]));
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });
      hub.subscribeCommands({ datasource: deviceSel('therm-1'), pageSize: 20 }, { next: () => {} });
      await settle();
      expect(h.gql).toHaveBeenCalledTimes(1); // initial poll

      h.gql.mockResolvedValue({ createCommand: { command: null, rejection: null } });
      await expect(hub.sendCommand('therm-1', 'reboot')).rejects.toThrow(/no answer/i);
      await settle();

      // initial poll (1) + createCommand (2) — and NO reconcile re-poll.
      expect(h.gql).toHaveBeenCalledTimes(2);
    });

    // An availability failure is still thrown: it says nothing about the command.
    it('throws when the enqueue could not be decided at all', async () => {
      h.gql.mockRejectedValue(new Error('command-delivery unreachable'));
      const hub = new DashboardHub({ resolver: newResolver(), authorities: ['*'] });

      await expect(hub.sendCommand('therm-1', 'reboot')).rejects.toThrow('command-delivery unreachable');
    });

    it('passes a null payload when none is given', async () => {
      h.gql.mockResolvedValue(enqueued());
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

      h.gql.mockResolvedValue(enqueued());
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

      h.gql.mockResolvedValue(enqueued());
      await hub.sendCommand('therm-1', 'reboot');
      await settle();

      // initial poll (1) + createCommand (2) only — no reconcile, the reconciler was removed.
      expect(h.gql).toHaveBeenCalledTimes(2);
    });
  });
});

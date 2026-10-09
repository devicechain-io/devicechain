// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The WidgetDataSource contract, as a suite any implementation can be held to. The live
// DashboardHub, the SyntheticDataSource and the RecordedDataSource all feed the same
// widgets, so what a widget may rely on is asserted ONCE here and run against each of
// them: a behaviour one source drops is a widget bug waiting for that source.
//
// An implementation that legitimately differs says so in `caps`, which makes each
// exception a visible line (and a skipped test with a name) instead of a quiet absence.
// The synthetic source, for instance, ignores scope on purpose: preview never resolves a
// device, so "unbound means zero devices" is not something it can honour.

import { afterEach, describe, expect, it } from 'vitest';

import type { AlarmSnapshot, LocationSnapshot, WidgetDataSource } from '../hub';
import type { DatasourceSelector, MeasurementSample } from '../types';

export interface ContractSubject {
  source: WidgetDataSource;
  // Make the source deliver what the scenario holds (the recorded source plays to the end,
  // the live hub's fake wire receives samples). Resolves when delivery has settled.
  deliver(): Promise<void>;
  close(): void;
}

export interface ContractCaps {
  // Honours scope: an `unbound` selector yields zero devices on every channel.
  scopesToSelector: boolean;
  // Refuses a `slot` selector that was never resolved, through the sink's error path.
  rejectsUnresolvedSlot: boolean;
  // Holds positions. A source that does not must answer a selector naming a location
  // series with an error, never an empty snapshot (which would read as "nothing located").
  recordsPositions: boolean;
}

// The device the scenario's samples and positions belong to.
export const CONTRACT_DEVICE = 'sp-hauler-01';
export const CONTRACT_MEASUREMENT = 'fuel_pct';
// The scenario holds this many alarms, which is what makes a page smaller than the total
// checkable.
export const CONTRACT_ALARM_COUNT = 3;

const FUEL: DatasourceSelector = {
  kind: 'device',
  deviceToken: CONTRACT_DEVICE,
  measurements: [CONTRACT_MEASUREMENT],
};
const UNBOUND: DatasourceSelector = { kind: 'unbound', measurements: [CONTRACT_MEASUREMENT] };
const UNBOUND_LOCATED: DatasourceSelector = {
  kind: 'unbound',
  measurements: [],
  location: { series: 'latest' },
};
const RAW_SLOT: DatasourceSelector = { kind: 'slot', slot: 'machine', measurements: [CONTRACT_MEASUREMENT] };
const LOCATED: DatasourceSelector = {
  kind: 'device',
  deviceToken: CONTRACT_DEVICE,
  measurements: [],
  location: { series: 'latest' },
};

// Let microtasks and zero-delay timers run, without advancing any long timer.
export async function settle(): Promise<void> {
  for (let i = 0; i < 10; i++) await new Promise((resolve) => setTimeout(resolve, 0));
}

function collector<T>() {
  const next: T[] = [];
  const errors: unknown[] = [];
  return {
    next,
    errors,
    sink: {
      next: (x: T) => {
        next.push(x);
      },
      error: (e: unknown) => {
        errors.push(e);
      },
    },
  };
}

export function describeDataSourceContract(
  label: string,
  make: () => ContractSubject | Promise<ContractSubject>,
  caps: ContractCaps,
): void {
  describe(`WidgetDataSource contract: ${label}`, () => {
    let subject: ContractSubject | undefined;
    const disposers: Array<() => void> = [];

    // Every subscription goes through here so a failing test cannot leak a live timer.
    const track = (dispose: () => void): (() => void) => {
      disposers.push(dispose);
      return dispose;
    };

    const start = async (): Promise<{ source: WidgetDataSource; subject: ContractSubject }> => {
      subject = await make();
      return { source: subject.source, subject };
    };

    afterEach(() => {
      for (const d of disposers.splice(0)) d();
      subject?.close();
      subject = undefined;
    });

    it('returns a disposer synchronously from every channel', async () => {
      const { source } = await start();
      const c = collector<any>(); // one sink shared across four differently-typed channels
      for (const d of [
        source.subscribeWidget(FUEL, c.sink),
        source.subscribeAlarms({ pageSize: 5 }, c.sink),
        source.subscribeCommands({ datasource: FUEL, pageSize: 5 }, c.sink),
        source.subscribeLocations({ datasource: LOCATED }, c.sink),
      ]) {
        expect(typeof d).toBe('function');
        track(d);
      }
    });

    it('emits nothing after dispose, on any channel', async () => {
      const { source, subject: s } = await start();
      const m = collector<MeasurementSample>();
      const a = collector<AlarmSnapshot>();
      const l = collector<LocationSnapshot>();
      const disposeAll = [
        source.subscribeWidget(FUEL, m.sink),
        source.subscribeAlarms({ pageSize: 5 }, a.sink),
        source.subscribeLocations({ datasource: LOCATED }, l.sink),
      ];
      // Whatever arrived synchronously (a source may back-fill on subscribe) is the baseline.
      for (const d of disposeAll) d();
      const counts = () => [m.next.length, a.next.length, l.next.length, m.errors.length, a.errors.length, l.errors.length];
      const before = counts();
      await s.deliver();
      await settle();
      expect(counts()).toEqual(before);
    });

    it('delivers well-formed samples, filtered to the requested measurement names', async () => {
      const { source, subject: s } = await start();
      const m = collector<MeasurementSample>();
      track(source.subscribeWidget(FUEL, m.sink));
      await s.deliver();
      await settle();
      expect(m.errors).toEqual([]);
      expect(m.next.length).toBeGreaterThan(0);
      for (const sample of m.next) {
        expect(sample.name).toBe(CONTRACT_MEASUREMENT);
        expect(typeof sample.id).toBe('string');
        expect(sample.value === null || typeof sample.value === 'number').toBe(true);
        expect(typeof sample.occurredTime).toBe('string');
        expect(Number.isNaN(Date.parse(sample.occurredTime as string))).toBe(false);
      }
    });

    it('reports alarms newest first, capped to the page, with the uncapped total', async () => {
      const { source, subject: s } = await start();
      const a = collector<AlarmSnapshot>();
      track(source.subscribeAlarms({ pageSize: 2 }, a.sink));
      await s.deliver();
      await settle();
      expect(a.errors).toEqual([]);
      const last = a.next[a.next.length - 1];
      expect(last).toBeDefined();
      expect(last.alarms).toHaveLength(2);
      expect(last.total).toBeGreaterThan(last.alarms.length);
      const raised = last.alarms.map((r) => r.raisedTime ?? '');
      expect([...raised].sort().reverse()).toEqual(raised);
    });

    it.skipIf(caps.recordsPositions)('refuses a located selector it holds no positions for, instead of answering empty', async () => {
      const { source, subject: s } = await start();
      const l = collector<LocationSnapshot>();
      track(source.subscribeLocations({ datasource: LOCATED }, l.sink));
      await s.deliver();
      await settle();
      expect(l.next).toEqual([]);
      expect(l.errors).toHaveLength(1);
    });

    it.skipIf(!caps.recordsPositions)('answers a located selector with a positions snapshot that carries its device tokens', async () => {
      const { source, subject: s } = await start();
      const l = collector<LocationSnapshot>();
      track(source.subscribeLocations({ datasource: LOCATED }, l.sink));
      await s.deliver();
      await settle();
      expect(l.errors).toEqual([]);
      const last = l.next[l.next.length - 1];
      expect(last.kind).toBe('positions');
      if (last.kind !== 'positions') return;
      expect(Array.isArray(last.deviceTokens)).toBe(true);
      expect(last.locations.length).toBeGreaterThan(0);
      for (const loc of last.locations) expect(last.deviceTokens).toContain(loc.deviceToken);
    });

    it('answers a selector that names no location series with the empty positions snapshot', async () => {
      const { source, subject: s } = await start();
      const l = collector<LocationSnapshot>();
      track(source.subscribeLocations({ datasource: FUEL }, l.sink));
      await s.deliver();
      await settle();
      expect(l.errors).toEqual([]);
      expect(l.next[l.next.length - 1]).toEqual({ kind: 'positions', deviceTokens: [], locations: [] });
    });

    it('settles the command channel with exactly one of a snapshot or an error', async () => {
      const { source, subject: s } = await start();
      const c = collector<unknown>();
      track(source.subscribeCommands({ datasource: FUEL, pageSize: 5 }, c.sink));
      await s.deliver();
      await settle();
      expect(c.next.length > 0 || c.errors.length > 0).toBe(true);
      expect(c.next.length > 0 && c.errors.length > 0).toBe(false);
    });

    it('reports availability as a boolean, and available for no datasource and for unbound', async () => {
      const { source } = await start();
      expect(await source.isDatasourceAvailable(undefined)).toBe(true);
      expect(await source.isDatasourceAvailable(UNBOUND)).toBe(true);
      expect(typeof (await source.isDatasourceAvailable(FUEL))).toBe('boolean');
    });

    it.skipIf(!caps.scopesToSelector)('treats an unbound selector as zero devices on every channel', async () => {
      const { source, subject: s } = await start();
      const m = collector<MeasurementSample>();
      const a = collector<AlarmSnapshot>();
      const l = collector<LocationSnapshot>();
      track(source.subscribeWidget(UNBOUND, m.sink));
      track(source.subscribeAlarms({ datasource: UNBOUND, pageSize: 5 }, a.sink));
      track(source.subscribeLocations({ datasource: UNBOUND_LOCATED }, l.sink));
      await s.deliver();
      await settle();
      expect(m.errors).toEqual([]);
      expect(m.next).toEqual([]);
      expect(a.next[a.next.length - 1]).toEqual({ alarms: [], total: 0 });
      expect(l.next[l.next.length - 1]).toEqual({ kind: 'positions', deviceTokens: [], locations: [] });
    });

    it.skipIf(!caps.rejectsUnresolvedSlot)('fails loudly for a slot that reached it unresolved', async () => {
      const { source, subject: s } = await start();
      const m = collector<MeasurementSample>();
      const a = collector<AlarmSnapshot>();
      track(source.subscribeWidget(RAW_SLOT, m.sink));
      track(source.subscribeAlarms({ datasource: RAW_SLOT, pageSize: 5 }, a.sink));
      await s.deliver();
      await settle();
      expect(m.next).toEqual([]);
      expect(a.next).toEqual([]);
      expect(String(m.errors[0])).toContain("slot 'machine'");
      expect(String(a.errors[0])).toContain("slot 'machine'");
    });
  });
}

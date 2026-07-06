// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver } from './hub';
import type { MeasurementSample } from './types';

// A controllable fake of the SDK's subscribe(): every call records the device token
// it was opened for and its sink, so a test can push samples into a stream and
// assert when it is torn down. vi.hoisted shares the registry with the hoisted
// vi.mock factory.
const h = vi.hoisted(() => ({
  streams: [] as Array<{
    deviceToken: string;
    sink: { next: (d: { measurementStream: MeasurementSample }) => void; error?: (e: unknown) => void };
    closed: boolean;
  }>,
}));

vi.mock('@devicechain/client', () => ({
  subscribe: (
    _area: string,
    _doc: unknown,
    variables: { deviceToken: string },
    sink: { next: (d: { measurementStream: MeasurementSample }) => void; error?: (e: unknown) => void },
  ) => {
    const entry = { deviceToken: variables.deviceToken, sink, closed: false };
    h.streams.push(entry);
    return () => {
      entry.closed = true;
    };
  },
}));

// Flush pending microtasks so async selector resolution (anchor→devices) settles.
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

const sampleFor = (deviceToken: string, name: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-${name}`,
  deviceToken,
  eventType: 0,
  occurredTime: null,
  name,
  value,
  classifier: null,
});

function newResolver(overrides: Partial<DeviceResolver> = {}): DeviceResolver {
  return {
    devicesForAnchor: vi.fn(async () => ['therm-004', 'therm-005']),
    ...overrides,
  };
}

beforeEach(() => {
  h.streams.length = 0;
});

describe('DashboardHub', () => {
  it('shares one upstream stream across widgets on the same device', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });

    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] }, { next: vi.fn() });
    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['humidity'] }, { next: vi.fn() });
    await flush();

    // Two widgets, one device → exactly one upstream subscription.
    expect(h.streams.length).toBe(1);
    expect(hub.openStreamCount).toBe(1);
  });

  it('fans out only the measurement names each subscriber wants', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const tempSink = { next: vi.fn() };
    const humiditySink = { next: vi.fn() };

    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] }, tempSink);
    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['humidity'] }, humiditySink);
    await flush();

    h.streams[0].sink.next({ measurementStream: sampleFor('therm-001', 'temperature', 21.5) });

    expect(tempSink.next).toHaveBeenCalledTimes(1);
    expect(tempSink.next).toHaveBeenCalledWith(expect.objectContaining({ name: 'temperature', value: 21.5 }));
    expect(humiditySink.next).not.toHaveBeenCalled();
  });

  it('closes the upstream only when the last subscriber detaches (ref-counting)', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });

    const disposeA = hub.subscribeWidget(
      { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] },
      { next: vi.fn() },
    );
    const disposeB = hub.subscribeWidget(
      { kind: 'device', deviceToken: 'therm-001', measurements: ['humidity'] },
      { next: vi.fn() },
    );
    await flush();

    disposeA();
    expect(h.streams[0].closed).toBe(false); // B still holds it open
    expect(hub.openStreamCount).toBe(1);

    disposeB();
    expect(h.streams[0].closed).toBe(true); // last subscriber gone → torn down
    expect(hub.openStreamCount).toBe(0);
  });

  it('expands an anchor selector to one stream per member device', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const sink = { next: vi.fn() };

    hub.subscribeWidget(
      {
        kind: 'anchor',
        anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-1' },
        measurements: ['temperature'],
      },
      sink,
    );
    await flush();

    expect(hub.openStreamCount).toBe(2); // members 'therm-004' and 'therm-005'
    h.streams
      .find((s) => s.deviceToken === 'therm-005')!
      .sink.next({ measurementStream: sampleFor('therm-005', 'temperature', 30) });
    expect(sink.next).toHaveBeenCalledWith(expect.objectContaining({ deviceToken: 'therm-005', value: 30 }));
  });

  it('rejects a reserved selector kind via sink.error, opening no stream', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const sink = { next: vi.fn(), error: vi.fn() };

    hub.subscribeWidget({ kind: 'devices', deviceTokens: ['a', 'b'], measurements: ['t'] }, sink);
    await flush();

    expect(sink.error).toHaveBeenCalledTimes(1);
    expect((sink.error.mock.calls[0][0] as Error).message).toContain('not supported yet');
    expect(hub.openStreamCount).toBe(0);
  });

  it('evicts a stream whose upstream errors so the next subscriber reopens it', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const sinkA = { next: vi.fn(), error: vi.fn() };

    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] }, sinkA);
    await flush();
    expect(hub.openStreamCount).toBe(1);

    // The upstream socket errors: the subscriber is notified AND the dead stream
    // is evicted (not left cached with a no-op unsubscribe).
    h.streams[0].sink.error!(new Error('socket dropped'));
    expect(sinkA.error).toHaveBeenCalledTimes(1);
    expect(hub.openStreamCount).toBe(0);

    // A fresh subscriber for the same device opens a NEW upstream rather than
    // attaching to the corpse and freezing.
    const sinkB = { next: vi.fn() };
    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] }, sinkB);
    await flush();
    expect(hub.openStreamCount).toBe(1);
    expect(h.streams.length).toBe(2); // one dead + one fresh
    h.streams[1].sink.next({ measurementStream: sampleFor('therm-001', 'temperature', 22) });
    expect(sinkB.next).toHaveBeenCalledWith(expect.objectContaining({ value: 22 }));
  });

  it('a stale detacher does not delete the fresh stream after an eviction+replace', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    const sinkA = { next: vi.fn(), error: vi.fn() };

    const disposeA = hub.subscribeWidget(
      { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] },
      sinkA,
    );
    await flush();

    // The upstream errors → the stream is evicted and A is notified.
    h.streams[0].sink.error!(new Error('socket dropped'));
    expect(hub.openStreamCount).toBe(0);

    // A fresh subscriber opens a NEW stream for the same device.
    const sinkB = { next: vi.fn() };
    hub.subscribeWidget({ kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] }, sinkB);
    await flush();
    expect(hub.openStreamCount).toBe(1);

    // Now the OLD subscriber detaches: the guard must leave the fresh stream intact.
    disposeA();
    expect(hub.openStreamCount).toBe(1);
    h.streams[1].sink.next({ measurementStream: sampleFor('therm-001', 'temperature', 99) });
    expect(sinkB.next).toHaveBeenCalledWith(expect.objectContaining({ value: 99 }));
  });

  it('does not attach if the widget is disposed before resolution completes', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });

    const dispose = hub.subscribeWidget(
      { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] },
      { next: vi.fn() },
    );
    dispose(); // tear down synchronously, before the async selector resolution settles
    await flush();

    expect(h.streams.length).toBe(0);
    expect(hub.openStreamCount).toBe(0);
  });

  it('resolves a slot selector through its device binding', async () => {
    const hub = new DashboardHub({
      resolver: newResolver(),
      bindings: { primary: { kind: 'device', deviceToken: 'therm-001' } },
    });
    const sink = { next: vi.fn() };

    hub.subscribeWidget({ kind: 'slot', slot: 'primary', measurements: ['temperature'] }, sink);
    await flush();

    expect(hub.openStreamCount).toBe(1);
    h.streams[0].sink.next({ measurementStream: sampleFor('therm-001', 'temperature', 21) });
    expect(sink.next).toHaveBeenCalledWith(expect.objectContaining({ value: 21 }));
  });

  it('resolves a slot selector through an anchor binding (one stream per member)', async () => {
    const hub = new DashboardHub({
      resolver: newResolver(),
      bindings: {
        area: { kind: 'anchor', anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-1' } },
      },
    });
    hub.subscribeWidget({ kind: 'slot', slot: 'area', measurements: ['temperature'] }, { next: vi.fn() });
    await flush();
    expect(hub.openStreamCount).toBe(2); // members '4' and '5'
  });

  it('treats a prototype-named slot as unbound, not a crash', async () => {
    const hub = new DashboardHub({ resolver: newResolver() }); // empty bindings
    const sink = { next: vi.fn(), error: vi.fn() };

    // 'constructor' etc. are inherited on a plain object; the own-property guard must
    // treat them as unbound (silent placeholder), not resolve to Object.prototype.
    hub.subscribeWidget({ kind: 'slot', slot: 'constructor', measurements: ['t'] }, sink);
    await flush();

    expect(hub.openStreamCount).toBe(0);
    expect(sink.error).not.toHaveBeenCalled();
  });

  it('renders an unbound slot as a silent placeholder — no stream, no error', async () => {
    const hub = new DashboardHub({ resolver: newResolver() }); // no bindings
    const sink = { next: vi.fn(), error: vi.fn() };

    hub.subscribeWidget({ kind: 'slot', slot: 'nope', measurements: ['temperature'] }, sink);
    await flush();

    expect(hub.openStreamCount).toBe(0);
    expect(sink.error).not.toHaveBeenCalled(); // unbound = placeholder, not an error
    expect(sink.next).not.toHaveBeenCalled();
  });

  it('setBindings applies to subsequent slot subscriptions', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });
    hub.setBindings({ primary: { kind: 'device', deviceToken: 'therm-001' } });

    hub.subscribeWidget({ kind: 'slot', slot: 'primary', measurements: ['temperature'] }, { next: vi.fn() });
    await flush();
    expect(hub.openStreamCount).toBe(1);
  });

  it('disposeAll tears down every open upstream', async () => {
    const hub = new DashboardHub({ resolver: newResolver() });

    hub.subscribeWidget(
      {
        kind: 'anchor',
        anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-1' },
        measurements: ['temperature'],
      },
      { next: vi.fn() },
    );
    await flush();
    expect(hub.openStreamCount).toBe(2);

    hub.disposeAll();
    expect(hub.openStreamCount).toBe(0);
    expect(h.streams.every((s) => s.closed)).toBe(true);
  });
});

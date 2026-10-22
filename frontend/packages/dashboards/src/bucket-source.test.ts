// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The buckets a stat widget folds, as the live hub and the preview source produce them. The
// aggregate arithmetic itself is the widget package's (aggregate-fold.test.ts); what is pinned
// here is that the read asks the backend for the fields that arithmetic needs, once per device
// and measurement, and that a source with nothing to read answers with nothing.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { DashboardHub, type DeviceResolver } from './hub';
import { BUCKETED_MEASUREMENTS } from './queries';
import { SyntheticDataSource } from './synthetic';
import type { DatasourceSelector } from './types';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, subscribe: () => () => {}, gql: (...args: unknown[]) => h.gql(...args) };
});

const WINDOW = { startTime: '2026-10-14T11:00:00.000Z', endTime: '2026-10-14T12:00:00.000Z', intervalSeconds: 60 };

const bucket = (name: string, over: Record<string, unknown> = {}) => ({
  bucketStart: '2026-10-14T11:00:00Z',
  name,
  avg: 20,
  min: 10,
  max: 40,
  sum: 400,
  count: 20,
  ...over,
});

function hubWith(members: string[] = []): DashboardHub {
  const resolver: DeviceResolver = { devicesForAnchor: vi.fn(async () => members), deviceExists: vi.fn(async () => true) };
  return new DashboardHub({ resolver });
}

beforeEach(() => {
  h.gql.mockReset();
  h.gql.mockImplementation(async () => ({ bucketedMeasurements: [bucket('temperature')] }));
});

describe('the bucket query', () => {
  it('asks for the count, minimum, maximum and sum of every bucket, as well as the average the charts seed from', () => {
    for (const field of ['avg', 'min', 'max', 'sum', 'count']) {
      expect(BUCKETED_MEASUREMENTS, `the document does not ask for ${field}`).toMatch(new RegExp(`\\b${field}\\b`));
    }
  });
});

describe('DashboardHub.bucketMeasurements', () => {
  it('reads one device and measurement over the window, from event-management', async () => {
    const hub = hubWith();
    const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };
    const out = await hub.bucketMeasurements(ds, WINDOW);
    expect(out).toEqual([bucket('temperature')]);
    expect(h.gql).toHaveBeenCalledTimes(1);
    const [area, doc, vars] = h.gql.mock.calls[0];
    expect(area).toBe('event-management');
    expect(doc).toBe(BUCKETED_MEASUREMENTS);
    expect(vars).toEqual({ criteria: { deviceToken: 'd-1', name: 'temperature', ...WINDOW } });
  });

  it('reads each named measurement separately, and joins the answers', async () => {
    h.gql.mockImplementation(async (_a: string, _d: unknown, v: { criteria: { name: string } }) => ({
      bucketedMeasurements: [bucket(v.criteria.name)],
    }));
    const out = await hubWith().bucketMeasurements({ kind: 'device', deviceToken: 'd-1', measurements: ['temperature', 'humidity'] }, WINDOW);
    expect(h.gql).toHaveBeenCalledTimes(2);
    expect(out.map((b) => b.name)).toEqual(['temperature', 'humidity']);
  });

  it('with no measurement named, asks for every measurement the device reports (one read, no name)', async () => {
    await hubWith().bucketMeasurements({ kind: 'device', deviceToken: 'd-1', measurements: [] }, WINDOW);
    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(h.gql.mock.calls[0][2].criteria.name).toBeUndefined();
  });

  it('reads every member device of an anchor', async () => {
    await hubWith(['a', 'b']).bucketMeasurements(
      { kind: 'anchor', anchor: { type: 'area', token: 'plant' }, measurements: ['temperature'] } as unknown as DatasourceSelector,
      WINDOW,
    );
    expect(h.gql.mock.calls.map((c) => c[2].criteria.deviceToken).sort()).toEqual(['a', 'b']);
  });

  it('reads nothing for a slot with no binding: zero devices, no round trip', async () => {
    const out = await hubWith().bucketMeasurements({ kind: 'unbound', measurements: ['temperature'] }, WINDOW);
    expect(out).toEqual([]);
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('fails loudly for a slot that reached it unresolved, instead of answering with no buckets', async () => {
    await expect(hubWith().bucketMeasurements({ kind: 'slot', slot: 'machine', measurements: [] }, WINDOW)).rejects.toThrow(/unresolved/);
  });

  it('rejects when the read fails, so the widget can say so', async () => {
    h.gql.mockRejectedValue(new Error('LIMIT_EXCEEDED'));
    await expect(hubWith().bucketMeasurements({ kind: 'device', deviceToken: 'd-1', measurements: ['t'] }, WINDOW)).rejects.toThrow('LIMIT_EXCEEDED');
  });
});

describe('DashboardHub.bucketMeasurements, asked the same question twice', () => {
  const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };

  it('shares one read between widgets that ask it while it is in flight', async () => {
    const hub = hubWith();
    const [a, b, c] = await Promise.all([
      hub.bucketMeasurements(ds, WINDOW),
      hub.bucketMeasurements({ ...ds, measurements: ['temperature'] }, { ...WINDOW }),
      hub.bucketMeasurements(ds, WINDOW),
    ]);
    expect(h.gql).toHaveBeenCalledTimes(1);
    expect(a).toEqual([bucket('temperature')]);
    expect(b).toEqual(a);
    expect(c).toEqual(a);
  });

  it('reads again once the shared read has settled: it is not a cache that could go stale', async () => {
    const hub = hubWith();
    await hub.bucketMeasurements(ds, WINDOW);
    await hub.bucketMeasurements(ds, WINDOW);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });

  it('does not share across a different window or measurement', async () => {
    const hub = hubWith();
    await Promise.all([
      hub.bucketMeasurements(ds, WINDOW),
      hub.bucketMeasurements(ds, { ...WINDOW, startTime: '2026-10-14T11:01:00.000Z' }),
      hub.bucketMeasurements({ ...ds, measurements: ['humidity'] }, WINDOW),
    ]);
    expect(h.gql).toHaveBeenCalledTimes(3);
  });

  it('does not keep a failure: the next ask reads again', async () => {
    const hub = hubWith();
    h.gql.mockRejectedValueOnce(new Error('blip'));
    const both = await Promise.allSettled([hub.bucketMeasurements(ds, WINDOW), hub.bucketMeasurements(ds, WINDOW)]);
    expect(both.map((r) => r.status)).toEqual(['rejected', 'rejected']);
    expect(h.gql).toHaveBeenCalledTimes(1);
    await expect(hub.bucketMeasurements(ds, WINDOW)).resolves.toEqual([bucket('temperature')]);
    expect(h.gql).toHaveBeenCalledTimes(2);
  });
});

describe('SyntheticDataSource.bucketMeasurements', () => {
  // A stat card's figure asks for the window as ONE bucket: the interval is the window's width.
  const ONE_BUCKET = { ...WINDOW, intervalSeconds: 3600 };

  it('answers with one bucket per measurement whose extremes the waveform actually reached', async () => {
    const source = new SyntheticDataSource({ generator: 'sine', min: 10, max: 30 });
    const out = await source.bucketMeasurements({ kind: 'unbound', measurements: ['temperature', 'humidity'] }, ONE_BUCKET);
    expect(out.map((b) => b.name)).toEqual(['temperature', 'humidity']);
    for (const b of out) {
      expect(b.count).toBe(120);
      expect(b.min!).toBeGreaterThanOrEqual(10);
      expect(b.max!).toBeLessThanOrEqual(30);
      expect(b.min!).toBeLessThan(b.avg!);
      expect(b.avg!).toBeLessThan(b.max!);
      expect(b.sum! / b.count!).toBeCloseTo(b.avg!, 9);
    }
  });

  it('is not the average pretending to be an extreme: a sine over a full period spans most of its range', async () => {
    const source = new SyntheticDataSource({ generator: 'sine', min: 0, max: 100, periodMs: 3_600_000 });
    const [b] = await source.bucketMeasurements({ kind: 'unbound', measurements: ['t'] }, ONE_BUCKET);
    expect(b.max! - b.min!).toBeGreaterThan(90);
  });
});

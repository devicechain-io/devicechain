// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SubscriptionSink } from '@devicechain/client';
import { DashboardHub } from './hub';
import { MEASUREMENT_STREAM, MEASUREMENT_STREAM_MULTI, type MeasurementStreamResult, type MeasurementStreamVariables } from './internal/measurement-doc';
import type { DatasourceSelector, MeasurementSample } from './types';

const h = vi.hoisted(() => ({
  operations: [] as Array<{
    doc: unknown;
    variables: MeasurementStreamVariables;
    sink: SubscriptionSink<MeasurementStreamResult>;
    closed: boolean;
  }>,
  refuseImmediately: false,
  resultError: undefined as string | undefined,
}));

vi.mock('@devicechain/client', () => ({
  subscribe: (_area: string, doc: unknown, variables: MeasurementStreamVariables, sink: SubscriptionSink<MeasurementStreamResult>) => {
    const op = { doc, variables, sink, closed: false };
    h.operations.push(op);
    if (h.resultError && variables.deviceTokens) {
      sink.errors?.([{ message: h.resultError }]);
      sink.complete?.();
    }
    if (h.refuseImmediately && variables.deviceTokens) sink.error?.([{ message: 'Unknown argument "deviceTokens" on field "Subscription.measurementStream".' }]);
    return () => { op.closed = true; };
  },
}));

const anchor = (measurements: string[] = []): DatasourceSelector => ({
  kind: 'anchor', anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'area' }, measurements,
});
const device = (deviceToken: string): DatasourceSelector => ({ kind: 'device', deviceToken, measurements: [] });
const sink = () => ({ next: vi.fn(), error: vi.fn(), reconnected: vi.fn() });
const refusal = [{ message: 'Unknown argument "deviceTokens" on field "Subscription.measurementStream".' }];
const flush = async () => { await vi.advanceTimersByTimeAsync(0); };
const emit = (op: number, deviceToken: string, name = 't') => {
  const sample: MeasurementSample = { id: `${deviceToken}-${name}`, deviceToken, name, value: 42, eventType: 0, occurredTime: null, classifier: null };
  h.operations[op].sink.next({ measurementStream: sample });
};
let hub: DashboardHub;
function setup(tokens = ['a', 'b', 'c']) {
  hub = new DashboardHub({ resolver: { devicesForAnchor: async () => tokens, deviceExists: async () => true } });
  return hub;
}
beforeEach(() => {
  vi.useFakeTimers();
  h.operations.length = 0;
  h.refuseImmediately = false;
  h.resultError = undefined;
});
afterEach(() => { hub?.disposeAll(); vi.useRealTimers(); });

describe('anchor measurement groups', () => {
  it('opens one multi operation and fans out by device and subscriber name', async () => {
    setup();
    const fleet = sink();
    hub.subscribeWidget(anchor(), fleet);
    await flush();
    const members = ['a', 'b', 'c'].map(() => sink());
    ['a', 'b', 'c'].forEach((t, i) => hub.subscribeWidget(device(t), members[i]));
    const filtered = sink();
    hub.subscribeWidget(anchor(['humidity']), filtered);
    await flush();
    expect(h.operations).toHaveLength(1);
    expect(h.operations[0].doc).toBe(MEASUREMENT_STREAM_MULTI);
    expect(h.operations[0].variables).toEqual({ deviceTokens: ['a', 'b', 'c'], name: null });
    ['a', 'b', 'c'].forEach((t) => emit(0, t));
    expect(fleet.next).toHaveBeenCalledTimes(3);
    members.forEach((s, i) => {
      expect(s.next).toHaveBeenCalledTimes(1);
      expect(s.next).toHaveBeenCalledWith(expect.objectContaining({ deviceToken: ['a', 'b', 'c'][i] }));
    });
    expect(filtered.next).not.toHaveBeenCalled();
    emit(0, 'b', 'humidity');
    expect(filtered.next).toHaveBeenCalledTimes(1);
  });

  it('excludes a device already streamed individually and never duplicates its samples', async () => {
    setup();
    const single = sink(), fleet = sink();
    hub.subscribeWidget(device('a'), single);
    await flush();
    hub.subscribeWidget(anchor(), fleet);
    await flush();
    expect(h.operations).toHaveLength(2);
    expect(h.operations[0].doc).toBe(MEASUREMENT_STREAM);
    expect(h.operations[0].variables).toEqual({ deviceToken: 'a', name: null });
    expect(h.operations[1].variables.deviceTokens).toEqual(['b', 'c']);
    emit(0, 'a');
    emit(1, 'a'); // a group must ignore an out-of-group reading
    expect(single.next).toHaveBeenCalledTimes(1);
    expect(fleet.next).toHaveBeenCalledTimes(1);
  });

  it('keeps the operation until the last member subscriber detaches', async () => {
    setup();
    const leaveFleet = hub.subscribeWidget(anchor(), sink());
    await flush();
    const remaining = sink();
    const leaveA = hub.subscribeWidget(device('a'), remaining);
    await flush();
    leaveFleet();
    expect(h.operations[0].closed).toBe(false);
    expect(hub.openStreamCount).toBe(1);
    emit(0, 'a');
    expect(remaining.next).toHaveBeenCalledTimes(1);
    leaveA();
    expect(h.operations[0].closed).toBe(true);
    expect(hub.openStreamCount).toBe(0);
  });

  it('chunks more than 1000 members without empty operations', async () => {
    setup(Array.from({ length: 2001 }, (_, i) => `d${i}`));
    hub.subscribeWidget(anchor(), sink());
    await flush();
    expect(h.operations.map((o) => o.variables.deviceTokens?.length)).toEqual([1000, 1000, 1]);
    expect(new Set(h.operations.flatMap((o) => o.variables.deviceTokens!)).size).toBe(2001);
  });

  it.each([false, true])('falls back permanently without widget errors (synchronous refusal: %s)', async (immediate) => {
    setup();
    h.refuseImmediately = immediate;
    const fleet = sink();
    const leave = hub.subscribeWidget(anchor(), fleet);
    await flush();
    if (!immediate) h.operations[0].sink.error?.(refusal);
    expect(h.operations[0].closed).toBe(true);
    expect(h.operations.slice(1).map((o) => o.variables)).toEqual(['a', 'b', 'c'].map((deviceToken) => ({ deviceToken, name: null })));
    [1, 2, 3].forEach((i) => emit(i, ['a', 'b', 'c'][i - 1]));
    expect(fleet.next).toHaveBeenCalledTimes(3);
    expect(fleet.error).not.toHaveBeenCalled();
    leave();
    expect(h.operations.every((o) => o.closed)).toBe(true);
    hub.subscribeWidget(anchor(), sink());
    await flush();
    expect(h.operations.slice(4)).toHaveLength(3);
    expect(h.operations.slice(4).every((o) => o.doc === MEASUREMENT_STREAM)).toBe(true);
  });

  it('falls back on an error-only result followed by complete and remembers the refusal', async () => {
    setup();
    h.resultError = refusal[0].message;
    const fleet = sink();
    const leave = hub.subscribeWidget(anchor(), fleet);
    await flush();
    expect(h.operations[0].closed).toBe(true);
    expect(h.operations.slice(1).map((o) => o.variables)).toEqual(
      ['a', 'b', 'c'].map((deviceToken) => ({ deviceToken, name: null })),
    );
    expect(h.operations.slice(1).every((o) => o.doc === MEASUREMENT_STREAM)).toBe(true);
    [1, 2, 3].forEach((i) => emit(i, ['a', 'b', 'c'][i - 1]));
    expect(fleet.next).toHaveBeenCalledTimes(3);
    expect(fleet.error).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.operations).toHaveLength(4);
    expect(h.operations.filter((o) => o.doc === MEASUREMENT_STREAM_MULTI)).toHaveLength(1);
    leave();
    const later = sink();
    hub.subscribeWidget(anchor(), later);
    await flush();
    expect(h.operations.slice(4).map((o) => o.variables)).toEqual(
      ['a', 'b', 'c'].map((deviceToken) => ({ deviceToken, name: null })),
    );
    expect(h.operations.slice(4).every((o) => o.doc === MEASUREMENT_STREAM)).toBe(true);
    [4, 5, 6].forEach((i) => emit(i, ['a', 'b', 'c'][i - 4]));
    expect(later.next).toHaveBeenCalledTimes(3);
    expect(later.error).not.toHaveBeenCalled();
  });

  it('reopens the group with backoff after an unrelated error-only result and complete', async () => {
    setup();
    h.resultError = 'Forbidden';
    const fleet = sink();
    hub.subscribeWidget(anchor(), fleet);
    await flush();
    expect(h.operations).toHaveLength(1);
    expect(hub.openStreamCount).toBe(3);
    expect(fleet.error).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(999);
    expect(h.operations).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(h.operations).toHaveLength(2);
    expect(h.operations.every((o) => o.doc === MEASUREMENT_STREAM_MULTI)).toBe(true);
    expect(h.operations[1].variables).toEqual({ deviceTokens: ['a', 'b', 'c'], name: null });
    expect(fleet.error).not.toHaveBeenCalled();
  });

  it.each(['Forbidden: deviceTokens are outside your scope', 'LIMIT_EXCEEDED: deviceTokens exceeds 1000'])('propagates %s and preserves dead connection accounting until detach', async (message) => {
    setup();
    const fleet = sink();
    const leave = hub.subscribeWidget(anchor(), fleet);
    await flush();
    h.operations[0].sink.closed?.();
    const err = new Error(message);
    h.operations[0].sink.error?.(err);
    expect(fleet.error).toHaveBeenCalledWith(err);
    expect(h.operations).toHaveLength(1);
    expect(h.operations[0].closed).toBe(true);
    expect(hub.openStreamCount).toBe(0);
    expect(hub.connectionState).not.toBe('live');
    // A replacement must survive stale detachers, and still uses groups.
    hub.subscribeWidget(anchor(), sink());
    await flush();
    leave();
    expect(hub.openStreamCount).toBe(3);
    expect(h.operations[1].closed).toBe(false);
    expect(hub.connectionState).toBe('live');
  });

  it('reopens on complete with backoff, signals resume, and cancels on teardown', async () => {
    setup();
    const fleet = sink();
    const leave = hub.subscribeWidget(anchor(), fleet);
    await flush();
    h.operations[0].sink.connected?.(false);
    expect(fleet.reconnected).not.toHaveBeenCalled();
    h.operations[0].sink.closed?.();
    expect(hub.connectionState).not.toBe('live');
    h.operations[0].sink.connected?.(true);
    expect(fleet.reconnected).toHaveBeenCalledTimes(3);
    expect(hub.connectionState).toBe('live');
    for (const delay of [1000, 2000]) {
      const before = h.operations.length;
      h.operations[h.operations.length - 1].sink.complete?.();
      await vi.advanceTimersByTimeAsync(delay - 1);
      expect(h.operations).toHaveLength(before);
      await vi.advanceTimersByTimeAsync(1);
      expect(h.operations).toHaveLength(before + 1);
      expect(h.operations[h.operations.length - 1].variables.deviceTokens).toEqual(['a', 'b', 'c']);
    }
    emit(2, 'c'); // a reading resets the backoff
    h.operations[2].sink.complete?.();
    await vi.advanceTimersByTimeAsync(1000);
    expect(h.operations).toHaveLength(4);
    expect(fleet.reconnected).toHaveBeenCalledTimes(12);
    h.operations[3].sink.complete?.();
    leave();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.operations).toHaveLength(4);
  });

  it('does not reopen a completion after the socket closes on sign-out', async () => {
    setup();
    hub.subscribeWidget(anchor(), sink());
    await flush();
    h.operations[0].sink.closed?.();
    h.operations[0].sink.complete?.();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(h.operations).toHaveLength(1);
  });
});

describe('a text reading on the stream', () => {
  it('both stream documents select textValue, and a text sample reaches the subscriber with it', async () => {
    expect(String(MEASUREMENT_STREAM)).toContain('textValue');
    expect(String(MEASUREMENT_STREAM_MULTI)).toContain('textValue');

    setup();
    const s = sink();
    hub.subscribeWidget(device('a'), s);
    await flush();
    const text: MeasurementSample = { id: 'a-mode', deviceToken: 'a', name: 'mode', value: null, textValue: 'eco', eventType: 0, occurredTime: null, classifier: null };
    h.operations[0].sink.next({ measurementStream: text });
    emit(0, 'a', 'temp');
    expect(s.next).toHaveBeenCalledTimes(2);
    expect(s.next).toHaveBeenNthCalledWith(1, expect.objectContaining({ name: 'mode', value: null, textValue: 'eco' }));
    expect(s.next).toHaveBeenNthCalledWith(2, expect.objectContaining({ name: 'temp', value: 42 }));
  });
});

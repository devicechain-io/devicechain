// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useMeasurementStream with a window as a SPAN and a pause: what it keeps, what it freezes, and
// what it hands back on resume. The clock is injected (`now`), so nothing here sleeps.

import type { DashboardHub, DatasourceSelector, MeasurementSample, WidgetStreamSink } from '@devicechain/dashboards';
import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { MAX_SPAN_SAMPLES, useMeasurementStream, withinSpan, type MeasurementStreamOptions } from './hooks';

afterEach(cleanup);

const NOW = Date.parse('2026-10-14T12:00:00.000Z');
const ago = (seconds: number) => new Date(NOW - seconds * 1000).toISOString();
const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['t'] };

const sample = (value: number, time: string | null, name = 't'): MeasurementSample => ({
  id: `${name}-${value}-${time}`,
  deviceToken: 'd-1',
  eventType: 0,
  occurredTime: time,
  name,
  value,
  classifier: null,
});

function fakeHub() {
  let sink: WidgetStreamSink | null = null;
  const subscribe = vi.fn((_d: DatasourceSelector, s: WidgetStreamSink) => {
    sink = s;
    return () => {};
  });
  const hub = { subscribeWidget: subscribe } as unknown as DashboardHub;
  return {
    hub,
    subscribe,
    push: (m: MeasurementSample) => act(() => sink?.next(m)),
    // A flood in ONE act: one render instead of one per sample.
    pushAll: (ms: MeasurementSample[]) => act(() => ms.forEach((m) => sink?.next(m))),
  };
}

const clock = () => NOW;

function mount(f: ReturnType<typeof fakeHub>, initial: MeasurementStreamOptions) {
  return renderHook((o: MeasurementStreamOptions) => useMeasurementStream(f.hub, ds, o), { initialProps: initial });
}

describe('withinSpan', () => {
  it('keeps what is inside the span, drops what is older, and keeps what cannot be dated', () => {
    const kept = withinSpan(
      [sample(1, ago(7200)), sample(2, ago(3599)), sample(3, null), sample(4, 'not a time'), sample(5, ago(0))],
      3600,
      NOW,
    );
    expect(kept.map((s) => s.value)).toEqual([2, 3, 4, 5]);
  });

  it('includes the boundary instant', () => {
    expect(withinSpan([sample(1, ago(3600))], 3600, NOW)).toHaveLength(1);
    expect(withinSpan([sample(1, ago(3601))], 3600, NOW)).toHaveLength(0);
  });
});

describe('a window as a span', () => {
  it('drops samples older than the span as new ones arrive, whatever their count', () => {
    const f = fakeHub();
    const { result } = mount(f, { windowSeconds: 600, now: clock });
    f.push(sample(1, ago(900)));
    f.push(sample(2, ago(500)));
    f.push(sample(3, ago(10)));
    expect(result.current.samples.map((s) => s.value)).toEqual([2, 3]);
    // The latest value is not a window: it is what the device last said, and the card shows it.
    expect(result.current.latest.t.value).toBe(3);
  });

  it('is bounded by a sample ceiling so a busy device cannot grow the buffer for days', () => {
    const f = fakeHub();
    const { result } = mount(f, { windowSeconds: 604800, now: clock });
    f.pushAll(Array.from({ length: MAX_SPAN_SAMPLES + 25 }, (_, i) => sample(i, ago(5))));
    expect(result.current.samples).toHaveLength(MAX_SPAN_SAMPLES);
    // The OLDEST went, not the newest.
    expect(result.current.samples[result.current.samples.length - 1].value).toBe(MAX_SPAN_SAMPLES + 24);
  });

  it('keeps a caller-chosen ceiling in place of the default', () => {
    const f = fakeHub();
    const { result } = mount(f, { windowSeconds: 3600, window: 3, now: clock });
    for (let i = 0; i < 6; i++) f.push(sample(i, ago(5)));
    expect(result.current.samples.map((s) => s.value)).toEqual([3, 4, 5]);
  });

  it('is unchanged when no span is given: the count window and its default of 300', () => {
    const f = fakeHub();
    const { result } = mount(f, { now: clock });
    f.push(sample(1, ago(999999)));
    expect(result.current.samples).toHaveLength(1);
  });

  it('prunes a seed to the span too, and re-applies a NEW span without resubscribing', () => {
    const f = fakeHub();
    const seed = [sample(1, ago(7000)), sample(2, ago(3000)), sample(3, ago(100))];
    const { result, rerender } = mount(f, { windowSeconds: 3600, initialSamples: seed, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([2, 3]);
    rerender({ windowSeconds: 600, initialSamples: seed, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([3]);
    rerender({ windowSeconds: 86400, initialSamples: seed, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([1, 2, 3]);
    expect(f.subscribe).toHaveBeenCalledTimes(1);
  });

  it('measures the span against the injected clock, not the wall clock (a recorded run is dated by the run)', () => {
    const f = fakeHub();
    const replay = Date.parse('2001-01-01T00:00:00Z');
    const { result } = mount(f, { windowSeconds: 600, now: () => replay });
    f.push(sample(1, new Date(replay - 60_000).toISOString()));
    expect(result.current.samples).toHaveLength(1);
  });
});

describe('pause', () => {
  it('freezes what the stream returns: no update while paused, and the same object comes back', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, now: clock });
    f.push(sample(1, ago(20)));
    const before = result.current;

    rerender({ windowSeconds: 3600, now: clock, pausedAt: NOW });
    f.push(sample(2, ago(10)));
    f.push(sample(3, ago(5)));

    expect(result.current).toBe(before);
    expect(result.current.latest.t.value).toBe(1);
    expect(result.current.samples.map((s) => s.value)).toEqual([1]);
  });

  it('keeps buffering while frozen, and a resume merges the whole gap, in order, at once', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, now: clock });
    f.push(sample(1, ago(20)));
    rerender({ windowSeconds: 3600, now: clock, pausedAt: NOW });
    f.push(sample(2, ago(10)));
    f.push(sample(3, ago(5)));

    rerender({ windowSeconds: 3600, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([1, 2, 3]);
    expect(result.current.latest.t.value).toBe(3);
  });

  it('does not resubscribe on pause or resume (that would lose the buffer, and re-fetch nothing useful)', () => {
    const f = fakeHub();
    const { rerender } = mount(f, { windowSeconds: 3600, now: clock });
    rerender({ windowSeconds: 3600, now: clock, pausedAt: NOW });
    rerender({ windowSeconds: 3600, now: clock });
    expect(f.subscribe).toHaveBeenCalledTimes(1);
  });

  it('a sample arriving AFTER the resume is merged live, not queued behind it', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, now: clock, pausedAt: NOW });
    rerender({ windowSeconds: 3600, now: clock });
    f.push(sample(9, ago(1)));
    expect(result.current.samples.map((s) => s.value)).toEqual([9]);
  });

  it('opens frozen (a deep link): nothing shows until it resumes, and then everything does', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, now: clock, pausedAt: NOW });
    f.push(sample(1, ago(3)));
    expect(result.current.samples).toEqual([]);
    rerender({ windowSeconds: 3600, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([1]);
  });

  it('bounds what it queues, keeping the newest, so a long pause cannot grow without limit', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, window: 4, now: clock, pausedAt: NOW });
    for (let i = 0; i < 20; i++) f.push(sample(i, ago(5)));
    rerender({ windowSeconds: 3600, window: 4, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([16, 17, 18, 19]);
  });

  it('measures the span back from the freeze instant, so a frozen chart does not slide while time passes', () => {
    const f = fakeHub();
    const seed = [sample(1, ago(3000)), sample(2, ago(100))];
    const later = NOW + 3_000_000; // the wall clock moves on 50 minutes while the chart is frozen
    const { result } = mount(f, { windowSeconds: 3600, initialSamples: seed, now: () => later, pausedAt: NOW });
    expect(result.current.samples.map((s) => s.value)).toEqual([1, 2]);
  });

  it('a different datasource while frozen starts empty and does not inherit the old queue', () => {
    const f = fakeHub();
    const { result, rerender } = renderHook(
      ({ d, o }: { d: DatasourceSelector; o: MeasurementStreamOptions }) => useMeasurementStream(f.hub, d, o),
      { initialProps: { d: ds, o: { windowSeconds: 3600, now: clock, pausedAt: NOW } as MeasurementStreamOptions } },
    );
    f.push(sample(1, ago(3)));
    rerender({ d: { ...ds, deviceToken: 'd-2' }, o: { windowSeconds: 3600, now: clock } });
    expect(result.current.samples).toEqual([]);
  });
});

describe('the retention ceiling never passes for full coverage', () => {
  it('marks a span window the ceiling cut while it was still inside the span, with where it now starts', () => {
    const f = fakeHub();
    const { result } = mount(f, { windowSeconds: 604800, now: clock });
    f.pushAll(Array.from({ length: MAX_SPAN_SAMPLES + 25 }, (_, i) => sample(i, ago(MAX_SPAN_SAMPLES + 25 - i))));
    expect(result.current.samples).toHaveLength(MAX_SPAN_SAMPLES);
    expect(result.current.truncatedBefore).toBe(result.current.samples[0].occurredTime);
    expect(result.current.truncatedBefore).toBe(ago(MAX_SPAN_SAMPLES));
  });

  it('does NOT mark a window that lost only what fell out of the span, and clears once the hole has aged out', () => {
    const f = fakeHub();
    let t = NOW;
    const now = () => t;
    const { result } = mount(f, { windowSeconds: 600, window: 3, now });
    f.push(sample(1, ago(900)));
    f.push(sample(2, ago(800)));
    f.push(sample(3, ago(10)));
    expect(result.current.truncatedBefore).toBeUndefined();
    // Evicting a sample still INSIDE the span is a hole at the window's start...
    for (let i = 4; i < 7; i++) f.push(sample(i, ago(13 - i)));
    expect(result.current.samples.map((s) => s.value)).toEqual([4, 5, 6]);
    expect(result.current.truncatedBefore).toBe(ago(9));
    // ...until the evicted sample would have aged out of the span anyway.
    t = NOW + 591_500;
    f.push(sample(7, new Date(t).toISOString()));
    expect(result.current.samples.map((s) => s.value)).toEqual([5, 6, 7]);
    expect(result.current.truncatedBefore).toBeUndefined();
  });

  it('marks a seed that alone is larger than the ceiling (many series over a long span)', () => {
    const f = fakeHub();
    const seed = Array.from({ length: 6 }, (_, i) => sample(i, ago(600 - i)));
    const { result } = mount(f, { windowSeconds: 3600, window: 4, initialSamples: seed, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([2, 3, 4, 5]);
    expect(result.current.truncatedBefore).toBe(ago(598));
  });

  it('marks a pause that outlasted the ceiling, once it resumes', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, window: 4, now: clock, pausedAt: NOW });
    for (let i = 0; i < 20; i++) f.push(sample(i, ago(100 - i)));
    rerender({ windowSeconds: 3600, window: 4, now: clock });
    expect(result.current.samples.map((s) => s.value)).toEqual([16, 17, 18, 19]);
    expect(result.current.truncatedBefore).toBe(ago(84));
  });

  it('a count window (no span) is never marked: it was never a promise of a span', () => {
    const f = fakeHub();
    const { result } = mount(f, { window: 2, now: clock });
    for (let i = 0; i < 5; i++) f.push(sample(i, ago(5)));
    expect(result.current.truncatedBefore).toBeUndefined();
  });
});

describe('pause: what resume hands back', () => {
  it('keeps the latest value of a rarely-reporting measurement whose sample the capped queue evicted', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, window: 4, now: clock });
    f.push(sample(0, ago(200), 'door'));
    rerender({ windowSeconds: 3600, window: 4, now: clock, pausedAt: NOW });
    f.push(sample(1, ago(100), 'door')); // the door changes once, early in the pause...
    for (let i = 0; i < 10; i++) f.push(sample(i, ago(50 - i))); // ...then a busy measurement floods the queue
    rerender({ windowSeconds: 3600, window: 4, now: clock });
    expect(result.current.latest.door.value).toBe(1);
    expect(result.current.latest.t.value).toBe(9);
  });

  it('merges each queued sample exactly once across repeated pauses and resumes', () => {
    const f = fakeHub();
    const { result, rerender } = mount(f, { windowSeconds: 3600, now: clock });
    const paused = { windowSeconds: 3600, now: clock, pausedAt: NOW };
    const live = { windowSeconds: 3600, now: clock };
    f.push(sample(1, ago(40)));
    rerender(paused);
    f.push(sample(2, ago(30)));
    rerender(live);
    rerender(live);
    f.push(sample(3, ago(20)));
    rerender(paused);
    f.push(sample(4, ago(10)));
    rerender(live);
    expect(result.current.samples.map((s) => s.value)).toEqual([1, 2, 3, 4]);
  });

  it('lets a sample go at ARRIVAL once it is outside the span, so a wider span later does not resurrect it', () => {
    // The live buffer holds the span and no more: widening is served by the renderer's re-seed,
    // which reads the wider span from history, not by a live buffer that grew without bound.
    const f = fakeHub();
    let t = NOW;
    const now = () => t;
    const { result, rerender } = mount(f, { windowSeconds: 60, now });
    f.push(sample(1, ago(30)));
    t = NOW + 50_000;
    f.push(sample(2, new Date(NOW + 49_000).toISOString()));
    rerender({ windowSeconds: 3600, now });
    expect(result.current.samples.map((s) => s.value)).toEqual([2]);
  });
});

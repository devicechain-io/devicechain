// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';

import { RecordedClock, type ClockEvent, type ClockTicker } from './recorded';

// A ticker that never fires by itself; the test drives the clock with advance().
function manualTicker(): ClockTicker & { started: number; stopped: number } {
  const t = {
    started: 0,
    stopped: 0,
    start() {
      t.started++;
      return () => {
        t.stopped++;
      };
    },
  };
  return t;
}

describe('RecordedClock', () => {
  it('starts paused at zero and does not move until played', () => {
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    expect([clock.timeMs, clock.playing, clock.rate, clock.ended]).toEqual([0, false, 1, false]);
    clock.advance(500);
    expect(clock.timeMs).toBe(0);
  });

  it('advances by wall time scaled by the playback rate', () => {
    const clock = new RecordedClock(100_000, { ticker: manualTicker() });
    clock.play();
    clock.advance(1_000);
    expect(clock.timeMs).toBe(1_000);
    clock.setRate(4);
    clock.advance(1_000);
    expect(clock.timeMs).toBe(5_000);
    clock.setRate(16);
    clock.advance(1_000);
    expect(clock.timeMs).toBe(21_000);
  });

  it('pauses, and resumes from where it stopped', () => {
    const ticker = manualTicker();
    const clock = new RecordedClock(10_000, { ticker });
    clock.play();
    clock.advance(2_000);
    clock.pause();
    expect(clock.playing).toBe(false);
    expect(ticker.stopped).toBe(1);
    clock.advance(5_000);
    expect(clock.timeMs).toBe(2_000);
    clock.play();
    clock.advance(1_000);
    expect(clock.timeMs).toBe(3_000);
    expect(ticker.started).toBe(2);
  });

  it('clamps a seek to the run and rejects a non-finite one', () => {
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    clock.seek(-5);
    expect(clock.timeMs).toBe(0);
    clock.seek(99_999);
    expect(clock.timeMs).toBe(10_000);
    clock.seek(4_000);
    expect(clock.timeMs).toBe(4_000);
    expect(() => clock.seek(Number.NaN)).toThrow(RangeError);
    expect(() => clock.seek(Infinity)).toThrow(RangeError);
  });

  it('stops at the end of the run without looping', () => {
    const ticker = manualTicker();
    const clock = new RecordedClock(10_000, { ticker });
    clock.play();
    clock.advance(9_000);
    clock.advance(9_000);
    expect(clock.timeMs).toBe(10_000);
    expect(clock.ended).toBe(true);
    expect(clock.playing).toBe(false);
    expect(ticker.stopped).toBe(1);
    clock.play();
    expect(clock.playing).toBe(false);
    clock.advance(1_000);
    expect(clock.timeMs).toBe(10_000);
  });

  it('can play again after seeking back from the end', () => {
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    clock.seek(10_000);
    clock.seek(0);
    clock.play();
    clock.advance(1_000);
    expect(clock.timeMs).toBe(1_000);
  });

  it('seeking to the end while playing stops playback', () => {
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    clock.play();
    clock.seek(10_000);
    expect(clock.playing).toBe(false);
  });

  it('rejects a rate outside the supported set', () => {
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    expect(() => clock.setRate(2 as never)).toThrow(RangeError);
    expect(clock.rate).toBe(1);
  });

  it('rejects a bad duration or a negative delta', () => {
    expect(() => new RecordedClock(0)).toThrow(RangeError);
    expect(() => new RecordedClock(Number.NaN)).toThrow(RangeError);
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    clock.play();
    expect(() => clock.advance(-1)).toThrow(RangeError);
  });

  it('tells subscribers why the cursor moved, and stops telling an unsubscribed one', () => {
    const clock = new RecordedClock(10_000, { ticker: manualTicker() });
    const events: ClockEvent[] = [];
    const off = clock.subscribe((ev) => events.push(ev));
    clock.play();
    clock.advance(1_000);
    clock.seek(5_000);
    clock.setRate(4);
    clock.pause();
    expect(events).toEqual([
      { timeMs: 0, reason: 'state' },
      { timeMs: 1_000, reason: 'tick' },
      { timeMs: 5_000, reason: 'seek' },
      { timeMs: 5_000, reason: 'state' },
      { timeMs: 5_000, reason: 'state' },
    ]);
    off();
    clock.seek(0);
    expect(events).toHaveLength(5);
  });

  it('starts at a given position', () => {
    expect(new RecordedClock(10_000, { startMs: 3_000, ticker: manualTicker() }).timeMs).toBe(3_000);
  });

  it('drives itself from the default frame source when none is injected', () => {
    vi.useFakeTimers();
    try {
      const clock = new RecordedClock(10_000);
      clock.play();
      vi.advanceTimersByTime(1_000);
      expect(clock.timeMs).toBeGreaterThan(0);
      clock.pause();
      const frozen = clock.timeMs;
      vi.advanceTimersByTime(1_000);
      expect(clock.timeMs).toBe(frozen);
    } finally {
      vi.useRealTimers();
    }
  });
});

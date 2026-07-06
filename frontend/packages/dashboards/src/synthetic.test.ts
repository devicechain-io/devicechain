// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { AlarmSnapshot, AlarmSubscription, CommandSnapshot } from './hub';
import { SyntheticDataSource } from './synthetic';
import type { MeasurementSample } from './types';
import type { DatasourceSelector } from './types';

function deviceSel(measurements: string[]): DatasourceSelector {
  return { kind: 'device', deviceToken: 'x', measurements };
}

function collect() {
  const samples: MeasurementSample[] = [];
  return { sink: { next: (s: MeasurementSample) => samples.push(s) }, samples };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-07-03T00:00:00Z'));
});
afterEach(() => {
  vi.useRealTimers();
});

describe('SyntheticDataSource', () => {
  it('backfills a full window per measurement on subscribe', () => {
    const src = new SyntheticDataSource({ backfill: 10 });
    const { sink, samples } = collect();
    src.subscribeWidget(deviceSel(['temperature', 'humidity']), sink);
    // 10 backfill points × 2 names, delivered synchronously.
    expect(samples).toHaveLength(20);
    expect(new Set(samples.map((s) => s.name))).toEqual(new Set(['temperature', 'humidity']));
    // Backfill is chronological (non-decreasing timestamps).
    const temps = samples.filter((s) => s.name === 'temperature').map((s) => Date.parse(s.occurredTime!));
    expect(temps).toEqual([...temps].sort((a, b) => a - b));
  });

  it('emits one more sample per name each interval tick', () => {
    const src = new SyntheticDataSource({ backfill: 0, intervalMs: 1000 });
    const { sink, samples } = collect();
    src.subscribeWidget(deviceSel(['t']), sink);
    expect(samples).toHaveLength(0); // no backfill
    vi.advanceTimersByTime(3000);
    expect(samples).toHaveLength(3);
  });

  it('keeps every value within [min, max] for all generators', () => {
    for (const generator of ['sine', 'ramp', 'random-walk'] as const) {
      const src = new SyntheticDataSource({ generator, backfill: 50, min: 10, max: 20 });
      const { sink, samples } = collect();
      src.subscribeWidget(deviceSel(['t']), sink);
      vi.advanceTimersByTime(50_000);
      for (const s of samples) {
        expect(s.value).not.toBeNull();
        expect(s.value! >= 10 && s.value! <= 20).toBe(true);
      }
    }
  });

  it('falls back to a "value" series when the selector lists no measurements', () => {
    const src = new SyntheticDataSource({ backfill: 3 });
    const { sink, samples } = collect();
    src.subscribeWidget(deviceSel([]), sink);
    expect(samples).toHaveLength(3);
    expect(samples.every((s) => s.name === 'value')).toBe(true);
  });

  it('stops emitting after the subscription is disposed', () => {
    const src = new SyntheticDataSource({ backfill: 0 });
    const { sink, samples } = collect();
    const dispose = src.subscribeWidget(deviceSel(['t']), sink);
    vi.advanceTimersByTime(2000);
    const afterTwo = samples.length;
    dispose();
    vi.advanceTimersByTime(5000);
    expect(samples).toHaveLength(afterTwo);
  });

  it('disposeAll stops every stream', () => {
    const src = new SyntheticDataSource({ backfill: 0 });
    const a = collect();
    const b = collect();
    src.subscribeWidget(deviceSel(['t']), a.sink);
    src.subscribeWidget(deviceSel(['t']), b.sink);
    vi.advanceTimersByTime(1000);
    src.disposeAll();
    const aCount = a.samples.length;
    const bCount = b.samples.length;
    vi.advanceTimersByTime(5000);
    expect(a.samples).toHaveLength(aCount);
    expect(b.samples).toHaveLength(bCount);
  });

  it('produces finite values even with a degenerate config (periodMs 0)', () => {
    const src = new SyntheticDataSource({ generator: 'sine', backfill: 5, periodMs: 0, intervalMs: 0 });
    const { sink, samples } = collect();
    src.subscribeWidget(deviceSel(['t']), sink);
    expect(samples).toHaveLength(5);
    for (const s of samples) expect(Number.isFinite(s.value!)).toBe(true);
  });

  it('gives distinct series different phase (sine)', () => {
    const src = new SyntheticDataSource({ generator: 'sine', backfill: 1 });
    const { sink, samples } = collect();
    src.subscribeWidget(deviceSel(['alpha', 'beta']), sink);
    const alpha = samples.find((s) => s.name === 'alpha')!.value;
    const beta = samples.find((s) => s.name === 'beta')!.value;
    expect(alpha).not.toBe(beta); // different phase → different value at the same instant
  });
});

function collectAlarms() {
  const snapshots: AlarmSnapshot[] = [];
  return { sink: { next: (s: AlarmSnapshot) => snapshots.push(s) }, snapshots };
}

describe('SyntheticDataSource.subscribeAlarms', () => {
  it('emits a non-empty snapshot of AlarmRows synchronously on subscribe', () => {
    const src = new SyntheticDataSource();
    const { sink, snapshots } = collectAlarms();
    const sub: AlarmSubscription = { pageSize: 50 };

    src.subscribeAlarms(sub, sink);

    expect(snapshots).toHaveLength(1); // immediate, before any timer tick
    const snap = snapshots[0];
    expect(snap.alarms.length).toBeGreaterThan(0);
    expect(snap.total).toBe(snap.alarms.length);
    // Each row is a well-formed AlarmRow.
    for (const row of snap.alarms) {
      expect(typeof row.token).toBe('string');
      expect(typeof row.severity).toBe('string');
      expect(typeof row.alarmKey).toBe('string');
      expect('raisedTime' in row).toBe(true);
    }
  });

  it('applies the severity filter, yielding only matching rows', () => {
    const src = new SyntheticDataSource();
    const { sink, snapshots } = collectAlarms();
    const sub: AlarmSubscription = { pageSize: 50, severity: 'CRITICAL' };

    src.subscribeAlarms(sub, sink);

    const snap = snapshots[0];
    expect(snap.alarms.length).toBeGreaterThan(0);
    expect(snap.alarms.every((a) => a.severity === 'CRITICAL')).toBe(true);
  });

  it('stops emitting after the returned disposer is called', () => {
    const src = new SyntheticDataSource({ intervalMs: 1000 });
    const { sink, snapshots } = collectAlarms();
    const dispose = src.subscribeAlarms({ pageSize: 50 }, sink);

    vi.advanceTimersByTime(2500); // ~2 re-emits on top of the initial
    const afterRun = snapshots.length;
    expect(afterRun).toBeGreaterThan(1);

    dispose();
    vi.advanceTimersByTime(5000);
    expect(snapshots).toHaveLength(afterRun); // no emit after dispose
  });
});

function collectCommands() {
  const snapshots: CommandSnapshot[] = [];
  return { sink: { next: (s: CommandSnapshot) => snapshots.push(s) }, snapshots };
}

describe('SyntheticDataSource.subscribeCommands', () => {
  it('emits a non-empty command history with a bound target device synchronously', () => {
    const src = new SyntheticDataSource();
    const { sink, snapshots } = collectCommands();

    src.subscribeCommands({ pageSize: 20 }, sink);

    expect(snapshots).toHaveLength(1); // immediate, before any timer tick
    const snap = snapshots[0];
    expect(snap.deviceToken).toBeTruthy(); // a target device so Send renders
    expect(snap.commands.length).toBeGreaterThan(0);
    expect(snap.total).toBe(snap.commands.length);
    for (const row of snap.commands) {
      expect(typeof row.token).toBe('string');
      expect(typeof row.name).toBe('string');
      expect(typeof row.status).toBe('string');
    }
  });

  it('caps the emitted history to the page size', () => {
    const src = new SyntheticDataSource();
    const { sink, snapshots } = collectCommands();

    src.subscribeCommands({ pageSize: 2 }, sink);

    expect(snapshots[0].commands).toHaveLength(2);
    expect(snapshots[0].total).toBeGreaterThan(2); // total still reflects the full set
  });

  it('stops emitting after the returned disposer is called', () => {
    const src = new SyntheticDataSource({ intervalMs: 1000 });
    const { sink, snapshots } = collectCommands();
    const dispose = src.subscribeCommands({ pageSize: 20 }, sink);

    vi.advanceTimersByTime(2500);
    const afterRun = snapshots.length;
    expect(afterRun).toBeGreaterThan(1);

    dispose();
    vi.advanceTimersByTime(5000);
    expect(snapshots).toHaveLength(afterRun);
  });
});

describe('SyntheticDataSource action stubs', () => {
  it('grants every authority so preview shows action controls', () => {
    expect(new SyntheticDataSource().can()).toBe(true);
  });

  it('acknowledge/clear resolve without touching a backend', async () => {
    const src = new SyntheticDataSource();
    await expect(src.acknowledgeAlarm()).resolves.toBeUndefined();
    await expect(src.clearAlarm()).resolves.toBeUndefined();
  });

  it('sendCommand resolves with a stub dispatch token', async () => {
    const src = new SyntheticDataSource();
    await expect(src.sendCommand()).resolves.toEqual({ token: expect.any(String) });
  });
});

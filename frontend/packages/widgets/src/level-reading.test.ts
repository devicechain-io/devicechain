// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What every level widget reads off its options and data: the configuration reason a scale
// widget has nothing to draw (the author's fix is named before the absence of data), and the
// reading itself -- sample, display, band and fill -- including an unbound slot, a missing
// sample and a normalised value.

import type { MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import type { MeasurementStreamState } from '@devicechain/widget-sdk';
import { renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';

import { emptyReason, useLevelReading, type LevelInput, type LevelReading } from './level-reading';
import { levelStrings } from './level-strings';
import { readRange } from './value-scale';

beforeEach(() => {
  document.documentElement.lang = 'en';
});

const widget = (options: Record<string, unknown> = {}, kind: 'device' | 'unbound' = 'device'): WidgetInstance =>
  ({
    id: 'w',
    type: 'level-gauge',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    datasource:
      kind === 'unbound'
        ? { kind: 'unbound', measurements: ['level'] }
        : { kind: 'device', deviceToken: 'd-1', measurements: ['level'] },
    options,
  }) as unknown as WidgetInstance;

const sample = (value: number | null): MeasurementSample => ({
  id: 's-1',
  deviceToken: 'd-1',
  eventType: 1,
  occurredTime: '2026-10-14T11:59:00Z',
  name: 'level',
  value,
  classifier: null,
});

const data = (latest: Record<string, MeasurementSample>): MeasurementStreamState =>
  ({ latest, history: [], error: null }) as unknown as MeasurementStreamState;

const input = (over: Partial<LevelInput> = {}): LevelInput => ({
  thresholds: [],
  unit: undefined,
  precision: undefined,
  ...over,
});

const read = (w: WidgetInstance, d: MeasurementStreamState, i: LevelInput): LevelReading =>
  renderHook(() => useLevelReading(w, d, i)).result.current;

describe('emptyReason', () => {
  const strings = levelStrings();
  const reading = (range: LevelReading['range']) => ({ range, strings }) as LevelReading;

  it('names the missing scale when neither end is configured', () => {
    expect(emptyReason(reading(readRange(undefined, undefined)))).toBe(strings.setRange);
  });

  it('names the missing scale when only one end is configured, either one', () => {
    expect(emptyReason(reading(readRange(0, undefined)))).toBe(strings.setRange);
    expect(emptyReason(reading(readRange(undefined, 100)))).toBe(strings.setRange);
  });

  it('names an inverted or empty scale as a different fix', () => {
    expect(emptyReason(reading(readRange(10, 10)))).toBe(strings.badRange);
    expect(emptyReason(reading(readRange(10, 0)))).toBe(strings.badRange);
    expect(strings.badRange).not.toBe(strings.setRange);
  });

  it('is undefined for a usable scale, and for a widget with no scale at all', () => {
    expect(emptyReason(reading(readRange(0, 100)))).toBeUndefined();
    expect(emptyReason(reading(undefined))).toBeUndefined();
  });

  it('takes the documented default for an end left unset, so it is not missing', () => {
    expect(emptyReason(reading(readRange(undefined, undefined, { min: 0, max: 100 })))).toBeUndefined();
  });
});

describe('useLevelReading', () => {
  it('reads the pinned measurement, formats it and finds its fill on the scale', () => {
    const r = read(widget(), data({ level: sample(25) }), input({ unit: '%', precision: 1, range: readRange(0, 100) }));
    expect(r.name).toBe('level');
    expect(r.unbound).toBe(false);
    expect(r.value).toBe(25);
    expect(r.display).toBe('25.0');
    expect(r.unit).toBe('%');
    expect(r.fill).toMatchObject({ fraction: 0.25, position: 'within' });
  });

  it('with no sample yet has no value and no fill, and the display is not a number', () => {
    const r = read(widget(), data({}), input({ range: readRange(0, 100) }));
    expect(r.sample).toBeUndefined();
    expect(r.value).toBeUndefined();
    expect(r.fill).toBeUndefined();
    expect(r.display).not.toMatch(/\d/);
  });

  it('with a sample that carries no value has no value either', () => {
    const r = read(widget(), data({ level: sample(null) }), input({ range: readRange(0, 100) }));
    expect(r.value).toBeUndefined();
    expect(r.fill).toBeUndefined();
  });

  it('marks an unbound slot, so an empty reading means "choose a device"', () => {
    const r = read(widget({}, 'unbound'), data({}), input());
    expect(r.unbound).toBe(true);
    expect(r.value).toBeUndefined();
  });

  it('has no fill with a scale that is not usable, even with a value', () => {
    const r = read(widget(), data({ level: sample(25) }), input({ range: readRange(undefined, undefined) }));
    expect(r.value).toBe(25);
    expect(r.fill).toBeUndefined();
  });

  it('finds the threshold band the value is in, and none below the first', () => {
    const thresholds = [
      { value: 20, level: 'warning' as const },
      { value: 80, level: 'critical' as const },
    ];
    expect(read(widget(), data({ level: sample(50) }), input({ thresholds })).band?.level).toBe('warning');
    expect(read(widget(), data({ level: sample(95) }), input({ thresholds })).band?.level).toBe('critical');
    const below = read(widget(), data({ level: sample(5) }), input({ thresholds }));
    expect(below.band).toBeUndefined();
    expect(below.levelColor).toBeUndefined();
  });

  it('shows the normalised value, and judges band, display and fill by it', () => {
    const normalize = (raw: number | undefined) => (raw === undefined ? undefined : ((raw % 360) + 360) % 360);
    const r = read(widget(), data({ level: sample(-90) }), input({ range: readRange(0, 360), normalize }));
    expect(r.value).toBe(270);
    expect(r.fill?.fraction).toBeCloseTo(0.75);
  });

  it('treats a value the normaliser rejects as no value', () => {
    const r = read(widget(), data({ level: sample(10) }), input({ range: readRange(0, 100), normalize: () => undefined }));
    expect(r.value).toBeUndefined();
    expect(r.fill).toBeUndefined();
  });
});

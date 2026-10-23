// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The level-and-indicator widgets (linear-gauge, level-gauge, battery, status-light,
// signal-bars): fill geometry, clamping, threshold bands, the neutral `normal` band, the
// explained empty states, and units/precision. The pure arithmetic is in value-scale.test.ts;
// these prove each widget WIRES it and renders what it computes.

import type { MeasurementSample, WidgetInstance, WidgetType } from '@devicechain/dashboards';
import type { MeasurementStreamState } from '@devicechain/widget-sdk';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Battery, BATTERY_FILL_WIDTH } from './widgets/battery';
import { LevelGauge } from './widgets/level-gauge';
import { LinearGauge } from './widgets/linear-gauge';
import { SETTLE_MS } from './no-reading';
import { SignalBars } from './widgets/signal-bars';
import { StatusLight } from './widgets/status-light';

// An explained empty state waits a moment (a board still loading has no reading yet) before it says why.
function settle() {
  act(() => {
    vi.advanceTimersByTime(SETTLE_MS);
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const widget = (type: WidgetType, options: Record<string, unknown>): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 3, rows: 3 } },
  datasource: { kind: 'device', deviceToken: 'd', measurements: ['m'] },
  options,
});

const sample = (value: number | null): MeasurementSample => ({
  id: 's',
  deviceToken: 'd',
  eventType: 0,
  occurredTime: '2026-07-02T10:00:00Z',
  name: 'm',
  value,
  classifier: null,
});

const data = (value: number | null | 'none'): MeasurementStreamState =>
  value === 'none'
    ? { latest: {}, samples: [], error: null }
    : { latest: { m: sample(value) }, samples: [sample(value)], error: null };

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const styleOf = (sel: string) => q(sel)?.getAttribute('style') ?? '';

// The neutral accent every widget keeps for a `normal` band and for no band.
const ACCENT = 'var(--chart-1)';
// SEVERITY_TOKENS light values, which paint when the host defines no token (jsdom).
// jsdom normalises a colour to rgb() when it is set on an element, so compare through the same parser.
const paint = (colour: string) => {
  const el = document.createElement('div');
  el.style.color = colour;
  return el.style.color;
};
const WARNING = paint('hsl(199, 89%, 44%)');
const CRITICAL = paint('hsl(0, 72%, 46%)');

describe('linear-gauge', () => {
  const opts = { min: 0, max: 100 };
  const render_ = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<LinearGauge widget={widget('linear-gauge', options)} data={data(value)} />);

  it.each([
    [0, 0, '0%'],
    [50, 0.5, '50%'],
    [100, 1, '100%'],
    [25, 0.25, '25%'],
  ])('value %s fills %s of the track', (value, fraction, width) => {
    render_(opts, value);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe(String(fraction));
    expect(styleOf('[data-fill-bar]')).toContain(`width: ${width}`);
    expect(q('[data-out-of-range]')).toBeNull();
  });

  it('pins a value off the scale to the end, marks it, and still prints the true value', () => {
    render_(opts, 130);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe('1');
    expect(styleOf('[data-fill-bar]')).toContain('width: 100%');
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('above');
    expect(screen.getByRole('img', { name: 'Above scale' })).toBeTruthy();
    expect(screen.getByText('130')).toBeTruthy();
    cleanup();
    render_(opts, -5);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe('0');
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('below');
    expect(screen.getByRole('img', { name: 'Below scale' })).toBeTruthy();
    expect(screen.getByText('-5')).toBeTruthy();
  });

  // Reviewer additions: the marker's direction, the meter's clamped value with the true text,
  // the scale named before the data, and the readout's neutral in a normal band.
  it('points the marker the way the value left the scale', () => {
    render_(opts, 130);
    expect(screen.getByRole('img', { name: 'Above scale' }).textContent).toBe('▲');
    cleanup();
    render_(opts, -5);
    expect(screen.getByRole('img', { name: 'Below scale' }).textContent).toBe('▼');
  });

  it('keeps the meter value on the scale and states the true reading in its text', () => {
    render_({ ...opts, unit: 'C' }, 130);
    const meter = screen.getByRole('meter');
    expect(meter.getAttribute('aria-valuenow')).toBe('100');
    expect(meter.getAttribute('aria-valuetext')).toBe('130 C');
  });

  it('asks for the scale before it reports missing data', () => {
    render_({}, 'none');
    expect(screen.getByText('Set min and max')).toBeTruthy();
    expect(screen.queryByText('No data')).toBeNull();
  });

  it('prints the readout in the foreground colour in a normal band, the band colour otherwise', () => {
    const t = [{ value: 0, level: 'normal' }, { value: 80, level: 'warning' }];
    render_({ ...opts, thresholds: t }, 50);
    expect(styleOf('[data-level-readout]')).toContain('var(--foreground)');
    cleanup();
    render_({ ...opts, thresholds: t }, 90);
    expect(styleOf('[data-level-readout]')).toContain(WARNING);
  });

  it('draws the vertical bar as a column, and the thermometer with a bulb', () => {
    render_({ ...opts, orientation: 'vertical' }, 50);
    expect(styleOf('[data-fill-bar]')).toContain('height: 50%');
    expect(q('[data-bulb]')).toBeNull();
    cleanup();
    render_({ ...opts, style: 'thermometer' }, 50);
    expect(styleOf('[data-fill-bar]')).toContain('height: 50%');
    expect(q('[data-bulb]')).not.toBeNull();
    cleanup();
    // The bar is horizontal by default and has no bulb.
    render_(opts, 50);
    expect(styleOf('[data-fill-bar]')).toContain('width: 50%');
    expect(q('[data-bulb]')).toBeNull();
  });

  const thresholds = [
    { value: 0, level: 'critical' },
    { value: 15, level: 'normal' },
    { value: 80, level: 'warning' },
  ];

  it.each([
    [-1, undefined],
    [0, 'critical'],
    [14.9, 'critical'],
    [15, 'normal'],
    [79.9, 'normal'],
    [80, 'warning'],
  ])('value %s sits in band %s', (value, level) => {
    render_({ ...opts, thresholds }, value);
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe(level);
  });

  it('colours the fill by the coloured band and keeps the neutral accent for normal', () => {
    render_({ ...opts, thresholds }, 90);
    expect(styleOf('[data-fill-bar]')).toContain(WARNING);
    cleanup();
    render_({ ...opts, thresholds }, 5);
    expect(styleOf('[data-fill-bar]')).toContain(CRITICAL);
    cleanup();
    // The normal band paints exactly what no thresholds at all paint, and no severity hue.
    render_({ ...opts, thresholds }, 50);
    const normal = styleOf('[data-fill-bar]');
    expect(normal).toContain(ACCENT);
    expect(normal).not.toContain(CRITICAL);
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('normal');
    cleanup();
    render_(opts, 50);
    expect(styleOf('[data-fill-bar]')).toBe(normal);
  });

  it('draws a band on the track only for a coloured level', () => {
    render_({ ...opts, thresholds }, 50);
    // critical (0-15) and warning (80-100); the normal stretch is the bare track.
    expect(document.querySelectorAll('[data-band]')).toHaveLength(2);
  });

  it('shows the value with its unit and precision', () => {
    render_({ ...opts, unit: 'C', precision: 1 }, 27.456);
    expect(screen.getByText('27.5')).toBeTruthy();
    expect(screen.getByText('C')).toBeTruthy();
  });

  it('says why it cannot draw instead of drawing a fake value', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    render_(opts, 'none');
    settle();
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(q('[data-fill]')).toBeNull();
    expect(q('[role="meter"]')).toBeNull();
    cleanup();
    render_(opts, null);
    settle();
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(screen.queryByText('0')).toBeNull();
  });

  it('requires a scale: no min/max is an explained empty state, even with data', () => {
    render_({}, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
    expect(q('[data-fill]')).toBeNull();
    cleanup();
    render_({ min: 0 }, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
    cleanup();
    render_({ min: 10, max: 10 }, 50);
    expect(screen.getByText('Max must be above min')).toBeTruthy();
    cleanup();
    // A scale that is not numbers is no scale (read as unset), never a coerced one.
    render_({ min: '0', max: '100' }, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
  });

  it('keeps a 13-digit reading inside its column at the narrowest size', () => {
    // The column is 160 px wide; the readout must size its font so the characters fit it.
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(private cb: (entries: unknown[]) => void) {}
        observe() {
          this.cb([{ contentRect: { width: 160, height: 120 } }]);
        }
        disconnect() {}
      },
    );
    render_({ min: 0, max: 1e12, unit: 'C' }, 1e12);
    const readout = q('[data-level-readout]')!;
    expect(readout.textContent).toContain('1000000000000');
    const font = parseFloat(readout.style.fontSize);
    // The curve's own bound: width / (chars * glyph width). 13 digits plus the unit's share.
    expect(font * (13 + 2) * 0.65).toBeLessThanOrEqual(160);
  });
});

describe('level-gauge', () => {
  const opts = { min: 0, max: 200 };
  const render_ = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<LevelGauge widget={widget('level-gauge', options)} data={data(value)} />);

  it.each([
    [0, '0%'],
    [50, '25%'],
    [100, '50%'],
    [200, '100%'],
  ])('value %s fills the liquid to %s', (value, height) => {
    render_(opts, value);
    expect(styleOf('[data-liquid]')).toContain(`height: ${height}`);
  });

  it('pins an overfull tank to the brim and marks it', () => {
    render_(opts, 260);
    expect(styleOf('[data-liquid]')).toContain('height: 100%');
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('above');
    expect(screen.getByText('260')).toBeTruthy();
  });

  it('reads out the percent of the scale, unclamped, when asked', () => {
    render_({ ...opts, readout: 'percent' }, 50);
    expect(screen.getByText('25')).toBeTruthy();
    expect(screen.getByText('%')).toBeTruthy();
    cleanup();
    render_({ ...opts, readout: 'percent' }, 260);
    expect(screen.getByText('130')).toBeTruthy();
  });

  it('colours the liquid by band and keeps the accent for normal', () => {
    const thresholds = [
      { value: 0, level: 'critical' },
      { value: 40, level: 'normal' },
    ];
    // The liquid's own background, not its rim: the rim shares the colour and would mask a miss.
    const liquid = () => q('[data-liquid]')!.style.background;
    render_({ ...opts, thresholds }, 10);
    expect(liquid()).toBe(CRITICAL);
    cleanup();
    render_({ ...opts, thresholds }, 100);
    const normal = liquid();
    expect(normal).toContain(ACCENT);
    cleanup();
    render_(opts, 100);
    expect(liquid()).toBe(normal);
  });

  it('draws a band as a muted scale marker until the level is in it', () => {
    // A low-fuel band: critical below 30 on a 0..200 scale, normal above.
    const thresholds = [
      { value: 0, level: 'critical' },
      { value: 30, level: 'normal' },
    ];
    const band = () => q('[data-band]')!;
    // A full tank: the band is on the strip, at the marker strength, and not the active one.
    render_({ ...opts, thresholds }, 190);
    expect(document.querySelectorAll('[data-band]')).toHaveLength(1);
    expect(band().style.opacity).toBe('0.35');
    expect(band().hasAttribute('data-band-active')).toBe(false);
    cleanup();
    // In the band (and at its empty end): full strength, agreeing with the liquid.
    for (const v of [10, 0]) {
      render_({ ...opts, thresholds }, v);
      expect(band().style.opacity, `at ${v}`).toBe('1');
      expect(band().hasAttribute('data-band-active'), `at ${v}`).toBe(true);
      cleanup();
    }
    // At the threshold itself the level has left the band (a "below 30" rule does not fire at 30).
    render_({ ...opts, thresholds }, 30);
    expect(band().style.opacity).toBe('0.35');
  });

  it('refuses to guess the capacity, and to show a level it does not have', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    render_({}, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
    expect(q('[data-liquid]')).toBeNull();
    cleanup();
    render_(opts, 'none');
    settle();
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(q('[data-liquid]')).toBeNull();
  });
});

describe('battery', () => {
  const render_ = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<Battery widget={widget('battery', options)} data={data(value)} />);
  const fillWidth = () => Number(q('[data-fill-bar]')?.getAttribute('width'));

  it('fills to value% of the documented 0..100 default when no scale is configured', () => {
    render_({}, 42);
    expect(fillWidth()).toBeCloseTo(BATTERY_FILL_WIDTH * 0.42, 6);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe('0.42');
  });

  it('uses a configured scale for a voltage', () => {
    render_({ min: 3, max: 4.2, unit: 'V', precision: 2 }, 3.6);
    expect(fillWidth()).toBeCloseTo(BATTERY_FILL_WIDTH * 0.5, 6);
    expect(screen.getByText('3.60')).toBeTruthy();
    expect(screen.getByText('V')).toBeTruthy();
  });

  it('clamps off the scale and marks it', () => {
    render_({}, 150);
    expect(fillWidth()).toBeCloseTo(BATTERY_FILL_WIDTH, 6);
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('above');
    cleanup();
    render_({}, -3);
    expect(q('[data-fill-bar]')).toBeNull();
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('below');
  });

  it('an empty battery draws no fill but still reads 0', () => {
    render_({}, 0);
    expect(q('[data-fill-bar]')).toBeNull();
    expect(screen.getByText('0')).toBeTruthy();
  });

  const thresholds = [
    { value: 0, level: 'critical' },
    { value: 15, level: 'normal' },
  ];

  it('colours a low battery and leaves a healthy one neutral', () => {
    render_({ thresholds }, 10);
    expect(styleOf('[data-fill-bar]')).toContain(CRITICAL);
    cleanup();
    render_({ thresholds }, 60);
    const normal = styleOf('[data-fill-bar]');
    expect(normal).toContain(ACCENT);
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('normal');
    cleanup();
    render_({}, 60);
    expect(styleOf('[data-fill-bar]')).toBe(normal);
  });

  it('shows no charge rather than 0% when it has no reading', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    render_({}, 'none');
    settle();
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(q('[data-fill-bar]')).toBeNull();
    expect(screen.queryByText('0')).toBeNull();
  });

  it('only a one-sided scale falls back for the other end, and an inverted one is refused', () => {
    render_({ min: 50 }, 75);
    expect(fillWidth()).toBeCloseTo(BATTERY_FILL_WIDTH * 0.5, 6);
    cleanup();
    render_({ min: 200 }, 75);
    expect(screen.getByText('Max must be above min')).toBeTruthy();
  });
});

describe('status-light', () => {
  const render_ = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<StatusLight widget={widget('status-light', options)} data={data(value)} />);
  const lit = () => q('[data-lit]')?.getAttribute('data-lit');

  it('is lit and captions the value while it has one and no onAt is set', () => {
    render_({ unit: 'C', precision: 1 }, 21.04);
    expect(lit()).toBe('true');
    expect(q('[data-lamp-caption]')?.textContent).toBe('21.0 C');
  });

  it('reads an on/off signal at onAt: lit at the threshold, dark below it', () => {
    render_({ onAt: 1 }, 1);
    expect(lit()).toBe('true');
    expect(q('[data-lamp-caption]')?.textContent).toBe('On');
    cleanup();
    render_({ onAt: 1 }, 0);
    expect(lit()).toBe('false');
    expect(q('[data-lamp-caption]')?.textContent).toBe('Off');
    // A dark lamp is hollow: no fill, no glow.
    expect(styleOf('[data-lamp]')).toContain('background: transparent');
    expect(styleOf('[data-lamp]')).toContain('box-shadow: none');
  });

  it('takes the colour of its band and the band label as the caption', () => {
    const thresholds = [
      { value: 0, level: 'normal', label: 'Running' },
      { value: 80, level: 'critical', label: 'Overheating' },
    ];
    render_({ thresholds }, 90);
    expect(styleOf('[data-lamp]')).toContain(CRITICAL);
    expect(q('[data-lamp-caption]')?.textContent).toBe('Overheating');
    cleanup();
    render_({ thresholds }, 20);
    const normal = styleOf('[data-lamp]');
    expect(normal).toContain(ACCENT);
    expect(normal).not.toContain(CRITICAL);
    expect(q('[data-lamp-caption]')?.textContent).toBe('Running');
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('normal');
    cleanup();
    // The normal band is what a lamp with no thresholds paints.
    render_({}, 20);
    expect(styleOf('[data-lamp]')).toBe(normal);
  });

  // Every element the frame observes reports this content box, as a laid-out board would.
  const sizedAt = (width: number, height: number) =>
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(private cb: (entries: unknown[]) => void) {}
        observe() {
          this.cb([{ contentRect: { width, height } }]);
        }
        disconnect() {}
      },
    );

  it('lays the caption beside the lamp at its 2x2 minimum, and under it when there is room', () => {
    // The 2x2 minimum with a subject line and a freshness footer, at the smallest cell: ~22 px.
    sizedAt(231, 22);
    render_({ onAt: 1 }, 1);
    expect(q('[data-lamp-layout]')?.getAttribute('data-lamp-layout')).toBe('beside');
    expect(q('[data-lamp-caption]')?.textContent).toBe('On');
    expect(parseFloat(q('[data-lamp]')!.style.width)).toBeLessThanOrEqual(22);
    cleanup();
    sizedAt(231, 120);
    render_({ onAt: 1 }, 1);
    expect(q('[data-lamp-layout]')?.getAttribute('data-lamp-layout')).toBe('under');
    // Under, the lamp takes the height the caption leaves, and is larger than the beside one.
    expect(parseFloat(q('[data-lamp]')!.style.width)).toBeGreaterThan(22);
  });

  it('is hollow and says so with no data, never "off"', () => {
    render_({ onAt: 1 }, 'none');
    expect(lit()).toBeUndefined();
    expect(q('[data-lamp-caption]')?.textContent).toBe('No data');
    expect(screen.queryByText('Off')).toBeNull();
    expect(styleOf('[data-lamp]')).toContain('background: transparent');
  });
});

describe('signal-bars', () => {
  const steps = [
    { value: -110, level: 'critical' },
    { value: -100, level: 'warning' },
    { value: -90, level: 'normal' },
    { value: -80, level: 'normal' },
  ];
  const render_ = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<SignalBars widget={widget('signal-bars', options)} data={data(value)} />);
  const bars = (state: 'lit' | 'unlit') => document.querySelectorAll(`[data-bar="${state}"]`).length;

  it.each([
    [-120, 0],
    [-110, 1],
    [-100, 2],
    [-95, 2],
    [-85, 3],
    [-80, 4],
    [-40, 4],
  ])('%s dBm lights %s of 4 bars', (value, lit) => {
    render_({ thresholds: steps, unit: 'dBm' }, value);
    expect(bars('lit')).toBe(lit);
    expect(bars('unlit')).toBe(4 - lit);
    expect(q('[data-bars-lit]')?.getAttribute('data-bars-lit')).toBe(String(lit));
    expect(screen.getByText('dBm')).toBeTruthy();
  });

  it('draws one bar per threshold, whatever the count', () => {
    render_({ thresholds: steps.slice(0, 2) }, -105);
    expect(bars('lit') + bars('unlit')).toBe(2);
    cleanup();
    render_({ thresholds: [...steps, { value: -70, level: 'normal' }] }, -75);
    expect(bars('lit') + bars('unlit')).toBe(5);
  });

  it('colours the lit bars by the band the value is in; normal keeps the accent', () => {
    render_({ thresholds: steps }, -105);
    expect(document.querySelector('[data-bar="lit"]')?.getAttribute('style')).toContain(CRITICAL);
    cleanup();
    render_({ thresholds: steps }, -85);
    const normal = document.querySelector('[data-bar="lit"]')?.getAttribute('style');
    expect(normal).toContain(ACCENT);
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('normal');
    // The unlit bars are the muted track, not a severity hue.
    expect(document.querySelector('[data-bar="unlit"]')?.getAttribute('style')).toContain('var(--muted)');
  });

  it('needs at least two thresholds and says so, with or without data', () => {
    render_({}, -85);
    expect(screen.getByText('Set at least two thresholds')).toBeTruthy();
    expect(bars('lit') + bars('unlit')).toBe(0);
    cleanup();
    render_({ thresholds: steps.slice(0, 1) }, -85);
    expect(screen.getByText('Set at least two thresholds')).toBeTruthy();
  });

  it('shows no bars rather than an empty signal when it has no reading', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    render_({ thresholds: steps }, 'none');
    settle();
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(bars('lit') + bars('unlit')).toBe(0);
  });
});

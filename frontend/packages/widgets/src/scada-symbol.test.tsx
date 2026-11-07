// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The process symbols: the rules that put one in a state (running, stopped, fault, flow direction),
// that every symbol draws in every state, that motion is SMIL and goes away for a viewer who asked
// for reduced motion, and that the binding reaches the symbol (the measurement, onAt, the bands).

import type { KnownWidgetInstance, MeasurementSample } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { SCADA_SYMBOLS, isContinuous, isFaultBand, readSymbol } from './scada-model';
import { SCADA_STRINGS } from './scada-strings';
import { ScadaSymbol } from './widgets/scada-symbol';

const sample = (name: string, value: number): MeasurementSample => ({
  id: name,
  deviceToken: 'pump-3',
  eventType: 0,
  occurredTime: '2026-11-07T12:00:00Z',
  name,
  value,
  classifier: null,
});

function widget(options: Record<string, unknown> = {}, measurements = ['running']): KnownWidgetInstance {
  return {
    id: 's',
    type: 'scada-symbol',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    datasource: { kind: 'device', deviceToken: 'pump-3', measurements },
    options: { symbol: 'pump', measurement: 'running', ...options },
  } as unknown as KnownWidgetInstance;
}

function show(options: Record<string, unknown>, value: number | undefined, name = 'running') {
  const latest = value === undefined ? {} : { [name]: sample(name, value) };
  return render(<ScadaSymbol widget={widget(options, [name])} data={{ latest, samples: [], error: null }} />);
}

const svg = (c: HTMLElement) => c.querySelector('svg[data-symbol]') as SVGElement;
const caption = (c: HTMLElement) => c.querySelector('[data-scada-caption]')?.textContent;

function reducedMotion(reduced: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: reduced && query.includes('prefers-reduced-motion'),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

beforeEach(() => {
  document.documentElement.lang = 'en';
  reducedMotion(false);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the state rules', () => {
  const crit = { value: 90, level: 'critical' as const };
  const warn = { value: 70, level: 'warning' as const };

  it('is unknown without a number, never stopped', () => {
    for (const s of SCADA_SYMBOLS) expect(readSymbol(s, undefined, undefined, undefined).state, s).toBe('unknown');
    expect(readSymbol('pump', Number.NaN, undefined, undefined).state).toBe('unknown');
  });

  it('switches at onAt (default 1), a value ON the threshold being on', () => {
    expect(readSymbol('pump', 0, undefined, undefined).state).toBe('stopped');
    expect(readSymbol('pump', 1, undefined, undefined).state).toBe('running');
    expect(readSymbol('valve', 1449, 1450, undefined).state).toBe('stopped');
    expect(readSymbol('valve', 1450, 1450, undefined).state).toBe('running');
  });

  it('is a fault in a major or critical band and in no other', () => {
    expect(isFaultBand(crit)).toBe(true);
    expect(isFaultBand({ value: 1, level: 'major' })).toBe(true);
    expect(isFaultBand(warn)).toBe(false);
    expect(isFaultBand({ value: 1, level: 'normal' })).toBe(false);
    expect(readSymbol('motor', 95, 1, crit).state).toBe('fault');
    expect(readSymbol('motor', 75, 1, warn).state).toBe('running');
  });

  it('gives a pipe a direction by the sign of its value, held to onAt', () => {
    expect(readSymbol('pipe', 5, 1, undefined)).toEqual({ state: 'running', flow: 'forward' });
    expect(readSymbol('pipe', -5, 1, undefined)).toEqual({ state: 'running', flow: 'reverse' });
    expect(readSymbol('pipe', 0.2, 1, undefined)).toEqual({ state: 'stopped', flow: 'none' });
    expect(readSymbol('pipe', -0.2, 1, undefined)).toEqual({ state: 'stopped', flow: 'none' });
    expect(readSymbol('pump', 5, 1, undefined).flow).toBe('none');
    // an onAt of 0 does not make a zero reading a flow
    expect(readSymbol('pipe', 0, 0, undefined)).toEqual({ state: 'stopped', flow: 'none' });
    expect(readSymbol('pipe', 0.1, 0, undefined).flow).toBe('forward');
  });

  it('shows a level or a reading for a tank and a sensor whenever there is a number', () => {
    expect(isContinuous('tank') && isContinuous('sensor')).toBe(true);
    expect(readSymbol('tank', 0, 1, undefined).state).toBe('running');
    expect(readSymbol('sensor', -4, 1, undefined).state).toBe('running');
  });
});

describe('every symbol draws in every state', () => {
  it.each(SCADA_SYMBOLS)('%s: running, stopped, fault and no data render, each with its own word', (symbol) => {
    const words = SCADA_STRINGS.en.states[symbol];
    const opts = { symbol, thresholds: [{ value: 90, level: 'critical' }], min: 0, max: 100, onAt: 1 };
    const seen = new Set<string>();
    for (const [value, state] of [[10, 'running'], [0, 'stopped'], [95, 'fault'], [undefined, 'unknown']] as const) {
      const view = show(opts, value);
      const el = svg(view.container);
      expect(el, `${symbol} ${state}`).toBeTruthy();
      // A tank and a sensor with a number are at their reading, not at "stopped".
      const expected = isContinuous(symbol) && value !== undefined && state !== 'fault' ? 'running' : state;
      expect(el.getAttribute('data-state'), `${symbol} ${value}`).toBe(expected);
      expect(el.getAttribute('aria-label')).toContain(SCADA_STRINGS.en.symbols[symbol]);
      seen.add(el.getAttribute('data-state')!);
      // ...and says it in words, in the symbol's own vocabulary (a tank and a sensor print their value)
      if (!isContinuous(symbol) || value === undefined) {
        // (a flowing pipe also names its direction)
        expect(caption(view.container), `${symbol} ${state}`).toBe(symbol === 'pipe' && (state === 'running' || state === 'fault') ? `${words[state]} (forward)` : words[state]);
      }
      expect(view.container.querySelector('[data-unknown]') !== null, `${symbol} ${state}`).toBe(state === 'unknown');
      cleanup();
    }
    expect(words.unknown).toBe('No data');
    expect(seen.has('unknown')).toBe(true);
    expect(seen.has('fault')).toBe(true);
  });
});

describe('binding and options', () => {
  it('reads the measurement named by the option from the datasource', () => {
    const { container } = render(
      <ScadaSymbol
        widget={widget({ symbol: 'pump', measurement: 'speed', onAt: 100 }, ['speed', 'other'])}
        data={{ latest: { speed: sample('speed', 1450), other: sample('other', 0) }, samples: [], error: null }}
      />,
    );
    expect(svg(container).getAttribute('data-state')).toBe('running');
  });

  it('turns the pump while it runs, and holds it still when stopped or faulted', () => {
    expect(show({ symbol: 'pump' }, 1).container.querySelector('animateTransform[type="rotate"]')).toBeTruthy();
    cleanup();
    expect(show({ symbol: 'pump' }, 0).container.querySelector('animateTransform')).toBeNull();
    cleanup();
    const fault = show({ symbol: 'pump', thresholds: [{ value: 5, level: 'critical' }] }, 9);
    expect(fault.container.querySelector('animateTransform')).toBeNull();
    expect(fault.container.querySelector('[data-fault-badge]')).toBeTruthy();
    expect(fault.container.querySelector('[data-fault-badge] animate')).toBeTruthy();
  });

  it('turns a motor and a fan too', () => {
    for (const symbol of ['motor', 'fan']) {
      expect(show({ symbol }, 1).container.querySelector('animateTransform'), symbol).toBeTruthy();
      cleanup();
    }
  });

  it('moves a pipe’s dashes along it, the other way for a negative value', () => {
    const fwd = show({ symbol: 'pipe' }, 3);
    const f = fwd.container.querySelector('path[data-flow]')!;
    expect(f.getAttribute('data-flow')).toBe('forward');
    expect(f.querySelector('animate')?.getAttribute('to')).toBe('-16');
    expect(caption(fwd.container)).toBe('Flowing (forward)');
    cleanup();
    const rev = show({ symbol: 'pipe' }, -3);
    expect(rev.container.querySelector('path[data-flow]')!.getAttribute('data-flow')).toBe('reverse');
    expect(rev.container.querySelector('path[data-flow] animate')?.getAttribute('to')).toBe('16');
    expect(caption(rev.container)).toBe('Flowing (reverse)');
    cleanup();
    const still = show({ symbol: 'pipe' }, 0);
    expect(still.container.querySelector('path[data-flow] animate')).toBeNull();
    expect(caption(still.container)).toBe('No flow');
  });

  it('opens and closes a valve, and closes and trips a breaker', () => {
    expect(show({ symbol: 'valve' }, 1).container.querySelector('[data-closed]')).toBeNull();
    cleanup();
    const closed = show({ symbol: 'valve' }, 0);
    expect(closed.container.querySelector('[data-closed]')).toBeTruthy();
    expect(caption(closed.container)).toBe('Closed');
    cleanup();
    expect(show({ symbol: 'breaker' }, 1).container.querySelector('[data-contact="closed"]')).toBeTruthy();
    cleanup();
    expect(show({ symbol: 'breaker' }, 0).container.querySelector('[data-contact="open"]')).toBeTruthy();
    cleanup();
    const trip = show({ symbol: 'breaker', thresholds: [{ value: 5, level: 'critical' }] }, 9);
    expect(trip.container.querySelector('[data-contact="open"]')).toBeTruthy();
    expect(caption(trip.container)).toBe('Tripped');
  });

  it('glows a light while it is on', () => {
    expect(show({ symbol: 'light' }, 1).container.querySelector('[data-glow]')).toBeTruthy();
    cleanup();
    expect(show({ symbol: 'light' }, 0).container.querySelector('[data-glow]')).toBeNull();
  });

  it('fills a tank to its level on its own scale and prints the value', () => {
    const half = show({ symbol: 'tank', measurement: 'level', min: 0, max: 200, unit: 'L', precision: 0 }, 100, 'level');
    const rect = half.container.querySelector('[data-liquid]')!;
    expect(Number(rect.getAttribute('height'))).toBeCloseTo(38);
    expect(caption(half.container)).toBe('100 L');
    cleanup();
    // No scale: no plausible fill, the configuration is named.
    const unscaled = show({ symbol: 'tank', measurement: 'level' }, 100, 'level');
    expect(unscaled.container.querySelector('[data-liquid]')).toBeNull();
    expect(unscaled.container.textContent).toContain('Set min and max');
  });

  it('prints a sensor’s reading and flags an alarm band beside it', () => {
    const v = show({ symbol: 'sensor', measurement: 'temp', unit: 'C', precision: 1, thresholds: [{ value: 80, level: 'critical' }] }, 85.25, 'temp');
    expect(caption(v.container)).toBe('85.3 C · Alarm');
    expect(svg(v.container).getAttribute('aria-label')).toBe('Sensor: 85.3 C · Alarm');
    expect(svg(v.container).getAttribute('data-state')).toBe('fault');
  });

  it('colours a faulted symbol with its band, not the neutral accent', () => {
    const v = show({ symbol: 'pump', thresholds: [{ value: 5, level: 'critical' }] }, 9);
    expect(v.container.querySelector('[data-scada-state]')?.getAttribute('data-threshold-level')).toBe('critical');
  });

  it('falls back to a pump for an unknown symbol, never throwing', () => {
    expect(svg(show({ symbol: 'reactor' }, 1).container).getAttribute('data-symbol')).toBe('pump');
  });

  it('words itself in Spanish', () => {
    document.documentElement.lang = 'es';
    expect(caption(show({ symbol: 'valve' }, 1).container)).toBe('Abierta');
    cleanup();
    expect(caption(show({ symbol: 'breaker' }, 0).container)).toBe('Abierto');
  });
});

describe('reduced motion', () => {
  it('stops a running symbol when the preference turns on while it is mounted', () => {
    let listener: (() => void) | undefined;
    const query = { matches: false, media: '', addEventListener: (_: string, l: () => void) => (listener = l), removeEventListener: () => {} };
    vi.stubGlobal('matchMedia', () => query);
    const { container } = show({ symbol: 'pump' }, 1);
    expect(container.querySelector('animateTransform')).toBeTruthy();
    query.matches = true;
    act(() => listener?.());
    expect(container.querySelector('animateTransform')).toBeNull();
    expect(container.querySelector('[data-scada-caption]')?.textContent).toBe('Running');
  });

  it('draws no animation at all for a viewer who asked for none, and keeps the state in shape and words', () => {
    reducedMotion(true);
    const { container } = show({ symbol: 'pipe' }, 4);
    expect(container.querySelector('animate, animateTransform')).toBeNull();
    expect(svg(container).getAttribute('data-motion-on')).toBeNull();
    expect(caption(container)).toBe('Flowing (forward)');
    cleanup();
    const fault = show({ symbol: 'pump', thresholds: [{ value: 5, level: 'critical' }] }, 9);
    expect(fault.container.querySelector('animate, animateTransform')).toBeNull();
    expect(fault.container.querySelector('[data-fault-badge]')).toBeTruthy();
  });
});

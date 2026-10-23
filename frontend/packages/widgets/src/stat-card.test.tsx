// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The stat card as a component: what it prints for the data it is given, and what it says when
// it has none. The feed that produces the data is covered in aggregate-feed.test.tsx.

import type { AggregateResult, AggregateStreamState, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { aggregateStrings } from './aggregate-strings';
import { SETTLE_MS } from './no-reading';
import { StatCard } from './widgets/stat-card';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.documentElement.lang = 'en';
});

// Every element the frame observes reports this content box, as a laid-out board would.
function sizedAt(width: number, height: number) {
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
}
beforeAll(() => {
  document.documentElement.lang = 'en';
});

const ready = (over: Partial<Extract<AggregateResult, { status: 'ready' }>> = {}): AggregateResult => ({
  status: 'ready',
  startTime: '2026-10-14T11:00:00Z',
  endTime: '2026-10-14T12:00:00Z',
  spanSeconds: 3600,
  byName: { temperature: { count: 40, min: 12.25, max: 31.5, sum: 800, avg: 20 } },
  ...over,
});

const sample = (value: number | null, time = '2026-10-14T11:59:00Z'): MeasurementSample => ({
  id: 's',
  deviceToken: 'd',
  eventType: 0,
  occurredTime: time,
  name: 'temperature',
  value,
  classifier: null,
});

const data = (aggregates: AggregateResult, latest: Record<string, MeasurementSample> = {}): AggregateStreamState => ({
  aggregates,
  latest,
  error: null,
});

const widget = (options: Record<string, unknown> = {}, measurements = ['temperature']): WidgetInstance =>
  ({
    id: 'w',
    type: 'stat-card',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    datasource: { kind: 'device', deviceToken: 'd', measurements },
    options,
  }) as unknown as WidgetInstance;

function show(options: Record<string, unknown>, state: AggregateStreamState) {
  const { container } = render(<StatCard widget={widget(options)} data={state} />);
  return {
    figure: container.querySelector('[data-level-readout]')?.textContent,
    caption: container.querySelector('[data-stat-caption]')?.textContent,
    empty: (container.querySelector('[data-level-empty]') ?? container.querySelector('[data-empty-detail]'))?.textContent,
    kind: container.querySelector('[data-empty-state]')?.getAttribute('data-empty-state'),
    card: container.querySelector<HTMLElement>('[data-stat]'),
    text: container.textContent ?? '',
  };
}

describe('the stat card figure', () => {
  it.each([
    ['avg', '20', 'Average · 1h'],
    ['min', '12.25', 'Minimum · 1h'],
    ['max', '31.5', 'Maximum · 1h'],
    ['sum', '800', 'Sum · 1h'],
    ['count', '40', 'Readings · 1h'],
  ])('shows the %s of the span, captioned with the statistic and the span', (stat, figure, caption) => {
    const r = show({ stat }, data(ready()));
    expect(r.figure).toBe(figure);
    expect(r.caption).toBe(caption);
    expect(r.card?.getAttribute('data-stat')).toBe(stat);
    expect(r.card?.getAttribute('data-stat-span')).toBe('3600');
  });

  it('is the average when no statistic is set, or an unknown one', () => {
    expect(show({}, data(ready())).caption).toBe('Average · 1h');
    expect(show({ stat: 'median' }, data(ready())).figure).toBe('20');
  });

  it('says the span it covers, whatever it is', () => {
    expect(show({ stat: 'max' }, data(ready({ spanSeconds: 86400 }))).caption).toBe('Maximum · 1d');
    expect(show({ stat: 'max' }, data(ready({ spanSeconds: 300 }))).caption).toBe('Maximum · 5m');
    expect(show({ stat: 'max' }, data(ready({ spanSeconds: 7200 }))).caption).toBe('Maximum · 2h');
  });

  it('shows the latest reading, which covers no span', () => {
    const r = show({ stat: 'last' }, data(ready(), { temperature: sample(22.5) }));
    expect(r.figure).toBe('22.5');
    expect(r.caption).toBe('Latest');
    expect(r.card?.hasAttribute('data-stat-span')).toBe(false);
  });

  it('applies the unit and the precision to the figure', () => {
    const r = show({ stat: 'min', unit: 'C', precision: 1 }, data(ready()));
    expect(r.figure).toBe('12.3C');
  });

  it('shows a count as a whole number with no unit, whatever the unit and precision say', () => {
    const r = show({ stat: 'count', unit: 'C', precision: 3 }, data(ready()));
    expect(r.figure).toBe('40');
  });

  it('reads the measurement the card is set to, not the first one the data holds', () => {
    const byName = {
      temperature: { count: 1, min: 1, max: 1, sum: 1, avg: 1 },
      humidity: { count: 1, min: 55, max: 55, sum: 55, avg: 55 },
    };
    expect(show({ measurement: 'humidity' }, data(ready({ byName }))).figure).toBe('55');
  });

  it('titles itself with the measurement, unless it is given a title', () => {
    expect(render(<StatCard widget={widget()} data={data(ready())} />).container.textContent).toContain('temperature');
    cleanup();
    expect(render(<StatCard widget={widget({ title: 'Peak' })} data={data(ready())} />).container.textContent).toContain('Peak');
  });
});

describe('the stat card at its smallest', () => {
  // The 2-row minimum with a subject line and the latest reading's age footer, at the smallest
  // cell, leaves the figure ~22 px: under it, the caption would push the figure out of the box.
  it('lays the caption beside the figure when there is no room under it, and keeps both', () => {
    sizedAt(364, 22);
    const r = show({ stat: 'last', unit: 't/h' }, data(ready(), { temperature: sample(822) }));
    expect(r.card?.getAttribute('data-stat-layout')).toBe('beside');
    expect(r.caption).toBe('Latest');
    expect(r.figure).toContain('822');
    // Fitted to the height it has, not to the height a caption under it would have left.
    expect(parseFloat(r.card!.querySelector<HTMLElement>('[data-level-readout]')!.style.fontSize)).toBeLessThanOrEqual(22);
  });

  it('keeps the caption under the figure when there is room for both', () => {
    sizedAt(364, 80);
    expect(show({ stat: 'last' }, data(ready(), { temperature: sample(822) })).card?.getAttribute('data-stat-layout')).toBe('under');
  });
});

describe('the stat card thresholds', () => {
  const bands = [
    { value: 0, level: 'normal' },
    { value: 28, level: 'warning' },
    { value: 32, level: 'critical' },
  ];

  it('colours the figure by the band the STATISTIC falls in', () => {
    const max = show({ stat: 'max', thresholds: bands }, data(ready())); // 31.5: warning
    expect(max.card?.getAttribute('data-threshold-level')).toBe('warning');
    const ink = max.card?.querySelector<HTMLElement>('[data-level-readout]')?.style.color;
    expect(ink, 'a warning band leaves the figure in the neutral foreground').toBeTruthy();
    expect(ink).not.toContain('--foreground');
    const avg = show({ stat: 'avg', thresholds: bands }, data(ready())); // 20: normal
    expect(avg.card?.getAttribute('data-threshold-level')).toBe('normal');
  });

  it('keeps the neutral foreground in a normal band, and below the lowest threshold', () => {
    const normal = show({ stat: 'avg', thresholds: bands }, data(ready()));
    expect(normal.card?.querySelector<HTMLElement>('[data-level-readout]')?.style.color).toContain('--foreground');
    const below = show({ stat: 'min', thresholds: [{ value: 50, level: 'critical' }] }, data(ready()));
    expect(below.card?.hasAttribute('data-threshold-level')).toBe(false);
    expect(below.card?.querySelector<HTMLElement>('[data-level-readout]')?.style.color).toContain('--foreground');
  });
});

describe('the stat card when there is no figure', () => {
  const S = aggregateStrings('en');

  it('says it is loading, rather than showing a figure or a zero', () => {
    const r = show({ stat: 'max' }, data({ status: 'loading' }));
    expect(r.empty).toBe(S.loading);
    expect(r.figure).toBeUndefined();
  });

  it('says the source cannot compute aggregates, and shows no number', () => {
    const r = show({ stat: 'max' }, data({ status: 'unavailable', reason: 'unsupported' }));
    expect(r.empty).toBe(S.unsupported);
    expect(r.text).not.toMatch(/\d/);
  });

  it('says why the read failed, and shows no number', () => {
    const r = show({ stat: 'max' }, data({ status: 'unavailable', reason: 'failed', message: 'boom' }));
    expect(r.empty).toBe('Aggregates unavailable: boom');
    expect(r.text).not.toMatch(/\d/);
  });

  it('says there were no readings in the span, naming it', () => {
    const r = show({ stat: 'max' }, data(ready({ byName: {} })));
    expect(r.empty).toBe('No readings in the last 1h');
  });

  it('refuses a measurement the span holds no readings of, even when another has some', () => {
    const r = show({ stat: 'max', measurement: 'humidity' }, data(ready()));
    expect(r.empty).toBe('No readings in the last 1h');
  });

  it('counts an empty span as zero readings: a count of nothing is a number', () => {
    const r = show({ stat: 'count' }, data(ready({ byName: {} })));
    expect(r.figure).toBe('0');
    expect(r.caption).toBe('Readings · 1h');
  });

  it('says its slot is unbound, not "0 Readings", when no device is bound to it', () => {
    // An unbound slot resolves to no devices, so the read comes back ready and empty.
    const w = { ...widget({ stat: 'count' }), datasource: { kind: 'unbound', measurements: ['temperature'] } } as unknown as WidgetInstance;
    const { container } = render(<StatCard widget={w} data={data(ready({ byName: {} }))} />);
    expect(container.querySelector('[data-level-readout]')).toBeNull();
    expect(container.querySelector('[data-empty-state="unbound"] [data-empty-title]')?.textContent).toBe('No device chosen');
  });

  it('shows no count until the read lands, and none for a card that names no measurement', () => {
    expect(show({ stat: 'count' }, data({ status: 'loading' })).empty).toBe('Loading');
    const { container } = render(<StatCard widget={widget({ stat: 'count' }, [])} data={data(ready({ byName: {} }))} />);
    expect(container.querySelector('[data-empty-detail]')?.textContent).toBe('No readings in the last 1h');
    expect(container.querySelector('[data-level-readout]')).toBeNull();
  });

  it('ages the latest reading on screen, and no aggregate', () => {
    const last = render(<StatCard widget={widget({ stat: 'last' })} data={data(ready(), { temperature: sample(22.5) })} />);
    expect(last.container.querySelector('[data-widget-freshness]')).not.toBeNull();
    last.unmount();
    const max = render(<StatCard widget={widget({ stat: 'max' })} data={data(ready(), { temperature: sample(22.5) })} />);
    expect(max.container.querySelector('[data-widget-freshness]')).toBeNull();
  });

  it('shows the latest reading as no data until one arrives, after the moment a loading board has none', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      const { container } = render(<StatCard widget={widget({ stat: 'last' })} data={data(ready())} />);
      const kind = () => container.querySelector('[data-empty-state]')?.getAttribute('data-empty-state');
      expect(kind()).toBeUndefined();
      act(() => void vi.advanceTimersByTime(SETTLE_MS));
      expect(kind()).toBe('no-data');
      cleanup();
      const nulled = render(<StatCard widget={widget({ stat: 'last' })} data={data(ready(), { temperature: sample(null) })} />);
      act(() => void vi.advanceTimersByTime(SETTLE_MS));
      expect(nulled.container.querySelector('[data-empty-state]')?.getAttribute('data-empty-state')).toBe('no-data');
    } finally {
      vi.useRealTimers();
    }
  });

  it('refuses a latest reading older than the span, instead of presenting it as the span latest', () => {
    const old = sample(22.5, '2026-10-14T09:00:00Z'); // two hours before the one-hour window starts
    const r = show({ stat: 'last' }, data(ready(), { temperature: old }));
    expect(r.empty).toBe('No readings in the last 1h');
    expect(r.figure).toBeUndefined();
  });

  it('offers the last reading known outside the span as a hint, not as the figure', () => {
    const old = sample(22.5, '2026-10-14T09:00:00Z');
    const { container } = render(<StatCard widget={widget({ stat: 'last', unit: 'C' })} data={data(ready(), { temperature: old })} />);
    expect(container.querySelector('[data-empty-state="no-data"]')).not.toBeNull();
    expect(container.querySelector('[data-empty-hint]')?.textContent).toMatch(/^Last value 22\.5 C · /);
    expect(container.querySelector('[data-level-readout]')).toBeNull();
  });

  it('still shows the latest reading when the source cannot compute aggregates', () => {
    const r = show({ stat: 'last' }, data({ status: 'unavailable', reason: 'unsupported' }, { temperature: sample(22.5) }));
    expect(r.figure).toBe('22.5');
  });

  it('speaks Spanish when the document is Spanish, and English otherwise', () => {
    document.documentElement.lang = 'es';
    const es = aggregateStrings();
    expect(show({ stat: 'max' }, data({ status: 'unavailable', reason: 'unsupported' })).empty).toBe(es.unsupported);
    expect(es.unsupported).not.toBe(S.unsupported);
    expect(show({ stat: 'max' }, data(ready())).caption).toBe('Máximo · 1h');
    cleanup();
    document.documentElement.lang = 'fr';
    expect(show({ stat: 'max' }, data(ready())).caption).toBe('Maximum · 1h');
  });
});

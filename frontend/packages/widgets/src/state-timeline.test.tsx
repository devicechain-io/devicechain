// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The state timeline: how a measurement's bucket averages become runs of a discrete state, that a gap
// is never merged into the state beside it, and that the widget says what its stretches are.

import type { HistoryWindow, KnownWidgetInstance, MeasurementBucket } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { buildLane, laneNames, shares, stateOf, statesUsed } from './state-timeline-model';
import { StateTimeline } from './widgets/state-timeline';

const BANDS = [
  { value: 0, level: 'critical' as const, label: 'Stopped' },
  { value: 1, level: 'normal' as const, label: 'Running' },
  { value: 2, level: 'major' as const, label: 'Fault' },
];

const START = Date.parse('2026-11-06T12:00:00Z');
const MIN = 60_000;
const window5: HistoryWindow = {
  startTime: new Date(START).toISOString(),
  endTime: new Date(START + 5 * MIN).toISOString(),
  intervalSeconds: 60,
} as HistoryWindow;

const bucket = (minute: number, name: string, avg: number | null): MeasurementBucket => ({
  bucketStart: new Date(START + minute * MIN).toISOString(),
  name,
  avg,
  min: avg,
  max: avg,
  sum: avg,
  count: avg === null ? 0 : 1,
});

describe('the model', () => {
  it('places an average in its band, below the lowest in the lead-in, a missing one in none', () => {
    expect(stateOf(BANDS, 0.4)).toBe(0);
    expect(stateOf(BANDS, 1)).toBe(1);
    expect(stateOf(BANDS, 2.5)).toBe(2);
    expect(stateOf(BANDS, -1)).toBe(-1);
    expect(stateOf(BANDS, null)).toBe('none');
    expect(stateOf(BANDS, Number.NaN)).toBe('none');
  });

  it('merges adjacent slots in one state into one run', () => {
    const lane = buildLane('run', [bucket(0, 'run', 1), bucket(1, 'run', 1), bucket(2, 'run', 0), bucket(3, 'run', 0), bucket(4, 'run', 2)], window5, BANDS);
    expect(lane.segments.map((s) => [s.state, (s.to - s.from) / MIN])).toEqual([[1, 2], [0, 2], [2, 1]]);
  });

  it('never merges a gap into the state either side of it', () => {
    const lane = buildLane('run', [bucket(0, 'run', 1), bucket(2, 'run', 1)], window5, BANDS);
    expect(lane.segments.map((s) => s.state)).toEqual([1, 'none', 1, 'none']);
  });

  it('ignores other measurements’ buckets in a lane', () => {
    const lane = buildLane('run', [bucket(0, 'other', 2), bucket(0, 'run', 0)], window5, BANDS);
    expect(lane.segments[0].state).toBe(0);
  });

  it('tiles the window exactly and reports each state’s share', () => {
    const lane = buildLane('run', [bucket(0, 'run', 1), bucket(1, 'run', 1), bucket(2, 'run', 0)], window5, BANDS);
    expect(lane.segments[0].from).toBe(START);
    expect(lane.segments[lane.segments.length - 1].to).toBe(START + 5 * MIN);
    const s = shares(lane);
    expect(s.get(1)).toBeCloseTo(0.4);
    expect(s.get(0)).toBeCloseTo(0.2);
    expect(s.get('none')).toBeCloseTo(0.4);
    expect(statesUsed([lane])).toEqual([0, 1, 'none']);
  });

  it('draws each bucket over its OWN span when the window starts off the store’s grid', () => {
    // The window starts at 12:02 but the store's five-minute buckets start at 12:00, 12:05, 12:10.
    const w = { startTime: new Date(START + 2 * MIN).toISOString(), endTime: new Date(START + 12 * MIN).toISOString(), intervalSeconds: 300 } as HistoryWindow;
    const b = [bucket(0, 'run', 0), bucket(5, 'run', 1), bucket(10, 'run', 2)];
    const lane = buildLane('run', b, w, BANDS);
    expect(lane.segments.map((s) => [s.state, (s.from - START) / MIN, (s.to - START) / MIN])).toEqual([[0, 2, 5], [1, 5, 10], [2, 10, 12]]);
  });

  it('draws nothing for an empty or inverted window', () => {
    expect(buildLane('run', [], { ...window5, endTime: window5.startTime }, BANDS).segments).toEqual([]);
  });

  it('takes its lanes from the datasource, else from what was read, capped', () => {
    expect(laneNames([], ['b', 'a', 'b'], ['z'])).toEqual(['b', 'a']);
    expect(laneNames([], [], ['z', 'a', 'z'])).toEqual(['a', 'z']);
    expect(laneNames([], Array.from({ length: 30 }, (_, i) => `m${i}`), []).length).toBe(12);
  });
});

function widget(options: Record<string, unknown> = {}, measurements = ['run']): KnownWidgetInstance {
  return {
    id: 'st',
    type: 'state-timeline',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 8, rows: 3 } },
    datasource: { kind: 'device', deviceToken: 'pump-3', measurements },
    options: { title: 'Pump state', thresholds: BANDS, ...options },
  } as unknown as KnownWidgetInstance;
}

const data = { latest: {}, samples: [], error: null };
beforeEach(() => {
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

describe('rendering and binding', () => {
  it('draws a lane per measurement with a run per state, banded by level, and a legend of the states used', () => {
    const buckets = [bucket(0, 'run', 1), bucket(1, 'run', 1), bucket(2, 'run', 0), bucket(3, 'run', 2), bucket(0, 'amps', 1)];
    const { container } = render(<StateTimeline widget={widget({}, ['run', 'amps'])} data={data} history={{ status: 'ready', buckets, window: window5 }} />);
    expect([...container.querySelectorAll('[data-lane]')].map((l) => l.getAttribute('data-lane'))).toEqual(['run', 'amps']);
    const run = container.querySelector('[data-lane="run"]')!;
    expect([...run.querySelectorAll('[data-segment]')].map((s) => s.getAttribute('data-segment'))).toEqual(['1', '0', '2', 'none']);
    expect(run.querySelector('[data-segment="0"]')?.getAttribute('data-threshold-level')).toBe('critical');
    expect(run.querySelector('[data-segment="2"]')?.getAttribute('title')).toContain('Fault');
    const legend = container.querySelector('[data-state-legend]')!.textContent!;
    for (const word of ['Stopped', 'Running', 'Fault', 'No readings']) expect(legend).toContain(word);
  });

  it('says the stretches are the state of a bucket average', () => {
    const { container } = render(<StateTimeline widget={widget()} data={data} history={{ status: 'ready', buckets: [bucket(0, 'run', 1)], window: window5 }} />);
    expect(container.querySelector('[data-state-basis]')?.textContent).toBe('state of the average per 1m');
  });

  it('describes each lane to assistive tech by its shares', () => {
    const { container } = render(<StateTimeline widget={widget()} data={data} history={{ status: 'ready', buckets: [bucket(0, 'run', 1), bucket(1, 'run', 1), bucket(2, 'run', 0), bucket(3, 'run', 0), bucket(4, 'run', 0)], window: window5 }} />);
    const label = container.querySelector('[data-lane="run"] [role="img"]')?.getAttribute('aria-label') ?? '';
    expect(label).toContain('run: Running 40%, Stopped 60%.');
    // the sequence is spoken too: the segments' own titles are hidden under role="img"
    expect(label.indexOf('Running,')).toBeGreaterThan(-1);
    expect(label.indexOf('Running,')).toBeLessThan(label.indexOf('Stopped,'));
  });

  it('says there are no readings when the whole span is empty, and explains every other absence', () => {
    const view = (history: React.ComponentProps<typeof StateTimeline>['history']) => render(<StateTimeline widget={widget()} data={data} history={history} />).container.textContent;
    expect(view({ status: 'ready', buckets: [], window: window5 })).toContain('No readings in this span');
    cleanup();
    expect(view(undefined)).toContain('Loading');
    cleanup();
    expect(view({ status: 'unbound' })).toContain('Choose a device');
    cleanup();
    expect(view({ status: 'failed' })).toContain('could not be read');
    cleanup();
    expect(view({ status: 'unsupported' })).toContain('cannot read stored history');
  });

  it('names an unlabelled band by its level, in the viewer’s language', () => {
    document.documentElement.lang = 'es';
    const bands = [{ value: 0, level: 'warning' as const }];
    const { container } = render(<StateTimeline widget={widget({ thresholds: bands })} data={data} history={{ status: 'ready', buckets: [bucket(0, 'run', 1)], window: window5 }} />);
    expect(container.querySelector('[data-state-legend]')?.textContent).toContain('Aviso');
  });

  it('words itself in Spanish', () => {
    document.documentElement.lang = 'es';
    const { container } = render(<StateTimeline widget={widget()} data={data} history={{ status: 'ready', buckets: [bucket(0, 'run', 1)], window: window5 }} />);
    expect(container.querySelector('[data-state-basis]')?.textContent).toBe('estado del promedio de cada 1m');
  });
});

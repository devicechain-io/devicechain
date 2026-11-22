// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a stat card shows, decided apart from the drawing: one figure, or the reason there is
// none. Every way of having no figure must be reached and named, "unbound" must never read as a
// zero, an unknown statistic must fall back to the average rather than to another statistic, and a
// figure must carry what the card ages or captions it with (a latest reading its time, an
// aggregate its span).

import type { AggregateStreamState, DatasourceSelector, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { parseStat, statReading } from './aggregate-reading';
import { AGGREGATE_STATS } from './aggregate-strings';

const SPAN = { startTime: '2026-10-14T11:00:00Z', endTime: '2026-10-14T12:00:00Z', spanSeconds: 3600 };
const AGG = { count: 4, min: 1, max: 9, sum: 20, avg: 5 };

const device: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };

const widget = (datasource: DatasourceSelector | null = device, options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'w',
    type: 'stat-card',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    ...(datasource ? { datasource } : {}),
    options,
  }) as unknown as WidgetInstance;

const sample = (name: string, value: number | null, occurredTime: string | null = '2026-10-14T11:59:00Z'): MeasurementSample => ({
  id: `s-${name}`,
  deviceToken: 'd-1',
  eventType: 1,
  occurredTime,
  name,
  value,
  classifier: null,
});

const ready = (byName: Record<string, typeof AGG>): AggregateStreamState['aggregates'] => ({ status: 'ready', byName, ...SPAN });

const state = (aggregates: AggregateStreamState['aggregates'], latest: Record<string, MeasurementSample> = {}): AggregateStreamState => ({
  aggregates,
  latest,
  error: null,
});

describe('parseStat', () => {
  it.each(AGGREGATE_STATS.map((s) => [s]))('accepts the known statistic %s', (s) => {
    expect(parseStat(s)).toBe(s);
  });

  it.each([[undefined], [null], [''], ['median'], ['AVG'], [3], [{}]])('falls back to the average for %j', (v) => {
    expect(parseStat(v)).toBe('avg');
  });
});

describe('statReading: every reason for having no figure', () => {
  it('loading: the aggregate read has not landed', () => {
    expect(statReading(widget(), state({ status: 'loading' }), 'avg')).toEqual({
      kind: 'empty',
      reason: 'loading',
      name: 'temperature',
    });
  });

  it('unsupported: the source cannot compute aggregates, with its message', () => {
    const r = statReading(widget(), state({ status: 'unavailable', reason: 'unsupported', message: 'no rollups' }), 'avg');
    expect(r).toEqual({ kind: 'empty', reason: 'unsupported', name: 'temperature', message: 'no rollups' });
  });

  it('failed: the read failed, with its message', () => {
    const r = statReading(widget(), state({ status: 'unavailable', reason: 'failed', message: 'timeout' }), 'max');
    expect(r).toEqual({ kind: 'empty', reason: 'failed', name: 'temperature', message: 'timeout' });
  });

  it('noReadings: the span holds no reading of the measurement, and says how long the span was', () => {
    const r = statReading(widget(), state(ready({})), 'avg');
    expect(r).toEqual({ kind: 'empty', reason: 'noReadings', name: 'temperature', spanSeconds: 3600 });
  });

  it('noData: the latest reading has no sample at all', () => {
    expect(statReading(widget(), state({ status: 'loading' }), 'last')).toEqual({
      kind: 'empty',
      reason: 'noData',
      name: 'temperature',
    });
  });

  it('noData: the latest sample exists but carries no value', () => {
    const r = statReading(widget(), state(ready({}), { temperature: sample('temperature', null) }), 'last');
    expect(r).toMatchObject({ kind: 'empty', reason: 'noData' });
  });

  it('unbound: a slot with no device bound has no figure, for every statistic', () => {
    const unbound = widget({ kind: 'unbound', measurements: ['temperature'] });
    for (const stat of AGGREGATE_STATS) {
      const r = statReading(unbound, state(ready({ temperature: AGG }), { temperature: sample('temperature', 7) }), stat);
      expect(r).toEqual({ kind: 'empty', reason: 'unbound', name: 'temperature' });
    }
  });
});

describe('statReading: a figure', () => {
  it('reads each aggregate statistic off the named measurement and carries the span', () => {
    for (const stat of ['avg', 'min', 'max', 'sum', 'count'] as const) {
      const r = statReading(widget(), state(ready({ temperature: AGG })), stat);
      expect(r).toEqual({ kind: 'value', stat, name: 'temperature', value: AGG[stat], spanSeconds: 3600 });
    }
  });

  it('never borrows a neighbouring measurement when its own has no readings', () => {
    const r = statReading(widget(), state(ready({ humidity: AGG })), 'avg');
    expect(r).toMatchObject({ kind: 'empty', reason: 'noReadings', name: 'temperature' });
  });

  it('with no measurement pinned, shows the first one the read returned', () => {
    const r = statReading(widget({ kind: 'device', deviceToken: 'd-1', measurements: [] }), state(ready({ humidity: AGG })), 'max');
    expect(r).toEqual({ kind: 'value', stat: 'max', name: 'humidity', value: 9, spanSeconds: 3600 });
  });

  it('the pinned measurement option wins over the datasource', () => {
    const r = statReading(widget(device, { measurement: 'humidity' }), state(ready({ temperature: AGG, humidity: { ...AGG, avg: 60 } })), 'avg');
    expect(r).toMatchObject({ kind: 'value', name: 'humidity', value: 60 });
  });

  it('a count over a span with no readings is the number zero, not an empty card', () => {
    const r = statReading(widget(), state(ready({})), 'count');
    expect(r).toEqual({ kind: 'value', stat: 'count', name: 'temperature', value: 0, spanSeconds: 3600 });
  });

  it('a count is zero only where a device and a measurement were asked about', () => {
    const noMeasurement = widget({ kind: 'device', deviceToken: 'd-1', measurements: [] });
    expect(statReading(noMeasurement, state(ready({})), 'count')).toMatchObject({ kind: 'empty', reason: 'noReadings' });
    const noSource = widget(null);
    expect(statReading(noSource, state(ready({})), 'count')).toMatchObject({ kind: 'empty', reason: 'noReadings' });
  });

  it('any other statistic of no readings is empty, never a zero', () => {
    for (const stat of ['avg', 'min', 'max', 'sum'] as const) {
      expect(statReading(widget(), state(ready({})), stat)).toMatchObject({ kind: 'empty', reason: 'noReadings' });
    }
  });
});

describe('statReading: the latest reading', () => {
  it('carries its own time and no span, and needs no aggregate read', () => {
    const r = statReading(widget(), state({ status: 'loading' }, { temperature: sample('temperature', 21.5, '2026-10-14T11:59:30Z') }), 'last');
    expect(r).toEqual({
      kind: 'value',
      stat: 'last',
      name: 'temperature',
      value: 21.5,
      spanSeconds: undefined,
      time: '2026-10-14T11:59:30Z',
    });
  });

  it('works where the source cannot compute aggregates', () => {
    const r = statReading(widget(), state({ status: 'unavailable', reason: 'unsupported' }, { temperature: sample('temperature', 3) }), 'last');
    expect(r).toMatchObject({ kind: 'value', value: 3 });
  });

  it('refuses a reading older than the span the read covers, saying how long the span was', () => {
    const r = statReading(widget(), state(ready({}), { temperature: sample('temperature', 9, '2026-10-14T10:00:00Z') }), 'last');
    expect(r).toEqual({ kind: 'empty', reason: 'noReadings', name: 'temperature', spanSeconds: 3600 });
  });

  it('shows a reading inside the span, and one with no time at all', () => {
    expect(statReading(widget(), state(ready({}), { temperature: sample('temperature', 9, '2026-10-14T11:00:00Z') }), 'last')).toMatchObject({
      kind: 'value',
      value: 9,
    });
    expect(statReading(widget(), state(ready({}), { temperature: sample('temperature', 9, null) }), 'last')).toMatchObject({
      kind: 'value',
      value: 9,
      time: null,
    });
  });

  it('with no measurement pinned, names the sample it shows', () => {
    const r = statReading(widget({ kind: 'device', deviceToken: 'd-1', measurements: [] }), state(ready({}), { humidity: sample('humidity', 55) }), 'last');
    expect(r).toMatchObject({ kind: 'value', name: 'humidity', value: 55 });
  });
});

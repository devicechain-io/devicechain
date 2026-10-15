// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a stat widget shows, decided once and apart from the drawing: one figure, or the reason
// there is none. Every path that cannot produce a real figure says so -- nothing here falls back
// to a neighbouring statistic or to a number it was not given.

import type { AggregateStreamState, WidgetInstance } from '@devicechain/dashboards';
import { pickSample, primaryMeasurementName } from '@devicechain/widget-sdk';

import { AGGREGATE_STATS, type AggregateStat } from './aggregate-strings';

export type EmptyReason = 'loading' | 'unsupported' | 'failed' | 'noReadings' | 'noData';

export type StatReading =
  | {
      kind: 'value';
      stat: AggregateStat;
      name: string | undefined;
      value: number;
      // The span the figure covers, in seconds; undefined for the latest reading, which has none.
      spanSeconds: number | undefined;
      // The latest reading's own time, which the card ages on screen; undefined for an aggregate.
      time?: string | null;
    }
  | { kind: 'empty'; reason: EmptyReason; name: string | undefined; message?: string; spanSeconds?: number };

// The statistic a widget is set to. Unset or unknown is the average, the one a card is added with.
// (The widget reads its own option and passes it here: option reads live in the widget's file.)
export function parseStat(v: unknown): AggregateStat {
  return AGGREGATE_STATS.find((s) => s === v) ?? 'avg';
}

export function statReading(widget: WidgetInstance, data: AggregateStreamState, stat: AggregateStat): StatReading {
  const { aggregates } = data;
  const pinned = primaryMeasurementName(widget);

  // The latest reading is a reading, not an aggregate: it needs no aggregate read, and works
  // where the source cannot compute one. When the read did land, a reading older than the span is
  // not "the latest in the span", so it is refused rather than shown.
  if (stat === 'last') {
    const sample = pickSample(data.latest, pinned);
    const name = pinned ?? sample?.name;
    if (sample?.value == null) return { kind: 'empty', reason: 'noData', name };
    if (aggregates.status === 'ready' && sample.occurredTime && Date.parse(sample.occurredTime) < Date.parse(aggregates.startTime)) {
      return { kind: 'empty', reason: 'noReadings', name, spanSeconds: aggregates.spanSeconds };
    }
    return { kind: 'value', stat, name, value: sample.value, spanSeconds: undefined, time: sample.occurredTime };
  }

  if (aggregates.status === 'loading') return { kind: 'empty', reason: 'loading', name: pinned };
  if (aggregates.status === 'unavailable') {
    return { kind: 'empty', reason: aggregates.reason, name: pinned, message: aggregates.message };
  }
  const name = pinned ?? Object.keys(aggregates.byName)[0];
  const a = name === undefined ? undefined : aggregates.byName[name];
  if (!a) {
    // The number of readings in a span that holds none is a number: zero, which is the figure a
    // "no readings in the last hour" tile exists to show. It needs a measurement the card is set
    // to and a datasource it was read from; every other statistic of no readings is undefined.
    if (stat === 'count' && pinned !== undefined && widget.datasource) {
      return { kind: 'value', stat, name: pinned, value: 0, spanSeconds: aggregates.spanSeconds };
    }
    return { kind: 'empty', reason: 'noReadings', name, spanSeconds: aggregates.spanSeconds };
  }
  return { kind: 'value', stat, name, value: a[stat], spanSeconds: aggregates.spanSeconds };
}

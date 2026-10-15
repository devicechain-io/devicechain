// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The data feed of an aggregate widget: the exact aggregates of its measurements over its span,
// plus the newest live reading of each. Loaded on first use (connected-widget.tsx imports it
// dynamically), so a board with no aggregate widget never downloads it.
//
// The aggregates are folded from the source's own buckets (WidgetDataSource.bucketMeasurements: each
// carries the exact count, min, max and sum), never from the chart seed: a seed is a series of bucket
// AVERAGES, and a minimum or maximum cannot be recovered from those. A source that produces no
// buckets is an explained state, not a guess.
//
//   - the span is the widget's pinned one, else the board's (DEFAULT_TIME_RANGE_SECONDS off a board);
//   - live, the read ends now and repeats every AGGREGATE_REFRESH_MS, so the figures keep up with
//     the readings arriving; paused, it ends at the freeze instant and is read once, so the number
//     on screen does not move;
//   - a read for a different selector, span or freeze instant is not shown under the new one: a
//     result is tagged with the key it was read for, and a mismatch reads as loading.

import {
  DEFAULT_TIME_RANGE_SECONDS,
  historyWindowFor,
  type AggregateResult,
  type AggregateStreamState,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { useMeasurementStream, useWidgetTimeRange } from '@devicechain/widget-sdk';
import { useEffect, useMemo, useState, type ComponentType } from 'react';

import { aggregateBuckets } from './aggregate-fold';
import { measurementSpan } from './binding-policy';

// How often a live aggregate is read again.
export const AGGREGATE_REFRESH_MS = 15_000;

const LOADING: AggregateResult = { status: 'loading' };

export interface AggregateFeedProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  descriptor: WidgetDescriptor;
  // The widget's component, and the host's pane for a stream that failed (before any figure).
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: ComponentType<any>;
  actions: WidgetActions | undefined;
  ErrorFrame: ComponentType<{ widget: WidgetInstance; error: unknown }>;
}

export default function AggregateFeed({ hub, widget, descriptor, Component, actions, ErrorFrame }: AggregateFeedProps) {
  const range = useWidgetTimeRange();
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const spanSeconds = measurementSpan(descriptor, widget, range?.spanSeconds) ?? DEFAULT_TIME_RANGE_SECONDS;
  const pausedAt = range?.paused ? range.pausedAt : undefined;
  const ds = widget.datasource;

  // The newest live reading per measurement. It holds one sample: only `latest` is read, and the
  // pause keeps it from moving.
  const stream = useMeasurementStream(hub, ds, { window: 1, pausedAt, now });

  const key = JSON.stringify([ds ?? null, spanSeconds, pausedAt ?? null]);
  const supported = hub.bucketMeasurements !== undefined;
  const [tagged, setTagged] = useState<{ key: string; result: AggregateResult }>({ key, result: LOADING });

  useEffect(() => {
    if (!supported || !ds) return;
    let current = true;
    const run = (): void => {
      const window = historyWindowFor(spanSeconds, pausedAt ?? now());
      // Folded inside the chain, so a bucket that cannot be trusted fails the read like a network error.
      hub.bucketMeasurements!(ds, window).then(aggregateBuckets).then(
        (byName) => {
          if (!current) return;
          const { startTime, endTime } = window;
          setTagged({ key, result: { status: 'ready', byName, startTime, endTime, spanSeconds } });
        },
        (err: unknown) => {
          if (!current) return;
          const message = err instanceof Error ? err.message : String(err);
          setTagged({ key, result: { status: 'unavailable', reason: 'failed', message } });
        },
      );
    };
    run();
    // Paused, the window is fixed at the freeze instant: nothing new can land in it.
    const timer = pausedAt === undefined ? setInterval(run, AGGREGATE_REFRESH_MS) : undefined;
    return () => {
      current = false;
      if (timer !== undefined) clearInterval(timer);
    };
    // `ds` is read through `key` (value identity), not by reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, key]);

  let aggregates: AggregateResult;
  if (!supported) aggregates = { status: 'unavailable', reason: 'unsupported' };
  else if (!ds) aggregates = { status: 'ready', byName: {}, startTime: '', endTime: '', spanSeconds };
  else aggregates = tagged.key === key ? tagged.result : LOADING;

  const data: AggregateStreamState = { aggregates, latest: stream.latest, error: stream.error };
  return data.error ? <ErrorFrame widget={widget} error={data.error} /> : <Component widget={widget} data={data} actions={actions} />;
}

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
//   - the window is whole minutes (aggregateWindow), so it holds no reading from before the span;
//   - live, the read ends at the last minute and repeats on the boundaries of aggregateRefreshMs
//     (one chart bucket of the span: a minute for an hour, 42 minutes for a week), so every card on
//     the board reads at the same moment and the hub serves identical reads once; paused, it ends
//     at the freeze instant and is read once, so the number on screen does not move;
//   - a card with `sparkline` set reads the SAME window in the span's chart buckets
//     (bucketSecondsFor) instead of one, and keeps each bucket as a trend point; the figure is
//     folded from those buckets and is the same number either way;
//   - a hidden tab does not read; it reads once when it is shown again;
//   - a live feed that resumes after an interruption reads again at once, so the figures take in
//     the readings sent during the outage (and a read that failed during it stops saying so) without
//     waiting for the next refresh, which on a week's span is 42 minutes away;
//   - a read for a different selector, span or freeze instant is not shown under the new one: a
//     result is tagged with the key it was read for, and a mismatch reads as loading.

import {
  bucketSecondsFor,
  DEFAULT_TIME_RANGE_SECONDS,
  type AggregateResult,
  type AggregateStreamState,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { optBoolean, useMeasurementStream, useWidgetTimeRange } from '@devicechain/widget-sdk';
import { useEffect, useMemo, useReducer, useState, type ComponentType } from 'react';

import { aggregateBuckets, aggregateRefreshMs, aggregateSeries, aggregateWindow } from './aggregate-fold';
import { measurementSpan } from './binding-policy';

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
  const [resumes, resumed] = useReducer((n: number) => n + 1, 0);
  const stream = useMeasurementStream(hub, ds, { window: 1, pausedAt, now, onReconnect: resumed });

  const trend = optBoolean(widget.options, 'sparkline');
  const key = JSON.stringify([ds ?? null, spanSeconds, pausedAt ?? null, trend]);
  const supported = hub.bucketMeasurements !== undefined;
  const [tagged, setTagged] = useState<{ key: string; result: AggregateResult }>({ key, result: LOADING });

  useEffect(() => {
    if (!supported || !ds) return;
    let current = true;
    const run = (): void => {
      const { window, spanSeconds: covered } = aggregateWindow(spanSeconds, pausedAt ?? now(), trend ? bucketSecondsFor(spanSeconds) : undefined);
      // Folded inside the chain, so a bucket that cannot be trusted fails the read like a network error.
      hub.bucketMeasurements!(ds, window).then((buckets) => ({ byName: aggregateBuckets(buckets), buckets })).then(
        ({ byName, buckets }) => {
          if (!current) return;
          const { startTime, endTime } = window;
          const series = trend ? { series: aggregateSeries(buckets) } : {};
          setTagged({ key, result: { status: 'ready', byName, ...series, startTime, endTime, spanSeconds: covered } });
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
    if (pausedAt !== undefined) return () => void (current = false);
    const hidden = () => document.hidden;
    const every = aggregateRefreshMs(spanSeconds);
    let timer: ReturnType<typeof setTimeout>;
    const schedule = (): void => {
      timer = setTimeout(
        () => {
          if (!hidden()) run();
          schedule();
        },
        every - (now() % every),
      );
    };
    schedule();
    const shown = (): void => {
      if (!hidden()) run();
    };
    document.addEventListener('visibilitychange', shown);
    return () => {
      current = false;
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', shown);
    };
    // `ds` is read through `key` (value identity), not by reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, key, resumes]);

  let aggregates: AggregateResult;
  if (!supported) aggregates = { status: 'unavailable', reason: 'unsupported' };
  else if (!ds) aggregates = { status: 'ready', byName: {}, startTime: '', endTime: '', spanSeconds };
  else aggregates = tagged.key === key ? tagged.result : LOADING;

  const data: AggregateStreamState = { aggregates, latest: stream.latest, error: stream.error };
  return data.error ? <ErrorFrame widget={widget} error={data.error} /> : <Component widget={widget} data={data} actions={actions} />;
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The data feed of the history table: one device's stored history over its span, in time buckets.
// Loaded with the table's chunk (connected-widget.tsx imports it dynamically), so a board without a
// history table never downloads it.
//
//   - the span is the widget's pinned one, else the board's (DEFAULT_TIME_RANGE_SECONDS off a board);
//   - the window is whole minutes ending at the last minute boundary, in the buckets of the span's chart
//     (bucketSecondsFor), read through the source's own bucketMeasurements: the same call a stat card
//     makes, so the hub shares identical reads. The table shows each bucket's AVERAGE and says so;
//   - there is no live tail. A reading that arrived since the last bucket read belongs to a bucket that
//     is still filling, and a figure that moved under the viewer between two reads would be a reading
//     passed off as an average. The table reads again on the boundaries of its bucket (live) and says
//     how old its read is (the frame's freshness is the last SUCCESSFUL read);
//   - paused, the window ends at the freeze instant and is read once;
//   - a hidden tab does not read; it reads once when it is shown again;
//   - a read for a different selector, span or freeze instant is not shown under the new one: a result
//     is tagged with the key it was read for, and a mismatch reads as loading. A read that fails keeps
//     the buckets it would have replaced (they are still true) and the frame ages from the last good one.

import { DEFAULT_TIME_RANGE_SECONDS, type WidgetActions, type WidgetDataSource, type WidgetDescriptor, type WidgetInstance } from '@devicechain/dashboards';
import { isForbiddenError } from '@devicechain/client';
import { resolveStaleAfterSeconds, useWidgetTimeRange, WidgetFreshnessProvider } from '@devicechain/widget-sdk';
import { useEffect, useMemo, useState, type ComponentType } from 'react';

import { measurementSpan } from './binding-policy';
import { historyRefreshMs, historyWindow, type HistoryView } from './history-model';

export interface HistoryFeedProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  descriptor: WidgetDescriptor;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: ComponentType<any>;
  actions: WidgetActions | undefined;
}

interface Read {
  key: string;
  view: HistoryView;
  // When the last successful read landed (epoch ms): the age of the figures on screen.
  readAt: number | null;
}

export default function HistoryFeed({ hub, widget, descriptor, Component, actions }: HistoryFeedProps) {
  const range = useWidgetTimeRange();
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const spanSeconds = measurementSpan(descriptor, widget, range?.spanSeconds) ?? DEFAULT_TIME_RANGE_SECONDS;
  const pausedAt = range?.paused ? range.pausedAt : undefined;
  const ds = widget.datasource;
  const key = JSON.stringify([ds ?? null, spanSeconds, pausedAt ?? null]);
  const supported = hub.bucketMeasurements !== undefined;
  const [read, setRead] = useState<Read>({ key, view: { status: 'loading' }, readAt: null });

  useEffect(() => {
    if (!supported || !ds || ds.kind === 'unbound') return;
    let current = true;
    const run = (): void => {
      const window = historyWindow(spanSeconds, pausedAt ?? now());
      hub.bucketMeasurements!(ds, window).then(
        (buckets) => {
          if (current) setRead({ key, view: { status: 'ready', buckets, window }, readAt: Date.now() });
        },
        (err: unknown) => {
          if (!current) return;
          // A refusal is a state, not a failure; a failure keeps what is on screen for THIS read's key.
          const status = isForbiddenError(err) ? 'forbidden' : 'failed';
          setRead((prev) => (status === 'failed' && prev.key === key && prev.view.status === 'ready' ? prev : { key, view: { status }, readAt: null }));
        },
      );
    };
    run();
    // Paused, the window is fixed at the freeze instant: nothing new can land in it.
    if (pausedAt !== undefined) return () => void (current = false);
    const every = historyRefreshMs(spanSeconds);
    let timer: ReturnType<typeof setTimeout>;
    const schedule = (): void => {
      timer = setTimeout(
        () => {
          if (!document.hidden) run();
          schedule();
        },
        every - (now() % every),
      );
    };
    schedule();
    const shown = (): void => {
      if (!document.hidden) run();
    };
    document.addEventListener('visibilitychange', shown);
    return () => {
      current = false;
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', shown);
    };
    // `ds` is read through `key` (value identity), not by reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, key]);

  let view: HistoryView;
  let readAt: number | null = null;
  if (!supported) view = { status: 'unsupported' };
  else if (!ds || ds.kind === 'unbound') view = { status: 'unbound' };
  else if (read.key === key) ({ view, readAt } = read);
  else view = { status: 'loading' };

  const freshness = useMemo(
    () => (descriptor.freshness === true ? { at: readAt ?? undefined, staleAfterSeconds: resolveStaleAfterSeconds(widget.options) } : undefined),
    [descriptor, widget.options, readAt],
  );
  const data = { latest: {}, samples: [], error: null };
  return (
    <WidgetFreshnessProvider freshness={freshness}>
      <Component widget={widget} data={data} actions={actions} history={view} />
    </WidgetFreshnessProvider>
  );
}

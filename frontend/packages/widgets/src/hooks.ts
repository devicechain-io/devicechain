// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// React bindings between the imperative DashboardHub / DOM theme and widget state.

import type {
  AlarmRow,
  AlarmSubscription,
  DatasourceSelector,
  MeasurementSample,
  WidgetDataSource,
} from '@devicechain/dashboards';
import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type RefObject } from 'react';

import { resolveChartTheme, type ChartTheme } from './theme';

export interface MeasurementStreamState {
  // The most recent sample per measurement name (drives cards, gauges, tables).
  latest: Record<string, MeasurementSample>;
  // A rolling, chronological window of recent samples (drives the time chart).
  samples: MeasurementSample[];
  error: unknown | null;
}

// The state an alarm widget renders from: the current rows (newest first, capped),
// the total match count (past the page — drives alarm-count), and load/error flags.
export interface AlarmStreamState {
  alarms: AlarmRow[];
  total: number;
  loading: boolean;
  error: unknown | null;
}

export interface MeasurementStreamOptions {
  // Max samples retained in the rolling window (oldest dropped past this).
  window?: number;
  // Historical samples to seed the window with before live data arrives (from a
  // bucketedMeasurements backfill). Assumed chronological and older than the live
  // tail; live samples for the same measurement name override the seed's latest.
  initialSamples?: MeasurementSample[];
}

const EMPTY: MeasurementStreamState = { latest: {}, samples: [], error: null };

// Build a name→last-sample map from a chronological list (last occurrence wins).
function latestOf(samples: MeasurementSample[]): Record<string, MeasurementSample> {
  const latest: Record<string, MeasurementSample> = {};
  for (const s of samples) latest[s.name] = s;
  return latest;
}

// useMeasurementStream subscribes a widget's datasource through the hub and keeps
// the latest value per measurement plus a bounded rolling window. It re-subscribes
// when the datasource changes (compared by value, so a re-rendered-but-equal
// selector doesn't churn the subscription) and tears down on unmount. When
// initialSamples is given, it is merged ahead of the live tail so a chart shows
// history immediately (and can arrive after the live stream opens).
export function useMeasurementStream(
  hub: WidgetDataSource,
  datasource: DatasourceSelector | undefined,
  options: MeasurementStreamOptions = {},
): MeasurementStreamState {
  const windowSize = options.window ?? 300;
  const initialSamples = options.initialSamples;
  const [live, setLive] = useState<MeasurementStreamState>(EMPTY);

  // Value-compare the selector so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = datasource ? JSON.stringify(datasource) : null;

  useEffect(() => {
    setLive(EMPTY); // reset the live buffer whenever the datasource changes (or clears)
    if (!datasource) return;

    return hub.subscribeWidget(datasource, {
      next: (sample) =>
        setLive((prev) => {
          const samples = prev.samples.concat(sample);
          if (samples.length > windowSize) samples.splice(0, samples.length - windowSize);
          return { latest: { ...prev.latest, [sample.name]: sample }, samples, error: null };
        }),
      error: (err) => setLive((prev) => ({ ...prev, error: err })),
    });
    // `datasource` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key, windowSize]);

  // Layer the history seed under the live tail. Kept separate from the live buffer
  // so late-arriving history (async backfill) recomputes without touching the
  // subscription, and switching datasource clears live while the new seed applies.
  return useMemo(() => {
    if (!initialSamples || initialSamples.length === 0) return live;
    const merged = initialSamples.concat(live.samples);
    const samples =
      merged.length > windowSize ? merged.slice(merged.length - windowSize) : merged;
    return {
      latest: { ...latestOf(initialSamples), ...live.latest },
      samples,
      error: live.error,
    };
  }, [initialSamples, live, windowSize]);
}

const EMPTY_ALARMS: AlarmStreamState = { alarms: [], total: 0, loading: true, error: null };

// useAlarmStream binds an alarm widget's scope+filters through the hub's alarm channel
// and keeps the latest snapshot. It re-subscribes when the subscription changes
// (value-compared, so a re-rendered-but-equal object doesn't churn) and tears down on
// unmount. The hub delivers whole snapshots (query-then-reconcile), so this holds the
// last one rather than accumulating — the query is the source of truth.
export function useAlarmStream(
  hub: WidgetDataSource,
  subscription: AlarmSubscription,
): AlarmStreamState {
  const [state, setState] = useState<AlarmStreamState>(EMPTY_ALARMS);

  // Value-compare the subscription so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = JSON.stringify(subscription);

  useEffect(() => {
    setState(EMPTY_ALARMS); // reset to loading whenever the subscription changes
    return hub.subscribeAlarms(subscription, {
      next: (snapshot) =>
        setState({ alarms: snapshot.alarms, total: snapshot.total, loading: false, error: null }),
      error: (err) => setState((prev) => ({ ...prev, loading: false, error: err })),
    });
    // `subscription` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key]);

  return state;
}

export interface ElementSize {
  width: number;
  height: number;
}

// useElementSize tracks an element's content-box size via ResizeObserver, so a
// widget can scale its contents (chart fonts, gauge ticks, a big number) to the
// slot it was resized to — ECharts' resize() re-lays-out the canvas but never
// scales absolute font sizes, so a small widget crams without this. Returns
// {0,0} until measured (and where ResizeObserver is unavailable, e.g. jsdom); the
// consumers treat a zero size as "use default sizing".
export function useElementSize<T extends HTMLElement>(): [RefObject<T | null>, ElementSize] {
  const ref = useRef<T>(null);
  const [size, setSize] = useState<ElementSize>({ width: 0, height: 0 });

  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) return;
      setSize((prev) =>
        prev.width === rect.width && prev.height === rect.height
          ? prev
          : { width: rect.width, height: rect.height },
      );
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  return [ref, size];
}

// --- shared chart-theme store ------------------------------------------------
//
// Charts need concrete colors resolved from the CSS-variable theme. Rather than
// have every chart widget mount its own MutationObserver on <html> and recompute
// the same theme, one page-wide observer resolves it once per change and every
// widget reads the shared, memoized value via useSyncExternalStore — so a busy
// dashboard pays O(1), not O(number of charts), per theme toggle.

let sharedTheme: ChartTheme | null = null;
const themeListeners = new Set<() => void>();
let themeObserver: MutationObserver | null = null;

function recomputeTheme(): void {
  sharedTheme = resolveChartTheme(document.documentElement);
}

function subscribeTheme(listener: () => void): () => void {
  if (!themeObserver) {
    recomputeTheme();
    themeObserver = new MutationObserver(() => {
      recomputeTheme();
      for (const l of themeListeners) l();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'style'],
    });
  }
  themeListeners.add(listener);
  return () => {
    themeListeners.delete(listener);
    if (themeListeners.size === 0 && themeObserver) {
      themeObserver.disconnect();
      themeObserver = null;
      sharedTheme = null;
    }
  };
}

function getThemeSnapshot(): ChartTheme {
  if (!sharedTheme) recomputeTheme();
  return sharedTheme as ChartTheme;
}

// useChartTheme returns the current chart colors and re-renders the caller when
// the document theme changes (a light/dark class or inline-style swap on <html>).
export function useChartTheme(): ChartTheme {
  return useSyncExternalStore(subscribeTheme, getThemeSnapshot, getThemeSnapshot);
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// React bindings between the imperative DashboardHub / DOM theme and widget state.

import type { DashboardHub, DatasourceSelector, MeasurementSample } from '@devicechain/dashboards';
import { useEffect, useState, useSyncExternalStore } from 'react';

import { resolveChartTheme, type ChartTheme } from './theme';

export interface MeasurementStreamState {
  // The most recent sample per measurement name (drives cards, gauges, tables).
  latest: Record<string, MeasurementSample>;
  // A rolling, chronological window of recent samples (drives the time chart).
  samples: MeasurementSample[];
  error: unknown | null;
}

const EMPTY: MeasurementStreamState = { latest: {}, samples: [], error: null };

// useMeasurementStream subscribes a widget's datasource through the hub and keeps
// the latest value per measurement plus a bounded rolling window. It re-subscribes
// when the datasource changes (compared by value, so a re-rendered-but-equal
// selector doesn't churn the subscription) and tears down on unmount.
export function useMeasurementStream(
  hub: DashboardHub,
  datasource: DatasourceSelector | undefined,
  options: { window?: number } = {},
): MeasurementStreamState {
  const windowSize = options.window ?? 300;
  const [state, setState] = useState<MeasurementStreamState>(EMPTY);

  // Value-compare the selector so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = datasource ? JSON.stringify(datasource) : null;

  useEffect(() => {
    setState(EMPTY); // reset whenever the datasource changes (or clears)
    if (!datasource) return;

    return hub.subscribeWidget(datasource, {
      next: (sample) =>
        setState((prev) => {
          const samples = prev.samples.concat(sample);
          if (samples.length > windowSize) samples.splice(0, samples.length - windowSize);
          return { latest: { ...prev.latest, [sample.name]: sample }, samples, error: null };
        }),
      error: (err) => setState((prev) => ({ ...prev, error: err })),
    });
    // `datasource` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key, windowSize]);

  return state;
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

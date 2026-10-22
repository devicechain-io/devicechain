// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A thin React wrapper over Apache ECharts (ADR-039 chart engine).
//
// 🔴 ECHARTS IS NOT IMPORTED HERE. The engine (ECharts + zrender, ~170 KB gzip, the largest single
// dependency of a board) lives in echart-engine.ts and is loaded on demand, ONCE, as one chunk:
// the first <EChart> to mount fetches it, and a host that wants the first frame complete fetches
// it first with loadChartEngine (preloadWidgets does, for a board that holds a chart). Only TYPES
// come from echarts here, and they are erased.
//
// The public surface is unchanged: <EChart option> is still a synchronous component. Once the
// engine is here, a mount initialises the chart exactly as it always did; before it is, the
// component renders its (empty) container and draws as soon as the engine arrives. A failed load
// is THROWN, so the widget's error boundary shows it rather than an empty box that looks like a
// chart with nothing to say, and it is NOT remembered: the next mount (the boundary's Reset)
// fetches again, and nothing retries on its own.
//
// The wrapper owns the imperative lifecycle: init on mount, setOption on change, resize with the
// container, dispose on unmount.

import type * as echarts from 'echarts/core';
import { useEffect, useRef, useState } from 'react';

import { useRegisterWidgetImage } from '../context';

type Engine = typeof import('./echart-engine');

let engine: Engine | undefined;
let engineLoading: Promise<Engine> | undefined;

// Memoized while in flight or loaded; a rejection is forgotten, so the next caller fetches again.
function loadEngine(): Promise<Engine> {
  engineLoading ??= import('./echart-engine').then(
    (m) => (engine = m),
    (err: unknown) => {
      engineLoading = undefined;
      throw err;
    },
  );
  return engineLoading;
}

/**
 * Fetches the chart engine that <EChart> draws with, so the charts a host renders after awaiting
 * it draw on their first frame. Optional: an <EChart> that mounts first loads the engine itself.
 * Rejects if the engine cannot be fetched; a later call (or mount) fetches it again.
 */
export function loadChartEngine(): Promise<void> {
  return loadEngine().then(() => undefined);
}

// markLine / markArea are registered on first use (see echart-markers.ts for why). Until they
// are, a chart that asks for them draws without them, and EChart re-applies its option once
// they arrive -- a beat later on the first threshold chart of a page, never silently absent.
let markersReady = false;
let markersLoading: Promise<void> | undefined;

export function loadThresholdMarkers(): Promise<void> {
  markersLoading ??= loadEngine()
    .then((e) => e.registerThresholdMarkers())
    .then(
      () => {
        markersReady = true;
      },
      (err: unknown) => {
        markersLoading = undefined; // a failed chunk load is retried by the next chart that needs it
        throw err;
      },
    );
  return markersLoading;
}

// True when any series of the option carries a markLine or markArea.
export function optionUsesMarkers(option: EChartOption): boolean {
  const series = (option as { series?: unknown }).series;
  const list = Array.isArray(series) ? series : series ? [series] : [];
  return list.some((s) => {
    const m = s as { markLine?: unknown; markArea?: unknown } | null;
    return typeof m === 'object' && m !== null && (m.markLine != null || m.markArea != null);
  });
}

// A resolved ECharts option object (the shape echarts.setOption accepts). Widgets
// build these with the pure builders in chart-options.ts.
export type EChartOption = echarts.EChartsCoreOption;

export interface EChartProps {
  option: EChartOption;
  className?: string;
  style?: React.CSSProperties;
}

// EChart fills its parent; the dashboard canvas sizes the parent, and a
// ResizeObserver keeps the chart matched to it.
export function EChart({ option, className, style }: EChartProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);
  const [loaded, setLoaded] = useState(engine);
  const [failed, setFailed] = useState<{ error: unknown }>();
  const [markersLoaded, setMarkersLoaded] = useState(markersReady);
  const needsMarkers = optionUsesMarkers(option) && !markersLoaded;

  // Offer the chart's image to the frame it is drawn in (its "Download image") once the engine is
  // here and the chart exists: a PNG at twice the on-screen resolution, on the background the frame
  // hands over, since the canvas itself is transparent.
  useRegisterWidgetImage(
    loaded ? ({ backgroundColor }) => chartRef.current?.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor }) : undefined,
  );

  // The engine, when this is the first chart to mount and nobody preloaded it.
  useEffect(() => {
    if (loaded) return;
    let live = true;
    loadEngine().then(
      (e) => live && setLoaded(e),
      (error: unknown) => live && setFailed({ error }),
    );
    return () => {
      live = false;
    };
  }, [loaded]);

  useEffect(() => {
    if (!needsMarkers) return;
    let live = true;
    loadThresholdMarkers().then(
      () => live && setMarkersLoaded(true),
      // Loud, not silent: the chart still draws, without its threshold lines.
      (err: unknown) => console.error('EChart: the threshold markers failed to load', err),
    );
    return () => {
      live = false;
    };
  }, [needsMarkers]);

  // Init + teardown once for the element's lifetime (from the moment the engine is here).
  useEffect(() => {
    const el = containerRef.current;
    if (!el || !loaded) return;

    const chart = loaded.init(el);
    chartRef.current = chart;

    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(el);

    return () => {
      observer.disconnect();
      chart.dispose();
      chartRef.current = null;
    };
  }, [loaded]);

  // Re-apply the option whenever it changes. notMerge:true so a shrinking series
  // (rolling window) doesn't leave stale points behind.
  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge: true });
  }, [option, markersLoaded, loaded]);

  // To the widget's error boundary: a chart that cannot draw says so.
  if (failed) throw failed.error;

  // `data-echart` marks the chart's box, so a host (and the readability gate) can tell a chart
  // that has drawn (ECharts stamps `_echarts_instance_` on it) from one still waiting.
  return (
    <div ref={containerRef} data-echart="" className={className} style={{ width: '100%', height: '100%', ...style }} />
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A thin React wrapper over Apache ECharts (ADR-039 chart engine).
//
// It imports from echarts/core and registers ONLY the chart types + components the
// widgets use (line + gauge, grid/tooltip/legend, canvas renderer; threshold markers on demand) —
// tree-shaking away the rest of ECharts so the bundle carries only what a dashboard needs. The wrapper
// owns the imperative lifecycle: init on mount, setOption on change, resize with the
// container, dispose on unmount.

import { GaugeChart, LineChart } from 'echarts/charts';
import { GridComponent, LegendPlainComponent, TooltipComponent } from 'echarts/components';
import * as echarts from 'echarts/core';
import { CanvasRenderer } from 'echarts/renderers';
import { useEffect, useRef, useState } from 'react';

// 🔴 EVERY COMPONENT AN OPTION BUILDER EMITS MUST BE REGISTERED -- here, or (the threshold
// markers) by loadThresholdMarkers below. ECharts ignores an option key whose component is
// not registered -- silently, no warning -- so a `legend` or a `markLine` the builder sets
// draws nothing. The legend was dropped this way for as long as multi-series charts existed.
// echart.registration.test.tsx renders each through a real ECharts instance, so a builder
// that grows a new component fails there until it is registered.
//
// The PLAIN legend, not LegendComponent: that one also installs the scrollable legend, which
// no builder asks for (~1.8 KB gzip).
echarts.use([LineChart, GaugeChart, GridComponent, TooltipComponent, LegendPlainComponent, CanvasRenderer]);

// markLine / markArea are registered on first use (see echart-markers.ts for why). Until they
// are, a chart that asks for them draws without them, and EChart re-applies its option once
// they arrive -- a beat later on the first threshold chart of a page, never silently absent.
let markersReady = false;
let markersLoading: Promise<void> | undefined;

export function loadThresholdMarkers(): Promise<void> {
  markersLoading ??= import('./echart-markers').then(
    (m) => {
      echarts.use(m.MARKER_COMPONENTS);
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
  const [markersLoaded, setMarkersLoaded] = useState(markersReady);
  const needsMarkers = optionUsesMarkers(option) && !markersLoaded;

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

  // Init + teardown once for the element's lifetime.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const chart = echarts.init(el);
    chartRef.current = chart;

    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(el);

    return () => {
      observer.disconnect();
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  // Re-apply the option whenever it changes. notMerge:true so a shrinking series
  // (rolling window) doesn't leave stale points behind.
  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge: true });
  }, [option, markersLoaded]);

  return (
    <div ref={containerRef} className={className} style={{ width: '100%', height: '100%', ...style }} />
  );
}

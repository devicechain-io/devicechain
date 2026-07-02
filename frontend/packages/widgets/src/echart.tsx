// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A thin React wrapper over Apache ECharts (ADR-039 chart engine).
//
// It imports from echarts/core and registers ONLY the chart types + components the
// widgets use (line + gauge, grid/tooltip, canvas renderer) — tree-shaking away the
// rest of ECharts so the bundle carries only what a dashboard needs. The wrapper
// owns the imperative lifecycle: init on mount, setOption on change, resize with the
// container, dispose on unmount.

import { GaugeChart, LineChart } from 'echarts/charts';
import { GridComponent, TooltipComponent } from 'echarts/components';
import * as echarts from 'echarts/core';
import { CanvasRenderer } from 'echarts/renderers';
import { useEffect, useRef } from 'react';

echarts.use([LineChart, GaugeChart, GridComponent, TooltipComponent, CanvasRenderer]);

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
  }, [option]);

  return (
    <div ref={containerRef} className={className} style={{ width: '100%', height: '100%', ...style }} />
  );
}

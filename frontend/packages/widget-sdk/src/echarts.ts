// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/widget-sdk/echarts — the chart helpers.
//
// 🔴 A SEPARATE ENTRY ON PURPOSE. The SDK root is chart-free so a DOM-only widget importing
// it does not inherit Apache ECharts; everything here that touches the chart engine (the
// <EChart> wrapper, the option builders, the colour theme resolved for a canvas) lives behind
// this subpath. `echarts` is an OPTIONAL peer of the package: a host that never imports this
// entry never needs it installed.
//
// Importing this entry does not load ECharts itself: <EChart> fetches the engine on demand, as one
// chunk, the first time a chart mounts. loadChartEngine fetches it ahead of that, for a host that
// wants its first frame to hold the chart.

export { EChart, loadChartEngine, type EChartOption, type EChartProps } from './chart/echart';
export {
  buildCompassOption,
  buildGaugeOption,
  buildLineOption,
  gaugeReadoutBox,
  severityFill,
  wrapHeading,
  type CompassChartOptions,
  type GaugeChartOptions,
  type LineChartOptions,
} from './chart/chart-options';
export { resolveChartTheme, useChartTheme, type ChartTheme } from './chart/chart-theme';

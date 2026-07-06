// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/widgets — presentational dashboard widgets (ADR-039).
//
// Six built-ins over the DashboardHub's live telemetry, themed via CSS custom
// properties (no Tailwind leakage — embeddable in any React host). A renderer
// looks widgets up by type through WIDGET_REGISTRY.

// The view-only dashboard renderer: lays a parsed definition's widgets on the
// canvas and binds each to the hub. The shared viewer for the console + external apps.
export { DashboardRenderer, type DashboardRendererProps } from './dashboard-renderer';

// Renderer entry point: binds the hub to a widget by type.
export { ConnectedWidget, type ConnectedWidgetProps } from './connected-widget';

// Registries + widget contract + channel classifier.
export {
  WIDGET_REGISTRY,
  ALARM_WIDGET_REGISTRY,
  WIDGET_CHANNEL,
  type WidgetChannel,
} from './registry';
export { pickSample, type WidgetProps, type WidgetComponent } from './widget';

// Individual widgets (for direct/custom use).
export { LatestCard } from './widgets/latest-card';
export { Gauge } from './widgets/gauge';
export { TimeSeriesChart } from './widgets/time-series-chart';
export { Table } from './widgets/table';
export { Label } from './widgets/label';
export { Image } from './widgets/image';
export { AlarmTable } from './widgets/alarm-table';
export { AlarmCount } from './widgets/alarm-count';
export {
  severityColor,
  severityLabel,
  severityRank,
  highestSeverity,
} from './widgets/severity';

// Building blocks for custom widgets.
export { WidgetFrame, type WidgetFrameProps } from './frame';
export { EChart, type EChartOption } from './echart';
export {
  useMeasurementStream,
  useAlarmStream,
  useChartTheme,
  type MeasurementStreamState,
  type AlarmStreamState,
} from './hooks';
export {
  buildLineOption,
  buildGaugeOption,
  type LineChartOptions,
  type GaugeChartOptions,
} from './chart-options';
export { css, resolveChartTheme, type ChartTheme, type ThemeVar } from './theme';

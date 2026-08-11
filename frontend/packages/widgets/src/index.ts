// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/widgets — presentational dashboard widgets (ADR-039).
//
// Six built-ins over the DashboardHub's live telemetry, themed via CSS custom
// properties (no Tailwind leakage — embeddable in any React host). A renderer
// looks widgets up by type through WIDGET_REGISTRY.

// The view-only dashboard renderer: lays a parsed definition's widgets on the
// canvas and binds each to the hub. The shared viewer for the console + external apps.
export { DashboardRenderer, widgetSubjectLabel, type DashboardRendererProps } from './dashboard-renderer';

// Renderer entry point: binds the hub to a widget by type.
export { ConnectedWidget, type ConnectedWidgetProps } from './connected-widget';

// Registries + widget contract + channel classifier.
export {
  WIDGET_REGISTRY,
  ALARM_WIDGET_REGISTRY,
  CONTROL_WIDGET_REGISTRY,
  SELECTION_WIDGET_REGISTRY,
  LOCATION_WIDGET_REGISTRY,
  WIDGET_CHANNEL,
  WIDGET_BINDS_DATASOURCE,
  type WidgetChannel,
} from './registry';
export { pickSample, optBoolean, type WidgetProps, type WidgetComponent } from './widget';

// The tenant basemap seam. A host installs the provider; map widgets read it.
export { TenantBasemapProvider, useTenantBasemap } from './basemap-context';

// The typed schema for each widget's options bag: what the renderer reads, at what type,
// with what legal values. WidgetOptions<T> types code that BUILDS options;
// validateWidgetOptions reports where a stored bag disagrees with the renderer.
export {
  WIDGET_OPTIONS,
  validateWidgetOptions,
  numberOptionSpec,
  clampNumberOption,
  enumOptionValues,
  type WidgetOptions,
  type WidgetOptionSpecs,
  type OptionSpec,
  type StringOptionSpec,
  type NumberOptionSpec,
  type BooleanOptionSpec,
  type EnumOptionSpec,
  type OptionIssue,
  type OptionIssueCode,
} from './options';

// The same schemas applied to a whole board: what a producer of definitions asks
// before it publishes one, plus the repair for the one issue an author cannot fix in
// a config panel.
export {
  validateDefinitionOptions,
  stripUnknownOptions,
  type DefinitionOptionIssue,
} from './definition-options';

// Directional change-flash (opt-in `flashOnChange`): a reusable hook + the flashing value cell.
export {
  useFlashOnChange,
  flashTextStyle,
  flashBackgroundStyle,
  FlashValue,
  type FlashDirection,
} from './flash';

// Individual widgets (for direct/custom use).
export { LatestCard } from './widgets/latest-card';
export { Gauge } from './widgets/gauge';
export { TimeSeriesChart } from './widgets/time-series-chart';
export { Table } from './widgets/table';
export { Label } from './widgets/label';
export { Image } from './widgets/image';
export { AlarmTable } from './widgets/alarm-table';
export { AlarmCount } from './widgets/alarm-count';
export { CommandButton } from './widgets/command-button';
export { EntitySelector } from './widgets/entity-selector';
export { MapWidget } from './widgets/map';
// The map's pure parts: which samples can be placed, where they land on a tile-less
// panel, and how a fix is described without inventing the fields it does not carry.
//
// loadLandStyle is the bundled world basemap — the style used when no tile source
// is configured. Exported so the console's fence editor and this package's map
// widget cannot end up drawing two different worlds; the geometry itself stays
// behind a dynamic import inside that module, so naming it here costs nothing.
export {
  placeable,
  projectToPanel,
  describePosition,
  formatDegrees,
  rasterStyleFor,
  landStyleFrom,
  loadLandStyle,
  BUNDLED_BASEMAP_ATTRIBUTION,
  type PlaceableLocation,
  type PanelPoint,
} from './widgets/map-geometry';
export {
  severityColor,
  severityLabel,
  severityRank,
  highestSeverity,
} from './widgets/severity';
export { commandStatusColor, commandStatusLabel, isTerminalStatus } from './widgets/command-status';
export {
  parseParameterSchema,
  defaultValues,
  validateParams,
  buildPayload,
  isScalar,
} from './widgets/command-params';

// Building blocks for custom widgets.
export {
  WidgetFrame,
  WidgetSubjectProvider,
  WidgetSelectProvider,
  useWidgetSelect,
  WidgetCandidatesProvider,
  useWidgetCandidates,
  type WidgetFrameProps,
  type WidgetSelect,
  type WidgetCandidates,
} from './frame';
export { EChart, type EChartOption } from './echart';
export {
  useMeasurementStream,
  useAlarmStream,
  useCommandStream,
  useLocationStream,
  useDatasourceAvailability,
  useResolvedBindings,
  useSlotCandidates,
  useCandidates,
  useChartTheme,
  type MeasurementStreamState,
  type AlarmStreamState,
  type CommandStreamState,
  type LocationStreamState,
  type CandidatesState,
  type DatasourceAvailability,
} from './hooks';
export {
  buildLineOption,
  buildGaugeOption,
  type LineChartOptions,
  type GaugeChartOptions,
} from './chart-options';
export { css, resolveChartTheme, type ChartTheme, type ThemeVar } from './theme';

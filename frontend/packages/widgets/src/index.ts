// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/widgets — presentational dashboard widgets (ADR-039).
//
// Eleven built-ins over the DashboardHub's live data, themed via CSS custom
// properties (no Tailwind leakage — embeddable in any React host). Each is a
// registration declared with the public `defineWidget` from @devicechain/widget-sdk;
// a renderer resolves a widget through a registry instance.

// The view-only dashboard renderer: lays a parsed definition's widgets on the
// canvas and binds each to the hub. The shared viewer for the console + external apps.
export { DashboardRenderer, resolveWidgets, widgetSubjectLabel, type DashboardRendererProps } from './dashboard-renderer';
export {
  DEFAULT_TIME_RANGE_LABELS,
  PausedIndicator,
  TimeRangeBar,
  useBoardTimeRange,
  type TimeRangeControls,
  type TimeRangeLabels,
} from './time-range-bar';

// Deep links that open a board with a device bound: the hook that keeps the address and the
// selection in step (and checks the named devices against the caller's scope), and the notice a
// host shows instead of the board when a link cannot be honoured.
export {
  useBoundDevices,
  type BoundDevices,
  type BoundDevicesAddress,
  type BoundDevicesPhase,
  type LinkProblems,
} from './use-bound-devices';
export {
  BoundDevicesNotice,
  describeBoundDevicesProblems,
  type BoundDevicesNoticeProps,
  type BoundDevicesWording,
} from './bound-devices-notice';

// Renderer entry point: binds the hub to a widget by type.
export { ConnectedWidget, type ConnectedWidgetProps } from './connected-widget';

// The built-ins as registrations (the same descriptor + component pairing a third-party
// package supplies), and the factory for a sealed registry holding exactly them.
export { builtinWidgetCatalog, builtinWidgetDescriptors } from './documented-descriptors';
export { builtinWidgetDefinitions, createBuiltinWidgetRegistry } from './builtin-registry';
export { preloadWidgets } from './preload';

// Read-only by-id views of the built-in descriptors (channel, datasource binding), and the
// widget contract. The registrations above are the source of truth; these are derived.
export { WIDGET_CHANNEL, WIDGET_BINDS_DATASOURCE, type WidgetChannel } from './registry';
export { pickSample, optBoolean, type WidgetProps, type WidgetComponent } from '@devicechain/widget-sdk';

// The tenant basemap seam. A host installs the provider; map widgets read it.
export { TenantBasemapProvider, useTenantBasemap } from './basemap-context';

// The map RUNTIME seam — the MapLibre pieces only the host's bundler can emit.
//
// 🔴 Unlike the basemap provider above, this one is REQUIRED by any host that renders a
// map widget: without it the widget shows a notice instead of a map. That is deliberate.
// The alternative to a host-supplied worker URL is MapLibre's own runtime derivation,
// which no bundler can follow, and which fails as a map that renders nothing at all.
export { MapRuntimeProvider, useMapRuntime, type MapRuntime } from './map-runtime-context';

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
  type ListItemFieldSpec,
  type ListOptionSpec,
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
  type DefinitionValidationOptions,
} from './definition-options';

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

// Building blocks for custom widgets. They live in @devicechain/widget-sdk (the one place the
// ambient host contexts may be defined) and are re-exported so existing imports keep resolving;
// the chart helpers come from its `/echarts` subpath.
export {
  WidgetFrame,
  WidgetSubjectProvider,
  WidgetSelectProvider,
  useWidgetSelect,
  WidgetTimeZoneProvider,
  useWidgetTimeZone,
  WidgetThemeRootProvider,
  useWidgetThemeRoot,
  WidgetCandidatesProvider,
  useWidgetCandidates,
  useFlashOnChange,
  flashTextStyle,
  flashBackgroundStyle,
  FlashValue,
  fitValueFont,
  useMeasurementStream,
  useAlarmStream,
  useCommandStream,
  useLocationStream,
  useDatasourceAvailability,
  useDashboardHub,
  useHubConnectionState,
  HubConnectionBadge,
  useResolvedBindings,
  useSlotCandidates,
  useCandidates,
  css,
  type FlashDirection,
  type WidgetFrameProps,
  type WidgetSelect,
  type WidgetCandidates,
  type MeasurementStreamState,
  type AlarmStreamState,
  type CommandStreamState,
  type LocationStreamState,
  type CandidatesState,
  type DatasourceAvailability,
  type HubConnectionBadgeProps,
  type ThemeVar,
} from '@devicechain/widget-sdk';
export {
  EChart,
  buildLineOption,
  buildGaugeOption,
  resolveChartTheme,
  useChartTheme,
  type EChartOption,
  type LineChartOptions,
  type GaugeChartOptions,
  type ChartTheme,
} from '@devicechain/widget-sdk/echarts';

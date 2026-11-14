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
  AbsoluteIndicator,
  DEFAULT_ABSOLUTE_LABELS,
  DEFAULT_TIME_RANGE_LABELS,
  PausedIndicator,
  TimeRangeBar,
  absoluteLinkFor,
  formatAbsoluteRange,
  rejectedLinkNote,
  useBoardTimeRange,
  type AbsoluteLabels,
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
//
// Every one of these carries each option's `doc`. `builtinWidgetDefinitionsWithoutDocs` is the same
// registrations without it -- for a host that only renders boards and never shows an option's help,
// which then does not download the prose (a built-in id is the one place the registry excuses a
// missing doc). Its descriptors are TYPED as documented; their `doc` is absent at runtime.
export {
  builtinWidgetCatalog,
  builtinWidgetDescriptors,
  builtinWidgetDefinitions,
  createBuiltinWidgetRegistry,
  WIDGET_OPTIONS,
} from './documented-descriptors';
export { builtinWidgetDefinitions as builtinWidgetDefinitionsWithoutDocs } from './builtin-registry';
export { preloadWidgets } from './preload';

// Read-only by-id views of the built-in descriptors (channel, datasource binding), and the
// widget contract. The registrations above are the source of truth; these are derived.
export { WIDGET_CHANNEL, WIDGET_BINDS_DATASOURCE, type WidgetChannel } from './registry';
export {
  /** @deprecated Import pickSample from `@devicechain/widget-sdk` instead. */
  pickSample,
  /** @deprecated Import optBoolean from `@devicechain/widget-sdk` instead. */
  optBoolean,
  /** @deprecated Import WidgetProps from `@devicechain/widget-sdk` instead. */
  type WidgetProps,
  /** @deprecated Import WidgetComponent from `@devicechain/widget-sdk` instead. */
  type WidgetComponent,
} from '@devicechain/widget-sdk';

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
  /** @deprecated Use BuiltinOptionIssue from `@devicechain/widgets` instead. */
  type OptionIssue,
  type OptionIssue as BuiltinOptionIssue,
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
export { AlarmSeverity } from './widgets/alarm-severity';
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
  /** @deprecated Import WidgetFrame from `@devicechain/widget-sdk` instead. */
  WidgetFrame,
  /** @deprecated Import WidgetSubjectProvider from `@devicechain/widget-sdk` instead. */
  WidgetSubjectProvider,
  /** @deprecated Import WidgetSelectProvider from `@devicechain/widget-sdk` instead. */
  WidgetSelectProvider,
  /** @deprecated Import useWidgetSelect from `@devicechain/widget-sdk` instead. */
  useWidgetSelect,
  /** @deprecated Import WidgetTimeZoneProvider from `@devicechain/widget-sdk` instead. */
  WidgetTimeZoneProvider,
  /** @deprecated Import useWidgetTimeZone from `@devicechain/widget-sdk` instead. */
  useWidgetTimeZone,
  /** @deprecated Import WidgetThemeRootProvider from `@devicechain/widget-sdk` instead. */
  WidgetThemeRootProvider,
  /** @deprecated Import useWidgetThemeRoot from `@devicechain/widget-sdk` instead. */
  useWidgetThemeRoot,
  /** @deprecated Import WidgetCandidatesProvider from `@devicechain/widget-sdk` instead. */
  WidgetCandidatesProvider,
  /** @deprecated Import useWidgetCandidates from `@devicechain/widget-sdk` instead. */
  useWidgetCandidates,
  /** @deprecated Import useFlashOnChange from `@devicechain/widget-sdk` instead. */
  useFlashOnChange,
  /** @deprecated Import flashTextStyle from `@devicechain/widget-sdk` instead. */
  flashTextStyle,
  /** @deprecated Import flashBackgroundStyle from `@devicechain/widget-sdk` instead. */
  flashBackgroundStyle,
  /** @deprecated Import FlashValue from `@devicechain/widget-sdk` instead. */
  FlashValue,
  /** @deprecated Import fitValueFont from `@devicechain/widget-sdk` instead. */
  fitValueFont,
  /** @deprecated Import useMeasurementStream from `@devicechain/widget-sdk` instead. */
  useMeasurementStream,
  /** @deprecated Import useAlarmStream from `@devicechain/widget-sdk` instead. */
  useAlarmStream,
  /** @deprecated Import useCommandStream from `@devicechain/widget-sdk` instead. */
  useCommandStream,
  /** @deprecated Import useLocationStream from `@devicechain/widget-sdk` instead. */
  useLocationStream,
  /** @deprecated Import useDatasourceAvailability from `@devicechain/widget-sdk` instead. */
  useDatasourceAvailability,
  /** @deprecated Import useDashboardHub from `@devicechain/widget-sdk` instead. */
  useDashboardHub,
  /** @deprecated Import useHubConnectionState from `@devicechain/widget-sdk` instead. */
  useHubConnectionState,
  /** @deprecated Import HubConnectionBadge from `@devicechain/widget-sdk` instead. */
  HubConnectionBadge,
  /** @deprecated Import useResolvedBindings from `@devicechain/widget-sdk` instead. */
  useResolvedBindings,
  /** @deprecated Import useViewerBindings from `@devicechain/widget-sdk` instead. */
  useViewerBindings,
  /** @deprecated Import useSlotCandidates from `@devicechain/widget-sdk` instead. */
  useSlotCandidates,
  /** @deprecated Import useCandidates from `@devicechain/widget-sdk` instead. */
  useCandidates,
  /** @deprecated Import css from `@devicechain/widget-sdk` instead. */
  css,
  /** @deprecated Import FlashDirection from `@devicechain/widget-sdk` instead. */
  type FlashDirection,
  /** @deprecated Import WidgetFrameProps from `@devicechain/widget-sdk` instead. */
  type WidgetFrameProps,
  /** @deprecated Import WidgetSelect from `@devicechain/widget-sdk` instead. */
  type WidgetSelect,
  /** @deprecated Import WidgetCandidates from `@devicechain/widget-sdk` instead. */
  type WidgetCandidates,
  /** @deprecated Import MeasurementStreamState from `@devicechain/widget-sdk` instead. */
  type MeasurementStreamState,
  /** @deprecated Import AlarmStreamState from `@devicechain/widget-sdk` instead. */
  type AlarmStreamState,
  /** @deprecated Import CommandStreamState from `@devicechain/widget-sdk` instead. */
  type CommandStreamState,
  /** @deprecated Import LocationStreamState from `@devicechain/widget-sdk` instead. */
  type LocationStreamState,
  /** @deprecated Import CandidatesState from `@devicechain/widget-sdk` instead. */
  type CandidatesState,
  /** @deprecated Import DatasourceAvailability from `@devicechain/widget-sdk` instead. */
  type DatasourceAvailability,
  /** @deprecated Import HubConnectionBadgeProps from `@devicechain/widget-sdk` instead. */
  type HubConnectionBadgeProps,
  /** @deprecated Import ThemeVar from `@devicechain/widget-sdk` instead. */
  type ThemeVar,
} from '@devicechain/widget-sdk';
export {
  /** @deprecated Import EChart from `@devicechain/widget-sdk/echarts` instead. */
  EChart,
  /** @deprecated Import buildLineOption from `@devicechain/widget-sdk/echarts` instead. */
  buildLineOption,
  /** @deprecated Import buildGaugeOption from `@devicechain/widget-sdk/echarts` instead. */
  buildGaugeOption,
  /** @deprecated Import resolveChartTheme from `@devicechain/widget-sdk/echarts` instead. */
  resolveChartTheme,
  /** @deprecated Import useChartTheme from `@devicechain/widget-sdk/echarts` instead. */
  useChartTheme,
  /** @deprecated Import EChartOption from `@devicechain/widget-sdk/echarts` instead. */
  type EChartOption,
  /** @deprecated Import LineChartOptions from `@devicechain/widget-sdk/echarts` instead. */
  type LineChartOptions,
  /** @deprecated Import GaugeChartOptions from `@devicechain/widget-sdk/echarts` instead. */
  type GaugeChartOptions,
  /** @deprecated Import ChartTheme from `@devicechain/widget-sdk/echarts` instead. */
  type ChartTheme,
} from '@devicechain/widget-sdk/echarts';

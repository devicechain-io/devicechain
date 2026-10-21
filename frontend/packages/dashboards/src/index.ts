// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/dashboards — the dashboard runtime + definition contract (ADR-039).
//
// The DashboardHub multiplexes live telemetry over the @devicechain/client wire;
// the types are the canonical shape of the dashboard-definition JSON that
// dashboard-management stores opaquely. Widget packages and the standalone
// dashboard app build on top of these.

export type {
  DashboardDefinition,
  BoardTimeRange,
  Canvas,
  CanvasBackground,
  CanvasGrid,
  CanvasSizing,
  Breakpoints,
  BreakpointSpec,
  GridLength,
  GridPlacement,
  RelativePlacement,
  RelativeSize,
  Placement,
  PlacementAnchor,
  PlacementMode,
  FrameBorder,
  FrameBorderWidth,
  FrameHeader,
  FrameRadius,
  FrameVariant,
  ResolvedFrame,
  SpaceTokenName,
  ThemeColorToken,
  WidgetFrameSpec,
  WidgetInstance,
  KnownWidgetInstance,
  ExtensionWidgetInstance,
  ExtensionWidgetType,
  ResolvedWidgetInstance,
  UnknownWidgetInstance,
  UnknownWidgetType,
  UnresolvedWidgetCode,
  UnresolvedWidgetReason,
  WidgetImplementationRef,
  WidgetType,
  WidgetLayout,
  DatasourceSelector,
  DeviceSelector,
  AnchorSelector,
  AnchorTarget,
  AnchorAggregation,
  DevicesSelector,
  RelatedTraversalSelector,
  SlotSelector,
  UnboundSelector,
  SlotDefinition,
  SlotScope,
  SlotBinding,
  SelectionTarget,
  SelectionCandidate,
  EntityCandidateLister,
  EntityListKind,
  MeasurementSample,
  LocationSample,
  LocationSeries,
  LocationSelection,
  AlarmRow,
  AlarmDeviceType,
  AlarmTypeCount,
  AlarmSeverityCount,
  CommandRow,
  CommandParameter,
  CommandParamDataType,
} from './types';

// The command-delivery lifecycle vocabulary — the ONE terminal set and the ONE
// cancellable set every surface (console panel, command-button widget, preview) reads, so
// a new status can't be learned by some of them and not the others.
//
// Two predicates, because "still in flight" and "still cancellable" are different
// questions with different answers: SENT is in flight but not cancellable. A caller that
// wants a cancel control asks isCancellableCommandStatus; anything else asks
// isTerminalCommandStatus.
//
// Neither SET is re-exported: the predicates are the only questions a consumer of this
// package has, and handing out a set invites a hand-rolled membership check per call site
// — the three-copies problem this module was created to end. They stay exported from
// ./command-status for the sibling test, which asserts their contents against a
// hand-written list.
export {
  COMMAND_STATUSES,
  isTerminalCommandStatus,
  isCancellableCommandStatus,
  type CommandStatus,
} from './command-status';

export {
  effectiveBindings,
  parseBindingManifest,
  resolveWidgetDatasource,
  stripDefaultBindings,
  type ParsedBindingManifest,
} from './bindings';

export {
  resolveContextBindings,
  hasScopedSlots,
  applySelection,
  bindingsWithoutScopedSlots,
  type MemberResolver,
} from './context';

export {
  BIND_PARAM_PREFIX,
  parseBindParams,
  validateBinds,
  bindsToSelection,
  selectionToBinds,
  withBindParams,
  bindParamsOf,
  type BindCheck,
  type BindParamIssue,
  type ParsedBinds,
} from './deep-link';

export {
  migrateToSlots,
  bindWidgetSlot,
  clearWidgetDatasource,
  pruneSlots,
  widgetBinding,
  widgetSlotName,
  sameBinding,
  resolveConcrete,
  setSlotScope,
  anchorSlotNames,
  type ConcreteSelector,
} from './slots';

// The context/entity-selector's candidate resolver + the injected entity lister that backs
// a root context-selector (ADR-039 selection amendment).
export { resolveSlotCandidates } from './candidates';
export { createEntityLister } from './entity-lister';

export {
  WIDGET_TYPES,
  UNKNOWN_WIDGET_TYPE,
  CURRENT_SCHEMA_VERSION,
  PLACEMENT_ANCHORS,
  PLACEMENT_MODES,
  RESERVED_PLACEMENT_MODES,
  THEME_COLOR_TOKENS,
  SPACE_TOKEN_PX,
  FRAME_VARIANTS,
  FRAME_RADII,
  FRAME_BORDER_WIDTHS,
  FRAME_HEADERS,
} from './types';
export {
  CANVAS_DENSITIES,
  DEFAULT_CANVAS_DENSITY,
  DENSITY_SCALE,
  isCanvasDensity,
  type CanvasDensity,
} from './types';
export { sameLabel } from './same-label';

export { ConnectionTracker, OFFLINE_AFTER_MS, type ConnectionHandle, type HubConnectionState } from './connection';

export {
  DashboardHub,
  type DashboardHubConfig,
  type DeviceResolver,
  type WidgetStreamSink,
  type WidgetDataSource,
  type AlarmSubscription,
  type AlarmSnapshot,
  type AlarmStreamSink,
  type CommandSubscription,
  type CommandSnapshot,
  type CommandStreamSink,
  type CommandDispatch,
  type LocationSubscription,
  type LocationSnapshot,
  type LocationStreamSink,
  type WidgetActions,
} from './hub';

export {
  SyntheticDataSource,
  SYNTHETIC_GENERATORS,
  type SyntheticGenerator,
  type SyntheticDataSourceConfig,
} from './synthetic';

// Playback of a recorded run through the same data-source contract: the recording format and
// its fail-closed parser, the replay clock, the source itself and the recorded resolver/lister.
export {
  RECORDING_FORMAT_VERSION,
  RECORDING_FORMAT_VERSIONS,
  RECORDING_KIND,
  RecordingFormatError,
  parseBoardRecording,
  recordedWallTimeMs,
  recordingBounds,
  simulationScaleAt,
  type BoardRecording,
  type RecordedAlarm,
  type RecordedAlarmEvent,
  type RecordedAlarmSnapshot,
  type RecordedChannels,
  type RecordedChapter,
  type RecordedClockSegment,
  type RecordedDevice,
  type RecordedLocationColumns,
  type RecordedMeasurementColumns,
} from './recording';
export {
  PLAYBACK_RATES,
  RECORDED_HISTORY_WINDOW_MS,
  NotInRecordingError,
  RecordedClock,
  RecordedDataSource,
  createRecordedClock,
  createRecordedLister,
  createRecordedResolver,
  retimeShiftMs,
  type ClockEvent,
  type ClockTicker,
  type PlaybackRate,
  type RecordedChannel,
  type RecordedSiteOptions,
  type RecordedSourceOptions,
} from './recorded';

export {
  parseDashboardDefinition,
  type ParseDefinitionOptions,
  parseSlotBinding,
  serializeDefinition,
  storedDefinition,
  isDirty,
  withoutConversionMark,
  generateWidgetId,
  canScopeSlot,
  DashboardDefinitionError,
  BASE_BREAKPOINT,
} from './definition';

// Pure canvas-editor transforms (move/resize/delete/reorder/add/retitle). The
// authoring host (console, /dash) wires these to its drag/resize + save UI.
export {
  basePlacement,
  clampSpan,
  moveWidget,
  resizeWidget,
  setWidgetPlacement,
  deleteWidget,
  setTitle,
  setCanvasGrid,
  setCanvasSizing,
  setCanvasDensity,
  updateWidget,
  addWidget,
  canHostRelative,
  fitToLimits,
  formatSpan,
  OFFSET_STEP,
  OFFSET_STEP_FINE,
  placeOnGrid,
  placeRelative,
  setRelativeAnchor,
  setRelativeOffset,
  snapOffset,
  gridPlacementToPx,
  pxToGridPlacement,
  type EditOptions,
  type PixelRect,
  type RelativeBounds,
  type GridGeometry,
} from './editor-model';

// The editor's undo/redo history and its duplicate / copy / paste transforms. Editor-only: each
// lives in its own module, which nothing a viewer renders imports, so a viewer bundle drops them.
export {
  DEFAULT_HISTORY_LIMIT,
  canRedo,
  canUndo,
  commitEdit,
  createEditHistory,
  rebaseHistory,
  redoEdit,
  sealHistory,
  undoEdit,
  type CommitOptions,
  type EditHistory,
  type HistoryEntry,
  type Step,
} from './editor-history';
export {
  WIDGET_CLIPBOARD_FORMAT,
  WIDGET_CLIPBOARD_VERSION,
  copyWidget,
  duplicateWidget,
  parseWidgetClipboard,
  pasteWidgets,
  serializeWidgetClipboard,
  type ClipboardOrigin,
  type PasteResult,
  type RefusedWidget,
  type UnboundPaste,
  type WidgetClipboard,
} from './editor-clipboard';

// The device-management-backed resolver the Hub injects, and the event-management
// history seeder the renderer backfills charts with — the runtime's coupling to the
// platform's GraphQL, kept out of the presentational widget layer.
export { createDeviceResolver } from './resolver';
export type { MeasurementBucket } from './queries';
export { ANCHOR_PAGE_SIZE } from './resolver';
export type {
  MemberBucketCriteria,
  MemberBucketRead,
  MemberRef,
  MemberRoster,
  MemberSeed,
  MemberSeedPlan,
  MemberSink,
  SeedFailure,
} from './members';
export { defaultHistoryWindow, fetchWidgetHistory, historyWindowFor, type HistoryWindow } from './history';

// The board's time range: a span, the presets a viewer picks from, pause, and the deep link.
export {
  DEFAULT_TIME_RANGE_SECONDS,
  MAX_SEED_BUCKETS,
  MAX_TIME_RANGE_SECONDS,
  MIN_TIME_RANGE_SECONDS,
  TIME_RANGE_PRESETS,
  bucketSecondsFor,
  initialTimeRange,
  isValidSpanSeconds,
  parsePausedParam,
  parseRangeParam,
  pauseRange,
  presetIdOf,
  rangeEnd,
  rangeWindow,
  resumeRange,
  seedBucketCount,
  setBoardTimeRange,
  spanLabel,
  timeRangeFromSearch,
  withSpan,
  type RangeWindow,
  type TimeRangePresetId,
  type TimeRangeState,
} from './time-range';

// The portable widget vocabulary — React-free, shared by the definition parser, the headless
// validators, @devicechain/widget-sdk and the built-in widget package.
export type {
  AggregateResult,
  AggregateSpan,
  AggregateTrendPoint,
  AggregateStreamState,
  AlarmStreamState,
  AlarmTypeTab,
  AlarmTypeTabs,
  CommandStreamState,
  FleetMember,
  FleetState,
  LocationStreamState,
  MeasurementAggregate,
  MeasurementStreamState,
} from './channel-snapshots';
export {
  applyOptionPatch,
  clampNumberOption,
  enumValues,
  numberSpec,
  validateOptions,
  HOST_OPTION_CONTROLS,
  type HostOptionControl,
  type BooleanOptionSpec,
  type DefaultsOf,
  type EnumOptionSpec,
  type ListItemFieldSpec,
  type ListOptionSpec,
  type GreaterThanConstraint,
  type NumberOptionSpec,
  type OptionConstraint,
  type OptionIssue,
  type OptionIssueCode,
  type OptionKeysOfKind,
  type OptionSpec,
  type OptionsOf,
  type RequiredOptionKeys,
  type SpecValue,
  type StringOptionSpec,
  type ValidateOptionsContext,
  type WidgetOptionSpecs,
} from './option-schema';
export {
  SDK_API_VERSION,
  defineWidgetDescriptor,
  isBuiltinWidgetId,
  isNamespacedWidgetId,
  type AckOptionRef,
  type BuiltinWidgetId,
  type ChannelBindingPolicy,
  type ChannelSnapshot,
  type ConstBinding,
  type DatasourcePolicy,
  type DatasourcePolicyFor,
  type DatasourceSelectorKind,
  type InteractionsFor,
  type NamespacedWidgetId,
  type NumberOptionRef,
  type StringOptionRef,
  type WidgetChannel,
  type WidgetDescriptor,
  type WidgetDisplay,
  type WidgetId,
  type WidgetInteractionDeclaration,
  FALLBACK_DEFAULT_SPAN,
  pinnedAxes,
  type WidgetLayoutSpec,
  type WidgetResizeMode,
  type WidgetSpan,
} from './widget-descriptor';
export {
  WidgetCatalogError,
  collectCatalogIssues,
  createWidgetCatalog,
  describeIssues,
  validateWidgetDescriptor,
  type DescriptorIssue,
  type WidgetCatalog,
} from './widget-catalog';

export {
  isBuiltinWidget,
  isResolvedWidget,
  parseWidgetRef,
  checkWidgetCompatibility,
  widgetVersionNotice,
  unresolvedWidgets,
  type UnresolvedWidgetReport,
} from './widget-compat';

// The square-cell grid's geometry: lengths, cell size, where a placement lands, collisions, and
// the effective frame. The renderers and the editor read it; so does the lint.
export {
  DEFAULT_GRID,
  PHONE_MIN_COLUMNS,
  activeBreakpoint,
  anchorFraction,
  breakpointColumns,
  breakpointMinWidth,
  cellSizeAt,
  containerWidthFor,
  firstFreeSlot,
  gridFootprintsOverlap,
  gridLengthPx,
  gridRect,
  gutterPx,
  isSpaceToken,
  layoutBreakpoints,
  layoutRects,
  paddingPx,
  pushDown,
  pushAside,
  relativeRect,
  resolveFrame,
  resolvePlacement,
  spanPx,
  type Gutter,
  type Insets,
  type PlacedGrid,
  type Rect,
} from './grid';
// Adaptive columns and reflow: the column count a container width gets, the deterministic pour of
// the base layout into fewer columns, and the layout a renderer draws for a width.
export {
  effectiveLayout,
  layoutShape,
  liveColumns,
  reflowGrid,
  STACK_BELOW_WIDTH,
  type EffectiveLayout,
  type LayoutShape,
  type ReflowItem,
} from './reflow';
export { LEGACY_REFERENCE_WIDTH } from './legacy-grid';

// The layout lint: placement against what each widget can show. Widget knowledge (span
// limits) is injected, so this package stays free of `widgets`.
export {
  blockingLayoutFindings,
  lintLayout,
  PUBLISH_BLOCKING_LAYOUT_CODES,
  spanLimitsOfLayout,
  type LayoutFinding,
  type LayoutLintCode,
  type LayoutLintOptions,
  type LayoutLintSeverity,
  type SpanLimits,
} from './layout-lint';
export { type EventWindow, eventWindowBounds } from './event-window';

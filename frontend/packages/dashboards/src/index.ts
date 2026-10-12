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
  Canvas,
  CanvasBackground,
  CanvasGrid,
  CanvasSizing,
  Breakpoints,
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
  WidgetBox,
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

export { WIDGET_TYPES, UNKNOWN_WIDGET_TYPE } from './types';

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
  type ClockEvent,
  type ClockTicker,
  type PlaybackRate,
  type RecordedChannel,
  type RecordedSiteOptions,
} from './recorded';

export {
  parseDashboardDefinition,
  type ParseDefinitionOptions,
  parseSlotBinding,
  serializeDefinition,
  storedDefinition,
  isDirty,
  resolveWidgetBox,
  activeBreakpoint,
  generateWidgetId,
  canScopeSlot,
  DashboardDefinitionError,
  BASE_BREAKPOINT,
} from './definition';

// Pure canvas-editor transforms (move/resize/delete/reorder/add/retitle). The
// authoring host (console, /dash) wires these to its drag/resize + save UI.
export {
  baseBox,
  setWidgetBox,
  deleteWidget,
  bringToFront,
  setTitle,
  setCanvasGrid,
  setCanvasSizing,
  updateWidget,
  addWidget,
  gridBoxToPx,
  pxToGridBox,
  type PixelRect,
  type GridGeometry,
} from './editor-model';

// The device-management-backed resolver the Hub injects, and the event-management
// history seeder the renderer backfills charts with — the runtime's coupling to the
// platform's GraphQL, kept out of the presentational widget layer.
export { createDeviceResolver } from './resolver';
export { defaultHistoryWindow, fetchWidgetHistory, type HistoryWindow } from './history';

// The portable widget vocabulary — React-free, shared by the definition parser, the headless
// validators, @devicechain/widget-sdk and the built-in widget package.
export type {
  AlarmStreamState,
  CommandStreamState,
  LocationStreamState,
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

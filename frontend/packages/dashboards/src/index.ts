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

// The command-delivery lifecycle vocabulary — the ONE terminal set every surface
// (console panel, command-button widget, preview) reads, so a new status can't be
// learned by some of them and not the others.
export {
  COMMAND_STATUSES,
  TERMINAL_COMMAND_STATUSES,
  isTerminalCommandStatus,
  type CommandStatus,
} from './command-status';

export {
  effectiveBindings,
  parseBindingManifest,
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

export { WIDGET_TYPES } from './types';

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

export {
  parseDashboardDefinition,
  parseSlotBinding,
  serializeDefinition,
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

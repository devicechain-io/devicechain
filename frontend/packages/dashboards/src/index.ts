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
  EntityFromStateSelector,
  SlotSelector,
  SlotDefinition,
  SlotBinding,
  MeasurementSample,
  AlarmRow,
  CommandRow,
  CommandParameter,
  CommandParamDataType,
} from './types';

export { effectiveBindings, parseBindingManifest, stripDefaultBindings } from './bindings';

export {
  migrateToSlots,
  bindWidgetSlot,
  clearWidgetDatasource,
  pruneSlots,
  widgetBinding,
  widgetSlotName,
  sameBinding,
  resolveConcrete,
  type ConcreteSelector,
} from './slots';

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
  updateWidget,
  addWidget,
  pxToCellBox,
  type PixelRect,
} from './editor-model';

// The device-management-backed resolver the Hub injects, and the event-management
// history seeder the renderer backfills charts with — the runtime's coupling to the
// platform's GraphQL, kept out of the presentational widget layer.
export { createDeviceResolver } from './resolver';
export { defaultHistoryWindow, fetchWidgetHistory, type HistoryWindow } from './history';

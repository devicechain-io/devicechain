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
  MeasurementSample,
} from './types';

export {
  DashboardHub,
  type DashboardHubConfig,
  type DeviceResolver,
  type WidgetStreamSink,
} from './hub';

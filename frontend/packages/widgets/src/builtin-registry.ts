// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The built-in widgets as registrations. Each widget module declares its own registration
// (descriptor + component) through the SAME public `defineWidget` a third-party package uses
// -- there is no privileged internal path and no id-keyed component table; this module only
// lists them, in the catalog's order.
//
// These are the RUNTIME registrations: their descriptors carry no option `doc`. The package
// publishes them as `builtinWidgetDefinitionsWithoutDocs`; its `builtinWidgetDefinitions` and
// `createBuiltinWidgetRegistry` are the documented forms (documented-descriptors.ts).

import { createWidgetRegistry, type WidgetRegistry } from '@devicechain/widget-sdk';

import { barChartWidget, fleetCountWidget, fleetTableWidget, pieChartWidget, statCardWidget } from './widgets/aggregate-lazy';
import { alarmCountWidget, alarmSeverityWidget, alarmTableWidget } from './widgets/alarm-lazy';
import { commandButtonWidget, commandSliderWidget, commandToggleWidget } from './widgets/control-lazy';
import { historyTableWidget } from './widgets/history-lazy';
import { deviceStatusWidget, onlineDoughnutWidget } from './widgets/presence-lazy';
import { entitySelectorWidget } from './widgets/selection-lazy';
import { gaugeWidget } from './widgets/gauge';
import { imageWidget } from './widgets/image';
import { labelWidget } from './widgets/label';
import { latestCardWidget } from './widgets/latest-card';
import {
  batteryWidget,
  compassWidget,
  levelGaugeWidget,
  linearGaugeWidget,
  progressRingWidget,
  signalBarsWidget,
  statusLightWidget,
} from './widgets/level-lazy';
import { mapWidget } from './widgets/map';
import { tableWidget } from './widgets/table-lazy';
import { timeSeriesChartWidget } from './widgets/time-series-chart';

export const builtinWidgetDefinitions = [
  timeSeriesChartWidget,
  latestCardWidget,
  gaugeWidget,
  tableWidget,
  labelWidget,
  imageWidget,
  alarmTableWidget,
  alarmCountWidget,
  alarmSeverityWidget,
  commandButtonWidget,
  entitySelectorWidget,
  mapWidget,
  linearGaugeWidget,
  levelGaugeWidget,
  batteryWidget,
  statusLightWidget,
  signalBarsWidget,
  compassWidget,
  progressRingWidget,
  commandSliderWidget,
  commandToggleWidget,
  statCardWidget,
  barChartWidget,
  pieChartWidget,
  fleetTableWidget,
  fleetCountWidget,
  deviceStatusWidget,
  onlineDoughnutWidget,
  historyTableWidget,
] as const;

// A fresh, sealed registry holding exactly the built-ins. Every call returns an independent
// instance; a host composing extensions passes `[...builtinWidgetDefinitions, theirs]` to
// createWidgetRegistry from @devicechain/widget-sdk instead.
export function createBuiltinWidgetRegistry(): WidgetRegistry {
  return createWidgetRegistry(builtinWidgetDefinitions);
}

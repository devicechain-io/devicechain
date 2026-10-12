// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The built-in widgets as registrations. Each widget module declares its own registration
// (descriptor + component) through the SAME public `defineWidget` a third-party package uses
// -- there is no privileged internal path and no id-keyed component table; this module only
// lists them, in the catalog's order.

import { createWidgetRegistry, type WidgetRegistry } from '@devicechain/widget-sdk';

import { alarmCountWidget } from './widgets/alarm-count';
import { alarmTableWidget } from './widgets/alarm-table';
import { commandButtonWidget } from './widgets/command-button';
import { entitySelectorWidget } from './widgets/entity-selector';
import { gaugeWidget } from './widgets/gauge';
import { imageWidget } from './widgets/image';
import { labelWidget } from './widgets/label';
import { latestCardWidget } from './widgets/latest-card';
import { mapWidget } from './widgets/map';
import { tableWidget } from './widgets/table';
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
  commandButtonWidget,
  entitySelectorWidget,
  mapWidget,
] as const;

// A fresh, sealed registry holding exactly the built-ins. Every call returns an independent
// instance; a host composing extensions passes `[...builtinWidgetDefinitions, theirs]` to
// createWidgetRegistry from @devicechain/widget-sdk instead.
export function createBuiltinWidgetRegistry(): WidgetRegistry {
  return createWidgetRegistry(builtinWidgetDefinitions);
}

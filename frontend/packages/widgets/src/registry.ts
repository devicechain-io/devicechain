// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget registries + the channel classifier. A renderer (ConnectedWidget) looks a
// widget up by `widget.type`, but FIRST reads WIDGET_CHANNEL to decide which data
// channel — and therefore which registry + hook — binds it: measurement widgets stream
// telemetry samples; alarm widgets consume the raised-alarm surface. Custom widgets
// extend these same maps.

import type { WidgetType } from '@devicechain/dashboards';

import type { AlarmStreamState } from './hooks';
import type { WidgetComponent } from './widget';
import { AlarmCount } from './widgets/alarm-count';
import { AlarmTable } from './widgets/alarm-table';
import { Gauge } from './widgets/gauge';
import { Image } from './widgets/image';
import { Label } from './widgets/label';
import { LatestCard } from './widgets/latest-card';
import { Table } from './widgets/table';
import { TimeSeriesChart } from './widgets/time-series-chart';

// A widget's data channel: which hub subscription + registry the renderer binds it
// through. WIDGET_CHANNEL is the SINGLE source of truth — `satisfies` keeps it
// exhaustive over WidgetType (a new type won't compile without a channel), and the
// per-channel type subsets are DERIVED from it, so the channel map and the two
// registries can't drift out of sync.
export type WidgetChannel = 'measurement' | 'alarm';

export const WIDGET_CHANNEL = {
  'timeseries-chart': 'measurement',
  'latest-card': 'measurement',
  gauge: 'measurement',
  table: 'measurement',
  label: 'measurement',
  image: 'measurement',
  'alarm-table': 'alarm',
  'alarm-count': 'alarm',
} as const satisfies Record<WidgetType, WidgetChannel>;

// The widget types on each channel, derived from WIDGET_CHANNEL so the registries below
// stay exhaustive without a hand-maintained parallel union.
type AlarmWidgetType = { [K in WidgetType]: (typeof WIDGET_CHANNEL)[K] extends 'alarm' ? K : never }[WidgetType];
type MeasurementWidgetType = Exclude<WidgetType, AlarmWidgetType>;

export const WIDGET_REGISTRY: Record<MeasurementWidgetType, WidgetComponent> = {
  'timeseries-chart': TimeSeriesChart,
  'latest-card': LatestCard,
  gauge: Gauge,
  table: Table,
  label: Label,
  image: Image,
};

export const ALARM_WIDGET_REGISTRY: Record<AlarmWidgetType, WidgetComponent<AlarmStreamState>> = {
  'alarm-table': AlarmTable,
  'alarm-count': AlarmCount,
};

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
// through. Adding a WidgetType forces an entry here (the Record is exhaustive), so a
// new type can never silently fall through the renderer's dispatch.
export type WidgetChannel = 'measurement' | 'alarm';

export const WIDGET_CHANNEL: Record<WidgetType, WidgetChannel> = {
  'timeseries-chart': 'measurement',
  'latest-card': 'measurement',
  gauge: 'measurement',
  table: 'measurement',
  label: 'measurement',
  image: 'measurement',
  'alarm-table': 'alarm',
  'alarm-count': 'alarm',
};

// The alarm-channel widget types. Kept as an explicit union so both registries stay
// exhaustive: alarm types are registered in ALARM_WIDGET_REGISTRY, and every OTHER
// type (Exclude) must appear in WIDGET_REGISTRY.
type AlarmWidgetType = 'alarm-table' | 'alarm-count';
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

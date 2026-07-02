// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// WIDGET_REGISTRY maps each dashboard WidgetType to its component. A renderer
// (the standalone app / console) looks a widget up by `widget.type` and renders
// it with { widget, hub }. Custom widgets extend this same map.

import type { WidgetType } from '@devicechain/dashboards';

import type { WidgetComponent } from './widget';
import { Gauge } from './widgets/gauge';
import { Image } from './widgets/image';
import { Label } from './widgets/label';
import { LatestCard } from './widgets/latest-card';
import { Table } from './widgets/table';
import { TimeSeriesChart } from './widgets/time-series-chart';

export const WIDGET_REGISTRY: Record<WidgetType, WidgetComponent> = {
  'timeseries-chart': TimeSeriesChart,
  'latest-card': LatestCard,
  gauge: Gauge,
  table: Table,
  label: Label,
  image: Image,
};

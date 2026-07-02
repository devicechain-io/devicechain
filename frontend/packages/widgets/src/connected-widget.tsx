// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ConnectedWidget is the one place the imperative DashboardHub meets the pure
// widget contract: it subscribes a widget's datasource (windowed per its options),
// looks the widget up by type, and renders it with resolved data. A dashboard
// renderer maps over its widgets rendering one ConnectedWidget each; the widgets
// themselves stay pure functions of (widget, data).

import type { DashboardHub, WidgetInstance } from '@devicechain/dashboards';

import { useMeasurementStream } from './hooks';
import { WIDGET_REGISTRY } from './registry';
import { optNumber } from './widget';

export interface ConnectedWidgetProps {
  widget: WidgetInstance;
  hub: DashboardHub;
}

export function ConnectedWidget({ widget, hub }: ConnectedWidgetProps) {
  const data = useMeasurementStream(hub, widget.datasource, {
    window: optNumber(widget.options, 'window'),
  });

  const Component = WIDGET_REGISTRY[widget.type];
  if (!Component) return null; // unknown type in a hand-edited definition
  return <Component widget={widget} data={data} />;
}

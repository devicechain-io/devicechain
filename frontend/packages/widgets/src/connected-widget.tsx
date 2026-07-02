// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ConnectedWidget is the one place the imperative DashboardHub meets the pure
// widget contract: it subscribes a widget's datasource (windowed per its options),
// looks the widget up by type, and renders it with resolved data. A dashboard
// renderer maps over its widgets rendering one ConnectedWidget each; the widgets
// themselves stay pure functions of (widget, data).

import type { DashboardHub, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';

import { useMeasurementStream } from './hooks';
import { WIDGET_REGISTRY } from './registry';
import { optNumber } from './widget';

export interface ConnectedWidgetProps {
  widget: WidgetInstance;
  hub: DashboardHub;
  // Optional history backfill for this widget (from bucketedMeasurements), seeded
  // under the live tail so a chart isn't blank until the first live sample.
  initialSamples?: MeasurementSample[];
}

export function ConnectedWidget({ widget, hub, initialSamples }: ConnectedWidgetProps) {
  const data = useMeasurementStream(hub, widget.datasource, {
    window: optNumber(widget.options, 'window'),
    initialSamples,
  });

  const Component = WIDGET_REGISTRY[widget.type];
  if (!Component) return null; // unknown type in a hand-edited definition
  return <Component widget={widget} data={data} />;
}

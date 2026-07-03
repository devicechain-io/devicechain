// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ConnectedWidget is the one place the imperative DashboardHub meets the pure
// widget contract: it subscribes a widget's datasource (windowed per its options),
// looks the widget up by type, and renders it with resolved data. A dashboard
// renderer maps over its widgets rendering one ConnectedWidget each; the widgets
// themselves stay pure functions of (widget, data).

import type { MeasurementSample, WidgetDataSource, WidgetInstance } from '@devicechain/dashboards';

import { WidgetFrame } from './frame';
import { useMeasurementStream } from './hooks';
import { WIDGET_REGISTRY } from './registry';
import { optNumber, optString } from './widget';

export interface ConnectedWidgetProps {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  // Optional history backfill for this widget (from bucketedMeasurements), seeded
  // under the live tail so a chart isn't blank until the first live sample.
  initialSamples?: MeasurementSample[];
}

export function ConnectedWidget({ widget, hub, initialSamples }: ConnectedWidgetProps) {
  const data = useMeasurementStream(hub, widget.datasource, {
    window: optNumber(widget.options, 'window'),
    initialSamples,
  });

  // A resolution/socket error surfaces as a muted error state rather than a blank
  // pane. label/image carry no datasource so they never error here.
  if (data.error) {
    return (
      <WidgetFrame title={optString(widget.options, 'title')}>
        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            gap: 4,
            width: '100%',
            height: '100%',
            padding: 12,
            boxSizing: 'border-box',
            textAlign: 'center',
            color: 'hsl(var(--muted-foreground))',
          }}
        >
          <div style={{ fontSize: 20, lineHeight: 1 }}>⚠</div>
          <div style={{ fontSize: 13, fontWeight: 600 }}>Data unavailable</div>
          <div style={{ fontSize: 11, opacity: 0.8, wordBreak: 'break-word' }}>{String(data.error)}</div>
        </div>
      </WidgetFrame>
    );
  }

  const Component = WIDGET_REGISTRY[widget.type];
  if (!Component) return null; // unknown type in a hand-edited definition
  return <Component widget={widget} data={data} />;
}

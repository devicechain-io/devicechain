// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ConnectedWidget is the one place the imperative DashboardHub meets the pure widget
// contract. It dispatches on the widget's data CHANNEL (WIDGET_CHANNEL): a measurement
// widget subscribes its datasource (windowed per its options); an alarm widget
// subscribes its scope+filters through the hub's alarm channel. Either way it resolves
// the data and renders the pure widget with { widget, data }. A dashboard renderer maps
// over its widgets rendering one ConnectedWidget each.

import type {
  AlarmSubscription,
  MeasurementSample,
  WidgetDataSource,
  WidgetInstance,
} from '@devicechain/dashboards';
import type { ReactNode } from 'react';

import { WidgetFrame } from './frame';
import { useAlarmStream, useMeasurementStream } from './hooks';
import { ALARM_WIDGET_REGISTRY, WIDGET_CHANNEL, WIDGET_REGISTRY } from './registry';
import { optNumber, optString } from './widget';

// Rows an alarm table shows before scrolling; also the default page size the count
// query reads (the count itself uses the server total, independent of this).
const DEFAULT_ALARM_ROWS = 50;

export interface ConnectedWidgetProps {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  // Optional history backfill for this widget (from bucketedMeasurements), seeded
  // under the live tail so a chart isn't blank until the first live sample. Ignored by
  // alarm widgets (their channel reconciles from a query, not a history seed).
  initialSamples?: MeasurementSample[];
}

export function ConnectedWidget({ widget, hub, initialSamples }: ConnectedWidgetProps) {
  if (WIDGET_CHANNEL[widget.type] === 'alarm') {
    return <AlarmConnectedWidget widget={widget} hub={hub} />;
  }
  return <MeasurementConnectedWidget widget={widget} hub={hub} initialSamples={initialSamples} />;
}

function MeasurementConnectedWidget({ widget, hub, initialSamples }: ConnectedWidgetProps) {
  const data = useMeasurementStream(hub, widget.datasource, {
    window: optNumber(widget.options, 'window'),
    initialSamples,
  });

  if (data.error) return <WidgetErrorFrame widget={widget} error={data.error} />;

  const Component = WIDGET_REGISTRY[widget.type as keyof typeof WIDGET_REGISTRY];
  if (!Component) return null; // unknown/misclassified type in a hand-edited definition
  return <Component widget={widget} data={data} />;
}

function AlarmConnectedWidget({ widget, hub }: { widget: WidgetInstance; hub: WidgetDataSource }) {
  const data = useAlarmStream(hub, alarmSubscription(widget));

  if (data.error) return <WidgetErrorFrame widget={widget} error={data.error} />;

  const Component = ALARM_WIDGET_REGISTRY[widget.type as keyof typeof ALARM_WIDGET_REGISTRY];
  if (!Component) return null;
  return <Component widget={widget} data={data} />;
}

// alarmSubscription reads an alarm widget's scope+filters from its definition: the
// datasource (undefined = tenant-wide) and the state/severity options. A count needs
// only the total, so it reads a minimal page; a table reads its configured row cap.
function alarmSubscription(widget: WidgetInstance): AlarmSubscription {
  const pageSize =
    widget.type === 'alarm-count' ? 1 : optNumber(widget.options, 'maxRows') ?? DEFAULT_ALARM_ROWS;
  return {
    datasource: widget.datasource,
    state: optString(widget.options, 'state'),
    severity: optString(widget.options, 'severity'),
    pageSize,
  };
}

// A resolution/socket error surfaces as a muted error state rather than a blank pane.
// Shared by both the measurement and alarm channels.
function WidgetErrorFrame({ widget, error }: { widget: WidgetInstance; error: unknown }): ReactNode {
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
        <div style={{ fontSize: 11, opacity: 0.8, wordBreak: 'break-word' }}>{String(error)}</div>
      </div>
    </WidgetFrame>
  );
}

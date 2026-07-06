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
  CommandSubscription,
  MeasurementSample,
  WidgetActions,
  WidgetDataSource,
  WidgetInstance,
} from '@devicechain/dashboards';
import type { ReactNode } from 'react';

import { WidgetFrame } from './frame';
import { useAlarmStream, useCommandStream, useMeasurementStream } from './hooks';
import {
  ALARM_WIDGET_REGISTRY,
  CONTROL_WIDGET_REGISTRY,
  WIDGET_CHANNEL,
  WIDGET_REGISTRY,
} from './registry';
import { optNumber, optString } from './widget';

// Rows an alarm table shows before scrolling; also the default page size the count
// query reads (the count itself uses the server total, independent of this).
const DEFAULT_ALARM_ROWS = 50;

// Recent commands a command-button shows in its history before scrolling.
const DEFAULT_COMMAND_ROWS = 20;

export interface ConnectedWidgetProps {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  // The action seam (writes), threaded to acting widgets. Undefined = read-only.
  actions?: WidgetActions;
  // Optional history backfill for this widget (from bucketedMeasurements), seeded
  // under the live tail so a chart isn't blank until the first live sample. Ignored by
  // alarm widgets (their channel reconciles from a query, not a history seed).
  initialSamples?: MeasurementSample[];
}

export function ConnectedWidget({ widget, hub, actions, initialSamples }: ConnectedWidgetProps) {
  const channel = WIDGET_CHANNEL[widget.type];
  if (channel === 'alarm') {
    return <AlarmConnectedWidget widget={widget} hub={hub} actions={actions} />;
  }
  if (channel === 'control') {
    return <ControlConnectedWidget widget={widget} hub={hub} actions={actions} />;
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

function AlarmConnectedWidget({
  widget,
  hub,
  actions,
}: {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  actions?: WidgetActions;
}) {
  const data = useAlarmStream(hub, alarmSubscription(widget));

  // Show the error pane ONLY when there's nothing to display (the initial load failed).
  // Once a snapshot has loaded, a later transient poll/query error keeps the last-good
  // rows on screen — the channel re-queries every 30s and self-heals, so blanking a
  // populated table on one blip is the exact blink the console page avoids.
  if (data.error && data.alarms.length === 0 && data.total === 0) {
    return <WidgetErrorFrame widget={widget} error={data.error} />;
  }

  const Component = ALARM_WIDGET_REGISTRY[widget.type as keyof typeof ALARM_WIDGET_REGISTRY];
  if (!Component) return null;
  return <Component widget={widget} data={data} actions={actions} />;
}

function ControlConnectedWidget({
  widget,
  hub,
  actions,
}: {
  widget: WidgetInstance;
  hub: WidgetDataSource;
  actions?: WidgetActions;
}) {
  const data = useCommandStream(hub, commandSubscription(widget));

  // Like the alarm channel, only blank to the error pane when the initial load failed
  // (nothing to show); a transient poll error after a good snapshot keeps the last-good
  // history on screen — the channel re-polls and self-heals.
  if (data.error && data.commands.length === 0 && data.total === 0) {
    return <WidgetErrorFrame widget={widget} error={data.error} />;
  }

  const Component = CONTROL_WIDGET_REGISTRY[widget.type as keyof typeof CONTROL_WIDGET_REGISTRY];
  if (!Component) return null;
  return <Component widget={widget} data={data} actions={actions} />;
}

// commandSubscription reads a command widget's scope (the target device) from its
// definition; the command name + parameter schema live in the widget's options (baked by
// the console), not the subscription. pageSize bounds the recent-command history shown.
function commandSubscription(widget: WidgetInstance): CommandSubscription {
  return {
    datasource: widget.datasource,
    pageSize: Math.max(1, optNumber(widget.options, 'maxRows') ?? DEFAULT_COMMAND_ROWS),
  };
}

// alarmSubscription reads an alarm widget's scope+filters from its definition: the
// datasource (undefined = tenant-wide) plus the state/severity/acknowledged filters. A
// count reports the server total (independent of pageSize) but still reads a page so its
// worst-severity accent samples the current alarms rather than only the single newest;
// a table reads its configured, clamped row cap.
function alarmSubscription(widget: WidgetInstance): AlarmSubscription {
  const pageSize =
    widget.type === 'alarm-count'
      ? DEFAULT_ALARM_ROWS
      : Math.max(1, optNumber(widget.options, 'maxRows') ?? DEFAULT_ALARM_ROWS);
  return {
    datasource: widget.datasource,
    state: optString(widget.options, 'state'),
    severity: optString(widget.options, 'severity'),
    acknowledged: parseAcknowledged(optString(widget.options, 'acknowledged')),
    pageSize,
  };
}

// The acknowledged filter is stored as 'true' / 'false' (or absent = any).
function parseAcknowledged(value: string | undefined): boolean | undefined {
  if (value === 'true') return true;
  if (value === 'false') return false;
  return undefined;
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

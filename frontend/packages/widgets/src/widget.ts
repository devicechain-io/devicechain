// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget contract — the shape every built-in (and every future custom widget)
// implements, plus small helpers for reading a widget's opaque `options` bag and
// picking the measurement a single-value widget should show.

import type { MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import type { ComponentType } from 'react';

import type { MeasurementStreamState } from './hooks';

// A widget is a PURE React component of its definition instance and already-
// resolved live data — it never touches the DashboardHub directly. This is the
// public custom-widget interface: a widget is a function of (widget, data), so it
// renders identically from a live stream, a recorded window, or a test fixture.
// ConnectedWidget binds the hub to this contract and dispatches on the widget's data
// CHANNEL (see WIDGET_CHANNEL): measurement widgets receive a MeasurementStreamState
// (the default), alarm widgets an AlarmStreamState. The data type is a generic so each
// widget declares exactly the shape it consumes.
export interface WidgetProps<D = MeasurementStreamState> {
  widget: WidgetInstance;
  data: D;
}

export type WidgetComponent<D = MeasurementStreamState> = ComponentType<WidgetProps<D>>;

// pickSample chooses the sample a single-value widget (card, gauge) shows: the
// named measurement, or the first available when no name is pinned.
export function pickSample(
  latest: Record<string, MeasurementSample>,
  name: string | undefined,
): MeasurementSample | undefined {
  return name ? latest[name] : Object.values(latest)[0];
}

// --- options helpers ---------------------------------------------------------
// A widget's `options` is opaque JSON (Record<string, unknown>) owned by the
// widget; these read a key defensively, returning undefined on the wrong type
// rather than throwing on a hand-edited definition.

type Options = Record<string, unknown> | undefined;

export function optString(options: Options, key: string): string | undefined {
  const value = options?.[key];
  return typeof value === 'string' ? value : undefined;
}

export function optNumber(options: Options, key: string): number | undefined {
  const value = options?.[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

// primaryMeasurementName is the measurement a single-value widget (card, gauge)
// displays: an explicit `options.measurement`, else the first measurement its
// datasource selects, else undefined (fall back to whatever arrives first).
export function primaryMeasurementName(widget: WidgetInstance): string | undefined {
  const explicit = optString(widget.options, 'measurement');
  if (explicit) return explicit;
  const ds = widget.datasource;
  if (ds && ds.measurements.length > 0) return ds.measurements[0];
  return undefined;
}

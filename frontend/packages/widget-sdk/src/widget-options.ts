// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Small helpers for reading a widget's opaque `options` bag and picking the measurement a
// single-value widget should show. They are the SDK's, not the built-ins': a third-party
// widget reads its own options the same defensive way.

import type { MeasurementSample, WidgetInstance } from '@devicechain/dashboards';

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

// A boolean flag option: true only when explicitly stored true (absent/other → false), so
// an opt-in feature stays off by default and a hand-edited value can't accidentally enable it.
export function optBoolean(options: Options, key: string): boolean {
  return options?.[key] === true;
}

// primaryMeasurementName is the measurement a single-value widget (card, gauge)
// displays: an explicit `options.measurement`, else the first measurement its
// datasource selects, else undefined (fall back to whatever arrives first).
export function primaryMeasurementName(widget: WidgetInstance): string | undefined {
  const explicit = optString(widget.options, 'measurement');
  if (explicit) return explicit;
  const ds = widget.datasource;
  if (ds?.measurements && ds.measurements.length > 0) return ds.measurements[0];
  return undefined;
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a single-device widget shows when it has no reading to draw, and WHY. One decision shared by
// every built-in, so a board does not explain the same absence three ways:
//
//   the slot has no device bound                     -> unbound
//   the device's active profile does not declare it  -> not-reported (it will never report this)
//   otherwise                                        -> no-data (declared, but nothing in the window)
//
// "Declared" is asked of the host only when this renders (useWidgetDeclaredMetrics.ask), so a board
// whose widgets have data pays nothing. While the answer is pending, or when the host cannot give
// one, the declaration is unknown, and unknown is read as declared: the widget says "no data" rather
// than confidently claiming the device never reports it. A reading of the measurement that the
// widget does hold (outside its window) proves the device reports it, whatever the profile says.
//
// It is apart from no-reading.tsx (the level and fleet wrappers) so a widget needing only this,
// the table, does not pull the wrappers' imports into its chunk: that one edge moved part of the
// first download into a chunk of its own in the replay build, about 600 B gzip.

import type { MeasurementSample } from '@devicechain/dashboards';
import { formatAge, formatValue, useWidgetDeclaredMetrics } from '@devicechain/widget-sdk';
import { lastValueHint, WidgetEmptyState, type WidgetEmptyKind } from '@devicechain/widget-sdk/empty-state';
import { useEffect, useState } from 'react';

// How long a widget stays empty before it asks the host what the device's profile declares.
export const ASK_DELAY_MS = 300;

// How long a widget with no reading stays blank before it says why. A board that is loading has no
// reading yet, and "nothing was reported in this range" would be false for the moment it takes the
// first one to arrive. A caller that has already waited (or knows the read has settled) passes 0.
export const SETTLE_MS = 1500;

export function emptyKindFor(input: {
  unbound: boolean;
  measurement: string | undefined;
  declared: readonly string[] | null | undefined;
  // The widget holds a reading of the measurement (outside what it shows): the device reports it.
  reported?: boolean;
}): Extract<WidgetEmptyKind, 'unbound' | 'not-reported' | 'no-data'> {
  if (input.unbound) return 'unbound';
  if (input.reported) return 'no-data';
  if (input.measurement !== undefined && input.declared && !input.declared.includes(input.measurement)) return 'not-reported';
  return 'no-data';
}

// "Last value 21.4 °C · 3 days ago", for a reading that exists but lies outside what the widget shows.
export function lastReadingHint(
  sample: MeasurementSample | undefined,
  options: { unit?: string; precision?: number; now?: number } = {},
): string | undefined {
  if (!sample || sample.value == null) return undefined;
  const value = formatValue(sample.value, options.precision);
  const at = sample.occurredTime ? Date.parse(sample.occurredTime) : NaN;
  if (Number.isNaN(at)) return undefined;
  const age = Math.max(0, Math.floor(((options.now ?? Date.now()) - at) / 1000));
  return lastValueHint(options.unit ? `${value} ${options.unit}` : value, formatAge(age));
}

export interface NoReadingProps {
  // The slot has no device bound.
  unbound: boolean;
  // The measurement the widget was asked for; undefined when it shows whatever arrives.
  measurement: string | undefined;
  // A reading known to exist but outside the window, shown as the hint of a no-data state.
  lastSample?: MeasurementSample;
  unit?: string;
  precision?: number;
  // Replaces the stock no-data explanation (a stat card's "No readings in the last 1h").
  detail?: string;
  // How long to stay blank before explaining (SETTLE_MS when omitted). An unbound slot never waits.
  waitMs?: number;
}

export function NoReading({ unbound, measurement, lastSample, unit, precision, detail, waitMs = SETTLE_MS }: NoReadingProps) {
  const { declared, ask } = useWidgetDeclaredMetrics();
  const [settled, setSettled] = useState(waitMs <= 0);
  useEffect(() => {
    if (settled || unbound) return;
    const timer = setTimeout(() => setSettled(true), waitMs);
    return () => clearTimeout(timer);
  }, [settled, unbound, waitMs]);
  // After a moment, not at once: on a board that is loading, the reading is about to arrive and the
  // lookup would be for nothing.
  useEffect(() => {
    if (unbound || measurement === undefined) return;
    const timer = setTimeout(ask, ASK_DELAY_MS);
    return () => clearTimeout(timer);
  }, [ask, unbound, measurement]);
  if (!unbound && !settled) return <div data-empty-pending="" aria-hidden="true" style={{ width: '100%', height: '100%' }} />;
  const kind = emptyKindFor({ unbound, measurement, declared, reported: lastSample !== undefined });
  const hint = kind === 'no-data' ? lastReadingHint(lastSample, { unit, precision }) : undefined;
  return <WidgetEmptyState kind={kind} measurement={measurement} hint={hint} detail={kind === 'no-data' ? detail : undefined} />;
}


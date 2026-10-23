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
// than confidently claiming the device never reports it.

import type { MeasurementSample } from '@devicechain/dashboards';
import { formatAge, formatValue, useWidgetDeclaredMetrics } from '@devicechain/widget-sdk';
import { lastValueHint, WidgetEmptyState, type WidgetEmptyKind } from '@devicechain/widget-sdk/empty-state';
import { useEffect } from 'react';

import type { LevelReading } from './level-reading';
import { LevelEmpty } from './level-readout';

// How long a widget stays empty before it asks the host what the device's profile declares.
export const ASK_DELAY_MS = 300;

export function emptyKindFor(input: {
  unbound: boolean;
  measurement: string | undefined;
  declared: readonly string[] | null | undefined;
}): Extract<WidgetEmptyKind, 'unbound' | 'not-reported' | 'no-data'> {
  if (input.unbound) return 'unbound';
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
}

export function NoReading({ unbound, measurement, lastSample, unit, precision, detail }: NoReadingProps) {
  const { declared, ask } = useWidgetDeclaredMetrics();
  // After a moment, not at once: on a board that is loading, the reading is about to arrive and the
  // lookup would be for nothing.
  useEffect(() => {
    if (unbound || measurement === undefined) return;
    const timer = setTimeout(ask, ASK_DELAY_MS);
    return () => clearTimeout(timer);
  }, [ask, unbound, measurement]);
  const kind = emptyKindFor({ unbound, measurement, declared });
  const hint = kind === 'no-data' ? lastReadingHint(lastSample, { unit, precision }) : undefined;
  return <WidgetEmptyState kind={kind} measurement={measurement} hint={hint} detail={kind === 'no-data' ? detail : undefined} />;
}

// A whole-widget state the platform or the binding decides (a refusal, an unchosen device or group),
// in the shared look. The widget's own sentence stays as the explanation, so a fleet widget still
// says "Choose a device group", not the single-device wording.
export function Explained({ kind, message }: { kind: 'unbound' | 'forbidden'; message: string }) {
  return <WidgetEmptyState kind={kind} detail={message} />;
}

// The empty state of a level widget: its own configuration problem when it has one (the author's fix
// comes first), else the explained absence of a reading.
export function LevelEmptyFor({ reading, reason }: { reading: LevelReading; reason: string | undefined }) {
  if (reason !== undefined) return <LevelEmpty message={reason} />;
  return <NoReading unbound={reading.unbound} measurement={reading.name} lastSample={reading.sample} unit={reading.unit} precision={reading.precision} />;
}

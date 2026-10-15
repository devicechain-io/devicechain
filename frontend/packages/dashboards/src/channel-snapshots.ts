// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The resolved-data shape each widget channel hands its component. Pure data over the
// hub's row types, so they live beside the portable widget descriptor (which names a
// channel) rather than beside the React hooks that happen to produce them — a headless
// validator, the SDK and the built-in widgets all read the same declarations.

import type { AlarmRow, CommandRow, LocationSample, MeasurementSample } from './types';

export interface MeasurementStreamState {
  // The most recent sample per measurement name (drives cards, gauges, tables).
  latest: Record<string, MeasurementSample>;
  // A rolling, chronological window of recent samples (drives the time chart).
  samples: MeasurementSample[];
  error: unknown | null;
  // Set only on a window that is a time SPAN, when the retention ceiling had to drop samples that
  // were still inside it: the window then covers from this instant (the oldest sample kept), not
  // the whole span. A chart must say so rather than draw a short window under a long span's label.
  truncatedBefore?: string;
}

// The state an alarm widget renders from: the current rows (newest first, capped),
// the total match count (past the page — drives alarm-count), and load/error flags.
export interface AlarmStreamState {
  alarms: AlarmRow[];
  total: number;
  loading: boolean;
  error: unknown | null;
}

// The state a command widget renders from: the resolved target device (null = unbound,
// so it can't issue), the recent commands (newest first, capped) with live status, the
// total count, and load/error flags.
export interface CommandStreamState {
  deviceToken: string | null;
  commands: CommandRow[];
  total: number;
  loading: boolean;
  error: unknown | null;
}

// The state a map widget renders from: the located devices' positions, the device
// tokens the selector resolved to (a never-located device has no position, so the two
// lists differ), whether the viewer was REFUSED, and load/error flags.
//
// 🔴 `forbidden` is a field of its own and not an `error`. `location:read` is
// deliberately outside the read-only viewer baseline, so being refused is the ordinary
// case for an ordinary member — an error state would show an outage where the platform
// is working exactly as designed, and an empty `locations` would blame the devices for
// a fact about the caller.
export interface LocationStreamState {
  locations: LocationSample[];
  deviceTokens: string[];
  forbidden: boolean;
  loading: boolean;
  error: unknown | null;
}

// What a stat widget reads for one measurement over its span: the exact count, minimum, maximum
// and sum of the readings, and the mean (the sum over the count, which is the count-weighted mean
// of the buckets' averages). Never derived from averages: a bucket's average hides its extremes.
export interface MeasurementAggregate {
  count: number;
  min: number;
  max: number;
  sum: number;
  avg: number;
}

// The span an aggregate covers, as the instants the read was asked for and its length.
export interface AggregateSpan {
  startTime: string;
  endTime: string;
  spanSeconds: number;
}

// Where an aggregate read stands. `unavailable` is a settled, explained state -- the source cannot
// compute aggregates (`unsupported`), or the read failed or came back unusable (`failed`) -- and
// the widget says so instead of drawing a number it could not compute.
export type AggregateResult =
  | { status: 'loading' }
  | ({ status: 'ready'; byName: Record<string, MeasurementAggregate> } & AggregateSpan)
  | { status: 'unavailable'; reason: 'unsupported' | 'failed'; message?: string };

// The state an aggregate widget renders from: the aggregates over its span, and the newest LIVE
// reading per measurement (no history seed, so it is a reading and never a bucket's average).
export interface AggregateStreamState {
  aggregates: AggregateResult;
  latest: Record<string, MeasurementSample>;
  error: unknown | null;
}

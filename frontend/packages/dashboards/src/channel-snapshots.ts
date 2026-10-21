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
  // Set only when the live feed was interrupted and the readings it missed have not been read back
  // from stored history yet (or could not be). Each entry is the time of the newest sample known
  // when the feed resumed: nothing is known between it and the next sample, so a chart breaks its
  // line there instead of joining the readings on either side as if the device had reported a
  // straight line. Once a history read made after the interruption lands, the entry goes away and
  // the window holds that history across the outage. Oldest first.
  gaps?: string[];
  // Present only on a widget that reads a whole fleet (its descriptor declares `binding.members`
  // and the mode is on): every member of the anchor with its OWN latest reading per measurement.
  // `latest` above stays keyed by measurement name alone, so two devices reporting the same name
  // collapse into one value there; this is the shape that keeps them apart.
  members?: FleetState;
}

// One device of a fleet, as a fleet widget sees it. `seed` is where its history read stands, and
// is what separates the empty states: a reading is a reading wherever it came from, but the lack
// of one means "still loading" until the seed has settled, and only then "no data in range" (the
// device sent nothing in the span) or "does not report this" (it sent other measurements).
export interface FleetMember {
  token: string;
  // The device's name, when the source knows it; the widget falls back to the token.
  name: string | null;
  // Its newest reading per measurement name, inside the span.
  latest: Record<string, MeasurementSample>;
  seed: 'pending' | 'ready' | 'failed';
  // Every measurement name it reported at least once inside the span.
  observed: string[];
  // Names it reported in the span whose newest reading was not loaded (MemberSeed.unread): "not
  // loaded", never "no data in range".
  unread?: string[];
}

// The members a fleet widget draws. `total` is the size of the anchor; `members` holds at most the
// first page the resolver reads, so `truncated` is how a widget knows to say "showing N of M"
// rather than pass a subset off as the fleet.
export interface FleetState {
  status: 'loading' | 'ready' | 'unbound' | 'forbidden' | 'unsupported';
  members: FleetMember[];
  total: number;
  truncated: boolean;
}

// The state an alarm widget renders from: the current rows (newest first, capped),
// the total match count (past the page — drives alarm-count), and load/error flags.
export interface AlarmStreamState {
  alarms: AlarmRow[];
  total: number;
  loading: boolean;
  error: unknown | null;
  // Present only on an alarm widget that groups by device type (its descriptor's `binding.groupBy`
  // and the mode is on). `alarms`/`total` above are then the SELECTED tab's rows; this carries the
  // tab strip around them.
  typeTabs?: AlarmTypeTabs;
}

// One device type among the active alarms in a widget's scope: how many are active, and the worst
// severity among them (null when none is). `name` is the type's name, null when it has none.
export interface AlarmTypeTab {
  token: string;
  name: string | null;
  active: number;
  worstSeverity: string | null;
}

// The tab strip of an alarm widget that groups by device type. `tabs` is every type present among
// the ACTIVE alarms of the scope, from one summary read (never a read per tab). `selected` is the
// token of the tab whose rows `alarms` holds, or null for All; `select` changes it. `truncated` is
// true when the summary read was capped, so the counts are lower bounds.
export interface AlarmTypeTabs {
  tabs: AlarmTypeTab[];
  selected: string | null;
  select: (token: string | null) => void;
  loading: boolean;
  truncated: boolean;
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

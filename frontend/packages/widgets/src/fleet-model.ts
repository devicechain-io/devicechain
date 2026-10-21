// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet widgets' state, decided apart from the drawing and from the hub: which reading a cell
// shows, why one is empty, how the rows are ordered and paged. Pure, so each rule is a test.
//
// Everything is keyed by DEVICE and measurement together. The measurement channel keys its latest
// values by name alone, so two thermostats reporting "temperature" overwrite each other there;
// nothing in this file ever looks a value up by name without the device beside it.

import {
  bucketSecondsFor,
  type FleetMember,
  type FleetState,
  type HistoryWindow,
  type MeasurementSample,
  type MemberRoster,
  type MemberSeed,
} from '@devicechain/dashboards';

const MINUTE_MS = 60_000;

// The window a fleet's seed reads: whole minutes ending at the last minute boundary at or before
// `at`, in buckets of bucketSecondsFor(span) (a multiple of 60, so the store answers from its
// one-minute summaries). Whole minutes are what let widgets reading the same span on the same
// board share one request per device (the hub keys in-flight reads by window), and what keep the
// window from holding a reading from before the span. The end is the instant just before the
// boundary, because the store's end is inclusive.
export function seedWindow(spanSeconds: number, at: number): HistoryWindow {
  const end = Math.floor(at / MINUTE_MS) * MINUTE_MS;
  const start = Math.ceil((end - spanSeconds * 1000) / MINUTE_MS) * MINUTE_MS;
  return {
    startTime: new Date(start).toISOString(),
    endTime: new Date(end - 1).toISOString(),
    intervalSeconds: bucketSecondsFor(Math.max(60, (end - start) / 1000)),
  };
}

// ---- Building the state ------------------------------------------------------

// What the feed has gathered. `seeds` holds a settled seed per device (`'failed'` for a read that
// did not come back); a device absent from it is still loading. `live` is every device's newest
// sample per measurement name since the board opened.
export interface FleetInputs {
  roster: MemberRoster | null;
  seeds: ReadonlyMap<string, MemberSeed | 'failed'>;
  live: ReadonlyMap<string, ReadonlyMap<string, MeasurementSample>>;
  // What each device type's active profile declares, by type token; null is a settled "nothing to
  // consult". A type absent from it is not looked up yet. Either way its members' declaration is
  // unknown, which cellFor reads as declared.
  declared?: ReadonlyMap<string, readonly string[] | null>;
  // The start of the span (epoch ms): a reading older than this is not "in range".
  since: number;
  forbidden: boolean;
}

function timeOf(sample: MeasurementSample | undefined): number {
  if (!sample?.occurredTime) return Number.NaN;
  return Date.parse(sample.occurredTime);
}

// A sample with no readable time is taken as current: only a live sample can lack one, and it just
// arrived.
function inRange(sample: MeasurementSample, since: number): boolean {
  const at = timeOf(sample);
  return Number.isNaN(at) || at >= since;
}

function newer(a: MeasurementSample | undefined, b: MeasurementSample | undefined): MeasurementSample | undefined {
  if (!a) return b;
  if (!b) return a;
  const ta = timeOf(a);
  const tb = timeOf(b);
  if (Number.isNaN(ta)) return a;
  if (Number.isNaN(tb)) return b;
  return tb >= ta ? b : a;
}

export function buildFleet(i: FleetInputs): FleetState {
  if (i.forbidden) return { status: 'forbidden', members: [], total: 0, truncated: false };
  if (!i.roster) return { status: 'loading', members: [], total: 0, truncated: false };
  if (i.roster.unbound) return { status: 'unbound', members: [], total: 0, truncated: false };

  // A seeded reading is stamped at the start of its minute (fleet-seed.ts, "Ages") and happened
  // somewhere in that minute, so it is in range while its minute is. Held to the exact instant, a
  // reading 59 minutes 45 seconds old on an hour's board was dropped as out of range.
  const seedSince = Math.floor(i.since / MINUTE_MS) * MINUTE_MS;
  const members = i.roster.members.map((ref): FleetMember => {
    const seed = i.seeds.get(ref.token);
    const seeded = seed && seed !== 'failed' ? seed : undefined;
    const live = i.live.get(ref.token);
    const latest: Record<string, MeasurementSample> = {};
    const names = new Set<string>([...Object.keys(seeded?.latest ?? {}), ...(live?.keys() ?? [])]);
    for (const name of names) {
      const best = newer(seeded?.latest[name], live?.get(name));
      if (best && inRange(best, best === seeded?.latest[name] ? seedSince : i.since)) latest[name] = best;
    }
    const declared = ref.deviceType ? i.declared?.get(ref.deviceType) : undefined;
    const observed = new Set<string>(seeded?.observed ?? []);
    for (const name of Object.keys(latest)) observed.add(name);
    return {
      token: ref.token,
      name: ref.name,
      latest,
      seed: seed === undefined ? 'pending' : seed === 'failed' ? 'failed' : 'ready',
      observed: [...observed],
      ...(declared ? { declared: [...declared] } : {}),
      // A live reading of an unread name is its value: cellFor looks at `latest` first.
      ...(seeded?.unread?.length ? { unread: seeded.unread } : {}),
    };
  });
  return {
    status: 'ready',
    members,
    total: i.roster.total,
    truncated: i.roster.total > members.length,
  };
}

// The newest reading anywhere in the fleet (epoch ms), or undefined when none carries a time.
export function newestMemberTime(fleet: FleetState): number | undefined {
  let newest: number | undefined;
  for (const m of fleet.members) {
    for (const sample of Object.values(m.latest)) {
      const at = timeOf(sample);
      if (!Number.isNaN(at) && (newest === undefined || at > newest)) newest = at;
    }
  }
  return newest;
}

// ---- Cells -------------------------------------------------------------------

// What one (device, measurement) cell holds, and when it holds nothing, why.
//
// "Does not report this" is decided by what the device's profile DECLARES, never by what it has
// sent: a sensor whose probe died while its heartbeat keeps arriving has sent other measurements
// and not this one, and is exactly the device an operator needs to hear about. A device whose
// declaration is not known (see FleetMember.declared) is taken to declare it.
export type Cell =
  | { kind: 'value'; sample: MeasurementSample; value: number }
  | { kind: 'loading' }
  // The device's history read failed and no live reading has arrived since.
  | { kind: 'failed' }
  // The device's active profile does not declare this measurement: it is not one the device reports.
  | { kind: 'not-reported' }
  // The device reported this in the span, but its newest reading was not loaded (MemberSeed.unread).
  | { kind: 'unread' }
  // The device reported nothing at all in the span.
  | { kind: 'no-data' };

export function cellFor(member: FleetMember, measurement: string): Cell {
  const sample = member.latest[measurement];
  if (sample && typeof sample.value === 'number' && Number.isFinite(sample.value)) {
    return { kind: 'value', sample, value: sample.value };
  }
  if (member.declared && !member.declared.includes(measurement)) return { kind: 'not-reported' };
  if (member.seed === 'pending') return { kind: 'loading' };
  if (member.seed === 'failed') return { kind: 'failed' };
  if (member.unread?.includes(measurement)) return { kind: 'unread' };
  return { kind: 'no-data' };
}

// The columns: the measurements the datasource names, in that order; with none named, every
// measurement any member reported, alphabetically (stable across renders however samples arrive).
export function columnNames(named: readonly string[], members: readonly FleetMember[]): string[] {
  if (named.length > 0) return [...new Set(named)];
  const seen = new Set<string>();
  for (const m of members) for (const name of m.observed) seen.add(name);
  return [...seen].sort((a, b) => a.localeCompare(b));
}

// ---- Order and pages ------------------------------------------------------------

export interface SortKey {
  // A measurement name, or null for the device-name column.
  by: string | null;
  descending: boolean;
}

export function deviceLabel(member: Pick<FleetMember, 'name' | 'token'>): string {
  return member.name && member.name !== '' ? member.name : member.token;
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

// Ordered by the key, ties broken by device name then token, so the order never depends on the
// order samples arrived in. A device with no value for the sorted measurement goes LAST in either
// direction: "largest first" must not open with the devices that reported nothing.
export function sortMembers(members: readonly FleetMember[], key: SortKey): FleetMember[] {
  const byDevice = (a: FleetMember, b: FleetMember): number =>
    collator.compare(deviceLabel(a), deviceLabel(b)) || (a.token < b.token ? -1 : a.token > b.token ? 1 : 0);
  const sign = key.descending ? -1 : 1;
  return [...members].sort((a, b) => {
    if (key.by === null) return sign * byDevice(a, b);
    const ca = cellFor(a, key.by);
    const cb = cellFor(b, key.by);
    if (ca.kind !== 'value' || cb.kind !== 'value') {
      if (ca.kind === cb.kind) return byDevice(a, b);
      return ca.kind === 'value' ? -1 : cb.kind === 'value' ? 1 : byDevice(a, b);
    }
    return sign * (ca.value - cb.value) || byDevice(a, b);
  });
}

export const DEFAULT_PAGE_SIZE = 20;

export function pageCount(rows: number, pageSize: number): number {
  return Math.max(1, Math.ceil(rows / Math.max(1, pageSize)));
}

// The page asked for, held inside what exists (a fleet that shrinks must not leave the viewer on a
// page past the end).
export function clampPage(page: number, rows: number, pageSize: number): number {
  return Math.min(Math.max(0, page), pageCount(rows, pageSize) - 1);
}

// ---- Bars ---------------------------------------------------------------------------

// One bar per member for one measurement, in the shape the bar chart already draws (category
// items), ordered like the categorical charts order theirs. A device with no number keeps its
// label with no bar and sorts after those that have one.
export interface DeviceBar {
  label: string;
  token: string;
  value: number | null;
}

export function deviceBars(members: readonly FleetMember[], measurement: string, sort: 'none' | 'ascending' | 'descending'): DeviceBar[] {
  const bars = members.map((m): DeviceBar => {
    const cell = cellFor(m, measurement);
    return { label: deviceLabel(m), token: m.token, value: cell.kind === 'value' ? cell.value : null };
  });
  if (sort === 'none') return bars;
  const sign = sort === 'ascending' ? 1 : -1;
  return bars
    .map((bar, i) => ({ bar, i }))
    .sort((a, b) => {
      if (a.bar.value === null || b.bar.value === null) {
        return a.bar.value === b.bar.value ? a.i - b.i : a.bar.value === null ? 1 : -1;
      }
      return sign * (a.bar.value - b.bar.value) || a.i - b.i;
    })
    .map((x) => x.bar);
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a fleet widget (one row or one bar per member of an anchor) reads from a data source.
//
// The measurement channel keys its latest values by measurement NAME alone, so an anchor of ten
// thermostats collapses into one "temperature". A fleet widget needs the device kept: a roster of
// the anchor's members, each member's own live samples (every sample already carries its device
// token), and one seed read per member for what it last reported before the board was opened.
//
// There is no batched "latest value of these devices" read in the platform: the event store
// aggregates one device at a time (bucketedMeasurements), and device-state holds presence and
// position but no measurement values. The seed is therefore read per member, and what keeps that
// from being a query storm is that (a) it is the only per-member read -- the roster is one
// relationship page plus one batched name lookup, whatever the size of the fleet -- (b) a hub-wide
// limiter bounds how many are in flight, (c) identical reads share one request, and (d) the widget's
// plan asks for as little as answers it (the fleet widgets read the last few minutes first; see
// fleet-seed.ts in the widgets package).

import type { MeasurementBucket } from './queries';
import type { DevicePresence, MeasurementSample } from './types';

// One device of a roster. `name` is null when the source does not know it. `deviceType` is the
// token of its device type, when the source knows it: it is what the device's declared metrics are
// looked up by, once per type.
export interface MemberRef {
  token: string;
  name: string | null;
  deviceType?: string | null;
}

// The members a selector resolves to. `total` is the size of the anchor, which can exceed
// `members.length`: the resolver reads one bounded page, and a fleet beyond it must be reported as
// truncated rather than shown as if it were whole. `unbound` marks a slot with no binding (zero
// members, but a placeholder, not an empty fleet).
export interface MemberRoster {
  members: MemberRef[];
  total: number;
  unbound: boolean;
}

// Where a fleet widget's data lands. `roster` arrives once, before any `next`.
export interface MemberSink {
  roster(roster: MemberRoster): void;
  next(sample: MeasurementSample): void;
  // The viewer may not read this data (a refusal, not a failure): the widget says so in words
  // rather than showing the generic error pane every ordinary member would see for a working platform.
  forbidden?(): void;
  error?(err: unknown): void;
  // A member's live feed resumed after an interruption (a dropped socket, or the server ending
  // the stream): readings sent meanwhile were never delivered, so a widget that seeds from history
  // reads it again. Called once per member stream that resumed. Optional on both sides: a source
  // that never drops a feed does not call it.
  reconnected?(): void;
}

// Where a presence widget's data lands: the roster once, then a whole snapshot of the roster's
// connectivity on every poll. A token the snapshot does not carry has no device-state row (the
// service omits those), which is a settled answer -- never connected -- not a failure.
export interface PresenceSink {
  roster(roster: MemberRoster): void;
  next(states: readonly DevicePresence[]): void;
  // The viewer may not read device state (a refusal, not a failure).
  forbidden?(): void;
  error?(err: unknown): void;
}

// Why one member's seed read did not come back.
export type SeedFailure = 'forbidden' | 'failed';

// One bucketed read of ONE member, as a seed plan asks for it: the hub fills in the device. `name`
// narrows the read to one measurement (the store takes one name or none).
export interface MemberBucketCriteria {
  startTime: string;
  endTime: string;
  intervalSeconds: number;
  name?: string;
}

export type MemberBucketRead = (criteria: MemberBucketCriteria) => Promise<MeasurementBucket[]>;

// What to read for one member and how to make its seed of the answer. The WIDGET decides that (which
// span, which measurements, read in what order); the hub supplies `read` -- bounded hub-wide and
// shared, so two widgets asking one device the same question cost one request -- and settles the
// result. A plan that rejects settles that member as failed (or forbidden).
export interface MemberSeedPlan {
  seed(deviceToken: string, read: MemberBucketRead): Promise<MemberSeed>;
}

// What one member last reported inside a window: its newest reading per measurement name, and every
// name it reported at all (which is how "does not report this" is told from "sent nothing").
export interface MemberSeed {
  latest: Record<string, MeasurementSample>;
  observed: string[];
  // Names it reported in the window whose newest reading the plan chose not to read (a bound on its
  // reads): the device HAS data for them, so they must never read as "no data".
  unread?: string[];
}

// How many seed reads may be in flight at once across the whole hub. Six is what a browser opens
// to one origin over HTTP/1.1, so a larger number only queues in the browser; a fleet of 500 then
// takes ~84 rounds rather than 500 simultaneous requests starving every other widget on the board.
export const MEMBER_SEED_CONCURRENCY = 6;

// The reason a queued read gave up because its widget went away before it started.
export const SEED_CANCELLED = Symbol('member seed cancelled');

// A first-in-first-out limiter: at most `max` tasks run at once, the rest wait their turn. A task
// whose `cancelled()` is true when its turn comes is dropped without running -- a widget that
// changed its span or unmounted must not leave hundreds of reads queued behind it.
export function createLimiter(max: number) {
  let active = 0;
  const queue: Array<() => void> = [];
  const next = (): void => {
    while (active < max && queue.length > 0) queue.shift()!();
  };
  return function run<T>(task: () => Promise<T>, cancelled: () => boolean = () => false): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const start = (): void => {
        if (cancelled()) {
          reject(SEED_CANCELLED);
          return;
        }
        active += 1;
        task().then(resolve, reject).finally(() => {
          active -= 1;
          next();
        });
      };
      queue.push(start);
      next();
    });
  };
}

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
// position but no measurement values. The seed is therefore ONE read per member, and what keeps
// that from being a query storm is that (a) it is the only per-member read -- the roster is one
// relationship page plus one batched name lookup, whatever the size of the fleet -- (b) a hub-wide
// limiter bounds how many are in flight, and (c) identical reads share one request.

import type { MeasurementBucket } from './queries';
import type { MeasurementSample } from './types';

// One device of a roster. `name` is null when the source does not know it.
export interface MemberRef {
  token: string;
  name: string | null;
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
}

// Why one member's seed read did not come back.
export type SeedFailure = 'forbidden' | 'failed';

// What one member last reported inside a window: its newest reading per measurement name, and every
// name it reported at all (which is how "does not report this" is told from "sent nothing").
export interface MemberSeed {
  latest: Record<string, MeasurementSample>;
  observed: string[];
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

// foldMemberSeed turns one device's buckets into its seed. The newest bucket with a mean wins per
// name, stamped at the bucket's START: the reading happened somewhere inside the bucket, so its age
// is never understated. A name whose buckets hold readings is observed even when none has a mean.
export function foldMemberSeed(deviceToken: string, buckets: readonly MeasurementBucket[]): MemberSeed {
  const latest: Record<string, MeasurementSample> = {};
  const observed = new Set<string>();
  for (const b of buckets) {
    if (b.count === undefined ? b.avg != null : b.count > 0) observed.add(b.name);
    if (b.avg == null) continue;
    const held = latest[b.name];
    if (held && held.occurredTime !== null && Date.parse(held.occurredTime) >= Date.parse(b.bucketStart)) continue;
    latest[b.name] = {
      id: `seed-${deviceToken}-${b.name}-${b.bucketStart}`,
      deviceToken,
      eventType: 0,
      occurredTime: b.bucketStart,
      name: b.name,
      value: b.avg,
      classifier: null,
    };
  }
  return { latest, observed: [...observed] };
}

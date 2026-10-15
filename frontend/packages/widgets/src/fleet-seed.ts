// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a fleet widget reads to fill its cells before the live feed has, and how it makes a seed of
// the answer. Loaded with the fleet feed (a lazy chunk), never by a board without a fleet widget.
//
// A fleet of 500 is 500 devices to ask, so the cost per device is what matters. The obvious read --
// every measurement of the device over the whole span in the span's buckets -- returns up to 240
// buckets per measurement per device (a megabyte or more per device on a long span, tens of
// megabytes per viewer) to find ONE number per cell. The plan instead asks the question the cell
// asks, cheapest first:
//
//   1. THE LAST FEW MINUTES (TAIL_SECONDS, in one-minute buckets): at most five rows per measurement,
//      read from the store's one-minute summaries. A device that is reporting -- the ordinary case,
//      and the expensive one, because a busy device is the one with the most history -- is done here,
//      with its newest minute.
//   2. Only for what that left empty, the whole span. One read PER missing measurement, narrowed to
//      that name (the store aggregates one name or all), when the device did report in the tail --
//      which is also what tells "does not report this" from "sent nothing", so the narrow read loses
//      nothing. Otherwise (a quiet device, or more missing names than are worth a read each) one read
//      of every measurement over the span, as before.
//
// A table that names no columns gets the span read alone: its columns ARE the names its devices
// reported in the span, which only a read of the whole span can list.
//
//   3. A value a span read found is the AVERAGE of a span bucket (42 minutes wide on a week), not the
//      device's latest reading: a machine that read 14, was refuelled to 40 and then parked would
//      show 27. So the newest span bucket is read again in one-minute buckets -- at most a bucket's
//      worth of rows -- and its newest minute is what the cell shows, as for a device seen in the
//      tail. Names whose newest buckets coincide share one such read, and a device gets at most
//      MAX_NARROW_READS of them, newest bucket first. A name left past that bound is not shown
//      rather than shown as an average (an average is never the latest reading), and is marked
//      `unread`: the device has data for it, so its cell says "not loaded", never "no data".
//
// Ages. The store returns a bucket's start, not the time of its newest reading, so a seeded reading is
// stamped at the start of its bucket (or of the span, when the bucket began before it). The reading
// happened at or after that instant, so its age is OVERSTATED, never understated: by less than a
// minute for a device seen in the tail, by up to one span bucket (bucketSecondsFor) otherwise. The
// first live reading replaces the stamp with the real time.

import type {
  HistoryWindow,
  MeasurementBucket,
  MeasurementSample,
  MemberBucketCriteria,
  MemberSeed,
  MemberSeedPlan,
} from '@devicechain/dashboards';

const MINUTE_MS = 60_000;

// The tail read first: long enough to catch a device that reports every minute or two, short enough
// that it is a handful of rows per measurement.
export const TAIL_SECONDS = 300;

// The most narrowed (one-name) span reads one device gets before a single read of every name is the
// cheaper question.
export const MAX_NARROW_READS = 2;

const has = (o: object, key: string): boolean => Object.prototype.hasOwnProperty.call(o, key);

// foldMemberSeed turns one device's buckets into its seed: the newest bucket with a mean wins per
// name, stamped at max(bucket start, `notBefore`) (see "Ages" above). A name whose buckets hold
// readings is observed even when none has a mean.
export function foldMemberSeed(deviceToken: string, buckets: readonly MeasurementBucket[], notBefore?: string): MemberSeed {
  const floor = notBefore === undefined ? Number.NaN : Date.parse(notBefore);
  const latest: Record<string, MeasurementSample> = {};
  const at: Record<string, number> = {};
  const observed = new Set<string>();
  for (const b of buckets) {
    if (b.count === undefined ? b.avg != null : b.count > 0) observed.add(b.name);
    if (b.avg == null) continue;
    const start = Date.parse(b.bucketStart);
    if (has(at, b.name) && at[b.name] >= start) continue;
    at[b.name] = start;
    latest[b.name] = {
      id: `seed-${deviceToken}-${b.name}-${b.bucketStart}`,
      deviceToken,
      eventType: 0,
      occurredTime: start < floor ? new Date(floor).toISOString() : b.bucketStart,
      name: b.name,
      value: b.avg,
      classifier: null,
    };
  }
  return { latest, observed: [...observed] };
}

// Two seeds of one device: `newer` (read over a later stretch) wins per name; observed is the union.
export function mergeSeeds(newer: MemberSeed, older: MemberSeed): MemberSeed {
  return {
    latest: { ...older.latest, ...newer.latest },
    observed: [...new Set([...newer.observed, ...older.observed])],
  };
}

// The tail of a window: its last TAIL_SECONDS, on whole minutes, in one-minute buckets. Undefined
// when the window is no longer than that (the window itself is then the cheap read).
export function tailWindow(window: HistoryWindow): HistoryWindow | undefined {
  const start = Math.floor((Date.parse(window.endTime) + 1) / MINUTE_MS) * MINUTE_MS - TAIL_SECONDS * 1000;
  if (!(start > Date.parse(window.startTime))) return undefined;
  return { startTime: new Date(start).toISOString(), endTime: window.endTime, intervalSeconds: 60 };
}

// The plan a fleet widget hands the hub: the window is the widget's span, `names` the measurements
// it shows (empty: every measurement its devices report).
export function memberSeedPlan(window: HistoryWindow, names: readonly string[]): MemberSeedPlan {
  const wanted = [...new Set(names)];
  return {
    async seed(deviceToken, read) {
      const fold = (c: MemberBucketCriteria) => read(c).then((b) => foldMemberSeed(deviceToken, b, c.startTime));
      const tail = tailWindow(window);
      let seed: MemberSeed;
      let recent: MemberSeed | undefined;
      if (wanted.length === 0 || !tail) seed = await fold(window);
      else {
        recent = await fold(tail);
        const missing = wanted.filter((name) => !has(recent!.latest, name));
        // Nothing missing falls through the loop below with no read: the tail was the whole answer.
        if (recent.observed.length === 0 || missing.length > MAX_NARROW_READS) seed = mergeSeeds(recent, await fold(window));
        else {
          seed = recent;
          for (const name of missing) seed = mergeSeeds(seed, await fold({ ...window, name }));
        }
      }
      return window.intervalSeconds > 60 ? sharpen(seed, recent, window, fold) : seed;
    },
  };
}

// Step 3 above: replaces each value a span read found (any not from the tail's minutes) with the
// newest minute of its span bucket. A span value is stamped at its bucket's start (or the window's),
// so the bucket's readings lie between that stamp and one bucket width after it.
async function sharpen(
  seed: MemberSeed,
  recent: MemberSeed | undefined,
  window: HistoryWindow,
  fold: (c: MemberBucketCriteria) => Promise<MemberSeed>,
): Promise<MemberSeed> {
  const byStamp = new Map<number, string[]>();
  for (const [name, sample] of Object.entries(seed.latest)) {
    if (recent && has(recent.latest, name)) continue;
    const at = Date.parse(sample.occurredTime ?? '');
    byStamp.set(at, [...(byStamp.get(at) ?? []), name]);
  }
  const latest = { ...seed.latest };
  const unread: string[] = [];
  const end = Date.parse(window.endTime) + 1;
  const stamps = [...byStamp.keys()].sort((a, b) => b - a);
  for (const [i, at] of stamps.entries()) {
    const names = byStamp.get(at)!;
    const minutes =
      i < MAX_NARROW_READS
        ? (
            await fold({
              startTime: new Date(at).toISOString(),
              endTime: new Date(Math.min(at + window.intervalSeconds * 1000, end) - 1).toISOString(),
              intervalSeconds: 60,
              ...(names.length === 1 ? { name: names[0] } : {}),
            })
          ).latest
        : {};
    for (const name of names) {
      if (has(minutes, name)) latest[name] = minutes[name];
      else {
        delete latest[name];
        unread.push(name);
      }
    }
  }
  return { latest, observed: seed.observed, unread };
}

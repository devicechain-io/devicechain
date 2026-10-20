// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Is this calculated metric being computed?", answered from what is already stored.
//
// A calculated value is an ordinary stored reading under the metric's own key, so the hourly
// reading counts the console already has (bucketedMeasurements, tenant-wide, by metric name) give
// the number of values computed. The same counts for the formula's measured inputs give the number
// of readings it had to work with. Nothing here samples, evaluates or guesses: the status is a
// comparison of those two numbers, and it is never a ratio of "samples seen", because a device
// that reports its metrics in separate messages would then look permanently broken for a formula
// over the metric that arrives rarely.
//
// Counts are by OCCURRED time, are floored at the moment the version became active (a reading
// stored before that cannot have been computed by it), and reach back only as far as the tenant's
// measurement retention.

import { getCalculatedMetricPlan } from '@/lib/api/device-management';
import { getHourlyCounts, type HourlyCount } from '@/lib/api/event-management';

export const HOUR_MS = 3_600_000;

/** The two windows the block offers, in hours. */
export const HEALTH_WINDOWS = [24, 168] as const;
export type HealthWindow = (typeof HEALTH_WINDOWS)[number];

/**
 * Not computing: inputs were stored and no value was. No input readings: nothing to compute from.
 * Unconfirmed: values are stored under the metric's name, but another profile declares the same
 * name, so they may all be that profile's and the count cannot show THIS formula is working.
 */
export type HealthStatus = 'not_computing' | 'computing' | 'unconfirmed' | 'no_input_readings';

export interface WindowCounts {
  /** Values stored under the metric's own key. */
  computed: number;
  /** Readings stored under each measured input the formula depends on. */
  inputs: { key: string; count: number }[];
  /** The sum of `inputs`. */
  inputTotal: number;
}

export interface MetricHealth {
  key: string;
  expression: string;
  dataType: string;
  minValue: number | null;
  maxValue: number | null;
  unit: string | null;
  /** Another live profile declares a metric of this name, so its readings are in the counts. */
  nameShared: boolean;
  /** The metrics the formula reads directly, as the active version compiles it; null when it cannot run there. */
  directInputs: string[] | null;
  /** The device constants the formula reads, through its whole chain. */
  constants: string[];
  /** The data type of every metric the formula may read, from the active version. */
  inputTypes: Record<string, string>;
  /** Keyed by window hours. Null counts when the inputs are unknown. */
  windows: Record<HealthWindow, WindowCounts | null>;
}

export interface CalcHealth {
  version: number;
  /** When the active version became active, as the profile records it; null when the server gave none. */
  activeSince: Date | null;
  metrics: MetricHealth[];
}

/**
 * Readings in the buckets that fall inside [max(now - hours, floor), now]. A bucket is an hour
 * starting at bucketStart; it counts when any part of it lies after the window start.
 */
export function countInWindow(buckets: HourlyCount[], hours: number, now: Date, floor: Date | null): number {
  let start = now.getTime() - hours * HOUR_MS;
  if (floor && floor.getTime() > start) start = floor.getTime();
  let total = 0;
  for (const b of buckets) {
    const from = new Date(b.bucketStart).getTime();
    if (Number.isNaN(from)) continue;
    if (from > now.getTime()) continue;
    if (from + HOUR_MS <= start) continue;
    total += b.count;
  }
  return total;
}

export function windowCounts(
  counts: Map<string, HourlyCount[]>,
  key: string,
  inputs: string[],
  hours: number,
  now: Date,
  floor: Date | null,
): WindowCounts {
  const per = inputs.map((k) => ({ key: k, count: countInWindow(counts.get(k) ?? [], hours, now, floor) }));
  return {
    computed: countInWindow(counts.get(key) ?? [], hours, now, floor),
    inputs: per,
    inputTotal: per.reduce((n, i) => n + i.count, 0),
  };
}

/**
 * Values computed beat everything else, unless the name is shared: then they are not evidence
 * about this formula, and reading them as healthy is the one false "Computing" the counts allow.
 * Otherwise the inputs decide between broken and idle. No values under a shared name is still
 * Not computing: no profile produced any.
 */
export function statusOf(c: WindowCounts, nameShared: boolean): HealthStatus {
  if (c.computed > 0) return nameShared ? 'unconfirmed' : 'computing';
  return c.inputTotal > 0 ? 'not_computing' : 'no_input_readings';
}

const FETCH_CONCURRENCY = 4;

async function fetchAll(names: string[], start: Date): Promise<Map<string, HourlyCount[]>> {
  const out = new Map<string, HourlyCount[]>();
  for (let i = 0; i < names.length; i += FETCH_CONCURRENCY) {
    const batch = names.slice(i, i + FETCH_CONCURRENCY);
    const got = await Promise.all(batch.map((n) => getHourlyCounts(n, start)));
    batch.forEach((n, j) => out.set(n, got[j]));
  }
  return out;
}

/** Reads the active version's calculated metrics, its activation time and the hourly counts. */
export async function loadCalcHealth(profileToken: string, now: Date): Promise<CalcHealth | null> {
  const plan = await getCalculatedMetricPlan(profileToken);
  if (plan.calculated.length === 0) return null;

  // The instant the profile records for its active version: the one the activation log carries
  // for it, and for a version activated before the log began, the platform's own reading of its
  // version rows (never later than the true activation, so no value it computed is excluded).
  const since = plan.activeSince ? new Date(plan.activeSince) : null;
  const floor = since && !Number.isNaN(since.getTime()) ? since : null;

  // What each formula reads comes from the ACTIVE version as the server compiles it, through
  // the calculated metrics it chains: the draft may have renamed or removed an input since.
  const names = new Set<string>();
  for (const m of plan.calculated) {
    names.add(m.key);
    m.measuredInputs?.forEach((k) => names.add(k));
  }
  const widest = Math.max(...HEALTH_WINDOWS) * HOUR_MS;
  let start = new Date(now.getTime() - widest);
  if (floor && floor > start) start = floor;
  const counts = await fetchAll([...names], start);

  const inputTypes: Record<string, string> = {};
  for (const m of plan.measured) inputTypes[m.key] = m.dataType;
  for (const m of plan.calculated) inputTypes[m.key] = m.dataType;

  const metrics = plan.calculated.map((m): MetricHealth => {
    const measured = m.measuredInputs ?? null;
    const windows = Object.fromEntries(
      HEALTH_WINDOWS.map((h) => [h, measured ? windowCounts(counts, m.key, measured, h, now, floor) : null]),
    ) as Record<HealthWindow, WindowCounts | null>;
    return {
      key: m.key,
      expression: m.expression,
      dataType: m.dataType,
      minValue: m.minValue ?? null,
      maxValue: m.maxValue ?? null,
      unit: m.unit ?? null,
      nameShared: m.nameSharedWithOtherProfiles,
      directInputs: m.inputs ?? null,
      constants: m.constants ?? [],
      inputTypes,
      windows,
    };
  });
  return { version: plan.version, activeSince: floor, metrics };
}

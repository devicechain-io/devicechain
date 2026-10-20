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

import {
  getCalculatedMetricPlan,
  getLatestActivation,
  testMetricFormula,
} from '@/lib/api/device-management';
import { getHourlyCounts, type HourlyCount } from '@/lib/api/event-management';

export const HOUR_MS = 3_600_000;

/** The two windows the block offers, in hours. */
export const HEALTH_WINDOWS = [24, 168] as const;
export type HealthWindow = (typeof HEALTH_WINDOWS)[number];

/** Not computing: inputs were stored and no value was. No input readings: nothing to compute from. */
export type HealthStatus = 'not_computing' | 'computing' | 'no_input_readings';

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
  /** The metrics the formula reads directly, as the server compiled it; null when it could not be compiled. */
  directInputs: string[] | null;
  /** The device constants the formula reads. */
  constants: string[];
  /** Keyed by window hours. Null counts when the inputs are unknown. */
  windows: Record<HealthWindow, WindowCounts | null>;
}

export interface CalcHealth {
  version: number;
  /** When the active version became active, per the activation log; null when the log has no entry for it. */
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

/** Values computed beat everything else; otherwise the inputs decide between broken and idle. */
export function statusOf(c: WindowCounts): HealthStatus {
  if (c.computed > 0) return 'computing';
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

  // The latest entry of the log is the version that is active now. An entry for another version
  // (the log and the profile disagree) is no evidence about this one, so it is treated as absent.
  const latest = await getLatestActivation(profileToken);
  const activeSince = latest && latest.version === plan.version ? new Date(latest.activatedAt) : null;
  const floor = activeSince && !Number.isNaN(activeSince.getTime()) ? activeSince : null;

  // The server's own compiler says which measured metrics each formula reads, through the
  // calculated metrics it chains. It checks against the profile's current definitions, so a
  // formula the draft has since diverged from reports no inputs rather than guessing them.
  const compiled = await Promise.all(
    plan.calculated.map((m) =>
      testMetricFormula({
        profileToken,
        expression: m.expression,
        dataType: m.dataType,
        metricKey: m.key,
        minValue: m.minValue,
        maxValue: m.maxValue,
        sample: [],
      }).catch(() => null),
    ),
  );

  const names = new Set<string>();
  plan.calculated.forEach((m, i) => {
    names.add(m.key);
    const c = compiled[i];
    if (c && !c.compileError) c.measuredInputs.forEach((k) => names.add(k));
  });
  const widest = Math.max(...HEALTH_WINDOWS) * HOUR_MS;
  let start = new Date(now.getTime() - widest);
  if (floor && floor > start) start = floor;
  const counts = await fetchAll([...names], start);

  const metrics = plan.calculated.map((m, i): MetricHealth => {
    const c = compiled[i];
    const ok = c != null && !c.compileError;
    const measured = ok ? c.measuredInputs : null;
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
      directInputs: ok ? c.inputs : null,
      constants: ok ? c.constants : [],
      windows,
    };
  });
  return { version: plan.version, activeSince: floor, metrics };
}

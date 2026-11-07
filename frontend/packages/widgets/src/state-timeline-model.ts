// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The state timeline's state, decided apart from the drawing: how the store's buckets for one
// measurement become runs of a discrete STATE over the window. Pure, so each rule is a test.
//
// 🔴 A BUCKET AVERAGE IS NOT A STATE CHANGE. The store answers with an average per time slot, so a
// signal that flips inside one slot reads as the in-between value. The timeline places each slot in
// the band its average is in and says so ("state of the average per 5m"); it does not claim to know
// when inside the slot the device changed. A slot with no readings is NO DATA, drawn hollow, never
// merged into the state either side of it.

import { bandAt, type Threshold } from '@devicechain/widget-sdk';
import type { HistoryWindow, MeasurementBucket } from '@devicechain/dashboards';

// What a stretch of time is: the index of the band its average is in (-1: below the lowest
// threshold, the neutral lead-in), or 'none' for a slot with no reading.
export type StateKey = number | 'none';

export interface Segment {
  state: StateKey;
  // Epoch ms, the segment covers [from, to).
  from: number;
  to: number;
  // The average of the first and last slot the segment holds, to explain a hover.
  slots: number;
}

export interface Lane {
  measurement: string;
  segments: Segment[];
}

// The measurements to draw, one lane each: an explicit list if one is given, else the datasource's
// measurements (where an author names them), else whatever the buckets hold (sorted, so the order is
// stable between reads). Capped, because a lane below a readable height is not a lane.
export const MAX_LANES = 12;

export function laneNames(authored: readonly string[], datasource: readonly string[], observed: Iterable<string>): string[] {
  const names = authored.length > 0 ? authored : datasource.length > 0 ? datasource : [...new Set(observed)].sort();
  return [...new Set(names)].slice(0, MAX_LANES);
}

export function stateOf(bands: readonly Threshold[], avg: number | null | undefined): StateKey {
  if (avg == null || !Number.isFinite(avg)) return 'none';
  const hit = bandAt(bands, avg);
  return hit === undefined ? -1 : bands.indexOf(hit);
}

// One lane over the window: every slot of the window is placed (a missing slot is `none`), and
// adjacent slots in the same state are merged into one segment.
export function buildLane(measurement: string, buckets: readonly MeasurementBucket[], window: Pick<HistoryWindow, 'startTime' | 'endTime' | 'intervalSeconds'>, bands: readonly Threshold[]): Lane {
  const start = Date.parse(window.startTime);
  const end = Date.parse(window.endTime);
  const step = Math.max(1, window.intervalSeconds) * 1000;
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return { measurement, segments: [] };
  const avgs = new Map<number, number | null | undefined>();
  for (const b of buckets) {
    if (b.name !== measurement) continue;
    const at = Date.parse(b.bucketStart);
    if (Number.isNaN(at)) continue;
    avgs.set(at, b.avg);
  }
  const segments: Segment[] = [];
  for (let from = start; from < end; from += step) {
    const to = Math.min(from + step, end);
    // The store's bucket starts are aligned to the interval, the window's are rounded to whole
    // minutes: find the slot that starts inside this step.
    let avg: number | null | undefined;
    for (const [at, v] of avgs) {
      if (at >= from && at < from + step) {
        avg = v;
        break;
      }
    }
    const state = stateOf(bands, avg);
    const last = segments[segments.length - 1];
    if (last && last.state === state && last.to === from) {
      last.to = to;
      last.slots += 1;
    } else {
      segments.push({ state, from, to, slots: 1 });
    }
  }
  return { measurement, segments };
}

// The states any lane uses, in the order of the bands (lowest first), the neutral lead-in before
// them and no-data last: the legend.
export function statesUsed(lanes: readonly Lane[]): StateKey[] {
  const used = new Set<StateKey>();
  for (const l of lanes) for (const s of l.segments) used.add(s.state);
  return [...used].sort((a, b) => (a === 'none' ? 1 : b === 'none' ? -1 : a - b));
}

// How long each state lasted across the lane, as a share of the window: the figure beside a lane
// ("running 82%"). `none` is its own share, never folded into another state.
export function shares(lane: Lane): Map<StateKey, number> {
  const total = lane.segments.reduce((sum, s) => sum + (s.to - s.from), 0);
  const out = new Map<StateKey, number>();
  if (total <= 0) return out;
  for (const s of lane.segments) out.set(s.state, (out.get(s.state) ?? 0) + (s.to - s.from) / total);
  return out;
}

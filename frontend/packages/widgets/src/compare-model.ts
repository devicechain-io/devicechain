// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The multi-device compare chart's state, decided apart from the drawing and from the hub: which
// members get a line, what each line is called, and which readings make it up. Pure, so each rule
// is a test.
//
// A line is one member's readings of ONE measurement over the span: its stored history (one point
// per bucket, the bucket's AVERAGE) followed by the readings that arrived live after the history
// was read (raw). Everything is keyed by device; nothing here looks a value up by name alone.

import type { MeasurementBucket, MeasurementSample, MemberRef } from '@devicechain/dashboards';

// The lines drawn when the author sets no limit: the chart palette's width, so no two lines share
// a colour. The most an author may ask for: a comparison past this is a table, not a chart.
export const DEFAULT_MAX_SERIES = 5;
export const MAX_SERIES_LIMIT = 12;

// Live readings kept per member. A comparison is a few lines over a span; this bounds the memory of
// a chatty device (a reading a second over a week) without touching the stored history, which is
// already bucketed to at most a couple of hundred points.
export const LIVE_PER_MEMBER = 600;

// The limit as the author set it, held to what the chart supports. Unset, or anything that is not a
// whole number of at least one, is the default: a typo must not turn into a different chart.
export function seriesLimit(value: number | undefined): number {
  if (value === undefined || !Number.isInteger(value) || value < 1) return DEFAULT_MAX_SERIES;
  return Math.min(value, MAX_SERIES_LIMIT);
}

export interface CappedMembers {
  shown: MemberRef[];
  // The size of the group, as the source reports it. Never less than the members read.
  total: number;
  // True when lines were left out, for either reason: the author's limit, or a group larger than
  // the page the source read (shown + not drawn < total).
  capped: boolean;
  // True when the source itself read less than the group: the total is then the group's size and
  // `read` the members it listed.
  pagedBySource: boolean;
  read: number;
}

// The first `limit` members of the roster, in the roster's order (a stable order, so a line keeps
// its colour as readings arrive). Which members are left out is the roster's, not the readings':
// choosing "the busiest" would reshuffle the chart as data moves.
export function capMembers(roster: { members: readonly MemberRef[]; total: number }, limit: number): CappedMembers {
  const read = roster.members.length;
  const total = Math.max(roster.total, read);
  const shown = roster.members.slice(0, limit);
  return { shown, total, capped: shown.length < total, pagedBySource: total > read, read };
}

// A name per member: its device name, else its token. Two members with the same name get their
// token appended, so two lines never carry one legend entry.
export function memberLabels(members: readonly MemberRef[]): Map<string, string> {
  const base = members.map((m) => m.name?.trim() || m.token);
  const count = new Map<string, number>();
  for (const b of base) count.set(b, (count.get(b) ?? 0) + 1);
  const labels = new Map<string, string>();
  members.forEach((m, i) => labels.set(m.token, (count.get(base[i]) ?? 0) > 1 && base[i] !== m.token ? `${base[i]} (${m.token})` : base[i]));
  return labels;
}

// One member's stored history as samples: a point per bucket that has an average, stamped at the
// bucket's start, oldest first. A bucket with no readings has no average and no point.
export function bucketSamples(token: string, measurement: string, buckets: readonly MeasurementBucket[]): MeasurementSample[] {
  return buckets
    .filter((b) => b.name === measurement && b.avg != null)
    .map((b) => ({
      id: `cmp-${token}-${b.name}-${b.bucketStart}`,
      deviceToken: token,
      eventType: 0,
      occurredTime: b.bucketStart,
      name: b.name,
      value: b.avg,
      classifier: null,
    }))
    .sort((a, b) => Date.parse(a.occurredTime as string) - Date.parse(b.occurredTime as string));
}

// A member's stored history once read: the points, and the instant the read ended (epoch ms), after
// which live readings are the only source.
export interface MemberHistory {
  samples: MeasurementSample[];
  end: number;
}

// Appends a live reading to a member's buffer: newest wins on a repeat id, and the buffer keeps the
// newest LIVE_PER_MEMBER.
export function pushLive(buffer: MeasurementSample[], sample: MeasurementSample): void {
  if (buffer.some((s) => s.id === sample.id)) return;
  buffer.push(sample);
  if (buffer.length > LIVE_PER_MEMBER) buffer.splice(0, buffer.length - LIVE_PER_MEMBER);
}

export interface ComposeInputs {
  shown: readonly MemberRef[];
  labels: ReadonlyMap<string, string>;
  measurement: string;
  history: ReadonlyMap<string, MemberHistory | 'failed'>;
  live: ReadonlyMap<string, readonly MeasurementSample[]>;
  // The span, epoch ms: nothing before `since` or after `until` is drawn (the board's freeze
  // instant while paused).
  since: number;
  until: number;
}

export interface Composed {
  // One sample per point, named by the member's LABEL so the line chart draws one line per member.
  samples: MeasurementSample[];
  // Members whose stored history could not be read: their lines hold live readings only.
  historyFailed: number;
  // The newest reading drawn, epoch ms, for the frame's age.
  newest: number | undefined;
}

export function composeSeries(i: ComposeInputs): Composed {
  const samples: MeasurementSample[] = [];
  let historyFailed = 0;
  let newest: number | undefined;
  for (const m of i.shown) {
    const label = i.labels.get(m.token) ?? m.token;
    const h = i.history.get(m.token);
    if (h === 'failed') historyFailed += 1;
    const stored = h && h !== 'failed' ? h.samples : [];
    // Live readings count only after the stored history's read ended: before it, the history holds
    // them already (as an average), and drawing both would put two values on one instant.
    const after = h && h !== 'failed' ? h.end : Number.NEGATIVE_INFINITY;
    const live = (i.live.get(m.token) ?? []).filter((s) => s.name === i.measurement && s.value != null && s.occurredTime != null && Date.parse(s.occurredTime) > after);
    const points = [...stored, ...live]
      .filter((s) => {
        const t = Date.parse(s.occurredTime as string);
        return t >= i.since && t <= i.until;
      })
      .sort((a, b) => Date.parse(a.occurredTime as string) - Date.parse(b.occurredTime as string));
    for (const s of points) {
      samples.push({ ...s, name: label });
      const t = Date.parse(s.occurredTime as string);
      if (newest === undefined || t > newest) newest = t;
    }
  }
  return { samples, historyFailed, newest };
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The data feed of a fleet widget (one row or one bar per member of an anchor): the roster, each
// member's own live readings, and one seed read per member for what it last reported before the
// board was opened. Loaded on first use (connected-widget.tsx imports it dynamically), so a board
// with no fleet widget never downloads it.
//
//   - ONE subscription per widget (hub.subscribeMembers). The samples it delivers carry their
//     device, and everything here is keyed by device and measurement together -- the measurement
//     channel's name-only `latest` is exactly what this exists to get away from;
//   - the seed is one read per member, bounded hub-wide and shared between widgets that ask the
//     same question (see members.ts for the cost and the bound). The window is the widget's pinned
//     span, else the board's, ending at the freeze instant while paused;
//   - paused, live readings queue (newest per device and measurement) and merge on resume, so the
//     table on screen does not move under a "Paused" badge;
//   - a change of span re-reads the seeds; a reading older than the span is no longer shown.
//
// Rebuilding the whole state per sample would re-render a 500-row table hundreds of times a
// second, so changes are folded into a store and flushed at most once per FLUSH_MS.

import {
  DEFAULT_TIME_RANGE_SECONDS,
  type FleetState,
  type MeasurementSample,
  type MeasurementStreamState,
  type MemberRoster,
  type MemberSeed,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { resolveStaleAfterSeconds, useWidgetTimeRange, WidgetFreshnessProvider } from '@devicechain/widget-sdk';
import { useEffect, useMemo, useReducer, useRef, useState, type ComponentType } from 'react';

import { measurementSpan } from './binding-policy';
import { buildFleet, newestMemberTime, seedWindow } from './fleet-model';

// The most often the state is rebuilt from live samples.
export const FLUSH_MS = 250;
// How often a reading that has aged out of the span is dropped when nothing else changes.
const AGE_OUT_MS = 30_000;

interface Store {
  // The selector this store was filled for: a render that carries a new selector must not show the
  // previous fleet's rows under it while the effect that resets the store has yet to run.
  key: string | null;
  roster: MemberRoster | null;
  live: Map<string, Map<string, MeasurementSample>>;
  seeds: Map<string, MemberSeed | 'failed'>;
  forbidden: boolean;
  error: unknown | null;
}

const emptyStore = (key: string | null): Store => ({ key, roster: null, live: new Map(), seeds: new Map(), forbidden: false, error: null });

function record(into: Map<string, Map<string, MeasurementSample>>, sample: MeasurementSample): void {
  let byName = into.get(sample.deviceToken);
  if (!byName) into.set(sample.deviceToken, (byName = new Map()));
  // Newest wins; an out-of-order older sample must not roll a cell back.
  const held = byName.get(sample.name);
  if (held?.occurredTime && sample.occurredTime && Date.parse(sample.occurredTime) < Date.parse(held.occurredTime)) return;
  byName.set(sample.name, sample);
}

export interface FleetFeedProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  descriptor: WidgetDescriptor;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: ComponentType<any>;
  actions: WidgetActions | undefined;
  ErrorFrame: ComponentType<{ widget: WidgetInstance; error: unknown }>;
}

export default function FleetFeed({ hub, widget, descriptor, Component, actions, ErrorFrame }: FleetFeedProps) {
  const range = useWidgetTimeRange();
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const spanSeconds = measurementSpan(descriptor, widget, range?.spanSeconds) ?? range?.spanSeconds ?? DEFAULT_TIME_RANGE_SECONDS;
  const pausedAt = range?.paused ? range.pausedAt : undefined;
  const ds = widget.datasource;
  const dsKey = JSON.stringify(ds ?? null);
  const supported = hub.subscribeMembers !== undefined && hub.readMemberSeeds !== undefined;

  const store = useRef<Store>(emptyStore(dsKey));
  // Live samples that arrive while paused, newest per device and measurement.
  const queued = useRef<Map<string, Map<string, MeasurementSample>>>(new Map());
  const pausedRef = useRef(pausedAt !== undefined);
  const [version, bump] = useReducer((n: number) => n + 1, 0);
  const [rosterKey, setRosterKey] = useState<string | null>(null);
  const flushTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const schedule = (): void => {
    if (flushTimer.current !== undefined) return;
    flushTimer.current = setTimeout(() => {
      flushTimer.current = undefined;
      bump();
    }, FLUSH_MS);
  };

  // The subscription: the roster, then the live samples. A new selector starts from nothing.
  useEffect(() => {
    const s = emptyStore(dsKey);
    store.current = s;
    queued.current = new Map();
    setRosterKey(null);
    bump();
    if (!ds || !supported) return;
    const dispose = hub.subscribeMembers!(ds, {
      roster: (roster) => {
        s.roster = roster;
        setRosterKey(roster.members.map((m) => m.token).join('\n'));
        schedule();
      },
      next: (sample) => {
        record(pausedRef.current ? queued.current : s.live, sample);
        if (!pausedRef.current) schedule();
      },
      forbidden: () => {
        s.forbidden = true;
        schedule();
      },
      error: (err) => {
        s.error = err;
        schedule();
      },
    });
    return () => {
      dispose();
      if (flushTimer.current !== undefined) clearTimeout(flushTimer.current);
      flushTimer.current = undefined;
    };
    // `ds` is read through `dsKey` (value identity), not by reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, dsKey, supported]);

  // The pause gate: resuming merges what queued while frozen.
  useEffect(() => {
    const was = pausedRef.current;
    pausedRef.current = pausedAt !== undefined;
    if (was && pausedAt === undefined) {
      for (const byName of queued.current.values()) for (const sample of byName.values()) record(store.current.live, sample);
      queued.current = new Map();
      schedule();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pausedAt]);

  // The seeds: one read per member for the window, again when the span or the freeze instant changes.
  useEffect(() => {
    const s = store.current;
    if (!supported || !s.roster || rosterKey === null) return;
    const tokens = s.roster.members.map((m) => m.token);
    if (tokens.length === 0) return;
    let cancelled = false;
    void hub.readMemberSeeds!(
      tokens,
      seedWindow(spanSeconds, pausedAt ?? now()),
      (token, seed, failure) => {
        if (failure === 'forbidden') s.forbidden = true;
        else s.seeds.set(token, seed ?? 'failed');
        schedule();
      },
      () => cancelled,
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, dsKey, rosterKey, spanSeconds, pausedAt]);

  // A reading ages out of the span with no event to say so.
  useEffect(() => {
    if (pausedAt !== undefined) return;
    const id = setInterval(bump, AGE_OUT_MS);
    return () => clearInterval(id);
  }, [pausedAt]);

  const members = useMemo<FleetState>(() => {
    const s = store.current.key === dsKey ? store.current : emptyStore(dsKey);
    if (!supported) return { status: 'unsupported', members: [], total: 0, truncated: false };
    return buildFleet({
      roster: s.roster,
      seeds: s.seeds,
      live: s.live,
      since: (pausedAt ?? now()) - spanSeconds * 1000,
      forbidden: s.forbidden,
    });
    // `version` is the store's change counter; the store itself is mutable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version, supported, dsKey, spanSeconds, pausedAt, now]);

  const error = store.current.key === dsKey ? store.current.error : null;
  const data: MeasurementStreamState = { latest: {}, samples: [], error, members };

  // Opt-in through the descriptor, as for any measurement widget. The frame's age is the newest
  // reading anywhere in the fleet; a widget that shows ages per row takes them from the members.
  const freshness = useMemo(
    () =>
      descriptor.freshness === true
        ? { at: newestMemberTime(members), staleAfterSeconds: resolveStaleAfterSeconds(widget.options) }
        : undefined,
    [descriptor, widget.options, members],
  );
  if (error) return <ErrorFrame widget={widget} error={error} />;
  return (
    <WidgetFreshnessProvider freshness={freshness}>
      <Component widget={widget} data={data} actions={actions} />
    </WidgetFreshnessProvider>
  );
}

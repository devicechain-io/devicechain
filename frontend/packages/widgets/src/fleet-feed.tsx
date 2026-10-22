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
//   - the seed is read per member, bounded hub-wide and shared between widgets that ask the same
//     question; what is read is fleet-seed.ts's plan (the last few minutes first, the span only for
//     what that left empty). The window is the widget's pinned span, else the board's, ending at the
//     freeze instant while paused;
//   - what each device's profile DECLARES is read once per distinct device type (hub.declaredMetrics),
//     and is what tells "does not report this" from "not reporting". A lookup that fails or has not
//     landed leaves the declaration unknown, which counts the device as declared;
//   - paused, live readings queue (newest per device and measurement) and merge on resume, so the
//     table on screen does not move under a "Paused" badge;
//   - a change of span re-reads the seeds; a reading older than the span is no longer shown;
//   - a member stream that resumes after an interruption re-reads the seeds, at once and again just
//     after the next minute boundary: the readings sent during the outage were never delivered, and
//     a seed window ends on a minute boundary, so only the second read reaches the outage's last
//     minute. A re-read that fails keeps the seed it would have replaced.
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
import { optString, resolveStaleAfterSeconds, useWidgetTimeRange, WidgetFrame, WidgetFreshnessProvider } from '@devicechain/widget-sdk';
import { useEffect, useMemo, useReducer, useRef, useState, type ComponentType } from 'react';

import { measurementSpan } from './binding-policy';
import type { CompareFeedProps } from './compare-feed';
import { buildFleet, newestMemberTime, seedWindow } from './fleet-model';
import { memberSeedPlan } from './fleet-seed';
import { fleetStrings } from './fleet-strings';
import { LevelEmpty } from './level-readout';
import { lazyChunk } from './widgets/lazy-chunk';

// The most often the state is rebuilt from live samples.
export const FLUSH_MS = 250;
// How often a reading that has aged out of the span is dropped when nothing else changes.
const AGE_OUT_MS = 30_000;
// How long past a minute boundary the second re-read after an interruption waits, so the minute
// that just closed is in the store's one-minute summaries before it is read.
export const RESEED_SETTLE_MS = 2_000;
const MINUTE_MS = 60_000;

interface Store {
  // The selector this store was filled for: a render that carries a new selector must not show the
  // previous fleet's rows under it while the effect that resets the store has yet to run.
  key: string | null;
  roster: MemberRoster | null;
  live: Map<string, Map<string, MeasurementSample>>;
  seeds: Map<string, MemberSeed | 'failed'>;
  // What each device type declares, by type token, as the lookups settle.
  declared: Map<string, readonly string[] | null>;
  forbidden: boolean;
  error: unknown | null;
}

const emptyStore = (key: string | null): Store => ({ key, roster: null, live: new Map(), seeds: new Map(), declared: new Map(), forbidden: false, error: null });

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
  // Not a fleet widget, but bound to a group: say so instead (GroupRefusal).
  refuse?: boolean;
}

// What a widget that shows ONE device's readings keyed by measurement name renders when it is bound
// to a group: every device reporting "temperature" would overwrite the others in it, so it shows a
// sentence instead of whichever device spoke last. A widget with a per-device mode (the bar chart's
// one bar per device) is pointed at that mode; any other at the fleet table.
export function GroupRefusal({ widget, descriptor }: { widget: WidgetInstance; descriptor: WidgetDescriptor }) {
  const strings = fleetStrings();
  const perDevice = (descriptor.binding as { members?: unknown }).members !== undefined;
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div data-group-refused="" style={{ display: 'contents' }}>
        <LevelEmpty message={`${strings.oneDevice} ${perDevice ? (comparesMembers(descriptor) ? strings.useLinePerDevice : strings.usePerDevice) : strings.useFleetTable}`} />
      </div>
    </WidgetFrame>
  );
}

// The measurements a fleet widget shows, and so the ones its seed must find AND its live feed must
// carry: the datasource's, and the `measurement` option a per-device bar chart compares. Empty: every
// measurement reported. One list for both, because the hub's live feed delivers only the names its
// selector lists: a bar chart comparing a measurement its datasource does not name would otherwise
// be seeded with it and then never hear of it again.
export function shownMeasurements(widget: WidgetInstance): string[] {
  const one = widget.options?.measurement;
  const named = [...(widget.datasource?.measurements ?? []), ...(typeof one === 'string' && one !== '' ? [one] : [])];
  return [...new Set(named)];
}

// A members-mode widget that draws a line per member over the span (the time-series chart set to one
// line per device) reads history, not a latest cell, so it has its own feed. The descriptor says so by
// declaring the option that limits those lines; the widget id is never consulted.
export function comparesMembers(descriptor: WidgetDescriptor): boolean {
  return (descriptor.options as Record<string, unknown>).maxSeries !== undefined;
}

// Its own chunk inside this one: a board with a fleet table but no compare chart never loads it.
const CompareFeed = lazyChunk<typeof import('./compare-feed')>(() => import('./compare-feed')).component<never, CompareFeedProps>('default');

export default function FleetEntry(props: FleetFeedProps) {
  if (props.refuse) return <GroupRefusal widget={props.widget} descriptor={props.descriptor} />;
  if (comparesMembers(props.descriptor)) {
    return <CompareFeed hub={props.hub} widget={props.widget} descriptor={props.descriptor} ErrorFrame={props.ErrorFrame} />;
  }
  return <FleetFeed {...props} />;
}

function FleetFeed({ hub, widget, descriptor, Component, actions, ErrorFrame }: FleetFeedProps) {
  const range = useWidgetTimeRange();
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const spanSeconds = measurementSpan(descriptor, widget, range?.spanSeconds) ?? range?.spanSeconds ?? DEFAULT_TIME_RANGE_SECONDS;
  const pausedAt = range?.paused ? range.pausedAt : undefined;
  const names = shownMeasurements(widget);
  const ds = widget.datasource && { ...widget.datasource, measurements: names };
  const dsKey = JSON.stringify(ds ?? null);
  const namesKey = JSON.stringify(names);
  const supported = hub.subscribeMembers !== undefined && hub.readMemberSeeds !== undefined;

  const store = useRef<Store>(emptyStore(dsKey));
  // Live samples that arrive while paused, newest per device and measurement.
  const queued = useRef<Map<string, Map<string, MeasurementSample>>>(new Map());
  const pausedRef = useRef(pausedAt !== undefined);
  const [version, bump] = useReducer((n: number) => n + 1, 0);
  // Which roster the seeds and declarations were read for: the subscription's generation and the
  // member tokens. The generation is what makes a NEW subscription's roster a new key even when its
  // members are the same (board-replay builds a new source after every seek, over the same
  // devices). By tokens alone, the reset to null and the new roster could land in one batch, React
  // saw no change, and the new store's seeds and declarations were never read: after a seek every
  // cell a live reading had not filled stayed "loading".
  const [rosterKey, setRosterKey] = useState<string | null>(null);
  const generation = useRef(0);
  const flushTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // Re-reads asked for by a member stream that resumed; the seeds effect re-runs on each.
  const [resumes, resumed] = useReducer((n: number) => n + 1, 0);
  const boundaryTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

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
    const gen = ++generation.current;
    if (!ds || !supported) return;
    const dispose = hub.subscribeMembers!(ds, {
      roster: (roster) => {
        s.roster = roster;
        setRosterKey(`${gen}\n${roster.members.map((m) => m.token).join('\n')}`);
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
      // Every member stream on the socket resumes at once; the reducer folds them into one render,
      // and one boundary read covers them all.
      reconnected: () => {
        resumed();
        if (boundaryTimer.current !== undefined) return;
        const at = now();
        boundaryTimer.current = setTimeout(() => {
          boundaryTimer.current = undefined;
          resumed();
        }, MINUTE_MS - (at % MINUTE_MS) + RESEED_SETTLE_MS);
      },
    });
    return () => {
      dispose();
      if (flushTimer.current !== undefined) clearTimeout(flushTimer.current);
      flushTimer.current = undefined;
      if (boundaryTimer.current !== undefined) clearTimeout(boundaryTimer.current);
      boundaryTimer.current = undefined;
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
      memberSeedPlan(seedWindow(spanSeconds, pausedAt ?? now()), names),
      (token, seed, failure) => {
        if (failure === 'forbidden') s.forbidden = true;
        else if (seed) s.seeds.set(token, seed);
        // A re-read that failed keeps the seed it would have replaced: that reading is still true.
        else if (s.seeds.get(token) === undefined) s.seeds.set(token, 'failed');
        schedule();
      },
      () => cancelled,
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, dsKey, rosterKey, spanSeconds, pausedAt, namesKey, resumes]);

  // The declarations: one lookup per distinct device type in the roster, however many devices share it.
  useEffect(() => {
    const s = store.current;
    if (!supported || !s.roster || rosterKey === null || !hub.declaredMetrics) return;
    const types = [...new Set(s.roster.members.map((m) => m.deviceType).filter((t): t is string => !!t))].filter((t) => !s.declared.has(t));
    let cancelled = false;
    for (const type of types) {
      hub.declaredMetrics(type).then(
        (names) => {
          if (cancelled) return;
          s.declared.set(type, names);
          schedule();
        },
        () => {
          // Unknown stays unknown: the members are counted as declared.
        },
      );
    }
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, dsKey, rosterKey]);

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
      declared: s.declared,
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

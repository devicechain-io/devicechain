// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// React bindings between the imperative DashboardHub / DOM theme and widget state.

import type {
  AlarmSubscription,
  CommandSubscription,
  DashboardDefinition,
  DatasourceSelector,
  DeviceResolver,
  EntityCandidateLister,
  HubConnectionState,
  LocationSubscription,
  MemberResolver,
  MeasurementSample,
  SelectionCandidate,
  SlotBinding,
  WidgetDataSource,
} from '@devicechain/dashboards';
import type {
  AggregateStreamState,
  AlarmStreamState,
  CommandStreamState,
  LocationStreamState,
  MeasurementStreamState,
} from '@devicechain/dashboards';
import {
  bindingsWithoutScopedSlots,
  DashboardHub,
  hasScopedSlots,
  resolveContextBindings,
  resolveSlotCandidates,
} from '@devicechain/dashboards';
import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type RefObject } from 'react';

import { useWidgetCandidates, type WidgetCandidates } from './context';

// The channel snapshots moved to @devicechain/dashboards (they are pure data over the hub's
// row types); re-exported so every import site here keeps resolving.
export type { AggregateStreamState, AlarmStreamState, CommandStreamState, LocationStreamState, MeasurementStreamState };

// useDashboardHub gives a host ONE live hub per (resolver, authorities) — compared by
// value, so a re-rendered but equal authorities array keeps the hub — and disposes it when
// it is replaced or the host unmounts.
//
// It deliberately takes no bindings. The hub holds none: the renderer resolves every slot
// through the settled bindings (resolveWidgetDatasource) before a widget subscribes, so a
// selection re-subscribes only the widgets bound to the re-pointed slot. A hub rebuilt per
// selection would tear down every subscription on the board and re-fetch every history.
export function useDashboardHub(resolver: DeviceResolver, authorities: string[] | undefined): DashboardHub {
  const authoritiesKey = JSON.stringify(authorities ?? []);
  const hub = useMemo(
    () => new DashboardHub({ resolver, authorities }),
    // authorities read via authoritiesKey (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [resolver, authoritiesKey],
  );
  useEffect(() => () => hub.disposeAll(), [hub]);
  return hub;
}

// useResolvedBindings runs the scoped-slot cascade (ADR-039 selection amendment) for a
// host: it takes the synchronous base manifest (effectiveBindings) plus the accumulated
// selection overlay and returns the settled slot→binding map the host feeds to the
// renderer. Selection state lives in the host; this hook just derives bindings from it.
//
// The common scope-FREE dashboard needs no async work — the synchronous overlay (base
// with the selection laid over it) is already correct, and the hook returns it directly,
// preserving today's behavior. Only a dashboard with scoped slots runs the async pass,
// guarded by a monotonic generation so a slow members response can't overwrite a newer
// selection; it keeps the last settled map until the new one arrives (one re-key per
// walk, never a stale partial). A resolution error is fail-safe: keep the last map.
export function useResolvedBindings(
  definition: DashboardDefinition,
  base: Record<string, SlotBinding>,
  selection: Record<string, SlotBinding>,
  resolver: MemberResolver,
): Record<string, SlotBinding> {
  const scoped = useMemo(() => hasScopedSlots(definition), [definition]);
  // Selection wins over base. Correct as-is for root + manual slots and every scope-free
  // dashboard; scoped `first` slots are refined by the async pass below.
  const overlaid = useMemo(() => ({ ...base, ...selection }), [base, selection]);
  // Seed the settled map with scoped slots STRIPPED, so a scoped dashboard's first paint
  // is unbound-not-stale (a scoped slot's default may be out of context) until the cascade
  // derives it. For a scope-free dashboard this equals `overlaid`.
  const [resolved, setResolved] = useState(() => bindingsWithoutScopedSlots(definition, overlaid));
  const genRef = useRef(0);

  useEffect(() => {
    // Scope-free: `overlaid` is authoritative. Keep `resolved` synced to it anyway so that
    // if the definition later gains a scope (an authoring edit), the flip to the `resolved`
    // path starts from the CURRENT bindings, not a stale mount-time snapshot.
    if (!scoped) {
      setResolved((prev) => (sameBindings(prev, overlaid) ? prev : overlaid));
      return;
    }
    const gen = ++genRef.current;
    let live = true;
    resolveContextBindings(definition, base, selection, resolver)
      .then((next) => {
        if (!live || gen !== genRef.current) return;
        // Only swap when the value actually changed, so an equal re-resolve doesn't churn
        // a new object reference (and re-render every widget) for nothing.
        setResolved((prev) => (sameBindings(prev, next) ? prev : next));
      })
      .catch(() => {
        /* fail-safe: keep the last settled map */
      });
    return () => {
      live = false;
    };
  }, [scoped, definition, base, selection, resolver, overlaid]);

  return scoped ? resolved : overlaid;
}

// sameBindings is a value compare of two slot→binding maps (stable-key JSON), so the
// hook can suppress a no-op re-key.
function sameBindings(a: Record<string, SlotBinding>, b: Record<string, SlotBinding>): boolean {
  const ak = Object.keys(a).sort();
  const bk = Object.keys(b).sort();
  if (ak.length !== bk.length) return false;
  for (let i = 0; i < ak.length; i += 1) {
    if (ak[i] !== bk[i]) return false;
    if (JSON.stringify(a[ak[i]]) !== JSON.stringify(b[bk[i]])) return false;
  }
  return true;
}

// useSlotCandidates builds the ambient candidate provider a host passes to the renderer
// (ADR-039 selection amendment): a function slot→candidates that a context/entity-selector
// widget calls. It closes over the CURRENT resolved bindings, so it is rebuilt whenever a
// binding changes — a selector's option set (a scoped child's members) then follows the
// parent for free, and a widget re-fetches because the provider identity changed. Returns
// undefined when no lister is supplied (a host that wires no selection), so the selector
// stays inert. Keyed by the bindings' VALUE so an equal-but-new bindings object doesn't
// churn the provider (and every open selector's fetch) each render.
export function useSlotCandidates(
  definition: DashboardDefinition,
  bindings: Record<string, SlotBinding>,
  resolver: MemberResolver,
  lister: EntityCandidateLister | undefined,
): WidgetCandidates | undefined {
  const bindingsKey = JSON.stringify(bindings);
  return useMemo(
    () => {
      if (!lister) return undefined;
      return (slot: string) => resolveSlotCandidates(definition, slot, bindings, resolver, lister);
    },
    // bindings read via bindingsKey (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [definition, bindingsKey, resolver, lister],
  );
}

// The state a selector widget renders from: the target slot's current candidate options,
// plus load/error flags. `wired` reports whether an ambient candidate provider exists — a
// selector feature-detects on it (with the select callback) to stay inert where selection
// isn't wired (edit/preview, a host that supplies no lister) rather than showing a dead picker.
export interface CandidatesState {
  candidates: SelectionCandidate[];
  loading: boolean;
  error: unknown | null;
  wired: boolean;
}

// useCandidates resolves the options a selector widget offers for `slot` through the ambient
// candidate provider. Re-fetches when the provider (i.e. the resolved bindings) or the slot
// changes, guarded by a monotonic generation so a slow list can't overwrite a newer one
// (the parent-switch-then-open race). Fail-safe: an error yields an empty option set.
export function useCandidates(slot: string | undefined): CandidatesState {
  const provider = useWidgetCandidates();
  const wired = !!provider;
  // Seed loading true when a fetch is imminent (provider + slot present) so the first paint
  // reads "Loading…", not a spurious "No options" for the frame before the effect runs.
  const [state, setState] = useState<Omit<CandidatesState, 'wired'>>(() => ({
    candidates: [],
    loading: Boolean(provider && slot),
    error: null,
  }));
  const genRef = useRef(0);

  useEffect(() => {
    if (!provider || !slot) {
      setState({ candidates: [], loading: false, error: null });
      return;
    }
    const gen = ++genRef.current;
    let live = true;
    // Clear any prior error when a fresh (healthy) fetch starts, so a retry doesn't show
    // "Loading…" and a stale error banner together.
    setState((prev) => ({ ...prev, loading: true, error: null }));
    provider(slot)
      .then((candidates) => {
        if (live && gen === genRef.current) setState({ candidates, loading: false, error: null });
      })
      .catch((error: unknown) => {
        if (live && gen === genRef.current) setState({ candidates: [], loading: false, error });
      });
    return () => {
      live = false;
    };
  }, [provider, slot]);

  return { ...state, wired };
}

export interface MeasurementStreamOptions {
  // Max samples retained in the rolling window (oldest dropped past this). With `windowSeconds`
  // this is the memory ceiling on a span (default MAX_SPAN_SAMPLES); without it, the whole
  // window (default 300).
  window?: number;
  // The window as a SPAN: only samples that occurred within this many seconds of now (or of
  // `pausedAt`) are kept, whatever their number, and `window` is demoted to a ceiling so a
  // busy device cannot grow the buffer without bound. Omit for the count-only window.
  windowSeconds?: number;
  // Freeze the stream at this instant (epoch ms). While set, live samples are BUFFERED, not merged:
  // the returned state does not change, its newest sample keeps its time (so an age readout
  // keeps counting), and the span is measured back from this instant. Clearing it merges
  // everything that arrived in the meantime at once -- the catch-up.
  pausedAt?: number;
  // The clock the span is measured against (epoch ms). Default: the wall clock. A data source that
  // plays a recorded run supplies its own, because the run's samples are dated by the run, not by
  // today, and a span measured from today would drop every one of them.
  now?: () => number;
  // Historical samples to seed the window with before live data arrives (from a
  // bucketedMeasurements backfill). Assumed chronological and older than the live
  // tail; live samples for the same measurement name override the seed's latest.
  initialSamples?: MeasurementSample[];
  // When `initialSamples` was read up to (epoch ms, wall clock). After the live feed is interrupted,
  // only a seed read at or after the moment it resumed covers the outage: such a seed REPLACES the
  // live samples from before the interruption, and until one arrives the window reports the outage
  // as a gap (MeasurementStreamState.gaps) rather than joining the readings either side. Omitted:
  // no seed is ever taken as covering an outage, so an interruption stays a gap.
  initialSamplesAsOf?: number;
  // Called when the live feed resumes after an interruption, after the gap is recorded: the host
  // reads the widget's history again and hands it back as `initialSamples` + `initialSamplesAsOf`.
  onReconnect?: () => void;
}

// The ceiling on retained samples when the window is a span. A span of days at a high event
// rate would otherwise hold every raw reading; past this the OLDEST go first.
export const MAX_SPAN_SAMPLES = 2000;

const EMPTY: MeasurementStreamState = { latest: {}, samples: [], error: null };

// Build a name→last-sample map from a chronological list (last occurrence wins).
function latestOf(samples: MeasurementSample[]): Record<string, MeasurementSample> {
  const latest: Record<string, MeasurementSample> = {};
  for (const s of samples) latest[s.name] = s;
  return latest;
}

// evictOldest keeps the newest `max` samples, recording in `evicted` the time of the newest one it
// drops (the hook's mark for "the window no longer reaches back to its start").
function evictOldest(samples: MeasurementSample[], max: number, evicted: { current: number }): MeasurementSample[] {
  if (samples.length <= max) return samples;
  const cut = samples.length - max;
  for (let i = 0; i < cut; i++) {
    const at = Date.parse(samples[i].occurredTime ?? '');
    if (at > evicted.current) evicted.current = at;
  }
  return samples.slice(cut);
}

// An interruption of the live feed: `after` is the time (epoch ms) of the newest sample known when
// the feed resumed, `at` the wall-clock instant it resumed. Nothing is known between `after` and the
// next sample until a seed read at or after `at` arrives.
interface Gap {
  after: number;
  at: number;
}

interface LiveBuffer {
  key: string | null;
  state: MeasurementStreamState;
  gaps: Gap[];
}

function timeOf(sample: MeasurementSample): number {
  return sample.occurredTime ? Date.parse(sample.occurredTime) : Number.NaN;
}

// The newest sample time among the given samples, or -Infinity when none carries one.
function newestTime(samples: Iterable<MeasurementSample>): number {
  let newest = -Infinity;
  for (const s of samples) {
    const at = timeOf(s);
    if (at > newest) newest = at;
  }
  return newest;
}

// settleGaps folds into the live buffer every gap a seed answers -- one read at or after the
// moment the feed resumed. The seed is then the history up to and across the outage, so the live
// samples it covers go: those from before the interruption, and any older than the seed's newest
// (they would draw backwards). A measurement's latest value from before the interruption gives way
// to the seed's when the seed has one. A sample with no readable time is kept: it cannot be shown
// to be covered. Pure: the render applies it at once, and an effect commits it to the buffer.
function settleGaps(buffer: LiveBuffer, seed: MeasurementSample[] | undefined, asOf: number | undefined): LiveBuffer {
  const { gaps, state } = buffer;
  if (!seed || asOf === undefined) return buffer;
  // Gaps are recorded oldest first, so the ones this seed answers are a prefix.
  const open = gaps.filter((g) => asOf < g.at);
  if (open.length === gaps.length) return buffer;
  const cutoff = Math.max(newestTime(seed), gaps[gaps.length - open.length - 1].after);
  const kept = (s: MeasurementSample): boolean => !(timeOf(s) <= cutoff);
  const latest: Record<string, MeasurementSample> = {};
  for (const [name, s] of Object.entries(state.latest)) {
    if (kept(s) || !seed.some((x) => x.name === name)) latest[name] = s;
  }
  return { key: buffer.key, state: { ...state, latest, samples: state.samples.filter(kept) }, gaps: open };
}

// withinSpan drops the samples that occurred before `end - spanSeconds`. A sample whose time is
// missing or unparseable is KEPT: it cannot be shown to be outside the window, and dropping it
// would hide a reading the count ceiling would have kept.
export function withinSpan(samples: MeasurementSample[], spanSeconds: number, end: number): MeasurementSample[] {
  const from = end - spanSeconds * 1000;
  return samples.filter((s) => {
    if (!s.occurredTime) return true;
    const at = Date.parse(s.occurredTime);
    return Number.isNaN(at) || at >= from;
  });
}

// useMeasurementStream subscribes a widget's datasource through the hub and keeps
// the latest value per measurement plus a bounded rolling window. It re-subscribes
// when the datasource changes (compared by value, so a re-rendered-but-equal
// selector doesn't churn the subscription) and tears down on unmount. When
// initialSamples is given, it is merged ahead of the live tail so a chart shows
// history immediately (and can arrive after the live stream opens).
export function useMeasurementStream(
  hub: WidgetDataSource,
  datasource: DatasourceSelector | undefined,
  options: MeasurementStreamOptions = {},
): MeasurementStreamState {
  const { windowSeconds, pausedAt } = options;
  const windowSize = options.window ?? (windowSeconds !== undefined ? MAX_SPAN_SAMPLES : 300);
  const initialSamples = options.initialSamples;
  const seedAsOf = options.initialSamplesAsOf;
  const paused = pausedAt !== undefined;

  // Value-compare the selector so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = datasource ? JSON.stringify(datasource) : null;

  // The span and the pause are read by the subscription's callback through refs, so changing the
  // range (or pausing) never tears the subscription down and loses what it has buffered.
  const spanRef = useRef(windowSeconds);
  const clockRef = useRef(options.now ?? Date.now);
  const pausedRef = useRef(paused);
  const queueRef = useRef<MeasurementSample[]>([]);
  // The newest QUEUED sample per measurement name. The queue is capped, so a long pause at a high
  // rate evicts its oldest entries -- and with them, possibly, the only sample of a measurement
  // that reports rarely. Without this, resuming would leave that measurement's latest value at
  // its pre-pause reading. Bounded by the number of measurement names, not by time.
  const queueLatestRef = useRef<Record<string, MeasurementSample>>({});
  // The time (epoch ms) of the newest sample the retention ceiling has evicted, live or queued.
  // While it is still inside the span, the window no longer covers the whole span and says so
  // (`truncatedBefore`), rather than drawing a short window under a long span's label.
  const evictedRef = useRef(-Infinity);
  const flushRef = useRef<() => void>(() => {});
  const clock = options.now ?? Date.now;
  // Read by the subscription's callbacks: the seed on hand when the feed resumes (part of what was
  // known before the gap), and the host's re-read.
  const seedRef = useRef(initialSamples);
  const onReconnectRef = useRef(options.onReconnect);
  useEffect(() => {
    spanRef.current = windowSeconds;
    clockRef.current = clock;
    seedRef.current = initialSamples;
    onReconnectRef.current = options.onReconnect;
  });

  // The live buffer is TAGGED with the selector it was filled from. The effect below
  // resets it when the selector changes, but an effect runs after the render that
  // carried the new selector — so without the tag that render would show the previous
  // device's values under the new one. A buffer whose tag is not the current key is
  // treated as empty, synchronously.
  const [tagged, setTagged] = useState<LiveBuffer>({ key, state: EMPTY, gaps: [] });
  // The buffer as the current seed leaves it: a seed that covers an outage replaces what it covers
  // in this very render, and the effect below commits that so the buffer does not keep it.
  const settled = settleGaps(tagged, initialSamples, seedAsOf);
  const live = settled.key === key ? settled.state : EMPTY;
  const gaps = settled.key === key ? settled.gaps : [];
  useEffect(() => {
    setTagged((prev) => settleGaps(prev, initialSamples, seedAsOf));
  }, [initialSamples, seedAsOf]);

  useEffect(() => {
    setTagged({ key, state: EMPTY, gaps: [] }); // reset the live buffer whenever the datasource changes (or clears)
    queueRef.current = [];
    queueLatestRef.current = {};
    evictedRef.current = -Infinity;
    flushRef.current = () => {};
    if (!datasource) return;

    // A callback for a selector the buffer has already moved past (a sink that answers
    // after its subscription was released) leaves the buffer alone rather than re-tagging
    // it to the old selector, which would blank the current one's values.
    const update = (fn: (prev: MeasurementStreamState) => MeasurementStreamState) =>
      setTagged((prev) => (prev.key === key ? { ...prev, state: fn(prev.state) } : prev));

    const fold = (
      prev: MeasurementStreamState,
      incoming: MeasurementSample[],
      newest: Record<string, MeasurementSample> = latestOf(incoming),
    ): MeasurementStreamState => {
      let samples = prev.samples.concat(incoming);
      const span = spanRef.current;
      if (span !== undefined) samples = withinSpan(samples, span, clockRef.current());
      samples = evictOldest(samples, windowSize, evictedRef);
      return { latest: { ...prev.latest, ...newest }, samples, error: null };
    };

    flushRef.current = () => {
      const queued = queueRef.current;
      if (queued.length === 0) return;
      const newest = queueLatestRef.current;
      queueRef.current = [];
      queueLatestRef.current = {};
      update((prev) => fold(prev, queued, newest));
    };

    return hub.subscribeWidget(datasource, {
      next: (sample) => {
        if (pausedRef.current) {
          // Buffered, bounded: a pause that outlasts the ceiling keeps the newest.
          queueRef.current = evictOldest([...queueRef.current, sample], windowSize, evictedRef);
          queueLatestRef.current[sample.name] = sample;
          return;
        }
        update((prev) => fold(prev, [sample]));
      },
      error: (err) => update((prev) => ({ ...prev, error: err })),
      // The feed resumed: what was sent meanwhile never arrived. Record where knowledge stops --
      // the newest sample known, live, queued or seeded -- so the window shows a gap there, then
      // ask the host for the history across it. Nothing known yet: no gap to show, but the
      // history is still read again.
      reconnected: () => {
        const at = Date.now();
        setTagged((prev) => {
          if (prev.key !== key) return prev;
          // `latest` holds the newest sample of every name the window holds.
          const after = newestTime([...Object.values(prev.state.latest), ...Object.values(queueLatestRef.current), ...(seedRef.current ?? [])]);
          if (after === -Infinity) return prev;
          // The newest eight only, so a display left open for months with a flaky link does not
          // grow the list with every reconnect. An older gap lies before most of the window anyway.
          return { ...prev, gaps: [...prev.gaps.slice(-7), { after, at }] };
        });
        onReconnectRef.current?.();
      },
    });
    // `datasource` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key, windowSize]);

  // The pause gate. Resuming merges what queued while frozen. The flag flips before the flush,
  // so a sample landing between the two is merged rather than queued behind it.
  useEffect(() => {
    pausedRef.current = paused;
    if (!paused) flushRef.current();
  }, [paused]);

  // Layer the history seed under the live tail. Kept separate from the live buffer
  // so late-arriving history (async backfill) recomputes without touching the
  // subscription, and switching datasource clears live while the new seed applies.
  const state = useMemo(() => {
    // The span is measured back from the freeze instant while paused, from now otherwise.
    const end = pausedAt ?? clock();
    if (windowSeconds === undefined) {
      if (!initialSamples || initialSamples.length === 0) return live;
      const merged = initialSamples.concat(live.samples);
      return {
        latest: { ...latestOf(initialSamples), ...live.latest },
        samples: merged.length > windowSize ? merged.slice(merged.length - windowSize) : merged,
        error: live.error,
      };
    }
    // A span. Everything kept is inside it, so anything the ceiling cuts here -- or cut earlier
    // from the live buffer or the pause queue, while still inside it -- is a hole at the start of
    // the window. The ceiling drops the OLDEST, so what is kept is one contiguous run; it is
    // marked with where that run begins, so a chart can say it covers less than its span.
    const from = end - windowSeconds * 1000;
    const merged = withinSpan(initialSamples ? initialSamples.concat(live.samples) : live.samples, windowSeconds, end);
    const samples = merged.length > windowSize ? merged.slice(merged.length - windowSize) : merged;
    const cut = samples.length < merged.length || evictedRef.current >= from;
    const truncatedBefore = cut ? (samples[0]?.occurredTime ?? undefined) : undefined;
    const latest = initialSamples ? { ...latestOf(initialSamples), ...live.latest } : live.latest;
    if (!truncatedBefore && latest === live.latest && samples.length === live.samples.length) return live;
    const state: MeasurementStreamState = { latest, samples, error: live.error };
    if (truncatedBefore) state.truncatedBefore = truncatedBefore;
    return state;
  }, [initialSamples, live, windowSize, windowSeconds, pausedAt, clock]);

  // The gaps still open, as instants. One before the window's first sample breaks nothing.
  return gaps.length ? { ...state, gaps: gaps.map((g) => new Date(g.after).toISOString()) } : state;
}

const EMPTY_ALARMS: AlarmStreamState = { alarms: [], total: 0, loading: true, error: null };

// useAlarmStream binds an alarm widget's scope+filters through the hub's alarm channel
// and keeps the latest snapshot. It re-subscribes when the subscription changes
// (value-compared, so a re-rendered-but-equal object doesn't churn) and tears down on
// unmount. The hub delivers whole snapshots (query-then-reconcile), so this holds the
// last one rather than accumulating — the query is the source of truth.
export function useAlarmStream(
  hub: WidgetDataSource,
  subscription: AlarmSubscription,
): AlarmStreamState {
  // Value-compare the subscription so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = JSON.stringify(subscription);
  // The snapshot is TAGGED with the subscription it came from, as the measurement buffer is: the
  // effect that resets it runs after the render carrying the new subscription, and that render
  // must not show the previous scope's rows under it (nor hand them to a pause hold to keep).
  const [[at, state], setState] = useState<[string, AlarmStreamState]>([key, EMPTY_ALARMS]);

  useEffect(() => {
    setState([key, EMPTY_ALARMS]); // reset to loading whenever the subscription changes
    return hub.subscribeAlarms(subscription, {
      next: (snapshot) =>
        setState([
          key,
          {
            alarms: snapshot.alarms,
            total: snapshot.total,
            ...(snapshot.typeCounts ? { typeCounts: snapshot.typeCounts } : {}),
            ...(snapshot.severityCounts ? { severityCounts: snapshot.severityCounts } : {}),
            loading: false,
            error: null,
          },
        ]),
      error: (err) => setState(([, prev]) => [key, { ...prev, loading: false, error: err }]),
    });
    // `subscription` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key]);

  return at === key ? state : EMPTY_ALARMS;
}

const EMPTY_COMMANDS: CommandStreamState = {
  deviceToken: null,
  commands: [],
  total: 0,
  loading: true,
  error: null,
};

// useCommandStream binds a command widget's scope through the hub's control channel and
// keeps the latest command-history snapshot. It re-subscribes when the subscription
// changes (value-compared) and tears down on unmount. The hub delivers whole snapshots
// (poll-then-emit), so this holds the last one rather than accumulating.
export function useCommandStream(
  hub: WidgetDataSource,
  subscription: CommandSubscription,
): CommandStreamState {
  const [state, setState] = useState<CommandStreamState>(EMPTY_COMMANDS);

  // Value-compare the subscription so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = JSON.stringify(subscription);

  useEffect(() => {
    setState(EMPTY_COMMANDS); // reset to loading whenever the subscription changes
    return hub.subscribeCommands(subscription, {
      next: (snapshot) =>
        setState({
          deviceToken: snapshot.deviceToken,
          commands: snapshot.commands,
          total: snapshot.total,
          loading: false,
          error: null,
        }),
      error: (err) => setState((prev) => ({ ...prev, loading: false, error: err })),
    });
    // `subscription` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key]);

  return state;
}

const EMPTY_LOCATIONS: LocationStreamState = {
  locations: [],
  deviceTokens: [],
  forbidden: false,
  loading: true,
  error: null,
};

// useLocationStream binds a map widget's selector through the hub's location channel and
// keeps the latest position snapshot. It re-subscribes when the subscription changes
// (value-compared, so a re-rendered-but-equal object doesn't churn) and tears down on
// unmount. The hub delivers whole snapshots (poll-then-emit), so this holds the last one
// rather than accumulating.
//
// A `forbidden` snapshot CLEARS the positions as it sets the flag: an authority can be
// revoked mid-session, and keeping the last-seen coordinates on screen under a
// permission banner would leave a refused viewer looking at the very data they were
// just refused.
export function useLocationStream(
  hub: WidgetDataSource,
  subscription: LocationSubscription,
): LocationStreamState {
  // Value-compare the subscription so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = JSON.stringify(subscription);
  // Tagged with its subscription, as useAlarmStream's snapshot is: no render shows the previous
  // selector's markers under a new one.
  const [[at, state], setState] = useState<[string, LocationStreamState]>([key, EMPTY_LOCATIONS]);

  useEffect(() => {
    setState([key, EMPTY_LOCATIONS]); // reset to loading whenever the subscription changes
    return hub.subscribeLocations(subscription, {
      next: (snapshot) =>
        setState([
          key,
          snapshot.kind === 'forbidden'
            ? { locations: [], deviceTokens: [], forbidden: true, loading: false, error: null }
            : {
                locations: snapshot.locations,
                deviceTokens: snapshot.deviceTokens,
                forbidden: false,
                loading: false,
                error: null,
              },
        ]),
      error: (err) => setState(([, prev]) => [key, { ...prev, loading: false, error: err }]),
    });
    // `subscription` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key]);

  return at === key ? state : EMPTY_LOCATIONS;
}

// A widget's bound-entity availability: 'unknown' until the async check resolves
// (render optimistically meanwhile), then 'available', or 'unavailable' when the bound
// device no longer exists (a deleted device's stable token, ADR-044).
export type DatasourceAvailability = 'unknown' | 'available' | 'unavailable';

// While a widget shows "unavailable", re-check on this cadence so a device deleted then
// recreated with the same token (ADR-042 frees tokens on delete) recovers on a long-lived
// viewer without a reload. Only runs while unavailable — a live device is checked once.
const AVAILABILITY_REVALIDATE_MS = 60_000;

// useDatasourceAvailability resolves whether a widget's bound device still exists,
// WITHOUT blocking the widget's data stream (optimistic + async): it starts 'unknown'
// (the widget renders normally), then flips to 'available'/'unavailable' once the hub's
// existence check resolves. Fails open (→'available') so a check outage never falsely
// blanks a live widget. A widget with no datasource is trivially available (no query).
// Re-checks when the datasource changes (value-compared), and periodically while
// unavailable so a recreated device recovers.
export function useDatasourceAvailability(
  hub: WidgetDataSource,
  datasource: DatasourceSelector | undefined,
): DatasourceAvailability {
  const [state, setState] = useState<DatasourceAvailability>('unknown');
  const key = datasource ? JSON.stringify(datasource) : null;

  useEffect(() => {
    // No datasource (label/image, or tenant-wide) has nothing to validate — available
    // immediately, no query, no re-render churn.
    if (!datasource) {
      setState('available');
      return;
    }
    setState('unknown');
    let cancelled = false;
    let timer: ReturnType<typeof setInterval> | undefined;

    const stopTimer = (): void => {
      if (timer) {
        clearInterval(timer);
        timer = undefined;
      }
    };

    const check = (): void => {
      hub
        .isDatasourceAvailable(datasource)
        .then((ok) => {
          if (cancelled) return;
          setState(ok ? 'available' : 'unavailable');
          if (ok) stopTimer(); // recovered (or was fine) — stop polling
        })
        .catch(() => {
          if (cancelled) return;
          setState('available'); // fail open
          stopTimer();
        });
    };

    check();
    // Poll only matters once unavailable; harmlessly runs until the first check clears it
    // for a live device (cleared in the resolver above, well before the first tick).
    timer = setInterval(check, AVAILABILITY_REVALIDATE_MS);

    return () => {
      cancelled = true;
      stopTimer();
    };
    // `datasource` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key]);

  return state;
}

export interface ElementSize {
  width: number;
  height: number;
}

// useElementSize tracks an element's content-box size via ResizeObserver, so a
// widget can scale its contents (chart fonts, gauge ticks, a big number) to the
// slot it was resized to — ECharts' resize() re-lays-out the canvas but never
// scales absolute font sizes, so a small widget crams without this. Returns
// {0,0} until measured (and where ResizeObserver is unavailable, e.g. jsdom); the
// consumers treat a zero size as "use default sizing".
//
// `step` quantises each dimension DOWN to a multiple of it (default 1: exact), so a caller that
// only needs a coarse size -- the frame feeding every widget's scale -- does not re-render for
// sub-pixel or few-pixel jitter. Down, so text fitted to the quantised box always fits the
// real one.
export function useElementSize<T extends HTMLElement>(step = 1): [RefObject<T | null>, ElementSize] {
  const ref = useRef<T>(null);
  const [size, setSize] = useState<ElementSize>({ width: 0, height: 0 });

  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) return;
      const width = step > 1 ? Math.floor(rect.width / step) * step : rect.width;
      const height = step > 1 ? Math.floor(rect.height / step) * step : rect.height;
      setSize((prev) => (prev.width === width && prev.height === height ? prev : { width, height }));
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [step]);

  return [ref, size];
}

// What useHubConnectionState needs of a hub: the live implementation (DashboardHub) has it, a
// synthetic preview source does not, and a host holding only that reads `live`.
export interface ConnectionStateSource {
  readonly connectionState: HubConnectionState;
  onConnectionState(listener: () => void): () => void;
}

const NO_CONNECTION_SOURCE: ConnectionStateSource = {
  connectionState: 'live',
  onConnectionState: () => () => {},
};

// useHubConnectionState follows a hub's socket state (live / reconnecting / offline) for a
// header badge. It re-renders only when the state changes.
export function useHubConnectionState(hub: Partial<ConnectionStateSource> | undefined): HubConnectionState {
  // A source that reports no connection (a synthetic preview, a recording) is not "down":
  // there is no socket to lose, so it reads live.
  const source =
    hub && typeof hub.onConnectionState === 'function' ? (hub as ConnectionStateSource) : NO_CONNECTION_SOURCE;
  return useSyncExternalStore(
    (listener) => source.onConnectionState(listener),
    () => source.connectionState,
  );
}

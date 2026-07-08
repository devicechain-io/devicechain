// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// React bindings between the imperative DashboardHub / DOM theme and widget state.

import type {
  AlarmRow,
  AlarmSubscription,
  CommandRow,
  CommandSubscription,
  DashboardDefinition,
  DatasourceSelector,
  EntityCandidateLister,
  MemberResolver,
  MeasurementSample,
  SelectionCandidate,
  SlotBinding,
  WidgetDataSource,
} from '@devicechain/dashboards';
import {
  bindingsWithoutScopedSlots,
  hasScopedSlots,
  resolveContextBindings,
  resolveSlotCandidates,
} from '@devicechain/dashboards';
import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type RefObject } from 'react';

import { useWidgetCandidates, type WidgetCandidates } from './frame';
import { resolveChartTheme, type ChartTheme } from './theme';

// useResolvedBindings runs the scoped-slot cascade (ADR-039 selection amendment) for a
// host: it takes the synchronous base manifest (effectiveBindings) plus the accumulated
// selection overlay and returns the settled slot→binding map the host feeds to the hub +
// renderer. Selection state lives in the host (so it survives the hub rebuild a rebind
// triggers); this hook just derives bindings from it.
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
        // a new object reference (and rebuild the hub) for nothing.
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

export interface MeasurementStreamState {
  // The most recent sample per measurement name (drives cards, gauges, tables).
  latest: Record<string, MeasurementSample>;
  // A rolling, chronological window of recent samples (drives the time chart).
  samples: MeasurementSample[];
  error: unknown | null;
}

// The state an alarm widget renders from: the current rows (newest first, capped),
// the total match count (past the page — drives alarm-count), and load/error flags.
export interface AlarmStreamState {
  alarms: AlarmRow[];
  total: number;
  loading: boolean;
  error: unknown | null;
}

// The state a command widget renders from: the resolved target device (null = unbound,
// so it can't issue), the recent commands (newest first, capped) with live status, the
// total count, and load/error flags.
export interface CommandStreamState {
  deviceToken: string | null;
  commands: CommandRow[];
  total: number;
  loading: boolean;
  error: unknown | null;
}

export interface MeasurementStreamOptions {
  // Max samples retained in the rolling window (oldest dropped past this).
  window?: number;
  // Historical samples to seed the window with before live data arrives (from a
  // bucketedMeasurements backfill). Assumed chronological and older than the live
  // tail; live samples for the same measurement name override the seed's latest.
  initialSamples?: MeasurementSample[];
}

const EMPTY: MeasurementStreamState = { latest: {}, samples: [], error: null };

// Build a name→last-sample map from a chronological list (last occurrence wins).
function latestOf(samples: MeasurementSample[]): Record<string, MeasurementSample> {
  const latest: Record<string, MeasurementSample> = {};
  for (const s of samples) latest[s.name] = s;
  return latest;
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
  const windowSize = options.window ?? 300;
  const initialSamples = options.initialSamples;
  const [live, setLive] = useState<MeasurementStreamState>(EMPTY);

  // Value-compare the selector so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = datasource ? JSON.stringify(datasource) : null;

  useEffect(() => {
    setLive(EMPTY); // reset the live buffer whenever the datasource changes (or clears)
    if (!datasource) return;

    return hub.subscribeWidget(datasource, {
      next: (sample) =>
        setLive((prev) => {
          const samples = prev.samples.concat(sample);
          if (samples.length > windowSize) samples.splice(0, samples.length - windowSize);
          return { latest: { ...prev.latest, [sample.name]: sample }, samples, error: null };
        }),
      error: (err) => setLive((prev) => ({ ...prev, error: err })),
    });
    // `datasource` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key, windowSize]);

  // Layer the history seed under the live tail. Kept separate from the live buffer
  // so late-arriving history (async backfill) recomputes without touching the
  // subscription, and switching datasource clears live while the new seed applies.
  return useMemo(() => {
    if (!initialSamples || initialSamples.length === 0) return live;
    const merged = initialSamples.concat(live.samples);
    const samples =
      merged.length > windowSize ? merged.slice(merged.length - windowSize) : merged;
    return {
      latest: { ...latestOf(initialSamples), ...live.latest },
      samples,
      error: live.error,
    };
  }, [initialSamples, live, windowSize]);
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
  const [state, setState] = useState<AlarmStreamState>(EMPTY_ALARMS);

  // Value-compare the subscription so an unchanged-but-new object reference doesn't
  // resubscribe every render.
  const key = JSON.stringify(subscription);

  useEffect(() => {
    setState(EMPTY_ALARMS); // reset to loading whenever the subscription changes
    return hub.subscribeAlarms(subscription, {
      next: (snapshot) =>
        setState({ alarms: snapshot.alarms, total: snapshot.total, loading: false, error: null }),
      error: (err) => setState((prev) => ({ ...prev, loading: false, error: err })),
    });
    // `subscription` is intentionally read via `key` (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, key]);

  return state;
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
export function useElementSize<T extends HTMLElement>(): [RefObject<T | null>, ElementSize] {
  const ref = useRef<T>(null);
  const [size, setSize] = useState<ElementSize>({ width: 0, height: 0 });

  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) return;
      setSize((prev) =>
        prev.width === rect.width && prev.height === rect.height
          ? prev
          : { width: rect.width, height: rect.height },
      );
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  return [ref, size];
}

// --- shared chart-theme store ------------------------------------------------
//
// Charts need concrete colors resolved from the CSS-variable theme. Rather than
// have every chart widget mount its own MutationObserver on <html> and recompute
// the same theme, one page-wide observer resolves it once per change and every
// widget reads the shared, memoized value via useSyncExternalStore — so a busy
// dashboard pays O(1), not O(number of charts), per theme toggle.

let sharedTheme: ChartTheme | null = null;
const themeListeners = new Set<() => void>();
let themeObserver: MutationObserver | null = null;

function recomputeTheme(): void {
  sharedTheme = resolveChartTheme(document.documentElement);
}

function subscribeTheme(listener: () => void): () => void {
  if (!themeObserver) {
    recomputeTheme();
    themeObserver = new MutationObserver(() => {
      recomputeTheme();
      for (const l of themeListeners) l();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'style'],
    });
  }
  themeListeners.add(listener);
  return () => {
    themeListeners.delete(listener);
    if (themeListeners.size === 0 && themeObserver) {
      themeObserver.disconnect();
      themeObserver = null;
      sharedTheme = null;
    }
  };
}

function getThemeSnapshot(): ChartTheme {
  if (!sharedTheme) recomputeTheme();
  return sharedTheme as ChartTheme;
}

// useChartTheme returns the current chart colors and re-renders the caller when
// the document theme changes (a light/dark class or inline-style swap on <html>).
export function useChartTheme(): ChartTheme {
  return useSyncExternalStore(subscribeTheme, getThemeSnapshot, getThemeSnapshot);
}

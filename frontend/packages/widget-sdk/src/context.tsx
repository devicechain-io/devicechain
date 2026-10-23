// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The ambient host context a widget reads: the resolved subject, the display time zone, the
// theme root, the selection callback and the candidate provider.
//
// 🔴 THESE CONTEXTS ARE DEFINED HERE AND NOWHERE ELSE. A React context is identified by the
// OBJECT createContext returned, so two copies of this module — one bundled into a widget
// package, one in the host — are two unrelated contexts: the host's provider writes to one
// and the widget reads the other, and the selector silently does nothing. Exactly one
// definition, in this package, which `@devicechain/widgets` and every third-party widget
// package depend on as a PEER (never a dependency) so the host's install supplies the
// single copy.

import { createContext, useContext, useEffect, useRef, type ReactNode } from 'react';

import type { ResolvedFrame, SelectionCandidate, SelectionTarget, TimeRangeState } from '@devicechain/dashboards';

// WidgetSubjectContext carries the resolved entity a widget shows (its datasource's
// bound device/anchor), computed once by the renderer and provided per widget. The
// frame reads it as a subtitle so every board answers "which asset is this?" — and,
// because it reads the RESOLVED binding, it updates for free once selection re-points a
// slot (ADR-039 selection-overlay amendment). Kept as ambient context, not a widget
// prop, so the pure widget contract (widget, data) stays untouched and no widget needs
// to know how a subject is resolved.
const WidgetSubjectContext = createContext<string | undefined>(undefined);

// useWidgetSubject reads the resolved subject line (the frame's fallback subtitle).
export function useWidgetSubject(): string | undefined {
  return useContext(WidgetSubjectContext);
}

export function WidgetSubjectProvider({
  label,
  children,
}: {
  label: string | undefined;
  children: ReactNode;
}) {
  return <WidgetSubjectContext.Provider value={label}>{children}</WidgetSubjectContext.Provider>;
}

// WidgetTimeZone is the zone widgets print instants in. Undefined (the default) means the
// viewer's own zone, which is what every live host wants; a host replaying a recording
// pins one so the board's times agree with the recording's own clock.
const WidgetTimeZoneContext = createContext<string | undefined>(undefined);

export function WidgetTimeZoneProvider({
  timeZone,
  children,
}: {
  timeZone: string | undefined;
  children: ReactNode;
}) {
  return <WidgetTimeZoneContext.Provider value={timeZone}>{children}</WidgetTimeZoneContext.Provider>;
}

export function useWidgetTimeZone(): string | undefined {
  return useContext(WidgetTimeZoneContext);
}

// WidgetThemeRoot is the element chart colors are read from. Undefined (the default) means
// the document root, which is where a console or dashboard app sets its tokens. A host that
// scopes its tokens to a container (an embed on a page whose own stylesheet owns the root)
// provides that container, and charts resolve their colors there instead.
const WidgetThemeRootContext = createContext<Element | undefined>(undefined);

export function WidgetThemeRootProvider({
  root,
  children,
}: {
  root: Element | undefined;
  children: ReactNode;
}) {
  return <WidgetThemeRootContext.Provider value={root}>{children}</WidgetThemeRootContext.Provider>;
}

export function useWidgetThemeRoot(): Element | undefined {
  return useContext(WidgetThemeRootContext);
}

// The density scale the board renders at (0.9 compact / 1 comfortable / 1.5 wall), provided by
// the renderer. Outside a renderer it is 1.
const WidgetDensityContext = createContext<number>(1);

export function WidgetDensityProvider({ scale, children }: { scale: number; children: ReactNode }) {
  return <WidgetDensityContext.Provider value={scale}>{children}</WidgetDensityContext.Provider>;
}

export function useWidgetDensityScale(): number {
  return useContext(WidgetDensityContext);
}

// The size of the widget frame's padded content column in px (quantised), provided by
// WidgetFrame to everything rendered inside it. {0,0} until measured, and outside a frame.
export interface WidgetBodySize {
  width: number;
  height: number;
}
const ZERO_SIZE: WidgetBodySize = { width: 0, height: 0 };
const WidgetBodySizeContext = createContext<WidgetBodySize>(ZERO_SIZE);

export function WidgetBodySizeProvider({ size, children }: { size: WidgetBodySize; children: ReactNode }) {
  return <WidgetBodySizeContext.Provider value={size}>{children}</WidgetBodySizeContext.Provider>;
}

export function useWidgetBodySize(): WidgetBodySize {
  return useContext(WidgetBodySizeContext);
}

// WidgetSelect is a view-interaction callback (ADR-039 selection amendment): a widget
// drives a slot selection (an alarm-table originator drill, a context-selector pick) by
// naming its target slot + the entity to bind. Kept DISTINCT from WidgetActions (the
// write seam) — selection changes only the view's binding overlay, never the backend.
// Provided by the renderer from the host; absent (undefined) in edit mode and any host
// that wires no selection, so a widget feature-detects it before offering a drill.
export type WidgetSelect = (target: SelectionTarget) => void;

const WidgetSelectContext = createContext<WidgetSelect | undefined>(undefined);

export function WidgetSelectProvider({
  select,
  children,
}: {
  select: WidgetSelect | undefined;
  children: ReactNode;
}) {
  return <WidgetSelectContext.Provider value={select}>{children}</WidgetSelectContext.Provider>;
}

// useWidgetSelect returns the ambient selection callback, or undefined where selection is
// not wired (edit mode, a read-only embedder). A widget renders its drill affordance only
// when this is defined.
export function useWidgetSelect(): WidgetSelect | undefined {
  return useContext(WidgetSelectContext);
}

// WidgetCandidates is the ambient companion to WidgetSelect (ADR-039 selection amendment): a
// context/entity-selector widget asks it for the options a target slot may bind to. The host
// computes candidates from the definition's scope + resolved bindings (useSlotCandidates), so
// the widget stays presentational and the pure widget contract stays untouched. Distinct from
// the hub's data channels — candidates are a fetch-on-open list, not a live stream. Undefined
// where not wired (edit/preview, a host that supplies no selection) → the selector is inert.
export type WidgetCandidates = (slot: string) => Promise<SelectionCandidate[]>;

const WidgetCandidatesContext = createContext<WidgetCandidates | undefined>(undefined);

export function WidgetCandidatesProvider({
  candidates,
  children,
}: {
  candidates: WidgetCandidates | undefined;
  children: ReactNode;
}) {
  return <WidgetCandidatesContext.Provider value={candidates}>{children}</WidgetCandidatesContext.Provider>;
}

// useWidgetCandidates returns the ambient candidate provider, or undefined where selection is
// not wired. A selector widget renders its picker only when both this and useWidgetSelect are
// defined.
export function useWidgetCandidates(): WidgetCandidates | undefined {
  return useContext(WidgetCandidatesContext);
}

// WidgetFreshness is what the host knows about how current a widget's data is: the time of its
// newest sample (epoch ms) and the age past which that is stale. Provided only for a widget
// whose descriptor opts in (`freshness: true`); the frame reads it and draws the age readout.
// Absent, the frame draws nothing, so a widget that does not opt in is unchanged.
//
// `staleAfterSeconds` is undefined when no threshold is known, which means the age is shown
// and nothing is ever styled stale. `at` is undefined while no sample has arrived.
export interface WidgetFreshness {
  at: number | undefined;
  staleAfterSeconds: number | undefined;
}

const WidgetFreshnessContext = createContext<WidgetFreshness | undefined>(undefined);

export function WidgetFreshnessProvider({
  freshness,
  children,
}: {
  freshness: WidgetFreshness | undefined;
  children: ReactNode;
}) {
  return <WidgetFreshnessContext.Provider value={freshness}>{children}</WidgetFreshnessContext.Provider>;
}

export function useWidgetFreshness(): WidgetFreshness | undefined {
  return useContext(WidgetFreshnessContext);
}

// What the widget's bound device's ACTIVE profile declares, for a widget that has nothing to draw and
// needs to say whether the device will ever report what it asks for. `declared` is the declared
// measurement names, `null` when the profile cannot say (none published, no way to look), and
// `undefined` until it has been asked for and answered. Nothing is read until a widget calls
// `ask()` -- a board whose widgets all have data costs no lookup. An unknown declaration is read
// as declared, never as absent.
export interface WidgetDeclaredMetrics {
  declared: readonly string[] | null | undefined;
  ask: () => void;
}

const NO_DECLARED: WidgetDeclaredMetrics = { declared: undefined, ask: () => undefined };
const WidgetDeclaredMetricsContext = createContext<WidgetDeclaredMetrics>(NO_DECLARED);

export function WidgetDeclaredMetricsProvider({
  value,
  children,
}: {
  value: WidgetDeclaredMetrics;
  children: ReactNode;
}) {
  return <WidgetDeclaredMetricsContext.Provider value={value}>{children}</WidgetDeclaredMetricsContext.Provider>;
}

export function useWidgetDeclaredMetrics(): WidgetDeclaredMetrics {
  return useContext(WidgetDeclaredMetricsContext);
}

// The frame a board gives a widget: its canvas default merged with the widget's own override
// (resolveFrame in @devicechain/dashboards). Absent -- a widget mounted outside a board -- the
// frame is today's card.
const WidgetFrameStyleContext = createContext<ResolvedFrame | undefined>(undefined);

export function WidgetFrameStyleProvider({ frame, children }: { frame: ResolvedFrame | undefined; children: ReactNode }) {
  return <WidgetFrameStyleContext.Provider value={frame}>{children}</WidgetFrameStyleContext.Provider>;
}

export function useWidgetFrameStyle(): ResolvedFrame | undefined {
  return useContext(WidgetFrameStyleContext);
}

// The board's time range: the span its time-series widgets draw (seconds back from now) and
// whether the viewer has frozen the board. Provided by the renderer. A widget that draws
// history reads it to label its axis or to ask its own questions; the built-in time-series chart
// needs no code of its own, because the host sizes its window from the same value. `pausedAt`
// is set exactly while `paused`, and is the instant the window ends at while frozen.
//
// Undefined outside a board (a widget mounted on its own), which is "no range is imposed" -- not
// a default span. A widget must handle that rather than assume an hour.
const WidgetTimeRangeContext = createContext<TimeRangeState | undefined>(undefined);

export function WidgetTimeRangeProvider({
  range,
  children,
}: {
  range: TimeRangeState | undefined;
  children: ReactNode;
}) {
  return <WidgetTimeRangeContext.Provider value={range}>{children}</WidgetTimeRangeContext.Provider>;
}

export function useWidgetTimeRange(): TimeRangeState | undefined {
  return useContext(WidgetTimeRangeContext);
}

// ---- Chart image export --------------------------------------------------------------------
//
// A chart widget draws into a canvas the frame cannot reach, and the frame owns the export menu. So
// the chart wrapper (<EChart>) REGISTERS a way to read its image, and the frame offers "Download
// image" only while one is registered: a widget that draws no chart has nothing to register and is
// offered no image. The registrar is provided by the frame around its content; outside a frame it is
// absent and registering does nothing.

/** Reads the chart as a PNG data URL on the given background (a canvas is transparent), or undefined while it has none. */
export type WidgetImageSource = (options: { backgroundColor: string }) => string | undefined;

const WidgetImageRegistrarContext = createContext<((source: WidgetImageSource | undefined) => void) | undefined>(undefined);

export function WidgetImageRegistrarProvider({
  register,
  children,
}: {
  register: (source: WidgetImageSource | undefined) => void;
  children: ReactNode;
}) {
  return <WidgetImageRegistrarContext.Provider value={register}>{children}</WidgetImageRegistrarContext.Provider>;
}

/**
 * Offers the caller's image to the enclosing frame while `source` is given. It may change every
 * render; passing `undefined` withdraws it (a chart that has not drawn yet has no image to offer).
 */
export function useRegisterWidgetImage(source: WidgetImageSource | undefined): void {
  const register = useContext(WidgetImageRegistrarContext);
  const latest = useRef(source);
  latest.current = source;
  const offered = source !== undefined;
  useEffect(() => {
    if (!register || !offered) return;
    register((options) => latest.current?.(options));
    return () => register(undefined);
  }, [register, offered]);
}

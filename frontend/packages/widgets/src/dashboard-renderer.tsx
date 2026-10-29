// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardRenderer maps a parsed DashboardDefinition onto a real CSS Grid of SQUARE cells: the
// canvas is `display:grid` with `repeat(columns, 1fr)`, so the board FILLS whatever width its
// container gives it, and every row is as tall as a column is wide (the cell size is computed in
// CSS from the grid's own width, `100cqw`, so it needs no measurement and has no first-frame
// flash). A `grid` widget occupies its footprint (`grid-column`/`grid-row`); a `relative` widget
// is drawn over its host's area at the host's anchor point, offset in cell pitches, and above
// every grid widget. Gutters and padding are lengths or spacing tokens scaled by the density;
// each widget's frame is the canvas default merged with its own (resolveFrame).
//
// Adaptive columns and reflow: the board measures its CONTAINER (not the window), and while a
// cell at the authored column count is at least `minCellSize` it renders as authored and the
// cells stretch. Narrower than that the column count drops to what fits and the base layout is
// poured into it in reading order (effectiveLayout), each widget clamped into the span limits
// its descriptor declares; below STACK_BELOW_WIDTH (a phone) that is a single column. A breakpoint the author defined for the width wins over the automatic
// reflow. Until the container has a measured width the authored layout is drawn. Container sizing
// (fill / fixed-width / fixed-height) is a mount knob the host may override via the `sizing`
// prop. It is view-only; the canvas editor lives in the console. This is the shared viewer the
// console and the reference /dash app mount.

import {
  anchorFraction,
  DEFAULT_CANVAS_DENSITY,
  DEFAULT_TIME_RANGE_SECONDS,
  defaultHistoryWindow,
  effectiveLayout,
  historyWindowFor,
  initialTimeRange,
  fetchWidgetHistory,
  layoutShape,
  resolveFrame,
  resolvePlacement,
  resolveWidgetDatasource,
  spanLimitsOfLayout,
  UNKNOWN_WIDGET_TYPE,
  type CanvasBackground,
  type CanvasDensity,
  type CanvasGrid,
  type CanvasSizing,
  type DashboardDefinition,
  type GridLength,
  type GridPlacement,
  type RelativePlacement,
  type WidgetActions,
  type WidgetDataSource,
  type MeasurementSample,
  type SlotBinding,
  type TimeRangeState,
  type WidgetFrameSpec,
  type WidgetInstance,
} from '@devicechain/dashboards';
import {
  densityScale,
  tokenStyle,
  WidgetDensityProvider,
  WidgetFrameStyleProvider,
  WidgetTimeRangeProvider,
  WidgetTokenStyles,
  type WidgetRegistry,
} from '@devicechain/widget-sdk';
import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode, type RefObject } from 'react';

import { measurementSpan, takesHistorySeed } from './binding-policy';
import { ConnectedWidget } from './connected-widget';
import {
  type WidgetCandidates,
  WidgetCandidatesProvider,
  type WidgetSelect,
  WidgetSelectProvider,
  WidgetSubjectProvider,
  WidgetThemeRootProvider,
  WidgetTimeZoneProvider,
} from '@devicechain/widget-sdk';
import { defaultWidgetRegistry } from './default-registry';

export interface DashboardRendererProps {
  definition: DashboardDefinition;
  hub: WidgetDataSource;
  // The widget registry that resolves every widget's component, channel, binding policy and
  // history eligibility. Omitted = the built-ins. A host with extension packages passes the
  // SAME registry it parsed `definition` with (parseDashboardDefinition(raw, { catalog:
  // registry.catalog })), so an extension widget is recognized and rendered consistently.
  registry?: WidgetRegistry;
  // The action seam (writes) for acting widgets (alarm ack/clear, command send). Omit
  // for a strictly read-only mount; the same instance as `hub` when the runtime provides
  // both (DashboardHub / SyntheticDataSource implement WidgetActions).
  actions?: WidgetActions;
  // Whether to backfill each widget from bucketedMeasurements (a real backend call).
  // Default true; set false for offline preview, where the data source (e.g.
  // SyntheticDataSource) supplies its own history and the backend must not be hit.
  seedHistory?: boolean;
  // The settled slot manifest (useResolvedBindings). Every widget's `slot` selector is
  // resolved through it HERE, before the widget subscribes, to the bound device or anchor
  // — or to an explicit `unbound` selector when the slot has no binding. The hub holds no
  // bindings, so a host keeps ONE hub (useDashboardHub) and a change to this map
  // re-subscribes only the widgets whose slot moved. Omitted = no slot is bound.
  bindings?: Record<string, SlotBinding>;
  // Host override for the definition's container sizing (the embed knob — a host can
  // force a dashboard authored `fill` into a fixed-px box, or vice versa). Omitted →
  // the definition's own `canvas.sizing`.
  sizing?: CanvasSizing;
  // The selection callback (ADR-039 selection amendment): a widget (e.g. an alarm-table
  // originator drill) calls it to re-point a slot; the host accumulates it into the
  // selection overlay that produces `bindings`. Omit for a static dashboard — widgets
  // then render no drill affordance.
  select?: WidgetSelect;
  // The candidate provider (useSlotCandidates) an entity-selector widget reads to offer a
  // target slot's options. Omit alongside `select` for a static/edit/preview mount — the
  // selector then renders inert.
  candidates?: WidgetCandidates;
  // The IANA zone widgets print instants in. Omit for the viewer's own zone (every live
  // host); a host replaying a recording pins one so the board agrees with the recording.
  timeZone?: string;
  // The element chart colors are read from, when the host scopes its design tokens to a
  // container rather than the document root. Omit for the document root (every other host).
  themeRoot?: Element;
  // A viewer's density override (the /dash app's `?density=`). Wins over the definition's
  // authored `canvas.density`, which wins over `comfortable`. Density scales every size token
  // (font, spacing) and the floor of the value-font curve, never the curve itself.
  density?: CanvasDensity;
  // The board's time range: the span its time-series widgets draw, and whether the viewer has
  // frozen it. Owned by the host (useBoardTimeRange + TimeRangeBar) so the control can live in the
  // host's own header. Omitted = live over the definition's default span (or one hour), with no
  // control -- a static embed, the replay page, an edit preview.
  timeRange?: TimeRangeState;
}

export function DashboardRenderer({
  definition,
  hub,
  registry: registryProp,
  actions,
  seedHistory = true,
  bindings,
  sizing,
  select,
  candidates,
  timeZone,
  themeRoot,
  density,
  timeRange,
}: DashboardRendererProps) {
  // Each widget with its slot resolved to a concrete selector. Recomputed per render;
  // that is cheap, and every channel keys its subscription on the selector's VALUE, so a
  // widget whose concrete selector did not change keeps its subscription.
  const widgets = useMemo(() => resolveWidgets(definition.widgets, bindings), [definition.widgets, bindings]);
  const registry = registryProp ?? defaultWidgetRegistry();
  // The default is derived, not stored: a host with no control still gets the board's own span.
  const defaultSeconds = definition.timeRange?.defaultSeconds;
  const range = useMemo(
    () => timeRange ?? initialTimeRange(defaultSeconds),
    [timeRange, defaultSeconds],
  );
  const { histories, reseed } = useWidgetHistories(widgets, seedHistory, registry, range);
  // Each fixed window is its own board state: entering, leaving or moving one remounts the widgets,
  // so no live buffer (samples from after the window) and no held snapshot carries across it.
  const windowKey = windowTag(range);

  const { grid, background: bg, frame: canvasFrame } = definition.canvas;
  const effectiveSizing = sizing ?? definition.canvas.sizing;
  const effectiveDensity = density ?? definition.canvas.density ?? DEFAULT_CANVAS_DENSITY;
  const scale = densityScale(effectiveDensity);
  // The width is kept only when it changes the layout's shape (breakpoint, columns, single
  // column): within one shape the cells stretch through CSS, so a resize redraws nothing until
  // a column comes or goes.
  const { canvas } = definition;
  const shapeKey = useCallback((width: number) => JSON.stringify(layoutShape(canvas, width, scale)), [canvas, scale]);
  const [boardRef, containerWidth] = useContainerWidth<HTMLDivElement>(shapeKey);
  const layout = useMemo(
    () =>
      effectiveLayout(definition, containerWidth, {
        scale,
        limitsOf: (w) => (w.type === UNKNOWN_WIDGET_TYPE ? undefined : spanLimitsOfLayout(registry.descriptor(w.type)?.layout)),
      }),
    [definition, containerWidth, scale, registry],
  );
  const { breakpoint, columns } = layout;
  const lengths = gridCss(grid, columns);

  return (
    <div
      ref={boardRef}
      data-density={effectiveDensity}
      data-columns={columns}
      data-time-paused={range.paused ? '' : undefined}
      style={{ ...sizingStyle(effectiveSizing, bg), ...tokenStyle(scale) }}
    >
      <WidgetTokenStyles />
      <WidgetDensityProvider scale={scale}>
      <WidgetThemeRootProvider root={themeRoot}>
      <WidgetTimeZoneProvider timeZone={timeZone}>
      <WidgetTimeRangeProvider range={range}>
      <WidgetSelectProvider select={select}>
        <WidgetCandidatesProvider candidates={candidates}>
        <div style={{ padding: lengths.padding }}>
          {/* The query container: its width is the grid's, so 100cqw below is the grid width. */}
          <div style={{ containerType: 'inline-size' }}>
            <div
              data-grid=""
              style={{
                display: 'grid',
                gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
                gridAutoRows: CELL,
                columnGap: lengths.gutterX,
                rowGap: lengths.gutterY,
                width: '100%',
                ...({ '--dc-cell': lengths.cell } as CSSProperties),
              }}
            >
              <PlacedWidgets
                widgets={widgets}
                breakpoint={breakpoint}
                columns={columns}
                grid={layout.grid}
                lengths={lengths}
                render={(widget) => (
                  <WidgetFrameStyleProvider frame={resolveFrame(canvasFrame, widgetFrame(widget))}>
                    <WidgetSubjectProvider label={widgetSubjectLabel(widget)}>
                      <ConnectedWidget
                        key={windowKey}
                        widget={widget}
                        hub={hub}
                        registry={registry}
                        actions={actions}
                        initialSamples={histories[widget.id]?.samples}
                        initialSamplesAsOf={histories[widget.id]?.asOf}
                        onReconnect={() => reseed(widget.id)}
                      />
                    </WidgetSubjectProvider>
                  </WidgetFrameStyleProvider>
                )}
              />
            </div>
          </div>
        </div>
        </WidgetCandidatesProvider>
      </WidgetSelectProvider>
      </WidgetTimeRangeProvider>
      </WidgetTimeZoneProvider>
      </WidgetThemeRootProvider>
      </WidgetDensityProvider>
    </div>
  );
}

// The live cell size, as the grid element defines it.
const CELL = 'var(--dc-cell)';

// The grid's lengths as CSS: px multiply by the density scale like every size token, and a
// spacing token reads the board's own variable.
export interface GridCss {
  gutterX: string;
  gutterY: string;
  padding: string;
  // The live cell size: the grid's width less its gutters, shared by the columns. Read inside
  // the query container, so `100cqw` is the grid's width.
  cell: string;
}

export function gridLengthCss(len: GridLength): string {
  if (typeof len === 'number') return `calc(${len}px * var(--dc-scale, 1))`;
  return len === 'space-0' ? '0px' : `var(--dc-${len})`;
}

export function gridCss(grid: CanvasGrid, columns: number): GridCss {
  const g = grid.gutter;
  const gutterX = gridLengthCss(typeof g === 'object' ? g.x : g);
  const gutterY = gridLengthCss(typeof g === 'object' ? g.y : g);
  const p = grid.padding;
  const padding =
    typeof p === 'object'
      ? [p.top, p.right, p.bottom, p.left].map(gridLengthCss).join(' ')
      : gridLengthCss(p);
  return { gutterX, gutterY, padding, cell: `calc((100cqw - ${columns - 1} * ${gutterX}) / ${columns})` };
}

// The frame override a widget carries (a placeholder carries none: its stored frame is the
// newer release's to read).
function widgetFrame(widget: WidgetInstance): WidgetFrameSpec | undefined {
  return widget.type === UNKNOWN_WIDGET_TYPE ? undefined : widget.frame;
}

// gridItemStyle maps a grid placement to a grid-item style. col/row are 0-based cells →
// CSS's 1-based `col+1 / span cols`. It CLAMPS the placement into the grid's column count: a
// footprint whose `col`/`cols` overruns `columns` (a hand-authored or imported one; the lint
// reports it) would otherwise land in implicit `auto` tracks CSS appends past the `1fr`
// columns — unequal widths + horizontal overflow instead of a clamped placement. Rows are
// unbounded (implicit rows are the intended vertical growth), so only columns clamp.
export function gridItemStyle(p: GridPlacement, columns: number): CSSProperties {
  const col = Math.min(Math.max(0, p.col), columns - 1);
  const cols = Math.min(Math.max(1, p.cols), columns - col);
  return {
    gridColumn: `${col + 1} / span ${cols}`,
    gridRow: `${p.row + 1} / span ${p.rows}`,
    // Let a grid item shrink below its content's intrinsic size so a wide chart/table
    // fits its track instead of overflowing it.
    minWidth: 0,
    minHeight: 0,
  };
}

// Relative widgets paint above every grid widget, in `layer` order among themselves.
const RELATIVE_Z = 100;

// relativeBoxStyle positions a relative widget inside its host's box (the containing block): the
// host's anchor point (a percentage of the host), minus this widget's own `self` point (a
// percentage of itself, via translate), plus the offset in cell pitches.
export function relativeBoxStyle(p: RelativePlacement, lengths: Pick<GridCss, 'gutterX' | 'gutterY'>): CSSProperties {
  const a = anchorFraction(p.anchor);
  const s = anchorFraction(p.self ?? p.anchor);
  const span = (n: number, gutter: string) => `calc(${n} * ${CELL} + ${n - 1} * ${gutter})`;
  const pitches = (n: number, gutter: string) => `${n} * (${CELL} + ${gutter})`;
  const fill = 'fill' in p.size;
  return {
    position: 'absolute',
    left: `calc(${a.fx * 100}% + ${pitches(p.offset?.x ?? 0, lengths.gutterX)})`,
    top: `calc(${a.fy * 100}% + ${pitches(p.offset?.y ?? 0, lengths.gutterY)})`,
    width: fill ? '100%' : span((p.size as { cols: number }).cols, lengths.gutterX),
    height: fill ? '100%' : span((p.size as { rows: number }).rows, lengths.gutterY),
    transform: `translate(${-s.fx * 100}%, ${-s.fy * 100}%)`,
    zIndex: RELATIVE_Z + (p.layer ?? 0),
    pointerEvents: 'auto',
  };
}

// PlacedWidgets draws every widget at a breakpoint: each grid widget as a grid item, and each
// relative widget inside a layer over its host's grid area (a relative widget hosted on another
// relative widget is drawn inside that one's box, one level deep, as the parser allows).
function PlacedWidgets({
  widgets,
  breakpoint,
  columns,
  grid,
  lengths,
  render,
}: {
  widgets: WidgetInstance[];
  breakpoint: string;
  columns: number;
  // Where each grid widget is DRAWN: its authored footprint, or the reflowed one.
  grid: ReadonlyMap<string, GridPlacement>;
  lengths: GridCss;
  render: (widget: WidgetInstance) => ReactNode;
}) {
  const placed = widgets.map((w) => ({ widget: w, placement: resolvePlacement(w.layout, breakpoint) }));
  const dependents = new Map<string, { widget: WidgetInstance; placement: RelativePlacement }[]>();
  for (const { widget, placement } of placed) {
    if (placement.mode !== 'relative') continue;
    dependents.set(placement.host, [...(dependents.get(placement.host) ?? []), { widget, placement }]);
  }
  const relative = (host: string): ReactNode =>
    (dependents.get(host) ?? []).map(({ widget, placement }) => (
      // data-widget names the box a widget occupies, so a check over the rendered board (the
      // readability gate in apps/board-replay) can find it.
      <div key={widget.id} data-widget={widget.id} data-host={host} style={relativeBoxStyle(placement, lengths)}>
        {render(widget)}
        {relative(widget.id)}
      </div>
    ));
  return (
    <>
      {placed.map(({ widget, placement }) => {
        if (placement.mode !== 'grid') return null;
        const style = gridItemStyle(grid.get(widget.id) ?? placement, columns);
        // Keyed by the widget, so removing or reordering widgets never remounts (and so never
        // re-subscribes) the ones that stay.
        return (
          <Fragment key={widget.id}>
            <div data-widget={widget.id} style={style}>
              {render(widget)}
            </div>
            {dependents.has(widget.id) ? (
              // The host's area again, as a layer for its relative widgets to be positioned in.
              // It takes no pointer events itself, so the host underneath stays usable.
              <div style={{ ...style, position: 'relative', pointerEvents: 'none', zIndex: RELATIVE_Z }}>
                {relative(widget.id)}
              </div>
            ) : null}
          </Fragment>
        );
      })}
    </>
  );
}

// sizingStyle turns the container-sizing knob into the outer wrapper's box. `fill`
// takes the mount container's full width+height (the grid fills it); `{width}` caps
// the width (the fluid grid adjusts within it); `{height}` pins the height (rows
// scroll). All scroll internally so the board owns its single scroll region. The
// background (color AND image) paints on this wrapper so it covers the whole container
// even when the grid's rows are shorter than the sizing box.
export function sizingStyle(sizing: CanvasSizing, bg: CanvasBackground | undefined): CSSProperties {
  const base: CSSProperties = {
    overflow: 'auto',
    backgroundColor: bg?.color ?? undefined,
    backgroundImage: bg?.imageUrl ? `url(${bg.imageUrl})` : undefined,
    backgroundSize: 'cover',
  };
  if (sizing === 'fill') return { ...base, width: '100%', height: '100%' };
  if ('width' in sizing) return { ...base, width: sizing.width, maxWidth: '100%', height: '100%' };
  return { ...base, width: '100%', height: sizing.height };
}

// resolveWidgets maps each widget's datasource through the settled bindings
// (resolveWidgetDatasource): a slot becomes the device or anchor it is bound to, or an
// explicit `unbound` selector. A widget with no datasource keeps none — that is a
// different fact (tenant-wide on the alarm channel) and must not be manufactured here.
export function resolveWidgets(
  widgets: WidgetInstance[],
  bindings: Record<string, SlotBinding> | undefined,
): WidgetInstance[] {
  return widgets.map((widget) => {
    if (widget.type === UNKNOWN_WIDGET_TYPE || widget.datasource?.kind !== 'slot') return widget;
    return { ...widget, datasource: resolveWidgetDatasource(widget.datasource, bindings) };
  });
}

// widgetSubjectLabel names the entity a widget shows — its CONCRETE datasource's device
// or anchor — for the frame subtitle. Pass the widget as the renderer resolved it
// (resolveWidgets), so the subtitle is read from the same selector the widget subscribes
// with and can never name an entity other than the one being shown. Returns undefined for
// a datasource-free widget (label/image, a tenant-wide alarm tile), an unbound slot, an
// unresolved slot, or a reserved selector kind — the frame then shows no subtitle.
export function widgetSubjectLabel(widget: WidgetInstance): string | undefined {
  const ds = widget.datasource;
  if (!ds) return undefined;
  if (ds.kind === 'device') return ds.deviceToken || undefined;
  if (ds.kind === 'anchor') return ds.anchor.targetToken;
  return undefined;
}

// useContainerWidth tracks an element's content width with a ResizeObserver, measured
// synchronously before first paint so a narrow container never flashes the authored layout. 0
// means "not measured" (no layout engine, or not yet laid out) and draws the authored layout.
// A new width is taken only when `keyOf` tells it apart from the one held, so a resize that
// does not change what is drawn does not re-render; a new `keyOf` re-measures under its rule.
function useContainerWidth<T extends HTMLElement>(keyOf: (width: number) => string): [RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  const keyOfRef = useRef(keyOf);
  const measure = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const next = el.clientWidth;
    setWidth((prev) => (keyOfRef.current(prev) === keyOfRef.current(next) ? prev : next));
  }, []);
  useLayoutEffect(() => {
    keyOfRef.current = keyOf;
    measure();
  }, [keyOf, measure]);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [measure]);
  return [ref, width];
}

// A widget's seed, tagged with the concrete selector it was fetched for, and when it was read up to.
interface Seed {
  key: string;
  // The fixed window it was read for ('' for a rolling range): see windowTag.
  window: string;
  samples: MeasurementSample[];
  asOf: number;
}

// A fixed window's identity, '' for a rolling range. A seed belongs to the window it was read for:
// one read for the live board ends now, and handed to a window yesterday it would draw the last
// hour under a "Fixed window" label (two windows of the same length differ only by this).
function windowTag(range: TimeRangeState): string {
  return range.absolute ? `${range.absolute.fromMs}-${range.absolute.toMs}` : '';
}

// What a measurement widget is handed: its seed and the instant the seed was read up to.
interface WidgetHistory {
  samples: MeasurementSample[];
  asOf: number;
}

// How long a failed re-read after an interruption waits before it is tried again. Until one lands
// the widget draws the outage as a gap, so a failure never shows as a quiet stretch of line.
export const RESEED_RETRY_MS = 30_000;

// seedKey is the identity of what a measurement widget's history is OF: its concrete
// selector, by value. Undefined for a widget that takes no seed.
function seedKey(widget: WidgetInstance, registry: WidgetRegistry): string | undefined {
  // Eligibility is registry metadata (takesHistorySeed). A placeholder, an unregistered id and every
  // other channel take none.
  const descriptor = widget.type === UNKNOWN_WIDGET_TYPE ? undefined : registry.descriptor(widget.type);
  if (!descriptor || !takesHistorySeed(descriptor, widget)) {
    return undefined;
  }
  return JSON.stringify(widget.datasource ?? null);
}

// useWidgetHistories backfills each measurement widget from bucketedMeasurements and
// returns an id→seed map. Each seed is keyed by the widget's OWN concrete selector,
// not by the board's bindings as a whole, so:
//
//   - a selection that re-points one slot re-fetches only the widgets bound to it;
//   - a seed fetched for a selector the widget no longer has is DROPPED at render,
//     synchronously, so a widget never shows the previous device's history under the new
//     device while the new fetch is pending;
//   - a slow answer for a superseded selector is discarded when it lands.
//
// `reseed(id)` reads a widget's history again after its live feed resumed from an interruption.
// It supersedes any read of that widget still in flight (one asked for before the outage cannot
// cover it), and the answer is handed over with the instant it was read up to, which is how the
// widget knows it covers the outage. A failed re-read keeps the seed on screen (the widget then
// draws the outage as a gap) and is tried again after RESEED_RETRY_MS. While the board is paused
// the re-read waits: a frozen board shows the moment it froze, and history up to that moment
// cannot cover an outage after it. Resuming reads it.
//
// A widget's samples array is the one stored for its seed, so an unchanged seed keeps
// its identity across renders and does not churn the stream's merge.
function useWidgetHistories(
  widgets: WidgetInstance[],
  enabled: boolean,
  registry: WidgetRegistry,
  range: TimeRangeState,
): { histories: Record<string, WidgetHistory | undefined>; reseed: (id: string) => void } {
  const [seeds, setSeeds] = useState<Record<string, Seed>>({});
  // widget id → the (selector, span, re-read) ask most recently REQUESTED for it. A fetch whose
  // ask is no longer the requested one is stale when it lands.
  const requested = useRef<Record<string, string>>({});
  // widget id → how many re-reads its live feed has asked for (part of the ask). A read asked for
  // before the latest interruption that lands before the effect below has run is still taken, but
  // it cannot close the gap: it is handed over with the instant it was read up to, which is before
  // the interruption (useMeasurementStream).
  const reseeds = useRef<Record<string, number>>({});
  const [reseedTick, bumpReseeds] = useState(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const reseed = useCallback((id: string) => {
    reseeds.current[id] = (reseeds.current[id] ?? 0) + 1;
    bumpReseeds((n) => n + 1);
  }, []);

  // The (id, key, span) triples that want a seed; value-compared so an equal render is a no-op.
  // `span` is the seconds a TIME-RANGED widget draws (null for one that is not, which keeps the
  // fixed default seed): a new span re-fetches, but the seed on screen stays until the new one lands,
  // because it is the same device's history and the stream prunes it to the span anyway.
  const wanted = widgets.flatMap((w) => {
    const key = seedKey(w, registry);
    if (key === undefined) return [];
    const descriptor = w.type === UNKNOWN_WIDGET_TYPE ? undefined : registry.descriptor(w.type);
    const span = descriptor ? (measurementSpan(descriptor, w, range.spanSeconds) ?? null) : null;
    return [[w.id, key, span] as const];
  });
  const tag = windowTag(range);
  const wantedKey = JSON.stringify([tag, wanted]);

  useEffect(() => {
    // Preview (enabled=false) must not touch the backend; clear any prior seed so a
    // toggle from live→preview doesn't leave stale real history under synthetic data.
    if (!enabled) {
      requested.current = {};
      setSeeds({});
      return;
    }
    const now = Date.now();
    const byId = new Map(widgets.map((w) => [w.id, w]));
    const next: Record<string, string> = {};
    for (const [id, key, span] of wanted) {
      // What was asked for: the selector, the span and the re-reads. Equal = fetched, or in flight.
      const base = `${key}|${span ?? ''}|${tag}|`;
      const n = reseeds.current[id] ?? 0;
      // A read made while frozen ends at the freeze instant, so it answers no re-read: it is asked
      // as `p`, and resuming reads the outage back.
      const asked = `${base}${range.paused && n ? 'p' : n}`;
      const previous = requested.current[id];
      // Already asked; or only the re-read is new and the board is frozen, so it waits for resume.
      if (previous === asked || (range.paused && previous?.startsWith(base))) {
        next[id] = previous!;
        continue;
      }
      next[id] = asked;
      const widget = byId.get(id);
      // Only a single device has stored history to read (fetchWidgetHistory answers any other
      // selector with nothing, at once). That empty answer is not history across an outage, and
      // taken as one it would wipe what the chart drew before it: such a widget gets no seed.
      if (widget?.datasource?.kind !== 'device') continue;
      // A frozen board reads up to the freeze instant, so a range change while paused re-fetches
      // the same moment at the new span instead of sliding forward.
      const end = range.paused && range.pausedAt !== undefined ? range.pausedAt : now;
      // A widget that is not time-ranged reads its fixed hour back from the window's end under a
      // fixed window (the value AS OF that window), and back from now otherwise.
      const historyWindow =
        span !== null ? historyWindowFor(span, end) : range.absolute ? historyWindowFor(DEFAULT_TIME_RANGE_SECONDS, end) : defaultHistoryWindow();
      const current = () => mounted.current && requested.current[id] === asked;
      void fetchWidgetHistory(widget, historyWindow, { rethrow: n > 0 }).then(
        (samples) => {
          if (!current()) return; // superseded or unmounted
          // `asOf`: what the read covers up to (the fixed hour reads to now, a moment after `now`).
          setSeeds((prev) => ({ ...prev, [id]: { key, window: tag, samples, asOf: end } }));
        },
        () => {
          // Only a re-read rejects. The seed on screen stays, the outage stays a gap, and it is
          // read again later -- unless something newer has been asked for meanwhile, or the board
          // has gone (current() says both).
          setTimeout(() => current() && reseed(id), RESEED_RETRY_MS);
        },
      );
    }
    requested.current = next;
    // `widgets`/`wanted` are read via wantedKey (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantedKey, enabled, reseedTick, range.paused]);

  // Hand each widget its seed ONLY if it was fetched for the selector the widget has now.
  const histories = useMemo(() => {
    const out: Record<string, WidgetHistory | undefined> = {};
    if (!enabled) return out;
    for (const w of widgets) {
      const seed = seeds[w.id];
      out[w.id] = seed && seed.key === seedKey(w, registry) && seed.window === tag ? seed : undefined;
    }
    return out;
  }, [widgets, seeds, enabled, registry, tag]);
  return { histories, reseed };
}

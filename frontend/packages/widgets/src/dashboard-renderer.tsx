// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardRenderer maps a parsed DashboardDefinition onto a real CSS Grid (ADR-039
// amendment 2026-07-08): the canvas is `display:grid` with `repeat(columns, 1fr)`, so
// the board FILLS whatever width its container gives it; each widget is placed by span
// (`grid-column`/`grid-row`), layered by `z`, and nudged by an optional signed-pixel
// `offset`. Container sizing (fill / fixed-width / fixed-height) is a mount knob the
// host may override via the `sizing` prop. It is view-only — the canvas editor lives in
// the console. This is the shared viewer the console and the reference /dash app mount.

import {
  activeBreakpoint,
  defaultHistoryWindow,
  fetchWidgetHistory,
  resolveWidgetBox,
  resolveWidgetDatasource,
  UNKNOWN_WIDGET_TYPE,
  type Breakpoints,
  type CanvasBackground,
  type CanvasSizing,
  type DashboardDefinition,
  type WidgetActions,
  type WidgetBox,
  type WidgetDataSource,
  type MeasurementSample,
  type SlotBinding,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';

import { ConnectedWidget } from './connected-widget';
import {
  WidgetCandidatesProvider,
  WidgetSelectProvider,
  WidgetSubjectProvider,
  type WidgetCandidates,
  type WidgetSelect,
} from './frame';
import { WIDGET_CHANNEL } from './registry';

export interface DashboardRendererProps {
  definition: DashboardDefinition;
  hub: WidgetDataSource;
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
}

export function DashboardRenderer({
  definition,
  hub,
  actions,
  seedHistory = true,
  bindings,
  sizing,
  select,
  candidates,
}: DashboardRendererProps) {
  const breakpoint = useActiveBreakpoint(definition.canvas.breakpoints);
  // Each widget with its slot resolved to a concrete selector. Recomputed per render;
  // that is cheap, and every channel keys its subscription on the selector's VALUE, so a
  // widget whose concrete selector did not change keeps its subscription.
  const widgets = useMemo(() => resolveWidgets(definition.widgets, bindings), [definition.widgets, bindings]);
  const histories = useWidgetHistories(widgets, seedHistory);

  const { grid, background: bg } = definition.canvas;
  const rowGap = typeof grid.gap === 'number' ? grid.gap : grid.gap.row;
  const colGap = typeof grid.gap === 'number' ? grid.gap : grid.gap.col;
  const effectiveSizing = sizing ?? definition.canvas.sizing;

  return (
    <div style={sizingStyle(effectiveSizing, bg)}>
      <WidgetSelectProvider select={select}>
        <WidgetCandidatesProvider candidates={candidates}>
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: `repeat(${grid.columns}, minmax(0, 1fr))`,
            gridAutoRows: `${grid.rowHeight}px`,
            columnGap: colGap,
            rowGap,
            width: '100%',
          }}
        >
          {widgets.map((widget) => {
            const box = resolveWidgetBox(widget.layout, breakpoint);
            return (
              <div key={widget.id} style={gridItemStyle(box, grid.columns)}>
                <WidgetSubjectProvider label={widgetSubjectLabel(widget)}>
                  <ConnectedWidget
                    widget={widget}
                    hub={hub}
                    actions={actions}
                    initialSamples={histories[widget.id]}
                  />
                </WidgetSubjectProvider>
              </div>
            );
          })}
        </div>
        </WidgetCandidatesProvider>
      </WidgetSelectProvider>
    </div>
  );
}

// gridItemStyle maps a span box to a grid-item style. col/row are 0-based lines →
// CSS's 1-based `col+1 / span colSpan`. It CLAMPS the placement into the grid's
// column count: a box whose `col`/`colSpan` overruns `columns` (a shrunk grid, a
// hand-authored/overflowing stored box, a boundary-drag rounding edge) would
// otherwise land in implicit `auto` tracks CSS appends past the `1fr` columns —
// unequal widths + horizontal overflow instead of a clamped placement. Rows are
// unbounded (implicit rows are the intended vertical growth), so only columns clamp.
export function gridItemStyle(box: WidgetBox, columns: number): CSSProperties {
  const col = Math.min(Math.max(0, box.col), columns - 1);
  const colSpan = Math.min(Math.max(1, box.colSpan), columns - col);
  return {
    gridColumn: `${col + 1} / span ${colSpan}`,
    gridRow: `${box.row + 1} / span ${box.rowSpan}`,
    zIndex: box.z,
    transform: box.offset ? `translate(${box.offset.x}px, ${box.offset.y}px)` : undefined,
    // Let a grid item shrink below its content's intrinsic size so a wide chart/table
    // fits its track instead of overflowing it.
    minWidth: 0,
    minHeight: 0,
  };
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

// useActiveBreakpoint tracks the viewport width and returns the active breakpoint
// name, recomputing on resize.
function useActiveBreakpoint(breakpoints: Breakpoints): string {
  const [width, setWidth] = useState(() =>
    typeof window === 'undefined' ? 0 : window.innerWidth,
  );
  useEffect(() => {
    const onResize = () => setWidth(window.innerWidth);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  return activeBreakpoint(breakpoints, width);
}

// A widget's seed, tagged with the concrete selector it was fetched for.
interface Seed {
  key: string;
  samples: MeasurementSample[];
}

// seedKey is the identity of what a measurement widget's history is OF: its concrete
// selector, by value. Undefined for a widget that takes no seed.
function seedKey(widget: WidgetInstance): string | undefined {
  if (widget.type === UNKNOWN_WIDGET_TYPE || WIDGET_CHANNEL[widget.type] !== 'measurement') return undefined;
  return JSON.stringify(widget.datasource ?? null);
}

// useWidgetHistories backfills each measurement widget from bucketedMeasurements and
// returns an id→samples map. Each seed is keyed by the widget's OWN concrete selector,
// not by the board's bindings as a whole, so:
//
//   - a selection that re-points one slot re-fetches only the widgets bound to it;
//   - a seed fetched for a selector the widget no longer has is DROPPED at render,
//     synchronously, so a widget never shows the previous device's history under the new
//     device while the new fetch is pending;
//   - a slow answer for a superseded selector is discarded when it lands.
//
// A widget's samples array is the one stored for its seed, so an unchanged seed keeps
// its identity across renders and does not churn the stream's merge.
function useWidgetHistories(
  widgets: WidgetInstance[],
  enabled: boolean,
): Record<string, MeasurementSample[] | undefined> {
  const [seeds, setSeeds] = useState<Record<string, Seed>>({});
  // widget id → the selector key most recently REQUESTED for it. A fetch whose key is no
  // longer the requested one is stale when it lands.
  const requested = useRef<Record<string, string>>({});
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // The (id, key) pairs that want a seed; value-compared so an equal render is a no-op.
  const wanted = widgets.flatMap((w) => {
    const key = seedKey(w);
    return key === undefined ? [] : [[w.id, key] as const];
  });
  const wantedKey = JSON.stringify(wanted);

  useEffect(() => {
    // Preview (enabled=false) must not touch the backend; clear any prior seed so a
    // toggle from live→preview doesn't leave stale real history under synthetic data.
    if (!enabled) {
      requested.current = {};
      setSeeds({});
      return;
    }
    const historyWindow = defaultHistoryWindow();
    const byId = new Map(widgets.map((w) => [w.id, w]));
    const next: Record<string, string> = {};
    for (const [id, key] of wanted) {
      next[id] = key;
      if (requested.current[id] === key) continue; // already fetched (or in flight) for this selector
      const widget = byId.get(id);
      if (!widget) continue;
      void fetchWidgetHistory(widget, historyWindow).then((samples) => {
        if (!mounted.current || requested.current[id] !== key) return; // superseded or unmounted
        setSeeds((prev) => ({ ...prev, [id]: { key, samples } }));
      });
    }
    requested.current = next;
    // `widgets`/`wanted` are read via wantedKey (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantedKey, enabled]);

  // Hand each widget its seed ONLY if it was fetched for the selector the widget has now.
  return useMemo(() => {
    const out: Record<string, MeasurementSample[] | undefined> = {};
    if (!enabled) return out;
    for (const w of widgets) {
      const seed = seeds[w.id];
      out[w.id] = seed && seed.key === seedKey(w) ? seed.samples : undefined;
    }
    return out;
  }, [widgets, seeds, enabled]);
}

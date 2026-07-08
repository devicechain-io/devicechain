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
import { useEffect, useState, type CSSProperties } from 'react';

import { ConnectedWidget } from './connected-widget';
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
  // The effective slot manifest, so a slot-based widget's history seed resolves to
  // the bound device (must match the bindings on `hub`). Omit for slot-free dashboards.
  bindings?: Record<string, SlotBinding>;
  // Host override for the definition's container sizing (the embed knob — a host can
  // force a dashboard authored `fill` into a fixed-px box, or vice versa). Omitted →
  // the definition's own `canvas.sizing`.
  sizing?: CanvasSizing;
}

export function DashboardRenderer({
  definition,
  hub,
  actions,
  seedHistory = true,
  bindings,
  sizing,
}: DashboardRendererProps) {
  const breakpoint = useActiveBreakpoint(definition.canvas.breakpoints);
  const histories = useWidgetHistories(definition.widgets, seedHistory, bindings);

  const { grid, background: bg } = definition.canvas;
  const rowGap = typeof grid.gap === 'number' ? grid.gap : grid.gap.row;
  const colGap = typeof grid.gap === 'number' ? grid.gap : grid.gap.col;
  const effectiveSizing = sizing ?? definition.canvas.sizing;

  return (
    <div style={sizingStyle(effectiveSizing, bg)}>
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
        {definition.widgets.map((widget) => {
          const box = resolveWidgetBox(widget.layout, breakpoint);
          return (
            <div key={widget.id} style={gridItemStyle(box, grid.columns)}>
              <ConnectedWidget
                widget={widget}
                hub={hub}
                actions={actions}
                initialSamples={histories[widget.id]}
              />
            </div>
          );
        })}
      </div>
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

// useWidgetHistories backfills each widget once from bucketedMeasurements and
// returns a stable id→samples map (a widget's array is set once, so it doesn't
// churn the stream's seed). Fetched in parallel; failures yield an empty seed.
function useWidgetHistories(
  widgets: WidgetInstance[],
  enabled: boolean,
  bindings: Record<string, SlotBinding> | undefined,
): Record<string, MeasurementSample[]> {
  const [histories, setHistories] = useState<Record<string, MeasurementSample[]>>({});
  // Value-compare the manifest so an unchanged-but-new object reference doesn't
  // refetch history every render.
  const bindingsKey = bindings ? JSON.stringify(bindings) : null;

  useEffect(() => {
    // Preview (enabled=false) must not touch the backend; clear any prior seed so a
    // toggle from live→preview doesn't leave stale real history under synthetic data.
    if (!enabled) {
      setHistories({});
      return;
    }
    let cancelled = false;
    const historyWindow = defaultHistoryWindow();
    Promise.all(
      widgets.map(
        // Only measurement widgets consume a history seed; a non-measurement widget
        // (alarm, command) reconciles from its own channel, so backfilling it would fire
        // a wasted (and all-measurements) bucketedMeasurements query it ignores.
        async (w) =>
          [
            w.id,
            WIDGET_CHANNEL[w.type] === 'measurement'
              ? await fetchWidgetHistory(w, historyWindow, bindings)
              : [],
          ] as const,
      ),
    ).then((entries) => {
      if (!cancelled) setHistories(Object.fromEntries(entries));
    });
    return () => {
      cancelled = true;
    };
    // bindings read via bindingsKey (value identity), not reference.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [widgets, enabled, bindingsKey]);

  return histories;
}

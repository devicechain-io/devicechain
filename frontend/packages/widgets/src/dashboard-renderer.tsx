// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardRenderer maps a parsed DashboardDefinition onto the canvas: each widget
// is absolutely positioned per the active breakpoint's box (in grid cells × grid
// size), layered by z, and rendered as a <ConnectedWidget> bound to the hub. It is
// view-only — the canvas editor lives in the console/app shell. This is the shared
// viewer the console and the reference external app both mount.

import {
  activeBreakpoint,
  defaultHistoryWindow,
  fetchWidgetHistory,
  resolveWidgetBox,
  type Breakpoints,
  type DashboardDefinition,
  type WidgetActions,
  type WidgetDataSource,
  type MeasurementSample,
  type SlotBinding,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { useEffect, useState } from 'react';

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
}

export function DashboardRenderer({
  definition,
  hub,
  actions,
  seedHistory = true,
  bindings,
}: DashboardRendererProps) {
  const breakpoint = useActiveBreakpoint(definition.canvas.breakpoints);
  const histories = useWidgetHistories(definition.widgets, seedHistory, bindings);

  // Boxes are expressed in grid cells; the grid size turns them into pixels so a
  // definition is resolution-independent (and snap-to-grid stays exact).
  const cell = definition.canvas.grid.size || 1;
  const bg = definition.canvas.background;

  return (
    <div
      style={{
        position: 'relative',
        width: '100%',
        height: '100%',
        overflow: 'auto',
        backgroundColor: bg?.color ?? undefined,
        backgroundImage: bg?.imageUrl ? `url(${bg.imageUrl})` : undefined,
        backgroundSize: 'cover',
      }}
    >
      {definition.widgets.map((widget) => {
        const box = resolveWidgetBox(widget.layout, breakpoint);
        return (
          <div
            key={widget.id}
            style={{
              position: 'absolute',
              left: box.x * cell,
              top: box.y * cell,
              width: box.w * cell,
              height: box.h * cell,
              zIndex: box.z,
            }}
          >
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
  );
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
        // Only measurement widgets consume a history seed; an alarm widget's channel
        // reconciles from a query, so backfilling it would fire a wasted (and
        // all-measurements) bucketedMeasurements query whose result it ignores.
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

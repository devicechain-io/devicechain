// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardCanvas — the edit-mode canvas (ADR-039, authoring in the console).
//
// The canvas mirrors the viewer's grid of SQUARE cells: it measures its own rendered width,
// derives the live cell size (the width less the gutters, shared by the columns; a row is as
// tall as a cell is wide), and maps each grid widget's placement to a pixel rect
// (gridPlacementToPx) for react-rnd. Drag + resize snap to cell lines and commit a new
// placement (pxToGridPlacement) through the pure editor-model transforms, which push any widget
// the new footprint covers down out of its way, so an edit can never produce an overlap the
// parser would refuse. A widget placed RELATIVE to another is drawn where its host puts it and
// can be selected and deleted, but not dragged: editing relative placement is a later slice.
// It is CONTROLLED: the workspace owns the working DashboardDefinition (and the save/dirty
// state); this component owns only which widget is selected. Widgets render live (WYSIWYG) but
// pointer-inert so the pointer drives react-rnd. Editing targets the 'base' breakpoint;
// per-breakpoint responsive editing is deferred.

import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  basePlacement,
  DEFAULT_CANVAS_DENSITY,
  deleteWidget,
  gridPlacementToPx,
  gutterPx,
  layoutRects,
  paddingPx,
  pxToGridPlacement,
  resolveFrame,
  setWidgetPlacement,
  BASE_BREAKPOINT,
  UNKNOWN_WIDGET_TYPE,
  type DashboardDefinition,
  type GridGeometry,
  type SlotBinding,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { densityScale, tokenStyle, WidgetDensityProvider, WidgetFrameStyleProvider } from '@devicechain/widget-sdk';
import { ConnectedWidget, WidgetSubjectProvider, resolveWidgets, widgetSubjectLabel } from '@devicechain/widgets';
import { Rnd } from 'react-rnd';
import { defaultWidgetRegistry, type EditorWidgetRegistry } from './widgetRegistry';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

export interface DashboardCanvasProps {
  definition: DashboardDefinition;
  onChange: (next: DashboardDefinition) => void;
  hub: WidgetDataSource;
  // The registry every widget on the canvas resolves through -- the one the definition was
  // parsed with. Defaults to the deployment's.
  registry?: EditorWidgetRegistry;
  // Action seam, so an action widget shows its controls in the layout (WYSIWYG). The
  // canvas makes each widget pointer-inert during editing, so the controls are visible
  // but inert — react-rnd owns drag/resize.
  actions?: WidgetActions;
  // The settled slot manifest each widget's slot is resolved through before it subscribes
  // (the data source holds no bindings). The workspace passes its edit-mode manifest,
  // debounced, so typing an anchor re-subscribes the affected widgets once.
  bindings: Record<string, SlotBinding>;
  // Selection is lifted to the workspace (which owns the config panel); the
  // canvas is fully controlled — it reports clicks and reflects the current id.
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}

// useMeasuredWidth reports an element's live content width (via ResizeObserver),
// measured synchronously on mount so the grid geometry is ready on first paint. The
// canvas needs it to turn fluid `1fr` columns into concrete pixel widths for react-rnd.
function useMeasuredWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setWidth(el.clientWidth);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width];
}

export function DashboardCanvas({
  definition,
  onChange,
  hub,
  registry = defaultWidgetRegistry(),
  actions,
  bindings,
  selectedId,
  onSelect,
}: DashboardCanvasProps) {
  const { t } = useTranslation('dashboards');
  const { grid, sizing, frame: canvasFrame } = definition.canvas;
  const [contentRef, measuredWidth] = useMeasuredWidth<HTMLDivElement>();
  // Each widget as it RENDERS: its slot resolved to a concrete selector. Render-only — the
  // editing transforms below act on `definition`, never on these copies.
  const resolved = useMemo(
    () => new Map(resolveWidgets(definition.widgets, bindings).map((w) => [w.id, w])),
    [definition.widgets, bindings],
  );

  // The authored density, applied exactly as the viewer applies it (the size tokens on the
  // board root, the scale in context), so a wall board is laid out at the type size it will
  // show at: the frame's header thresholds and every fitted readout depend on it. The gutters
  // and padding scale with it too.
  const scale = densityScale(definition.canvas.density);
  const gutter = gutterPx(grid, scale);
  const pad = paddingPx(grid, scale);
  // The live cell at the current canvas width, the same math the viewer's CSS does: the grid's
  // width (the measured element sits inside the padding) less the gutters, shared by the
  // columns. Floored at 1 so an unmeasured (width 0) first frame can't divide-by-zero.
  const cell = Math.max(1, (measuredWidth - (grid.columns - 1) * gutter.x) / grid.columns);
  const geom: GridGeometry = { cell, gutter };
  const colStride = cell + gutter.x;
  const rowStride = cell + gutter.y;
  // Mirror the viewer's fixed-width sizing so editing a fixed-width board sees the
  // same cell size (hence the same widget sizes) it will render at — the ResizeObserver
  // tracks this capped element, so `cell` follows automatically. Fixed-HEIGHT and fill both
  // fill the pane width, so only {width} caps here.
  const maxWidth = typeof sizing === 'object' && 'width' in sizing ? sizing.width : undefined;
  // Where every widget lands, relative ones included (they follow their host).
  const rects = layoutRects(definition.widgets, BASE_BREAKPOINT, cell, gutter);

  // Give the scroll area room below the lowest widget so there's somewhere to drag.
  const contentHeight = Math.max(0, ...[...rects.values()].map((r) => r.y + r.h)) + 240;

  // A widget, live and inert, framed as the viewer frames it.
  const live = (widget: WidgetInstance): ReactNode => (
    // `inert` removes it from pointer AND keyboard/focus, so an action widget's controls (alarm
    // Ack/Clear) can't fire a real mutation via Tab+Enter mid-layout — pointer-events alone
    // wouldn't stop keyboard activation.
    <div inert data-shot="dashboard-widget" style={{ width: '100%', height: '100%', pointerEvents: 'none' }}>
      {/* Same resolved-entity subtitle the viewer shows, so the widget's frame (and hence its
          body height) is WYSIWYG between editing and viewing. Read from the selector the
          widget subscribes with — edit mode has no live selection overlay (selection is inert
          here), so the author's own bindings are the right subject. */}
      <WidgetFrameStyleProvider
        frame={resolveFrame(canvasFrame, widget.type === UNKNOWN_WIDGET_TYPE ? undefined : widget.frame)}
      >
        <WidgetSubjectProvider label={widgetSubjectLabel(resolved.get(widget.id) ?? widget)}>
          <ConnectedWidget widget={resolved.get(widget.id) ?? widget} hub={hub} registry={registry} actions={actions} />
        </WidgetSubjectProvider>
      </WidgetFrameStyleProvider>
    </div>
  );

  const toolbar = (widgetId: string) => (
    <div
      className="rnd-no-drag"
      style={{
        // Overlay the widget's top-right corner (inside its bounds) so
        // it isn't clipped by the scroll area for widgets at y:0.
        position: 'absolute',
        top: 2,
        right: 2,
        display: 'flex',
        gap: 4,
        pointerEvents: 'auto',
      }}
    >
      <ToolbarButton
        danger
        onClick={() => {
          onChange(deleteWidget(definition, widgetId));
          onSelect(null);
        }}
      >
        {t('delete')}
      </ToolbarButton>
    </div>
  );

  return (
    <div
      onMouseDown={() => onSelect(null)}
      style={{ position: 'relative', width: '100%', height: '100%', overflow: 'auto' }}
    >
      <div
        data-density={definition.canvas.density ?? DEFAULT_CANVAS_DENSITY}
        style={{
          width: '100%',
          maxWidth,
          padding: `${pad.top}px ${pad.right}px ${pad.bottom}px ${pad.left}px`,
          boxSizing: 'border-box',
          ...tokenStyle(scale),
        }}
      >
      <div ref={contentRef} style={{ position: 'relative', width: '100%', minHeight: contentHeight }}>
        <WidgetDensityProvider scale={scale}>
        {definition.widgets.map((widget) => {
          const placement = basePlacement(widget);
          const selected = widget.id === selectedId;
          if (placement.mode !== 'grid') {
            const r = rects.get(widget.id);
            if (!r) return null;
            return (
              <div
                key={widget.id}
                style={{
                  position: 'absolute',
                  left: r.x,
                  top: r.y,
                  width: r.w,
                  height: r.h,
                  zIndex: 100 + (placement.layer ?? 0),
                  outline: selected ? '2px solid hsl(var(--primary))' : '1px dashed hsl(var(--border))',
                }}
                onMouseDown={(e) => {
                  e.stopPropagation(); // don't let the canvas deselect
                  onSelect(widget.id);
                }}
              >
                {live(widget)}
                {selected && toolbar(widget.id)}
              </div>
            );
          }
          const rect = gridPlacementToPx(placement, geom);
          // Both drag and resize end by snapping a pixel rect back to a footprint, clamped
          // inside the column count; whatever it now covers is pushed down.
          const commit = (px: { x: number; y: number; w: number; h: number }) =>
            onChange(setWidgetPlacement(definition, widget.id, pxToGridPlacement(px, geom, grid.columns)));
          return (
            <Rnd
              key={widget.id}
              size={{ width: rect.w, height: rect.h }}
              position={{ x: rect.x, y: rect.y }}
              // Drag snaps live to the grid (react-draggable snaps DELTAS, so positions
              // stay grid-congruent). Resize deliberately does NOT use `resizeGrid`:
              // re-resizable snaps absolute size to stride multiples, but a legal
              // N-span width is `N*stride - colGap` (never a stride multiple), so a
              // grid-snapped resize ghost overhangs the gutter and, on a narrow canvas,
              // can commit a different span than it showed. Instead resize is free and
              // `pxToGridBox` snaps to the nearest legal span on release.
              dragGrid={[colStride, rowStride]}
              // `bounds`/`cancel` are react-rnd API values (a keyword + a CSS selector),
              // not user-facing text.
              // eslint-disable-next-line i18next/no-literal-string
              bounds="parent"
              // eslint-disable-next-line i18next/no-literal-string
              cancel=".rnd-no-drag"
              style={{ outline: selected ? '2px solid hsl(var(--primary))' : '1px dashed hsl(var(--border))' }}
              onMouseDown={(e: MouseEvent) => {
                e.stopPropagation(); // don't let the canvas deselect
                onSelect(widget.id);
              }}
              onDragStop={(_e, d) => commit({ x: d.x, y: d.y, w: rect.w, h: rect.h })}
              onResizeStop={(_e, _dir, ref, _delta, pos) =>
                commit({ x: pos.x, y: pos.y, w: ref.offsetWidth, h: ref.offsetHeight })
              }
            >
              {live(widget)}
              {selected && toolbar(widget.id)}
            </Rnd>
          );
        })}
        </WidgetDensityProvider>
      </div>
      </div>
    </div>
  );
}

function ToolbarButton({
  children,
  onClick,
  danger,
}: {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
}) {
  return (
    <Button
      variant="outline"
      size="inline"
      // rnd-no-drag is react-rnd's cancel selector: without it, pressing the button
      // starts a drag instead. stopPropagation keeps the mousedown from reaching the
      // widget beneath.
      className={cn(
        'rnd-no-drag rounded border-border bg-card px-2 py-0.5 text-xs',
        danger && 'text-destructive hover:text-destructive',
      )}
      onMouseDown={(e) => e.stopPropagation()}
      onClick={onClick}
    >
      {children}
    </Button>
  );
}

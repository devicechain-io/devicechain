// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardCanvas — the edit-mode canvas (ADR-039, authoring in the console).
//
// The canvas mirrors the viewer's CSS Grid (ADR-039 amendment 2026-07-08): it
// measures its own rendered width, derives the pixel width of one fluid column, and
// maps each widget's SPAN box to a pixel rect (gridBoxToPx) for react-rnd. Drag +
// resize snap to grid lines (dragGrid/resizeGrid = one track's stride) and commit a
// new span box (pxToGridBox) through the pure editor-model transforms. It is
// CONTROLLED: the workspace owns the working DashboardDefinition (and the save/dirty
// state); this component owns only which widget is selected. Widgets render live
// (WYSIWYG) but pointer-inert so the pointer drives react-rnd. Editing targets the
// 'base' breakpoint; per-breakpoint responsive editing is deferred.

import { useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  baseBox,
  bringToFront,
  deleteWidget,
  gridBoxToPx,
  pxToGridBox,
  setWidgetBox,
  type DashboardDefinition,
  type GridGeometry,
  type WidgetActions,
  type WidgetDataSource,
} from '@devicechain/dashboards';
import { ConnectedWidget, WidgetSubjectProvider, widgetSubjectLabel } from '@devicechain/widgets';
import { Rnd } from 'react-rnd';

export interface DashboardCanvasProps {
  definition: DashboardDefinition;
  onChange: (next: DashboardDefinition) => void;
  hub: WidgetDataSource;
  // Action seam, so an action widget shows its controls in the layout (WYSIWYG). The
  // canvas makes each widget pointer-inert during editing, so the controls are visible
  // but inert — react-rnd owns drag/resize.
  actions?: WidgetActions;
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

export function DashboardCanvas({ definition, onChange, hub, actions, selectedId, onSelect }: DashboardCanvasProps) {
  const { t } = useTranslation('dashboards');
  const { grid, sizing } = definition.canvas;
  const [contentRef, measuredWidth] = useMeasuredWidth<HTMLDivElement>();

  const rowGap = typeof grid.gap === 'number' ? grid.gap : grid.gap.row;
  const colGap = typeof grid.gap === 'number' ? grid.gap : grid.gap.col;
  // The pixel width of one fluid column at the current canvas width — the same math
  // CSS applies to `repeat(columns, 1fr)`: total minus the inter-column gaps, split
  // evenly. Floored at 1 so an unmeasured (width 0) first frame can't divide-by-zero.
  const colWidth = Math.max(1, (measuredWidth - (grid.columns - 1) * colGap) / grid.columns);
  const geom: GridGeometry = { colWidth, colGap, rowHeight: grid.rowHeight, rowGap };
  const colStride = colWidth + colGap;
  const rowStride = grid.rowHeight + rowGap;
  // Mirror the viewer's fixed-width sizing so editing a fixed-width board sees the
  // same column width (hence the same widget aspect ratios) it will render at — the
  // ResizeObserver tracks this capped element, so `colWidth` follows automatically.
  // Fixed-HEIGHT and fill both fill the pane width, so only {width} caps here.
  const maxWidth = typeof sizing === 'object' && 'width' in sizing ? sizing.width : undefined;

  // Give the scroll area room below the lowest widget so there's somewhere to drag.
  const contentHeight =
    definition.widgets.reduce((max, w) => {
      const b = baseBox(w);
      return Math.max(max, (b.row + b.rowSpan) * rowStride);
    }, 0) + 240;

  return (
    <div
      onMouseDown={() => onSelect(null)}
      style={{ position: 'relative', width: '100%', height: '100%', overflow: 'auto' }}
    >
      <div ref={contentRef} style={{ position: 'relative', width: '100%', maxWidth, minHeight: contentHeight }}>
        {definition.widgets.map((widget) => {
          const box = baseBox(widget);
          const rect = gridBoxToPx(box, geom);
          const selected = widget.id === selectedId;
          // Both drag and resize end by snapping a pixel rect back to a span box,
          // preserving z and any hand-set offset, clamped inside the column count.
          const commitBox = (px: { x: number; y: number; w: number; h: number }) =>
            onChange(setWidgetBox(definition, widget.id, pxToGridBox(px, geom, box.z, box.offset, grid.columns)));
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
              style={{
                zIndex: box.z,
                outline: selected ? '2px solid hsl(var(--primary))' : '1px dashed hsl(var(--border))',
              }}
              onMouseDown={(e: MouseEvent) => {
                e.stopPropagation(); // don't let the canvas deselect
                onSelect(widget.id);
              }}
              onDragStop={(_e, d) => commitBox({ x: d.x, y: d.y, w: rect.w, h: rect.h })}
              onResizeStop={(_e, _dir, ref, _delta, pos) =>
                commitBox({ x: pos.x, y: pos.y, w: ref.offsetWidth, h: ref.offsetHeight })
              }
            >
              {/* Live widget, fully INERT while editing so react-rnd owns interaction:
                  `inert` removes it from pointer AND keyboard/focus, so an action widget's
                  controls (alarm Ack/Clear) can't fire a real mutation via Tab+Enter mid-
                  layout — pointer-events alone wouldn't stop keyboard activation. */}
              <div inert style={{ width: '100%', height: '100%', pointerEvents: 'none' }}>
                {/* Same resolved-entity subtitle the viewer shows, so the widget's frame
                    (and hence its body height) is WYSIWYG between editing and viewing.
                    Resolved from the working definition's slot defaults — edit mode has no
                    live selection overlay (selection is inert here), so the author's own
                    bindings are the right subject. */}
                <WidgetSubjectProvider label={widgetSubjectLabel(widget, definition.slots, undefined)}>
                  <ConnectedWidget widget={widget} hub={hub} actions={actions} />
                </WidgetSubjectProvider>
              </div>

              {selected && (
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
                  <ToolbarButton onClick={() => onChange(bringToFront(definition, widget.id))}>
                    {t('canvasFront')}
                  </ToolbarButton>
                  <ToolbarButton
                    danger
                    onClick={() => {
                      onChange(deleteWidget(definition, widget.id));
                      onSelect(null);
                    }}
                  >
                    {t('delete')}
                  </ToolbarButton>
                </div>
              )}
            </Rnd>
          );
        })}
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
    <button
      className="rnd-no-drag"
      onMouseDown={(e) => e.stopPropagation()}
      onClick={onClick}
      style={{
        fontSize: 12,
        padding: '2px 8px',
        borderRadius: 4,
        border: '1px solid hsl(var(--border))',
        cursor: 'pointer',
        color: danger ? 'hsl(var(--destructive))' : 'hsl(var(--foreground))',
        background: 'hsl(var(--card))',
      }}
    >
      {children}
    </button>
  );
}

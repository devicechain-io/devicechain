// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardCanvas — the edit-mode canvas (ADR-039, authoring in the console).
//
// Each widget is wrapped in react-rnd for drag + resize, snapped to the canvas
// grid; drag/resize commit a new base box through the pure editor-model transforms
// from @devicechain/dashboards. It is CONTROLLED: the workspace owns the working
// DashboardDefinition (and the save/dirty state); this component owns only which
// widget is selected. Widgets render live (WYSIWYG) but with pointer-events
// disabled so the pointer drives react-rnd, not the widget. Editing targets the
// 'base' breakpoint; per-breakpoint responsive editing is deferred.

import {
  baseBox,
  bringToFront,
  deleteWidget,
  pxToCellBox,
  setWidgetBox,
  type DashboardDefinition,
  type WidgetDataSource,
} from '@devicechain/dashboards';
import { ConnectedWidget } from '@devicechain/widgets';
import { Rnd } from 'react-rnd';

export interface DashboardCanvasProps {
  definition: DashboardDefinition;
  onChange: (next: DashboardDefinition) => void;
  hub: WidgetDataSource;
  // Selection is lifted to the workspace (which owns the config panel); the
  // canvas is fully controlled — it reports clicks and reflects the current id.
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}

export function DashboardCanvas({ definition, onChange, hub, selectedId, onSelect }: DashboardCanvasProps) {
  const cell = definition.canvas.grid.size || 1;

  // Give the scroll area room below the lowest widget so there's somewhere to drag.
  const contentHeight =
    definition.widgets.reduce((max, w) => {
      const b = baseBox(w);
      return Math.max(max, (b.y + b.h) * cell);
    }, 0) + 240;

  return (
    <div
      onMouseDown={() => onSelect(null)}
      style={{ position: 'relative', width: '100%', height: '100%', overflow: 'auto' }}
    >
      <div style={{ position: 'relative', width: '100%', minHeight: contentHeight }}>
        {definition.widgets.map((widget) => {
          const box = baseBox(widget);
          const selected = widget.id === selectedId;
          // Both drag and resize end by snapping a pixel rect back to a cell box.
          const commitBox = (px: { x: number; y: number; w: number; h: number }) =>
            onChange(setWidgetBox(definition, widget.id, pxToCellBox(px, cell, box.z)));
          return (
            <Rnd
              key={widget.id}
              size={{ width: box.w * cell, height: box.h * cell }}
              position={{ x: box.x * cell, y: box.y * cell }}
              dragGrid={[cell, cell]}
              resizeGrid={[cell, cell]}
              bounds="parent"
              cancel=".rnd-no-drag"
              style={{
                zIndex: box.z,
                outline: selected ? '2px solid hsl(var(--primary))' : '1px dashed hsl(var(--border))',
              }}
              onMouseDown={(e: MouseEvent) => {
                e.stopPropagation(); // don't let the canvas deselect
                onSelect(widget.id);
              }}
              onDragStop={(_e, d) => commitBox({ x: d.x, y: d.y, w: box.w * cell, h: box.h * cell })}
              onResizeStop={(_e, _dir, ref, _delta, pos) =>
                commitBox({ x: pos.x, y: pos.y, w: ref.offsetWidth, h: ref.offsetHeight })
              }
            >
              {/* Live widget, but inert to the pointer so react-rnd owns drag/resize. */}
              <div style={{ width: '100%', height: '100%', pointerEvents: 'none' }}>
                <ConnectedWidget widget={widget} hub={hub} />
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
                  <ToolbarButton onClick={() => onChange(bringToFront(definition, widget.id))}>Front</ToolbarButton>
                  <ToolbarButton
                    danger
                    onClick={() => {
                      onChange(deleteWidget(definition, widget.id));
                      onSelect(null);
                    }}
                  >
                    Delete
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

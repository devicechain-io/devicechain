// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardCanvas — the edit-mode canvas (ADR-039, authoring in the console).
//
// The canvas mirrors the viewer's grid of SQUARE cells: it measures its own rendered width,
// derives the live cell size (the width less the gutters, shared by the columns; a row is as
// tall as a cell is wide) and draws each widget at the rect its placement gives (layoutRects).
//
// Every edit is CELL-SNAPPED and goes through the pure editor-model transforms; this component
// owns no placement arithmetic of its own beyond turning pointer movement into cells
// (canvasGeometry). A gesture (drag, resize, offset drag) never mutates the definition while it
// runs: each pointer move recomputes a PREVIEW definition from the unchanged original, and the
// preview is what is drawn, so a neighbour in the way is seen moving to the nearest free room (a
// swap, when the room is the one the dragged widget left) and then returning if the pointer goes
// back; nothing that is not in the way moves. Releasing commits the preview; Escape, a cancelled
// or lost pointer, or the window losing focus discards it.
//
//   - dragging a grid widget shows a footprint ghost snapped to the cells it would take, while the
//     widget itself follows the pointer;
//   - resizing snaps to cells, stops at the widget's minSpan / maxSpan and at the canvas edge, has
//     handles only on the axes its `resize` mode allows, and reads out the size ("2×3");
//   - a RELATIVE widget is dragged by its offset in whole cells (quarter cells with Shift), with a
//     dashed guide to its host;
//   - arrow keys move the focused widget one cell, Shift+arrows resize it.
//
// It is CONTROLLED: the workspace owns the working DashboardDefinition (and the save/dirty
// state); this component owns the gesture state only. Widgets render live (WYSIWYG) but inert, so
// the pointer drives the gesture. Editing targets the 'base' breakpoint; per-breakpoint
// responsive editing is deferred.

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  basePlacement,
  canHostRelative,
  DEFAULT_CANVAS_DENSITY,
  deleteWidget,
  formatSpan,
  gutterPx,
  layoutRects,
  moveWidget,
  paddingPx,
  pinnedAxes,
  placeOnGrid,
  placeRelative,
  resizeWidget,
  resolveFrame,
  setRelativeAnchor,
  setRelativeOffset,
  spanLimitsOfLayout,
  spanPx,
  BASE_BREAKPOINT,
  UNKNOWN_WIDGET_TYPE,
  type DashboardDefinition,
  type GridPlacement,
  type PlacementAnchor,
  type Rect,
  type SlotBinding,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { densityScale, tokenStyle, WidgetDensityProvider, WidgetFrameStyleProvider } from '@devicechain/widget-sdk';
import { ConnectedWidget, WidgetSubjectProvider, resolveWidgets, widgetSubjectLabel } from '@devicechain/widgets';
import { defaultWidgetRegistry, type EditorWidgetRegistry } from './widgetRegistry';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import {
  enabledHandles,
  guideLine,
  handleEdges,
  moveTarget,
  resizeTarget,
  type Handle,
  type Strides,
} from './canvasGeometry';
import { inlineFindings, layoutFindingsOf, LAYOUT_FINDING_KEYS } from './layoutFindings';
import '@/i18n/lazy/dashboards';

// What a canvas edit did, and to which widget: the label the workspace's undo history records (and
// announces when the edit is undone). A gesture reports ONE edit, when it is released.
export interface CanvasEdit {
  action: 'move' | 'resize' | 'offset' | 'delete' | 'placement' | 'anchor';
  widgetId: string;
}

// The edits the toolbar reports (identifiers, not text).
const PLACEMENT_EDIT: CanvasEdit['action'] = 'placement';
const ANCHOR_EDIT: CanvasEdit['action'] = 'anchor';
const DELETE_EDIT: CanvasEdit['action'] = 'delete';

export interface DashboardCanvasProps {
  definition: DashboardDefinition;
  onChange: (next: DashboardDefinition, edit: CanvasEdit) => void;
  // Duplicate and Copy on the selected widget's toolbar. Each is offered only when given.
  onDuplicate?: (id: string) => void;
  onCopy?: (id: string) => void;
  hub: WidgetDataSource;
  // The registry every widget on the canvas resolves through -- the one the definition was
  // parsed with. Defaults to the deployment's.
  registry?: EditorWidgetRegistry;
  // Action seam, so an action widget shows its controls in the layout (WYSIWYG). The
  // canvas makes each widget pointer-inert during editing, so the controls are visible
  // but inert -- the pointer drives the drag and resize.
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

// What the pointer is doing to which widget, from where it started, and which pointer it is (a
// second finger, or a pen beside a mouse, is not the gesture).
type Gesture = GestureStart & { x: number; y: number; pointerId: number | undefined };

type GestureStart =
  | { kind: 'move'; id: string; origin: GridPlacement }
  | { kind: 'resize'; id: string; origin: GridPlacement; handle: Handle }
  | { kind: 'offset'; id: string; origin: { x: number; y: number } };

interface Pointer {
  dx: number;
  dy: number;
  shift: boolean;
}

// useMeasuredWidth reports an element's live content width (via ResizeObserver),
// measured synchronously on mount so the grid geometry is ready on first paint. The
// canvas needs it to turn fluid `1fr` columns into concrete pixel widths.
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

// The cursor each handle shows.
const HANDLE_CURSOR: Record<Handle, string> = {
  n: 'ns-resize',
  s: 'ns-resize',
  e: 'ew-resize',
  w: 'ew-resize',
  ne: 'nesw-resize',
  sw: 'nesw-resize',
  nw: 'nwse-resize',
  se: 'nwse-resize',
};

// Where a handle sits on the widget's box: a 10px square centred on the edge or corner.
function handleStyle(h: Handle): React.CSSProperties {
  const size = 10;
  const half = -size / 2;
  const style: React.CSSProperties = { position: 'absolute', width: size, height: size, cursor: HANDLE_CURSOR[h], zIndex: 6 };
  if (h.includes('n')) style.top = half;
  else if (h.includes('s')) style.bottom = half;
  else {
    style.top = '50%';
    style.marginTop = half;
  }
  if (h.includes('w')) style.left = half;
  else if (h.includes('e')) style.right = half;
  else {
    style.left = '50%';
    style.marginLeft = half;
  }
  return style;
}

const ANCHOR_KEYS: Record<PlacementAnchor, string> = {
  'top-left': 'canvasAnchorTopLeft',
  top: 'canvasAnchorTop',
  'top-right': 'canvasAnchorTopRight',
  left: 'canvasAnchorLeft',
  center: 'canvasAnchorCenter',
  right: 'canvasAnchorRight',
  'bottom-left': 'canvasAnchorBottomLeft',
  bottom: 'canvasAnchorBottom',
  'bottom-right': 'canvasAnchorBottomRight',
};

// The nine anchors in reading order, as a 3x3 picker lays them out.
const ANCHOR_ORDER: PlacementAnchor[] = [
  'top-left',
  'top',
  'top-right',
  'left',
  'center',
  'right',
  'bottom-left',
  'bottom',
  'bottom-right',
];

// A widget's name as an author recognises it: its title option, else its type.
function nameOf(widget: WidgetInstance): string {
  const title = widget.options?.title;
  return typeof title === 'string' && title.trim() !== '' ? title.trim() : widget.type;
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
  onDuplicate,
  onCopy,
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
  const strides: Strides = { col: cell + gutter.x, row: cell + gutter.y };
  // Mirror the viewer's fixed-width sizing so editing a fixed-width board sees the
  // same cell size (hence the same widget sizes) it will render at — the ResizeObserver
  // tracks this capped element, so `cell` follows automatically. Fixed-HEIGHT and fill both
  // fill the pane width, so only {width} caps here.
  const maxWidth = typeof sizing === 'object' && 'width' in sizing ? sizing.width : undefined;

  const limitsOf = useCallback(
    (widget: WidgetInstance) =>
      spanLimitsOfLayout(widget.type === UNKNOWN_WIDGET_TYPE ? undefined : registry.descriptor(widget.type)?.layout),
    [registry],
  );

  // ---- gestures -------------------------------------------------------------------

  const [gesture, setGesture] = useState<Gesture | null>(null);
  const [pointer, setPointer] = useState<Pointer>({ dx: 0, dy: 0, shift: false });
  // The widget being given a host: the next click on a valid host widget completes it.
  const [pickingFor, setPickingFor] = useState<string | null>(null);
  // What a keyboard edit just did, for a screen reader.
  const [announcement, setAnnouncement] = useState('');

  // The definition a gesture would produce at a pointer position, from the unchanged original.
  const previewOf = (g: Gesture, p: Pointer): DashboardDefinition => {
    const widget = definition.widgets.find((w) => w.id === g.id);
    if (!widget) return definition;
    if (g.kind === 'move') {
      return moveWidget(definition, g.id, moveTarget(g.origin, p.dx, p.dy, strides), grid.columns, { nearest: true });
    }
    if (g.kind === 'resize') {
      return resizeWidget(
        definition,
        g.id,
        resizeTarget(g.origin, g.handle, p.dx, p.dy, strides),
        limitsOf(widget),
        handleEdges(g.handle),
        grid.columns,
        { nearest: true },
      );
    }
    return setRelativeOffset(
      definition,
      g.id,
      { x: g.origin.x + p.dx / strides.col, y: g.origin.y + p.dy / strides.row },
      { fine: p.shift, bounds: { cell, gutter, columns: grid.columns } },
    );
  };
  const previewRef = useRef(previewOf);
  previewRef.current = previewOf;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const definitionRef = useRef(definition);
  definitionRef.current = definition;

  useEffect(() => {
    if (!gesture) return;
    const at = (e: MouseEvent): Pointer => ({ dx: e.clientX - gesture.x, dy: e.clientY - gesture.y, shift: e.shiftKey });
    const ours = (e: Event) => (e as PointerEvent).pointerId === gesture.pointerId;
    const cancel = () => setGesture(null);
    const move = (e: MouseEvent) => {
      if (!ours(e)) return;
      // A mouse whose button is no longer down was released where no pointerup reached us (outside
      // the window, over a frame): the gesture did not end with a drop, so it commits nothing.
      if ((e as PointerEvent).pointerType === 'mouse' && e.buttons === 0) {
        cancel();
        return;
      }
      setPointer(at(e));
    };
    const up = (e: MouseEvent) => {
      if (!ours(e)) return;
      const next = previewRef.current(gesture, at(e));
      setGesture(null);
      // The whole gesture is one edit, reported once, on release.
      if (next !== definitionRef.current) onChangeRef.current(next, { action: gesture.kind, widgetId: gesture.id });
    };
    const lost = (e: Event) => {
      if (ours(e)) cancel();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') cancel();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', lost);
    window.addEventListener('lostpointercapture', lost);
    window.addEventListener('blur', cancel);
    window.addEventListener('keydown', key);
    return () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', lost);
      window.removeEventListener('lostpointercapture', lost);
      window.removeEventListener('blur', cancel);
      window.removeEventListener('keydown', key);
    };
  }, [gesture]);

  useEffect(() => {
    if (!pickingFor) return;
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setPickingFor(null);
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [pickingFor]);

  // What is drawn: the preview while a gesture runs, the definition otherwise.
  const view = gesture ? previewOf(gesture, pointer) : definition;
  // Where every widget lands, relative ones included (they follow their host).
  const rects = layoutRects(view.widgets, BASE_BREAKPOINT, cell, gutter);
  const originalRects = gesture ? layoutRects(definition.widgets, BASE_BREAKPOINT, cell, gutter) : rects;

  // Give the scroll area room below the lowest widget so there's somewhere to drag.
  const contentHeight = Math.max(0, ...[...rects.values()].map((r) => r.y + r.h)) + 240;

  const findings = useMemo(() => layoutFindingsOf(definition, registry), [definition, registry]);
  const inline = useMemo(() => {
    const byWidget = new Map<string, ReturnType<typeof inlineFindings>>();
    for (const f of inlineFindings(findings)) byWidget.set(f.widgetId, [...(byWidget.get(f.widgetId) ?? []), f]);
    return byWidget;
  }, [findings]);

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

  // Whether a pointer down may begin a gesture: the primary button, while no other gesture runs (a
  // second finger or pen cannot start one, and the running one only follows its own pointerId),
  // from the widget's own DOM. React bubbles an event out of a PORTAL to the component that
  // rendered it, so a popover a widget portals out of its (inert) body would otherwise reach the
  // widget's handler from somewhere that is not the widget at all.
  const mayStart = (e: React.PointerEvent) =>
    e.button === 0 && gesture === null && e.currentTarget.contains(e.target as Node);

  const startGesture = (e: React.PointerEvent, start: GestureStart) => {
    setPointer({ dx: 0, dy: 0, shift: e.shiftKey });
    setGesture({ ...start, x: e.clientX, y: e.clientY, pointerId: e.pointerId });
  };

  // The widget elements by id, to put focus on the one being edited.
  const elements = useRef(new Map<string, HTMLDivElement>());

  // Completes a host pick on `widget`, if one is under way: a valid host takes the widget being
  // placed (which keeps the selection and gets the focus); anything else ends the pick unchanged.
  const pickHost = (widget: WidgetInstance): boolean => {
    if (!pickingFor || pickingFor === widget.id) return false;
    const placing = pickingFor;
    setPickingFor(null);
    if (canHostRelative(definition, placing, widget.id)) {
      onChange(placeRelative(definition, placing, widget.id, 'top-right'), { action: 'placement', widgetId: placing });
      onSelect(placing);
      elements.current.get(placing)?.focus();
    }
    return true;
  };

  // Pointer down on a widget's body: pick a host if one is being chosen, else begin a drag.
  const onWidgetPointerDown = (e: React.PointerEvent, widget: WidgetInstance) => {
    if (!mayStart(e)) return;
    if ((e.target as HTMLElement).closest('[data-no-drag]')) return;
    e.stopPropagation();
    if (pickHost(widget)) return;
    setPickingFor(null);
    onSelect(widget.id);
    const placement = basePlacement(widget);
    if (placement.mode === 'grid') startGesture(e, { kind: 'move', id: widget.id, origin: placement });
    else startGesture(e, { kind: 'offset', id: widget.id, origin: placement.offset ?? { x: 0, y: 0 } });
  };

  // Arrow keys move the focused widget a cell (a relative one its offset: a cell, or a quarter
  // with Shift); Shift+arrows resize a grid widget by a cell.
  const onWidgetKeyDown = (e: React.KeyboardEvent, widget: WidgetInstance) => {
    if (e.target !== e.currentTarget) return;
    // Enter or Space on a focused widget completes a host pick, as a click does.
    if ((e.key === 'Enter' || e.key === ' ') && pickingFor && pickingFor !== widget.id) {
      e.preventDefault();
      pickHost(widget);
      return;
    }
    const dx = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    const dy = e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0;
    if (dx === 0 && dy === 0) return;
    e.preventDefault();
    const p = basePlacement(widget);
    let next: DashboardDefinition;
    let action: CanvasEdit['action'] = 'move';
    if (p.mode === 'relative') {
      action = 'offset';
      const step = e.shiftKey ? 0.25 : 1;
      next = setRelativeOffset(
        definition,
        widget.id,
        { x: (p.offset?.x ?? 0) + dx * step, y: (p.offset?.y ?? 0) + dy * step },
        { fine: e.shiftKey, bounds: { cell, gutter, columns: grid.columns } },
      );
    } else if (e.shiftKey) {
      action = 'resize';
      next = resizeWidget(
        definition,
        widget.id,
        { col: p.col, row: p.row, cols: p.cols + dx, rows: p.rows + dy },
        limitsOf(widget),
        {},
        grid.columns,
        { nearest: true },
      );
    } else {
      next = moveWidget(definition, widget.id, { col: p.col + dx, row: p.row + dy }, grid.columns, { nearest: true });
    }
    if (next === definition) return;
    onChange(next, { action, widgetId: widget.id });
    const after = next.widgets.find((w) => w.id === widget.id);
    const q = after && basePlacement(after);
    if (q?.mode === 'grid') {
      setAnnouncement(t('canvasAnnounce', { name: nameOf(widget), size: formatSpan(q), col: q.col + 1, row: q.row + 1 }));
    } else if (q?.mode === 'relative') {
      setAnnouncement(t('canvasAnnounceOffset', { name: nameOf(widget), x: q.offset?.x ?? 0, y: q.offset?.y ?? 0 }));
    }
  };

  const toolbar = (widget: WidgetInstance, rect: Rect) => {
    const placement = basePlacement(widget);
    return (
      <FloatingToolbar rect={rect} canvasWidth={measuredWidth} roomAbove={rect.y + pad.top + TOOLBAR_ROOM}>
        <div role="group" aria-label={t('canvasPlacementLabel')} className="flex gap-px">
          <ToolbarButton
            data-testid="placement-grid"
            pressed={placement.mode === 'grid'}
            onClick={() => {
              setPickingFor(null);
              onChange(placeOnGrid(definition, widget.id), { action: PLACEMENT_EDIT, widgetId: widget.id });
            }}
          >
            {t('canvasPlacementGrid')}
          </ToolbarButton>
          <ToolbarButton
            data-testid="placement-relative"
            pressed={placement.mode === 'relative' || pickingFor === widget.id}
            onClick={() => setPickingFor(pickingFor === widget.id ? null : widget.id)}
          >
            {t('canvasPlacementRelative')}
          </ToolbarButton>
        </div>
        {placement.mode === 'relative' && (
          <AnchorPicker
            value={placement.anchor}
            onPick={(anchor) => onChange(setRelativeAnchor(definition, widget.id, anchor), { action: ANCHOR_EDIT, widgetId: widget.id })}
          />
        )}
        {onDuplicate && widget.type !== UNKNOWN_WIDGET_TYPE && (
          <ToolbarButton data-testid="widget-duplicate" onClick={() => onDuplicate(widget.id)}>
            {t('canvasDuplicate')}
          </ToolbarButton>
        )}
        {onCopy && widget.type !== UNKNOWN_WIDGET_TYPE && (
          <ToolbarButton data-testid="widget-copy" onClick={() => onCopy(widget.id)}>
            {t('canvasCopy')}
          </ToolbarButton>
        )}
        <ToolbarButton
          danger
          data-testid="widget-delete"
          onClick={() => {
            onChange(deleteWidget(definition, widget.id), { action: DELETE_EDIT, widgetId: widget.id });
            onSelect(null);
          }}
        >
          {t('delete')}
        </ToolbarButton>
      </FloatingToolbar>
    );
  };

  const lintBadges = (widgetId: string) => {
    const list = inline.get(widgetId);
    if (!list) return null;
    return (
      <ul
        data-testid={`widget-lint-${widgetId}`}
        style={{
          position: 'absolute',
          left: 2,
          bottom: 2,
          right: 2,
          margin: 0,
          padding: 0,
          listStyle: 'none',
          pointerEvents: 'none',
          zIndex: 4,
        }}
      >
        {list.map((f, i) => (
          <li
            key={`${f.code}:${f.breakpoint ?? ''}:${i}`}
            data-severity={f.severity}
            className={cn(
              'mb-0.5 w-fit max-w-full rounded px-1.5 py-0.5 text-[11px] leading-tight',
              f.severity === 'error' ? 'bg-destructive text-destructive-foreground' : 'bg-amber-200 text-amber-950',
            )}
          >
            {t(LAYOUT_FINDING_KEYS[f.code])}
            {f.breakpoint ? ` (${f.breakpoint})` : ''}
          </li>
        ))}
      </ul>
    );
  };

  const guides: ReactNode[] = [];
  for (const widget of view.widgets) {
    const p = basePlacement(widget);
    if (p.mode !== 'relative') continue;
    const host = rects.get(p.host);
    const box = rects.get(widget.id);
    if (!host || !box) continue;
    const line = guideLine(host, box, p.anchor, p.self);
    guides.push(
      <g key={widget.id} data-testid={`guide-${widget.id}`}>
        <line {...line} stroke="hsl(var(--primary))" strokeWidth={1.5} strokeDasharray="5 4" />
        <circle cx={line.x1} cy={line.y1} r={3} fill="hsl(var(--primary))" />
      </g>,
    );
  }

  // The box a widget is drawn in: the preview's, except the one being dragged, which follows the
  // pointer (held inside the canvas) while the ghost shows the cells it would take.
  const boxOf = (widget: WidgetInstance): { rect: Rect; dragging: boolean } | null => {
    const rect = rects.get(widget.id);
    if (!rect) return null;
    if (gesture?.kind === 'move' && gesture.id === widget.id) {
      const o = originalRects.get(widget.id) ?? rect;
      const total = spanPx(grid.columns, cell, gutter.x);
      return {
        rect: { ...o, x: Math.min(Math.max(0, o.x + pointer.dx), Math.max(0, total - o.w)), y: Math.max(0, o.y + pointer.dy) },
        dragging: true,
      };
    }
    return { rect, dragging: false };
  };

  const ghost = gesture?.kind === 'move' ? rects.get(gesture.id) : undefined;

  return (
    <div
      onMouseDown={() => {
        onSelect(null);
        setPickingFor(null);
      }}
      style={{ position: 'relative', width: '100%', height: '100%', overflow: 'auto' }}
    >
      {pickingFor && (
        // Over everything on the canvas, the selected widget's toolbar (250) and the placement
        // guides (300) included: scrolled, they pass under it, and it is what tells the author
        // what the next click does.
        <div
          role="status"
          data-testid="pick-host-hint"
          className="sticky left-0 top-0 z-[400] bg-primary px-3 py-1.5 text-sm text-primary-foreground"
        >
          {t('canvasPickHost')}
        </div>
      )}
      <div className="sr-only" role="status" aria-live="polite">
        {announcement}
      </div>
      <div
        data-density={definition.canvas.density ?? DEFAULT_CANVAS_DENSITY}
        style={{
          width: '100%',
          maxWidth,
          // The extra room on top is the editor's, not the board's: it is where the selected
          // widget's toolbar sits for a widget in the first row (FloatingToolbar).
          padding: `${pad.top + TOOLBAR_ROOM}px ${pad.right}px ${pad.bottom}px ${pad.left}px`,
          boxSizing: 'border-box',
          ...tokenStyle(scale),
        }}
      >
        <div ref={contentRef} style={{ position: 'relative', width: '100%', minHeight: contentHeight }}>
          <WidgetDensityProvider scale={scale}>
            {ghost && (
              <div
                data-testid="drag-ghost"
                aria-hidden
                style={{
                  position: 'absolute',
                  left: ghost.x,
                  top: ghost.y,
                  width: ghost.w,
                  height: ghost.h,
                  border: '2px dashed hsl(var(--primary))',
                  background: 'hsl(var(--primary) / 0.12)',
                  borderRadius: 4,
                  pointerEvents: 'none',
                  zIndex: 40,
                }}
              />
            )}
            {definition.widgets.map((original) => {
              // The widget as the preview has it (a neighbour moves while another is dragged).
              const widget = view.widgets.find((w) => w.id === original.id) ?? original;
              const box = boxOf(widget);
              if (!box) return null;
              const { rect, dragging } = box;
              const placement = basePlacement(widget);
              const selected = widget.id === selectedId;
              const layout = widget.type === UNKNOWN_WIDGET_TYPE ? undefined : registry.descriptor(widget.type)?.layout;
              const pinned = pinnedAxes(layout?.resize);
              const handles =
                selected && placement.mode === 'grid' ? enabledHandles(pinned.includes('cols'), pinned.includes('rows')) : [];
              const resizing = gesture?.kind === 'resize' && gesture.id === widget.id;
              const name = nameOf(widget);
              const candidate = pickingFor !== null && pickingFor !== widget.id && canHostRelative(definition, pickingFor, widget.id);
              return (
                <div
                  key={widget.id}
                  ref={(el) => {
                    if (el) elements.current.set(widget.id, el);
                    else elements.current.delete(widget.id);
                  }}
                  role="group"
                  tabIndex={0}
                  data-testid={`canvas-widget-${widget.id}`}
                  data-selected={selected || undefined}
                  aria-label={
                    placement.mode === 'grid'
                      ? t('canvasWidgetLabel', {
                          name,
                          size: formatSpan(placement),
                          col: placement.col + 1,
                          row: placement.row + 1,
                        })
                      : t('canvasWidgetLabelRelative', { name })
                  }
                  className="outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-1"
                  style={{
                    position: 'absolute',
                    left: rect.x,
                    top: rect.y,
                    width: rect.w,
                    height: rect.h,
                    zIndex: dragging ? 90 : placement.mode === 'relative' ? 100 + (placement.layer ?? 0) : resizing ? 60 : undefined,
                    opacity: dragging ? 0.85 : 1,
                    touchAction: 'none',
                    cursor: dragging ? 'grabbing' : 'grab',
                    outline: candidate
                      ? '2px dashed hsl(var(--primary))'
                      : selected
                        ? '2px solid hsl(var(--primary))'
                        : '1px dashed hsl(var(--border))',
                  }}
                  onPointerDown={(e) => onWidgetPointerDown(e, widget)}
                  onMouseDown={(e) => e.stopPropagation() /* don't let the canvas deselect */}
                  onFocus={(e) => {
                    if (e.target === e.currentTarget) onSelect(widget.id);
                  }}
                  onKeyDown={(e) => onWidgetKeyDown(e, widget)}
                >
                  {live(widget)}
                  {lintBadges(widget.id)}
                  {selected && toolbar(widget, rect)}
                  {handles.map((h) => (
                    <div
                      key={h}
                      data-testid={`handle-${h}`}
                      data-no-drag
                      aria-hidden
                      className="rounded-sm border border-primary bg-background"
                      style={handleStyle(h)}
                      onPointerDown={(e) => {
                        if (!mayStart(e) || placement.mode !== 'grid') return;
                        e.stopPropagation();
                        startGesture(e, { kind: 'resize', id: widget.id, origin: placement, handle: h });
                      }}
                      onMouseDown={(e) => e.stopPropagation()}
                    />
                  ))}
                  {resizing && placement.mode === 'grid' && (
                    <div
                      data-testid="size-readout"
                      role="status"
                      className="rounded bg-primary px-1.5 py-0.5 text-xs font-medium tabular-nums text-primary-foreground"
                      style={{ position: 'absolute', right: 4, bottom: 4, zIndex: 6, pointerEvents: 'none' }}
                    >
                      {formatSpan(placement)}
                    </div>
                  )}
                </div>
              );
            })}
            <svg
              aria-hidden
              data-testid="placement-guides"
              style={{
                position: 'absolute',
                left: 0,
                top: 0,
                width: '100%',
                height: '100%',
                pointerEvents: 'none',
                zIndex: 300,
                overflow: 'visible',
              }}
            >
              {guides}
            </svg>
          </WidgetDensityProvider>
        </div>
      </div>
    </div>
  );
}

// The gap between a widget and its toolbar, and the room the editor keeps above the first row so
// a one-line toolbar fits there.
const TOOLBAR_GAP = 6;
const TOOLBAR_ROOM = 32;

// The selected widget's toolbar, laid OUTSIDE the widget it edits: above it where there is room,
// below it where there is not, never over it. Inside the widget it covered the widget's own
// title (and, wrapped in a narrow widget, its content). It is right-aligned with the widget and
// slid back inside the canvas when that would run past an edge, and it wraps only when the
// canvas itself is narrower than it. It measures itself to choose, so a longer translation or a
// wrapped second line moves it rather than clipping it.
//
// Rendered inside the widget's box (positioned against it) so it stays the widget's descendant:
// a press on it is a press on the widget, which keeps it selected.
function FloatingToolbar({
  rect,
  canvasWidth,
  roomAbove,
  children,
}: {
  rect: Rect;
  canvasWidth: number;
  // From the widget's top edge up to the top of what the canvas can scroll to.
  roomAbove: number;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState<{ w: number; h: number } | null>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () =>
      setSize((s) => (s && s.w === el.offsetWidth && s.h === el.offsetHeight ? s : { w: el.offsetWidth, h: el.offsetHeight }));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const w = size?.w ?? 0;
  const h = size?.h ?? 0;
  // In canvas coordinates: right-aligned with the widget, then held inside [0, canvasWidth].
  const x = Math.max(0, Math.min(rect.x + rect.w - w, canvasWidth - w));
  const above = roomAbove >= h + TOOLBAR_GAP;
  return (
    <div
      ref={ref}
      data-no-drag
      data-testid="widget-toolbar"
      data-side={above ? 'above' : 'below'}
      style={{
        position: 'absolute',
        left: x - rect.x,
        top: above ? -(h + TOOLBAR_GAP) : rect.h + TOOLBAR_GAP,
        width: 'max-content',
        maxWidth: Math.max(0, canvasWidth),
        display: 'flex',
        flexWrap: 'wrap',
        justifyContent: 'flex-end',
        alignItems: 'center',
        gap: 4,
        pointerEvents: 'auto',
        // Over its neighbours (and the resize handles), under the placement guides.
        zIndex: 250,
        // Until it has measured itself it has nowhere to be.
        visibility: size ? undefined : 'hidden',
      }}
    >
      {children}
    </div>
  );
}

// The nine host points a relative widget can hang from.
function AnchorPicker({ value, onPick }: { value: PlacementAnchor; onPick: (anchor: PlacementAnchor) => void }) {
  const { t } = useTranslation('dashboards');
  return (
    <div
      role="group"
      aria-label={t('canvasAnchorLabel')}
      className="grid grid-cols-3 gap-px rounded border border-border bg-card p-0.5"
    >
      {ANCHOR_ORDER.map((anchor) => (
        <Button
          key={anchor}
          variant="outline"
          size="inline"
          data-no-drag
          data-testid={`anchor-${anchor}`}
          aria-label={t(ANCHOR_KEYS[anchor])}
          aria-pressed={value === anchor}
          title={t(ANCHOR_KEYS[anchor])}
          className={cn(
            'h-3.5 w-3.5 min-w-0 rounded-sm border-border p-0',
            value === anchor ? 'bg-primary hover:bg-primary' : 'bg-muted hover:bg-accent',
          )}
          onPointerDown={(e) => e.stopPropagation()}
          onMouseDown={(e) => e.stopPropagation()}
          onClick={() => onPick(anchor)}
        />
      ))}
    </div>
  );
}

function ToolbarButton({
  children,
  onClick,
  danger,
  pressed,
  'data-testid': testId,
}: {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
  pressed?: boolean;
  'data-testid'?: string;
}) {
  return (
    <Button
      variant="outline"
      size="inline"
      // data-no-drag keeps the canvas from starting a drag when the button is pressed;
      // stopPropagation keeps the pointer from reaching the widget beneath.
      data-no-drag
      data-testid={testId}
      aria-pressed={pressed}
      className={cn(
        'rounded border-border bg-card px-2 py-0.5 text-xs',
        pressed && 'border-primary bg-primary/10',
        danger && 'text-destructive hover:text-destructive',
      )}
      onPointerDown={(e) => e.stopPropagation()}
      onMouseDown={(e) => e.stopPropagation()}
      onClick={onClick}
    >
      {children}
    </Button>
  );
}

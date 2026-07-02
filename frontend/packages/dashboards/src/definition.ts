// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Load-time helpers for a stored dashboard definition.
//
// dashboard-management stores the definition as opaque JSON (well-formed object,
// size-bounded — nothing more). This package owns the *shape*, so validation and
// default-filling live here rather than on the server: parseDashboardDefinition
// turns an untrusted parsed value into a DashboardDefinition or throws, so the
// renderer never has to guess at a missing canvas/breakpoint/box. Kept permissive
// where a sensible default exists (a bare `{ widgets: [] }` is valid) and strict
// where a wrong value would silently mis-render (unknown widget type, no base box).

import {
  WIDGET_TYPES,
  type Breakpoints,
  type Canvas,
  type DashboardDefinition,
  type WidgetBox,
  type WidgetInstance,
  type WidgetLayout,
  type WidgetType,
} from './types';

// The breakpoint every layout must define; a widget/viewport with no more specific
// box falls back to it. Named once so parse + resolveWidgetBox agree.
export const BASE_BREAKPOINT = 'base';

const WIDGET_TYPE_SET: ReadonlySet<string> = new Set(WIDGET_TYPES);

const DEFAULT_GRID = { snap: true, size: 8 } as const;

// Thrown when a definition cannot be coerced into a renderable shape. The message
// names the offending path so a bad document is diagnosable, not just "invalid".
export class DashboardDefinitionError extends Error {
  constructor(message: string) {
    super(`invalid dashboard definition: ${message}`);
    this.name = 'DashboardDefinitionError';
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function numberAt(rec: Record<string, unknown>, key: string, fallback: number): number {
  const v = rec[key];
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

// parseDashboardDefinition validates a parsed JSON value and returns a definition
// with defaults filled, or throws DashboardDefinitionError. `raw` is the value
// after JSON.parse (the caller owns the parse so a syntax error is theirs to
// handle); an already-typed object round-trips unchanged.
export function parseDashboardDefinition(raw: unknown): DashboardDefinition {
  if (!isRecord(raw)) throw new DashboardDefinitionError('not a JSON object');

  const widgetsRaw = raw.widgets;
  if (!Array.isArray(widgetsRaw)) throw new DashboardDefinitionError('widgets must be an array');

  return {
    schemaVersion: numberAt(raw, 'schemaVersion', 1),
    title: typeof raw.title === 'string' ? raw.title : '',
    canvas: parseCanvas(raw.canvas),
    widgets: widgetsRaw.map((w, i) => parseWidget(w, i)),
  };
}

function parseCanvas(raw: unknown): Canvas {
  const rec = isRecord(raw) ? raw : {};

  const gridRec = isRecord(rec.grid) ? rec.grid : {};
  const grid = {
    snap: typeof gridRec.snap === 'boolean' ? gridRec.snap : DEFAULT_GRID.snap,
    size: numberAt(gridRec, 'size', DEFAULT_GRID.size),
  };

  // Breakpoints must define 'base'; default a single base:0 so a definition that
  // omits responsive layouts still resolves.
  const bpRec = isRecord(rec.breakpoints) ? rec.breakpoints : {};
  const breakpoints: Breakpoints = {};
  for (const [name, width] of Object.entries(bpRec)) {
    if (typeof width === 'number' && Number.isFinite(width)) breakpoints[name] = width;
  }
  if (!(BASE_BREAKPOINT in breakpoints)) breakpoints[BASE_BREAKPOINT] = 0;

  const canvas: Canvas = { grid, breakpoints };
  if (isRecord(rec.background)) {
    const { color, imageUrl } = rec.background;
    canvas.background = {
      color: typeof color === 'string' ? color : null,
      imageUrl: typeof imageUrl === 'string' ? imageUrl : null,
    };
  }
  return canvas;
}

function parseWidget(raw: unknown, index: number): WidgetInstance {
  if (!isRecord(raw)) throw new DashboardDefinitionError(`widgets[${index}] is not an object`);

  const type = raw.type;
  if (typeof type !== 'string' || !WIDGET_TYPE_SET.has(type)) {
    throw new DashboardDefinitionError(`widgets[${index}] has unknown type ${JSON.stringify(type)}`);
  }

  const widget: WidgetInstance = {
    id: typeof raw.id === 'string' && raw.id.length > 0 ? raw.id : generateWidgetId(),
    type: type as WidgetType,
    layout: parseLayout(raw.layout, index),
  };
  // datasource/options are owned by the hub/widget respectively — kept opaque here
  // (the hub validates the selector kind at resolve time), only carried through
  // when present and shaped like a selector (a `kind` discriminant).
  if (isRecord(raw.datasource) && typeof raw.datasource.kind === 'string') {
    widget.datasource = raw.datasource as unknown as WidgetInstance['datasource'];
  }
  if (isRecord(raw.options)) widget.options = raw.options as Record<string, unknown>;
  return widget;
}

function parseLayout(raw: unknown, index: number): WidgetLayout {
  if (!isRecord(raw)) throw new DashboardDefinitionError(`widgets[${index}].layout is missing`);

  const layout: WidgetLayout = {};
  for (const [bp, box] of Object.entries(raw)) {
    if (isRecord(box)) layout[bp] = parseBox(box);
  }
  if (!(BASE_BREAKPOINT in layout)) {
    throw new DashboardDefinitionError(`widgets[${index}].layout has no '${BASE_BREAKPOINT}' box`);
  }
  return layout;
}

function parseBox(rec: Record<string, unknown>): WidgetBox {
  return {
    x: numberAt(rec, 'x', 0),
    y: numberAt(rec, 'y', 0),
    w: numberAt(rec, 'w', 1),
    h: numberAt(rec, 'h', 1),
    z: numberAt(rec, 'z', 0),
  };
}

// resolveWidgetBox returns the box for the active breakpoint, falling back to the
// widget's 'base' box when it defines no override for that breakpoint. Parse
// guarantees a base box exists, so this always resolves.
export function resolveWidgetBox(layout: WidgetLayout, breakpoint: string): WidgetBox {
  return layout[breakpoint] ?? layout[BASE_BREAKPOINT];
}

// activeBreakpoint picks the breakpoint whose min width is the largest one that
// still fits the viewport (falling back to 'base' / the smallest). Deterministic
// given equal widths by preferring the wider min.
export function activeBreakpoint(breakpoints: Breakpoints, viewportWidth: number): string {
  let best = BASE_BREAKPOINT;
  let bestWidth = -1;
  for (const [name, minWidth] of Object.entries(breakpoints)) {
    if (viewportWidth >= minWidth && minWidth > bestWidth) {
      best = name;
      bestWidth = minWidth;
    }
  }
  return best;
}

// generateWidgetId mints a unique widget-instance id. Uses crypto.randomUUID where
// available (browsers, modern Node); the counter fallback keeps ids unique within a
// session for the rare environment without it.
let idCounter = 0;
export function generateWidgetId(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === 'function') return `w-${c.randomUUID()}`;
  idCounter += 1;
  return `w-${idCounter.toString(36)}`;
}

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
  type AnchorTarget,
  type Breakpoints,
  type Canvas,
  type DashboardDefinition,
  type SlotBinding,
  type SlotDefinition,
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

  const def: DashboardDefinition = {
    schemaVersion: numberAt(raw, 'schemaVersion', 1),
    title: typeof raw.title === 'string' ? raw.title : '',
    canvas: parseCanvas(raw.canvas),
    widgets: widgetsRaw.map((w, i) => parseWidget(w, i)),
  };
  // slots — the runtime-binding section (ADR-039). Normalized when present so a
  // stored definition round-trips, but omitted entirely otherwise so a slot-free
  // dashboard serializes unchanged (no spurious `"slots":{}` diff).
  const slots = parseSlots(raw.slots);
  if (slots) def.slots = slots;
  return def;
}

function parseSlots(raw: unknown): Record<string, SlotDefinition> | undefined {
  if (!isRecord(raw)) return undefined;
  const slots: Record<string, SlotDefinition> = {};
  for (const [name, spec] of Object.entries(raw)) {
    if (!isRecord(spec)) continue;
    const type = spec.type === 'anchor' ? 'anchor' : 'device';
    const slot: SlotDefinition = { type };
    if (typeof spec.label === 'string') slot.label = spec.label;
    const binding = parseBinding(spec.defaultBinding);
    if (binding) slot.defaultBinding = binding;
    slots[name] = slot;
  }
  return Object.keys(slots).length > 0 ? slots : undefined;
}

// parseBinding normalizes a slot's default entity binding (device token or anchor
// target), or drops it (undefined) when absent/malformed. The entity only — a
// binding never carries measurement names (those live on the widget's selector).
function parseBinding(raw: unknown): SlotBinding | undefined {
  if (!isRecord(raw)) return undefined;
  if (raw.kind === 'device') {
    const token = stringAt(raw, 'deviceToken');
    return token ? { kind: 'device', deviceToken: token } : undefined;
  }
  if (raw.kind === 'anchor') {
    const anchorRec = isRecord(raw.anchor) ? raw.anchor : {};
    const targetToken = stringAt(anchorRec, 'targetToken');
    // Drop an anchor binding with no target (symmetric with the empty-device-token
    // case) — it names no entity, so it can't be a default binding.
    if (!targetToken) return undefined;
    return {
      kind: 'anchor',
      anchor: {
        relationship: stringAt(anchorRec, 'relationship'),
        targetType: stringAt(anchorRec, 'targetType') as AnchorTarget['targetType'],
        targetToken,
      },
    };
  }
  return undefined;
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
  // datasource is owned by the hub, but the two supported kinds are NORMALIZED here
  // so downstream (hub/widgets) never sees a partial shape (a device selector with
  // no measurements array, an anchor with a non-object target, …). Reserved/other
  // kinds are carried through opaquely — the hub rejects them.
  const ds = parseDatasource(raw.datasource);
  if (ds) widget.datasource = ds;
  if (isRecord(raw.options)) widget.options = raw.options as Record<string, unknown>;
  return widget;
}

function stringAt(rec: Record<string, unknown>, key: string): string {
  const v = rec[key];
  return typeof v === 'string' ? v : '';
}

function stringArrayAt(rec: Record<string, unknown>, key: string): string[] {
  const v = rec[key];
  return Array.isArray(v) ? v.filter((m): m is string => typeof m === 'string') : [];
}

// parseDatasource coerces a raw datasource into a normalized selector, or drops it
// (returns undefined) when it is absent or its `kind` is not a non-empty string.
function parseDatasource(raw: unknown): WidgetInstance['datasource'] | undefined {
  if (!isRecord(raw)) return undefined;
  const kind = raw.kind;
  if (typeof kind !== 'string' || kind.length === 0) return undefined;

  if (kind === 'device') {
    return { kind: 'device', deviceToken: stringAt(raw, 'deviceToken'), measurements: stringArrayAt(raw, 'measurements') };
  }
  if (kind === 'anchor') {
    const anchorRec = isRecord(raw.anchor) ? raw.anchor : {};
    const selector: WidgetInstance['datasource'] = {
      kind: 'anchor',
      anchor: {
        relationship: stringAt(anchorRec, 'relationship'),
        // targetType defaults to '' (the config panel constrains it to the union;
        // a hand-edited/empty value round-trips rather than being silently coerced).
        targetType: stringAt(anchorRec, 'targetType') as AnchorTarget['targetType'],
        targetToken: stringAt(anchorRec, 'targetToken'),
      },
      measurements: stringArrayAt(raw, 'measurements'),
    };
    if (isRecord(raw.aggregation)) {
      (selector as { aggregation?: unknown }).aggregation = raw.aggregation;
    }
    return selector;
  }

  if (kind === 'slot') {
    // Runtime-binding kind: the entity is resolved at mount via the Hub's binding
    // manifest; the widget carries only the slot name + the measurements it wants.
    return { kind: 'slot', slot: stringAt(raw, 'slot'), measurements: stringArrayAt(raw, 'measurements') };
  }

  // Reserved/other kinds: carry through opaquely (the hub rejects them).
  return raw as unknown as WidgetInstance['datasource'];
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

// serializeDefinition is the canonical on-the-wire JSON — the inverse of
// parseDashboardDefinition — that dashboard-management stores. Named (not an inline
// JSON.stringify) so every consumer that persists a definition shares one format.
export function serializeDefinition(def: DashboardDefinition): string {
  return JSON.stringify(def);
}

// isDirty reports whether two definitions differ. A structural JSON compare is
// enough: both sides come from parse/edit transforms that preserve key order, so
// it only flips on a real change. Drives an editor's save/dirty state.
export function isDirty(a: DashboardDefinition, b: DashboardDefinition): boolean {
  return serializeDefinition(a) !== serializeDefinition(b);
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

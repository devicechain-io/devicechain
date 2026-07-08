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
  type CanvasGrid,
  type CanvasSizing,
  type DashboardDefinition,
  type SlotBinding,
  type SlotDefinition,
  type SlotScope,
  type WidgetBox,
  type WidgetInstance,
  type WidgetLayout,
  type WidgetType,
} from './types';

// The breakpoint every layout must define; a widget/viewport with no more specific
// box falls back to it. Named once so parse + resolveWidgetBox agree.
export const BASE_BREAKPOINT = 'base';

const WIDGET_TYPE_SET: ReadonlySet<string> = new Set(WIDGET_TYPES);

// The default canvas grid: 24 fluid columns, an 8px gutter, 40px rows — a
// high-resolution grid that fills its container width (ADR-039 amendment).
const DEFAULT_GRID: CanvasGrid = { columns: 24, gap: 8, rowHeight: 40 };
const DEFAULT_SIZING: CanvasSizing = 'fill';

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
    // Skip a `__proto__` key: `slots['__proto__'] = …` hits the prototype setter (the
    // slot is lost + the map's prototype is swapped) rather than creating an own
    // property — the same guard parseBindingManifest applies to its untrusted input.
    if (name === '__proto__') continue;
    if (!isRecord(spec)) continue;
    const type = spec.type === 'anchor' ? 'anchor' : 'device';
    const slot: SlotDefinition = { type };
    if (typeof spec.label === 'string') slot.label = spec.label;
    const binding = parseSlotBinding(spec.defaultBinding);
    if (binding) slot.defaultBinding = binding;
    // Carry a well-formed `scope` (the context hierarchy). Structural parse only here;
    // cross-slot validity (parent exists, is an anchor, no cycles) is checked once the
    // whole map is built — a scope-blind whitelist would DROP the field and silently
    // erase the hierarchy on every load.
    const scope = parseScope(spec.scope);
    if (scope) slot.scope = scope;
    slots[name] = slot;
  }
  if (Object.keys(slots).length === 0) return undefined;
  validateScopes(slots); // drops any scope with a missing/non-anchor/self/cyclic parent
  return slots;
}

// parseScope reads a candidate `{ parent, strategy }` — structural shape only. A
// non-string/empty parent drops the scope; an unrecognized strategy defaults to 'first'.
function parseScope(raw: unknown): SlotScope | undefined {
  if (!isRecord(raw)) return undefined;
  const parent = typeof raw.parent === 'string' ? raw.parent : '';
  if (!parent) return undefined;
  return { parent, strategy: raw.strategy === 'manual' ? 'manual' : 'first' };
}

// validateScopes drops (in place) any slot scope whose parent is missing, not an
// anchor-typed slot, self-referential, or part of a cycle — degrade, don't throw, since
// the definition is opaque JSON the backend never validated. Dropping a bad scope leaves
// the slot as a plain (root) slot rather than failing the whole dashboard to parse.
function validateScopes(slots: Record<string, SlotDefinition>): void {
  const drop: string[] = [];
  for (const [name, slot] of Object.entries(slots)) {
    if (!slot.scope) continue;
    const parentName = slot.scope.parent;
    const parent = Object.prototype.hasOwnProperty.call(slots, parentName) ? slots[parentName] : undefined;
    if (!parent || parent.type !== 'anchor' || parentName === name || inScopeCycle(slots, name)) {
      drop.push(name);
    }
  }
  for (const name of drop) delete slots[name].scope;
}

// inScopeCycle walks the parent chain from `start`; a revisited node means the chain
// reaches a loop (a forest has none). This also drops a slot that merely POINTS INTO a
// cycle without being on it — a deliberately conservative degrade: every affected slot
// becomes a safe root rather than risking a dangling parent, and real (non-hand-edited)
// definitions never contain cycles. Reads scopes via own-property lookup so a slot named
// '__proto__'/'constructor' can't reach an inherited member.
function inScopeCycle(slots: Record<string, SlotDefinition>, start: string): boolean {
  const seen = new Set<string>();
  let cur: string | undefined = start;
  while (cur) {
    if (seen.has(cur)) return true;
    seen.add(cur);
    const slot: SlotDefinition | undefined = Object.prototype.hasOwnProperty.call(slots, cur)
      ? slots[cur]
      : undefined;
    cur = slot?.scope?.parent;
  }
  return false;
}

// parseSlotBinding normalizes a slot binding (device token or anchor target), or
// drops it (undefined) when absent/malformed. The entity only — a binding never
// carries measurement names (those live on the widget's selector). Exported so the
// runtime binding manifest (parseBindingManifest) validates host input the same way.
export function parseSlotBinding(raw: unknown): SlotBinding | undefined {
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

// parseGrid coerces the canvas grid, filling defaults. `columns`/`rowHeight` are
// floored to >=1 so a zero never yields an unusable `repeat(0,1fr)` / 0-px rows.
// `gap` accepts a single number or a {row,col} pair; anything else → the default.
function parseGrid(raw: unknown): CanvasGrid {
  const rec = isRecord(raw) ? raw : {};
  const columns = Math.max(1, Math.round(numberAt(rec, 'columns', DEFAULT_GRID.columns)));
  const rowHeight = Math.max(1, numberAt(rec, 'rowHeight', DEFAULT_GRID.rowHeight));
  let gap: CanvasGrid['gap'] = DEFAULT_GRID.gap;
  if (typeof rec.gap === 'number' && Number.isFinite(rec.gap)) {
    gap = Math.max(0, rec.gap);
  } else if (isRecord(rec.gap)) {
    // A missing axis falls back to the default gutter (not 0), so a partial
    // `{gap:{row:12}}` keeps the default column gutter rather than losing it.
    gap = {
      row: Math.max(0, numberAt(rec.gap, 'row', DEFAULT_GRID.gap as number)),
      col: Math.max(0, numberAt(rec.gap, 'col', DEFAULT_GRID.gap as number)),
    };
  }
  return { columns, gap, rowHeight };
}

// parseSizing coerces the container-sizing knob. 'fill' (the default) or a
// single-axis fixed box `{width}` / `{height}`; a malformed value falls back to fill.
function parseSizing(raw: unknown): CanvasSizing {
  if (isRecord(raw)) {
    if (typeof raw.width === 'number' && Number.isFinite(raw.width)) {
      return { width: Math.max(1, raw.width) };
    }
    if (typeof raw.height === 'number' && Number.isFinite(raw.height)) {
      return { height: Math.max(1, raw.height) };
    }
  }
  return DEFAULT_SIZING;
}

function parseCanvas(raw: unknown): Canvas {
  const rec = isRecord(raw) ? raw : {};

  const grid = parseGrid(rec.grid);
  const sizing = parseSizing(rec.sizing);

  // Breakpoints must define 'base'; default a single base:0 so a definition that
  // omits responsive layouts still resolves.
  const bpRec = isRecord(rec.breakpoints) ? rec.breakpoints : {};
  const breakpoints: Breakpoints = {};
  for (const [name, width] of Object.entries(bpRec)) {
    if (typeof width === 'number' && Number.isFinite(width)) breakpoints[name] = width;
  }
  if (!(BASE_BREAKPOINT in breakpoints)) breakpoints[BASE_BREAKPOINT] = 0;

  const canvas: Canvas = { grid, sizing, breakpoints };
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
  // col/row are 0-based start lines (clamped >=0); spans are >=1 so a widget can't
  // vanish. offset is an optional signed-pixel nudge — carried only when present so a
  // box without it round-trips unchanged.
  const box: WidgetBox = {
    col: Math.max(0, Math.round(numberAt(rec, 'col', 0))),
    colSpan: Math.max(1, Math.round(numberAt(rec, 'colSpan', 1))),
    row: Math.max(0, Math.round(numberAt(rec, 'row', 0))),
    rowSpan: Math.max(1, Math.round(numberAt(rec, 'rowSpan', 1))),
    // z rounds too: a fractional zIndex is invalid CSS and silently drops to auto,
    // so keep it an integer like every other box field.
    z: Math.round(numberAt(rec, 'z', 0)),
  };
  if (isRecord(rec.offset)) {
    box.offset = { x: numberAt(rec.offset, 'x', 0), y: numberAt(rec.offset, 'y', 0) };
  }
  return box;
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

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
// where a wrong value would silently mis-render (a widget with no type, no base box).
//
// A widget whose type this build does not know is NOT an error: it is a widget a newer
// release added. It parses to a placeholder that keeps the stored object verbatim, and
// serializeDefinition writes that object back (see UnknownWidgetInstance), so an older
// host that loads and saves a newer board leaves the newer widget exactly as it was.

import {
  CURRENT_SCHEMA_VERSION,
  FRAME_BORDER_WIDTHS,
  FRAME_HEADERS,
  FRAME_RADII,
  FRAME_VARIANTS,
  PLACEMENT_ANCHORS,
  RESERVED_PLACEMENT_MODES,
  THEME_COLOR_TOKENS,
  WIDGET_TYPES,
  isCanvasDensity,
  type AnchorTarget,
  type BoardTimeRange,
  type Breakpoints,
  type Canvas,
  type CanvasGrid,
  type CanvasSizing,
  type DashboardDefinition,
  type ExtensionWidgetInstance,
  type ExtensionWidgetType,
  type FrameBorder,
  type GridLength,
  type KnownWidgetInstance,
  type LocationSelection,
  type Placement,
  type PlacementAnchor,
  type SlotBinding,
  type SlotDefinition,
  type SlotCandidates,
  type SlotScope,
  type WidgetFrameSpec,
  type WidgetImplementationRef,
  type WidgetInstance,
  type WidgetLayout,
  type UnknownWidgetInstance,
  type UnresolvedWidgetReason,
  type WidgetType,
  UNKNOWN_WIDGET_TYPE,
} from './types';
import {
  BASE_BREAKPOINT,
  DEFAULT_GRID,
  gridFootprintsOverlap,
  isSpaceToken,
  layoutBreakpoints,
  resolvePlacement,
} from './grid';
import { convertLegacyDefinition, GRID_SCHEMA_VERSION, storedSchemaVersion } from './legacy-grid';
import { convertSampleWindows } from './legacy-window';
import { isValidSpanSeconds } from './time-range';
import { parseWidgetRef, unresolvedReason } from './widget-compat';
import type { WidgetCatalog } from './widget-catalog';
import { isRecord } from './is-record';

// The breakpoint every layout must define; a widget/viewport with no more specific placement
// falls back to it.
export { BASE_BREAKPOINT };

const WIDGET_TYPE_SET: ReadonlySet<string> = new Set(WIDGET_TYPES);

const DEFAULT_SIZING: CanvasSizing = 'fill';

// Thrown when a definition cannot be coerced into a renderable shape. The message
// names the offending path so a bad document is diagnosable, not just "invalid".
//
// `code` is set only when the document is one this build must not read at all:
// 'schemaTooNew' means it was saved by a newer release (its schemaVersion, `found`, is above
// CURRENT_SCHEMA_VERSION), and reading it as the current format would draw it wrong without
// saying so (every widget collapsed to a single cell). It is the same class, so a caller that
// knows nothing of the code still refuses the document; a caller that wants to say WHY tests
// `code`, which -- unlike `instanceof` -- also holds across two bundled copies of this package.
export class DashboardDefinitionError extends Error {
  constructor(
    message: string,
    readonly code?: 'schemaTooNew',
    readonly found?: number,
  ) {
    super(`invalid dashboard definition: ${message}`);
    this.name = 'DashboardDefinitionError';
  }
}

function numberAt(rec: Record<string, unknown>, key: string, fallback: number): number {
  const v = rec[key];
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

// parseDashboardDefinition validates a parsed JSON value and returns a definition
// with defaults filled, or throws DashboardDefinitionError. `raw` is the value
// after JSON.parse (the caller owns the parse so a syntax error is theirs to
// handle); an already-typed object round-trips unchanged, an unknown-type
// placeholder included.
//
// `options.catalog` is the React-free view of the host's widget registry. With it, a
// namespaced extension id the catalog holds parses to an ExtensionWidgetInstance, and a
// stored `widget` version reference is checked against the installed descriptor; a widget
// the catalog cannot resolve (absent, no/invalid metadata, settings or SDK generation
// differing) becomes a placeholder carrying its reason and its stored JSON. Without a
// catalog the parser knows the built-ins only (legacy behavior), and any other id is a
// placeholder.
export interface ParseDefinitionOptions {
  catalog?: WidgetCatalog;
}

export function parseDashboardDefinition(input: unknown, options: ParseDefinitionOptions = {}): DashboardDefinition {
  if (!isRecord(input)) throw new DashboardDefinitionError('not a JSON object');
  // Before anything reads the shape: a newer format is refused whole, not read as this one.
  const stored = storedSchemaVersion(input);
  if (stored > CURRENT_SCHEMA_VERSION) {
    throw new DashboardDefinitionError(`schemaVersion ${stored} is too new`, 'schemaTooNew', stored);
  }
  if (!Array.isArray(input.widgets)) throw new DashboardDefinitionError('widgets must be an array');

  // A document stored in an older format is converted on load, one step at a time: the square-cell
  // grid (approximately; see ./legacy-grid), then the time span (./legacy-window). The result
  // says so. Everything below reads the current format only.
  const legacy = stored < CURRENT_SCHEMA_VERSION;
  const gridConverted = stored < GRID_SCHEMA_VERSION;
  let raw: Record<string, unknown> = input;
  if (gridConverted) raw = convertLegacyDefinition(raw);
  let flaggedWindows: number[] = [];
  if (legacy) {
    const converted = convertSampleWindows(raw);
    raw = converted.doc;
    flaggedWindows = converted.flagged;
  }
  const widgetsRaw = raw.widgets as unknown[];

  const def: DashboardDefinition = {
    // The format this release writes (a newer one was refused above): a save writes that format.
    schemaVersion: CURRENT_SCHEMA_VERSION,
    title: typeof raw.title === 'string' ? raw.title : '',
    canvas: parseCanvas(raw.canvas),
    widgets: widgetsRaw.map((w, i) => parseWidget(w, i, options.catalog)),
  };
  // slots — the runtime-binding section (ADR-039). Normalized when present so a
  // stored definition round-trips, but omitted entirely otherwise so a slot-free
  // dashboard serializes unchanged (no spurious `"slots":{}` diff).
  const slots = parseSlots(raw.slots);
  if (slots) def.slots = slots;
  const timeRange = parseBoardTimeRange(raw.timeRange);
  if (timeRange) def.timeRange = timeRange;
  validatePlacements(def);
  // Only a conversion that changed something worth reviewing leaves a mark: a re-laid-out grid
  // (an empty board has no layout to review), or a chart that lost its sample-count window. A
  // v2 board with nothing to flag converts silently -- it lost nothing. A typed definition handed
  // back to the parser keeps the marks it already carries.
  const flaggedIds = flaggedWindows.map((i) => def.widgets[i]?.id).filter((id): id is string => id !== undefined);
  if ((gridConverted && def.widgets.length > 0) || flaggedIds.length > 0) def.convertedFrom = stored;
  else if (typeof raw.convertedFrom === 'number') def.convertedFrom = raw.convertedFrom;
  if (flaggedIds.length > 0) def.convertedWindows = flaggedIds;
  else if (Array.isArray(raw.convertedWindows) && raw.convertedWindows.every((id) => typeof id === 'string')) {
    def.convertedWindows = raw.convertedWindows as string[];
  }
  return def;
}

// validatePlacements refuses a layout that cannot be drawn as declared, at every breakpoint a
// widget can be shown at (its own placement there, or its base one):
//   - two GRID footprints that overlap. Overlap is something a widget declares (the relative
//     mode), never something that happens to a grid widget;
//   - a relative widget whose host does not exist, is itself, or is a relative widget whose own
//     host is relative too (a chain deeper than one level).
// It throws rather than degrading: a definition that overlaps would draw one widget over another
// with nothing to say why.
function validatePlacements(def: DashboardDefinition): void {
  for (const bp of layoutBreakpoints(def.canvas.breakpoints, def.widgets)) {
    const at = bp === BASE_BREAKPOINT ? '' : ` at breakpoint '${bp}'`;
    const placements = new Map<string, Placement>();
    for (const w of def.widgets) placements.set(w.id, resolvePlacement(w.layout, bp));
    const grid = [...placements].filter((e): e is [string, Extract<Placement, { mode: 'grid' }>] => e[1].mode === 'grid');
    for (let i = 0; i < grid.length; i++) {
      for (let j = i + 1; j < grid.length; j++) {
        if (gridFootprintsOverlap(grid[i][1], grid[j][1])) {
          throw new DashboardDefinitionError(
            `widgets '${grid[i][0]}' and '${grid[j][0]}' overlap on the grid${at}; ` +
              `a widget that is meant to sit over another is placed relative to it`,
          );
        }
      }
    }
    for (const [id, p] of placements) {
      if (p.mode !== 'relative') continue;
      if (p.host === id) throw new DashboardDefinitionError(`widget '${id}' is placed relative to itself${at}`);
      const host = placements.get(p.host);
      if (!host) {
        throw new DashboardDefinitionError(`widget '${id}' is placed relative to '${p.host}', which does not exist${at}`);
      }
      if (host.mode === 'relative' && placements.get(host.host)?.mode !== 'grid') {
        throw new DashboardDefinitionError(
          `widget '${id}' is placed relative to '${p.host}', which is relative to another relative widget${at}; ` +
            `a relative widget's host may be relative only when that host sits on a grid widget`,
        );
      }
    }
  }
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
    const candidates = parseCandidates(spec.candidates);
    if (candidates) slot.candidates = candidates;
    slots[name] = slot;
  }
  if (Object.keys(slots).length === 0) return undefined;
  validateScopes(slots); // drops any scope with a missing/non-anchor/self/cyclic parent
  return slots;
}

// parseCandidates reads a root slot's picker type filter: non-empty string tokens only,
// anything else dropped, and nothing left means no filter.
function parseCandidates(raw: unknown): SlotCandidates | undefined {
  if (!isRecord(raw)) return undefined;
  const token = (v: unknown) => (typeof v === 'string' && v ? v : undefined);
  const areaType = token(raw.areaType);
  const assetType = token(raw.assetType);
  const deviceType = token(raw.deviceType);
  if (!areaType && !assetType && !deviceType) return undefined;
  return {
    ...(areaType ? { areaType } : {}),
    ...(assetType ? { assetType } : {}),
    ...(deviceType ? { deviceType } : {}),
  };
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

// canScopeSlot reports whether `child` may take a scope with parent `parentName` — the
// authoring-time equivalent of the loader's validateScopes rules, so the editor cannot write
// a scope the loader would silently drop on reload. A scope is valid iff the parent exists,
// is an anchor-typed slot, is not the child itself, and adding the child→parent edge creates
// no cycle (walking the parent's own ancestor chain must not reach the child). Reads scopes
// via own-property lookup so a slot named '__proto__'/'constructor' can't reach an inherited
// member. Pure — never mutates the slots map.
export function canScopeSlot(
  slots: Record<string, SlotDefinition>,
  child: string,
  parentName: string,
): boolean {
  if (child === parentName) return false;
  const parent = Object.prototype.hasOwnProperty.call(slots, parentName) ? slots[parentName] : undefined;
  if (!parent || parent.type !== 'anchor') return false;
  // Would child→parent close a loop? The parent (transitively) must not already depend on
  // the child. Guard against a pre-existing upstream cycle so this can't spin.
  const seen = new Set<string>();
  let cur: string | undefined = parentName;
  while (cur) {
    if (cur === child) return false;
    // A chain that reaches a pre-existing cycle is rejected too (not just accepted-by-break),
    // matching the loader's inScopeCycle, which drops any slot whose ancestry reaches a loop.
    if (seen.has(cur)) return false;
    seen.add(cur);
    const slot: SlotDefinition | undefined = Object.prototype.hasOwnProperty.call(slots, cur)
      ? slots[cur]
      : undefined;
    cur = slot?.scope?.parent;
  }
  return true;
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

// parseGridLength reads one length: a finite number (clamped to >= 0) or a spacing token. An
// absent value takes the fallback; any other string or type is refused, since a misspelled token
// would otherwise draw a different board from the one authored.
function parseGridLength(raw: unknown, fallback: GridLength, where: string): GridLength {
  if (raw === undefined) return fallback;
  if (typeof raw === 'number' && Number.isFinite(raw)) return Math.max(0, raw);
  if (isSpaceToken(raw)) return raw;
  throw new DashboardDefinitionError(`${where} is ${JSON.stringify(raw)}; a length is a number of px or a space-0..space-4 token`);
}

// parseGrid coerces the canvas grid, filling defaults. `columns` is floored to an integer >= 1
// and `minCellSize` to >= 1, so neither can yield an unusable grid. `gutter` is one length or an
// {x,y} pair; `padding` one length or a {top,right,bottom,left} set; a side an object leaves
// out takes the default.
function parseGrid(raw: unknown): CanvasGrid {
  const rec = isRecord(raw) ? raw : {};
  const columns = Math.max(1, Math.round(numberAt(rec, 'columns', DEFAULT_GRID.columns)));
  const minCellSize = Math.max(1, numberAt(rec, 'minCellSize', DEFAULT_GRID.minCellSize));
  const dg = DEFAULT_GRID.gutter as GridLength;
  const gutter: CanvasGrid['gutter'] = isRecord(rec.gutter)
    ? {
        x: parseGridLength(rec.gutter.x, dg, 'canvas.grid.gutter.x'),
        y: parseGridLength(rec.gutter.y, dg, 'canvas.grid.gutter.y'),
      }
    : parseGridLength(rec.gutter, dg, 'canvas.grid.gutter');
  const dp = DEFAULT_GRID.padding as GridLength;
  const padding: CanvasGrid['padding'] = isRecord(rec.padding)
    ? {
        top: parseGridLength(rec.padding.top, dp, 'canvas.grid.padding.top'),
        right: parseGridLength(rec.padding.right, dp, 'canvas.grid.padding.right'),
        bottom: parseGridLength(rec.padding.bottom, dp, 'canvas.grid.padding.bottom'),
        left: parseGridLength(rec.padding.left, dp, 'canvas.grid.padding.left'),
      }
    : parseGridLength(rec.padding, dp, 'canvas.grid.padding');
  return { columns, minCellSize, gutter, padding };
}

// parseBreakpoints keeps every well-formed breakpoint (a min width, or {minWidth, columns}) and
// drops a malformed one, as it always has. `base` is added at 0 when absent; it is always in the
// grid's own column count, so a `base` that declares another count is refused.
function parseBreakpoints(raw: unknown, grid: CanvasGrid): Breakpoints {
  const rec = isRecord(raw) ? raw : {};
  const breakpoints: Breakpoints = {};
  for (const [name, spec] of Object.entries(rec)) {
    if (name === '__proto__') continue;
    if (typeof spec === 'number' && Number.isFinite(spec)) {
      breakpoints[name] = spec;
    } else if (
      isRecord(spec) &&
      typeof spec.minWidth === 'number' &&
      Number.isFinite(spec.minWidth) &&
      typeof spec.columns === 'number' &&
      Number.isInteger(spec.columns) &&
      spec.columns >= 1
    ) {
      breakpoints[name] = { minWidth: spec.minWidth, columns: spec.columns };
    }
  }
  const base = breakpoints[BASE_BREAKPOINT];
  if (base === undefined) breakpoints[BASE_BREAKPOINT] = 0;
  else if (typeof base === 'object' && base.columns !== grid.columns) {
    throw new DashboardDefinitionError(
      `breakpoint '${BASE_BREAKPOINT}' declares ${base.columns} columns; the base layout is in canvas.grid.columns (${grid.columns})`,
    );
  }
  return breakpoints;
}

function oneOf<T extends string | number>(values: readonly T[], raw: unknown, where: string): T | undefined {
  if (raw === undefined) return undefined;
  if ((values as readonly unknown[]).includes(raw)) return raw as T;
  throw new DashboardDefinitionError(`${where} is ${JSON.stringify(raw)}; expected one of ${values.map((v) => JSON.stringify(v)).join(', ')}`);
}

// parseFrame reads a frame (canvas default or widget override). Every value is a token from a
// closed list and anything else is REFUSED, a free colour above all: a frame that quietly dropped
// `#f00` would render a different card from the one authored, and one that kept it would break
// dark mode and the ISA-101 theme. Keys a frame does not have are ignored.
function parseFrame(raw: unknown, where: string): WidgetFrameSpec | undefined {
  if (raw === undefined) return undefined;
  if (!isRecord(raw)) throw new DashboardDefinitionError(`${where} is not an object`);
  const frame: WidgetFrameSpec = {};
  const variant = oneOf(FRAME_VARIANTS, raw.variant, `${where}.variant`);
  if (variant) frame.variant = variant;
  if (raw.border !== undefined) {
    if (!isRecord(raw.border)) throw new DashboardDefinitionError(`${where}.border is not an object`);
    const border: FrameBorder = {};
    const width = oneOf(FRAME_BORDER_WIDTHS, raw.border.width, `${where}.border.width`);
    if (width !== undefined) border.width = width;
    const radius = oneOf(FRAME_RADII, raw.border.radius, `${where}.border.radius`);
    if (radius) border.radius = radius;
    const color = oneOf(THEME_COLOR_TOKENS, raw.border.color, `${where}.border.color`);
    if (color) border.color = color;
    frame.border = border;
  }
  if (raw.padding !== undefined) {
    if (!isSpaceToken(raw.padding)) {
      throw new DashboardDefinitionError(`${where}.padding is ${JSON.stringify(raw.padding)}; expected a space-0..space-4 token`);
    }
    frame.padding = raw.padding;
  }
  const background = oneOf([...THEME_COLOR_TOKENS, 'transparent' as const], raw.background, `${where}.background`);
  if (background) frame.background = background;
  const header = oneOf(FRAME_HEADERS, raw.header, `${where}.header`);
  if (header) frame.header = header;
  return frame;
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

// parseBoardTimeRange reads the board's authored default span. A value that is not a usable span
// (not an integer, under a minute, over a week) is IGNORED, so the board draws the default hour:
// failing the whole board for a bad range would hide every widget on it, and clamping would turn
// a typo into a window nobody wrote.
function parseBoardTimeRange(raw: unknown): BoardTimeRange | undefined {
  if (!isRecord(raw) || !isValidSpanSeconds(raw.defaultSeconds)) return undefined;
  return { defaultSeconds: raw.defaultSeconds };
}

function parseCanvas(raw: unknown): Canvas {
  const rec = isRecord(raw) ? raw : {};

  const grid = parseGrid(rec.grid);
  const sizing = parseSizing(rec.sizing);
  // Breakpoints must define 'base'; a definition that omits responsive layouts gets base:0.
  const breakpoints = parseBreakpoints(rec.breakpoints, grid);

  const canvas: Canvas = { grid, sizing, breakpoints };
  const frame = parseFrame(rec.frame, 'canvas.frame');
  if (frame) canvas.frame = frame;
  // An unknown value falls back to the default (absent) rather than failing the parse: a
  // definition written by a newer release still renders.
  if (isCanvasDensity(rec.density)) canvas.density = rec.density;
  if (isRecord(rec.background)) {
    const { color, imageUrl } = rec.background;
    canvas.background = {
      color: typeof color === 'string' ? color : null,
      imageUrl: typeof imageUrl === 'string' ? imageUrl : null,
    };
  }
  return canvas;
}

function parseWidget(raw: unknown, index: number, catalog: WidgetCatalog | undefined): WidgetInstance {
  if (!isRecord(raw)) throw new DashboardDefinitionError(`widgets[${index}] is not an object`);

  const type = raw.type;
  // A missing, empty or non-string type names nothing a newer viewer could render: the
  // document is broken, not newer.
  if (typeof type !== 'string' || type.length === 0) {
    throw new DashboardDefinitionError(`widgets[${index}] has unknown type ${JSON.stringify(type)}`);
  }
  if (type === UNKNOWN_WIDGET_TYPE && isRecord(raw.raw)) return reparseUnknownWidget(raw, index, catalog);

  const builtin = WIDGET_TYPE_SET.has(type);
  const reason = unresolvedReason(type, raw.widget, catalog, builtin) ?? (builtin ? undefined : reservedFieldReason(type, raw));
  if (reason) return parseUnknownWidget(raw, index, reason);

  const ref = parseWidgetRef(raw.widget) ?? undefined;
  const widget: KnownWidgetInstance | ExtensionWidgetInstance = builtin
    ? {
        id: storedWidgetId(raw) ?? generateWidgetId(),
        type: type as WidgetType,
        ...(ref ? { widget: ref } : {}),
        layout: parseLayout(raw.layout, index),
      }
    : {
        id: storedWidgetId(raw) ?? generateWidgetId(),
        type: type as ExtensionWidgetType,
        widget: ref as WidgetImplementationRef, // unresolvedReason guarantees a valid ref here
        layout: parseLayout(raw.layout, index),
      };
  // datasource is owned by the hub, but the two supported kinds are NORMALIZED here
  // so downstream (hub/widgets) never sees a partial shape (a device selector with
  // no measurements array, an anchor with a non-object target, …). Reserved/other
  // kinds are carried through opaquely — the hub rejects them.
  const ds = parseDatasource(raw.datasource, index);
  if (ds) widget.datasource = ds;
  if (isRecord(raw.options)) widget.options = raw.options as Record<string, unknown>;
  const frame = parseFrame(raw.frame, `widgets[${index}].frame`);
  if (frame) widget.frame = frame;
  if (!builtin) return withStoredFields(raw, widget as ExtensionWidgetInstance);
  return widget;
}

// The envelope fields the parser owns on a resolved widget. Everything else a stored widget
// carries is outside the owned schema.
const OWNED_WIDGET_KEYS: ReadonlySet<string> = new Set(['id', 'type', 'widget', 'layout', 'datasource', 'options', 'frame']);

// withStoredFields keeps, on a resolved EXTENSION widget, every stored field the envelope does
// not own (a package's own top-level additions, extra keys inside `widget`), in the stored key
// order, so a load/save round trip writes them back byte-for-byte. Owned fields take their
// parsed (normalized) values. Built-ins are deliberately left as they always were (only the
// owned envelope survives), so an existing board's saved bytes do not change.
function withStoredFields(raw: Record<string, unknown>, parsed: ExtensionWidgetInstance): ExtensionWidgetInstance {
  const owned = parsed as unknown as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  // defineProperty, not assignment: a JSON.parse'd `__proto__` key is an own field to keep,
  // and assigning it would set the prototype instead.
  const put = (key: string, value: unknown) =>
    Object.defineProperty(out, key, { value, enumerable: true, writable: true, configurable: true });
  for (const key of Object.keys(raw)) {
    if (!OWNED_WIDGET_KEYS.has(key)) put(key, JSON.parse(JSON.stringify(raw[key])) as unknown);
    else if (key === 'widget') put(key, { ...(raw.widget as Record<string, unknown>), ...parsed.widget });
    else if (owned[key] !== undefined) put(key, owned[key]);
  }
  for (const key of OWNED_WIDGET_KEYS) {
    if (!Object.prototype.hasOwnProperty.call(out, key) && owned[key] !== undefined) put(key, owned[key]);
  }
  return out as unknown as ExtensionWidgetInstance;
}

// A stored extension widget that uses a field name the parsed model reserves for placeholders
// cannot be carried on a resolved instance without being mistaken for one, so it is kept
// verbatim as a placeholder instead of having the field dropped.
function reservedFieldReason(type: string, raw: Record<string, unknown>): UnresolvedWidgetReason | undefined {
  const clash = ['raw', 'reason'].find((k) => Object.prototype.hasOwnProperty.call(raw, k));
  return clash === undefined
    ? undefined
    : { code: 'invalid-metadata', message: `"${type}" carries a top-level "${clash}" field, which the dashboard format reserves` };
}

// The id a stored widget carries, or undefined when it has none worth keeping.
function storedWidgetId(raw: Record<string, unknown>): string | undefined {
  return typeof raw.id === 'string' && raw.id.length > 0 ? raw.id : undefined;
}

// parseUnknownWidget builds the placeholder for a widget of a type this build does not
// know. Only `id` and `layout` are read, through the same rules as every other widget,
// so the canvas can place it; a layout with no base box still fails the parse, because
// a widget that cannot be placed cannot be shown even as a placeholder.
//
// `raw` is a JSON copy of the stored object: detached from the caller's value (which a
// host may go on to mutate), and exactly what JSON.stringify will write back.
function parseUnknownWidget(
  raw: Record<string, unknown>,
  index: number,
  reason: UnresolvedWidgetReason,
): UnknownWidgetInstance {
  return {
    id: storedWidgetId(raw) ?? generateWidgetId(),
    type: UNKNOWN_WIDGET_TYPE,
    layout: parseLayout(raw.layout, index),
    raw: JSON.parse(JSON.stringify(raw)) as Record<string, unknown>,
    reason,
  };
}

// reparseUnknownWidget takes back a placeholder this parser already built — a typed
// definition handed to the parser again — as the same placeholder: its stored object is
// kept as its raw, and its id and layout (which may carry an edit) come from the
// placeholder. Wrapping it again would make the save write the wrapper and lose the
// widget. The serializer never writes the placeholder type, so no stored document takes
// this path.
function reparseUnknownWidget(
  raw: Record<string, unknown>,
  index: number,
  catalog: WidgetCatalog | undefined,
): UnknownWidgetInstance {
  const stored = JSON.parse(JSON.stringify(raw.raw)) as Record<string, unknown>;
  const storedType = typeof stored.type === 'string' ? stored.type : '';
  // The reason is recomputed against THIS catalog (it is never stored); a stored object the
  // catalog now resolves stays a placeholder here, because only a parse of the stored
  // document — not a re-parse of a typed definition — promotes a widget.
  const reason = unresolvedReason(storedType, stored.widget, catalog, WIDGET_TYPE_SET.has(storedType)) ?? {
    code: 'unregistered' as const,
    message: `"${storedType}" is not available in this host`,
  };
  return {
    id: storedWidgetId(raw) ?? generateWidgetId(),
    type: UNKNOWN_WIDGET_TYPE,
    layout: parseLayout(raw.layout, index),
    raw: stored,
    reason,
  };
}

function stringAt(rec: Record<string, unknown>, key: string): string {
  const v = rec[key];
  return typeof v === 'string' ? v : '';
}

function stringArrayAt(rec: Record<string, unknown>, key: string): string[] {
  const v = rec[key];
  return Array.isArray(v) ? v.filter((m): m is string => typeof m === 'string') : [];
}

// parseLocationSelection reads a selector's OPTIONAL location-series field.
//
// 🔴 Additive, and it has to stay that way: every definition stored before the location
// channel existed carries no `location`, and undefined here is exactly right for all of
// them — the selector names no location series, which is true. Nothing about the
// measurement path is touched, so the regression risk is confined to what an author
// deliberately adds.
//
// The vocabulary is closed, so an unrecognized series is DROPPED rather than carried
// through: the hub resolves a location scope only for a selector naming a series it
// understands, and carrying an unknown one would produce a widget that looks configured
// and reads nothing. Dropping it makes the widget's empty state honest.
function parseLocationSelection(raw: unknown): LocationSelection | undefined {
  if (!isRecord(raw)) return undefined;
  return raw.series === 'latest' ? { series: 'latest' } : undefined;
}

// withLocation attaches a parsed location selection to a selector, and OMITS the key
// entirely when there is none — so a measurement-only definition serializes byte-for-
// byte as it did before this field existed (no spurious `"location":undefined` in the
// diff, and isDirty stays quiet on a load/save round trip).
function withLocation<S extends object>(selector: S, raw: Record<string, unknown>): S {
  const location = parseLocationSelection(raw.location);
  return location ? { ...selector, location } : selector;
}

// parseDatasource coerces a raw datasource into a normalized selector, or drops it
// (returns undefined) when it is absent or its `kind` is not a non-empty string.
function parseDatasource(raw: unknown, index: number): WidgetInstance['datasource'] | undefined {
  if (!isRecord(raw)) return undefined;
  const kind = raw.kind;
  if (typeof kind !== 'string' || kind.length === 0) return undefined;

  // 🔴 `unbound` is what the RENDERER produces for a slot with no binding; it is never
  // stored. Carried through, a stored one would render as an empty pane on every channel
  // with nothing to say why — the shape of a correctly unbound slot, not of a broken
  // definition. A widget with no entity is stored as a `slot` with no binding.
  if (kind === 'unbound') {
    throw new DashboardDefinitionError(
      `widgets[${index}].datasource has kind 'unbound', which is never stored; bind the widget through a slot`,
    );
  }

  if (kind === 'device') {
    return withLocation(
      { kind: 'device' as const, deviceToken: stringAt(raw, 'deviceToken'), measurements: stringArrayAt(raw, 'measurements') },
      raw,
    );
  }
  if (kind === 'anchor') {
    const anchorRec = isRecord(raw.anchor) ? raw.anchor : {};
    const selector: WidgetInstance['datasource'] = withLocation(
      {
        kind: 'anchor' as const,
        anchor: {
          relationship: stringAt(anchorRec, 'relationship'),
          // targetType defaults to '' (the config panel constrains it to the union;
          // a hand-edited/empty value round-trips rather than being silently coerced).
          targetType: stringAt(anchorRec, 'targetType') as AnchorTarget['targetType'],
          targetToken: stringAt(anchorRec, 'targetToken'),
        },
        measurements: stringArrayAt(raw, 'measurements'),
      },
      raw,
    );
    if (isRecord(raw.aggregation)) {
      (selector as { aggregation?: unknown }).aggregation = raw.aggregation;
    }
    return selector;
  }

  if (kind === 'slot') {
    // Runtime-binding kind: the entity is resolved at mount via the Hub's binding
    // manifest; the widget carries only the slot name + the series it wants (the
    // measurement names, and/or the location series).
    return withLocation(
      { kind: 'slot' as const, slot: stringAt(raw, 'slot'), measurements: stringArrayAt(raw, 'measurements') },
      raw,
    );
  }

  // Reserved/other kinds: carry through opaquely (the hub rejects them).
  return raw as unknown as WidgetInstance['datasource'];
}

function parseLayout(raw: unknown, index: number): WidgetLayout {
  if (!isRecord(raw)) throw new DashboardDefinitionError(`widgets[${index}].layout is missing`);

  const layout: WidgetLayout = {};
  for (const [bp, placement] of Object.entries(raw)) {
    if (bp === '__proto__') continue;
    if (isRecord(placement)) layout[bp] = parsePlacement(placement, `widgets[${index}].layout.${bp}`);
  }
  if (!Object.prototype.hasOwnProperty.call(layout, BASE_BREAKPOINT)) {
    throw new DashboardDefinitionError(`widgets[${index}].layout has no '${BASE_BREAKPOINT}' placement`);
  }
  return layout;
}

// A whole number of cells >= min, or a refusal. Required: a span the author left out has no
// value this parser could honestly pick.
function cellCount(rec: Record<string, unknown>, key: string, min: number, where: string): number {
  const v = rec[key];
  if (typeof v !== 'number' || !Number.isFinite(v)) {
    throw new DashboardDefinitionError(`${where}.${key} is ${JSON.stringify(v)}; expected a number of cells`);
  }
  return Math.max(min, Math.round(v));
}

// parsePlacement reads one breakpoint's placement. A missing `mode` is a grid placement. Every
// value the mode needs is required and checked: a placement is where a widget is drawn, and a
// guessed one draws it somewhere nobody chose.
function parsePlacement(rec: Record<string, unknown>, where: string): Placement {
  const mode = rec.mode ?? 'grid';
  if (mode === 'grid') {
    return {
      mode: 'grid',
      col: cellCount(rec, 'col', 0, where),
      row: cellCount(rec, 'row', 0, where),
      cols: cellCount(rec, 'cols', 1, where),
      rows: cellCount(rec, 'rows', 1, where),
    };
  }
  if (mode === 'relative') return parseRelative(rec, where);
  if ((RESERVED_PLACEMENT_MODES as readonly unknown[]).includes(mode)) {
    throw new DashboardDefinitionError(`${where}.mode '${String(mode)}' is reserved and not supported by this release`);
  }
  throw new DashboardDefinitionError(`${where}.mode is ${JSON.stringify(mode)}; expected 'grid' or 'relative'`);
}

function parseAnchor(raw: unknown, where: string): PlacementAnchor {
  if ((PLACEMENT_ANCHORS as readonly unknown[]).includes(raw)) return raw as PlacementAnchor;
  throw new DashboardDefinitionError(`${where} is ${JSON.stringify(raw)}; expected one of ${PLACEMENT_ANCHORS.join(', ')}`);
}

function parseRelative(rec: Record<string, unknown>, where: string): Placement {
  if (typeof rec.host !== 'string' || rec.host.length === 0) {
    throw new DashboardDefinitionError(`${where}.host is ${JSON.stringify(rec.host)}; a relative widget names the widget it is placed against`);
  }
  const placement: Extract<Placement, { mode: 'relative' }> = {
    mode: 'relative',
    host: rec.host,
    anchor: parseAnchor(rec.anchor, `${where}.anchor`),
    size: { fill: 'host' },
  };
  if (rec.self !== undefined) placement.self = parseAnchor(rec.self, `${where}.self`);
  if (rec.offset !== undefined) {
    const o = rec.offset;
    if (!isRecord(o) || !isFiniteNumber(o.x) || !isFiniteNumber(o.y)) {
      throw new DashboardDefinitionError(`${where}.offset is ${JSON.stringify(o)}; expected {x, y} in cells`);
    }
    placement.offset = { x: o.x, y: o.y };
  }
  const size = rec.size;
  if (isRecord(size) && size.fill === 'host') {
    placement.size = { fill: 'host' };
  } else if (isRecord(size)) {
    placement.size = { cols: cellCount(size, 'cols', 1, `${where}.size`), rows: cellCount(size, 'rows', 1, `${where}.size`) };
  } else {
    throw new DashboardDefinitionError(`${where}.size is ${JSON.stringify(size)}; expected {cols, rows} or {fill: 'host'}`);
  }
  if (rec.layer !== undefined) {
    if (!isFiniteNumber(rec.layer) || !Number.isInteger(rec.layer)) {
      throw new DashboardDefinitionError(`${where}.layer is ${JSON.stringify(rec.layer)}; expected a whole number`);
    }
    placement.layer = rec.layer;
  }
  return placement;
}

function isFiniteNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v);
}

// The stored shape of a definition: what serializeDefinition writes. A placeholder for
// an unknown widget type is written as the stored object it was parsed from.
export type StoredDashboardDefinition = Omit<DashboardDefinition, 'widgets'> & {
  widgets: Array<KnownWidgetInstance | ExtensionWidgetInstance | Record<string, unknown>>;
};

// storedDefinition is the definition as it is stored: every unknown-type placeholder
// replaced by its raw object (carrying any move or id change the editor made to it).
// Anything that writes a definition out — a save, a copy-as-JSON export — goes through
// this, because JSON.stringify of the parsed model would write the placeholder wrapper
// and lose the widget. Returns the same reference when there is no placeholder.
//
// The parser's `convertedFrom` mark is never written: it describes how this copy was read, not
// the board, and a save is exactly what retires it.
export function storedDefinition(def: DashboardDefinition): StoredDashboardDefinition {
  const unmarked = withoutConversionMark(def);
  if (!unmarked.widgets.some((w) => w.type === UNKNOWN_WIDGET_TYPE)) return unmarked as StoredDashboardDefinition;
  return {
    ...unmarked,
    widgets: unmarked.widgets.map((w) => (w.type === UNKNOWN_WIDGET_TYPE ? storedUnknownWidget(w) : w)),
  };
}

// storedUnknownWidget writes a placeholder back as its raw object. The raw object is
// returned untouched unless the editor changed one of the two fields a placeholder
// exposes: a changed id is written over the stored one, and a moved breakpoint box is
// written over that breakpoint (every untouched breakpoint keeps its stored bytes). An
// id the parser GENERATED for an id-less widget is not written, so the stored widget
// stays id-less exactly as it was.
function storedUnknownWidget(w: UnknownWidgetInstance): Record<string, unknown> {
  let out = w.raw;
  const rawId = storedWidgetId(w.raw);
  if (rawId !== undefined && rawId !== w.id) out = { ...out, id: w.id };
  const layout = storedUnknownLayout(w.layout, w.raw.layout);
  if (layout !== w.raw.layout) out = { ...out, layout };
  return out;
}

// storedUnknownLayout merges the placeholder's (parsed, possibly edited) layout into
// the stored one, breakpoint by breakpoint. A stored box that parses to the same box is
// kept as stored; a changed one is replaced; a breakpoint the stored layout did not
// have is appended; a stored entry that is not a box at all (the parser skipped it) is
// carried. Returns the stored reference when nothing changed.
function storedUnknownLayout(layout: WidgetLayout, rawLayout: unknown): unknown {
  const stored = isRecord(rawLayout) ? rawLayout : {};
  const out: Record<string, unknown> = {};
  let changed = !isRecord(rawLayout);
  for (const [bp, box] of Object.entries(stored)) {
    if (!isRecord(box)) {
      out[bp] = box;
      continue;
    }
    const current = Object.prototype.hasOwnProperty.call(layout, bp) ? layout[bp] : undefined;
    if (!current) {
      changed = true; // the editor removed this breakpoint
      continue;
    }
    if (samePlacement(parsePlacement(box, bp), current)) {
      out[bp] = box;
    } else {
      out[bp] = movedPlacement(box, current);
      changed = true;
    }
  }
  for (const [bp, box] of Object.entries(layout)) {
    if (Object.prototype.hasOwnProperty.call(stored, bp) && isRecord(stored[bp])) continue;
    out[bp] = box;
    changed = true;
  }
  return changed ? out : rawLayout;
}

// Every field a placement has in this build, in either mode.
const PLACEMENT_KEYS: ReadonlySet<string> = new Set([
  'mode', 'col', 'row', 'cols', 'rows', 'host', 'anchor', 'self', 'offset', 'size', 'layer',
]);

// movedPlacement writes an edited placement over a stored one. The edited placement replaces
// every field this build knows (so a field the edit dropped, or one of the other mode, is gone);
// any other field the stored entry carries (one a newer release added) is kept.
function movedPlacement(stored: Record<string, unknown>, current: Placement): Record<string, unknown> {
  const out: Record<string, unknown> = { ...current };
  for (const [k, v] of Object.entries(stored)) if (!PLACEMENT_KEYS.has(k)) out[k] = v;
  return out;
}

function samePlacement(a: Placement, b: Placement): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// serializeDefinition is the canonical on-the-wire JSON — the inverse of
// parseDashboardDefinition — that dashboard-management stores. Named (not an inline
// JSON.stringify) so every consumer that persists a definition shares one format.
export function serializeDefinition(def: DashboardDefinition): string {
  return JSON.stringify(storedDefinition(def));
}

// withoutConversionMark is the definition minus the parser's `convertedFrom` mark (the same
// reference when it carries none): what an editor keeps once a save has written the current
// format.
export function withoutConversionMark(def: DashboardDefinition): DashboardDefinition {
  if (def.convertedFrom === undefined && def.convertedWindows === undefined) return def;
  const { convertedFrom: _mark, convertedWindows: _windows, ...rest } = def;
  return rest;
}

// isDirty reports whether two definitions differ. A structural JSON compare is
// enough: both sides come from parse/edit transforms that preserve key order, so
// it only flips on a real change. Drives an editor's save/dirty state.
export function isDirty(a: DashboardDefinition, b: DashboardDefinition): boolean {
  return serializeDefinition(a) !== serializeDefinition(b);
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

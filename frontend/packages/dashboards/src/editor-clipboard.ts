// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Duplicate, copy and paste for the canvas editor: pure transforms over a definition, free of
// React and the DOM (the console owns the keyboard and the system clipboard). Kept in its own
// module, outside anything the viewer imports.
//
// What a copy carries is the widget as a STORED widget, so a paste reads it back through the same
// parser a load does, against the pasting console's widget registry: a type that console does not
// have, or a release it cannot run, is refused and named rather than pasted as something else.
//
// Decisions a reader needs:
//   - A pasted or duplicated widget gets a new id and lands at its own span (no wider than the
//     grid) in the first free slot, scanning rows, exactly as an added widget does.
//   - A RELATIVE widget is copied ONTO THE GRID, at its own size (a `fill` one at its host's
//     span). Kept relative, a duplicate would sit exactly on top of the original, invisible, and
//     a paste on another board would name a host that is not there. The author re-hosts it with
//     one click. Only the base placement is copied; per-breakpoint entries are left behind.
//   - A slot binding is KEPT when the target board has the slot: on the same board, a slot of that
//     name and kind; on another board, a slot of that name, kind AND default binding (slot names
//     are minted `slot-N` per board, so the same name on two boards usually means two different
//     devices, and following the name would silently show the wrong one). Otherwise the widget is
//     pasted UNBOUND: it points at a new slot of the same kind with nothing bound to it, and the
//     paste reports it, so the editor can say why. Never dropped to "no datasource": to an alarm
//     widget an absent datasource means the whole tenant; an unbound slot means no device yet.
//   - 🔴 A copy is stamped with the TENANT it was made in, and a paste into any other tenant keeps
//     NO entity reference at all. Device and entity tokens are only unique within a tenant, so a
//     binding carried across would quietly show the same-named device of the other tenant (or
//     publish the first tenant's identifiers on a board the second tenant's users can read). One
//     browser tab can change tenant, and its clipboard outlives the change, so this is decided at
//     paste time, from the stamp, never assumed. Every slot reference, and every concrete device or
//     entity selector, becomes a new unbound slot with no label (a slot's label is often the token
//     it was bound to), and the board stamp is ignored: board tokens are per tenant too.

import { CURRENT_SCHEMA_VERSION } from './types';
import { BASE_BREAKPOINT, generateWidgetId, parseDashboardDefinition, storedDefinition, type ParseDefinitionOptions } from './definition';
import { basePlacement, hostSpan, resolveEverywhere } from './editor-model';
import { firstFreeSlot } from './grid';
import { nextSlotName, sameBinding } from './slots';
import {
  UNKNOWN_WIDGET_TYPE,
  type Canvas,
  type DashboardDefinition,
  type GridPlacement,
  type ResolvedWidgetInstance,
  type SlotDefinition,
  type WidgetInstance,
} from './types';

export const WIDGET_CLIPBOARD_FORMAT = 'devicechain/dashboard-widgets';
export const WIDGET_CLIPBOARD_VERSION = 1;

// Where a copy was made, or where a paste lands: the tenant and the board token within it.
export interface ClipboardOrigin {
  tenant: string;
  board: string;
}

// What a copy holds: the widgets (on the grid, ids as they were), the slots they reference as the
// source board defined them, the tenant and board they came from and the canvas they were authored on.
export interface WidgetClipboard {
  tenant: string;
  board: string;
  canvas: Canvas;
  widgets: ResolvedWidgetInstance[];
  slots: Record<string, SlotDefinition>;
}

// A widget a paste could not take: its type and why.
export interface RefusedWidget {
  type: string;
  reason: string;
}

// A pasted widget that lost its binding: it now points at `slot`, a new slot with nothing bound
// to it, because the board has no slot `from` (`missing`), has one bound to something else, or
// of the other kind (`different`), or the copy was made in another tenant (`tenant`: `from` is
// then the source slot's name, or empty for a widget that named its device directly).
export interface UnboundPaste {
  widgetId: string;
  slot: string;
  from: string;
  reason: 'missing' | 'different' | 'tenant';
}

export interface PasteResult {
  definition: DashboardDefinition;
  ids: string[];
  unbound: UnboundPaste[];
}

// The slot names a widget references: the one its datasource binds, and the one a selector widget
// re-points (`options.selectionTarget`).
function slotReferences(w: WidgetInstance): string[] {
  const out: string[] = [];
  if (w.datasource?.kind === 'slot' && w.datasource.slot) out.push(w.datasource.slot);
  const target = w.options?.selectionTarget;
  if (typeof target === 'string' && target !== '' && !out.includes(target)) out.push(target);
  return out;
}

const own = <V>(map: Record<string, V> | undefined, key: string): V | undefined =>
  map && Object.prototype.hasOwnProperty.call(map, key) ? map[key] : undefined;

// copyWidget takes widget `id` of `def` (on `origin`'s board) into a clipboard. A placeholder for a
// widget this console cannot show is not copied: it carries no settings this build can vouch for.
export function copyWidget(def: DashboardDefinition, id: string, origin: ClipboardOrigin): WidgetClipboard | undefined {
  const w = def.widgets.find((x) => x.id === id);
  if (!w || w.type === UNKNOWN_WIDGET_TYPE) return undefined;
  const p = basePlacement(w);
  const size = p.mode === 'grid' ? { cols: p.cols, rows: p.rows } : 'fill' in p.size ? hostSpan(p, def, BASE_BREAKPOINT) : p.size;
  const placement: GridPlacement = { mode: 'grid', col: 0, row: 0, ...size };
  const widget = structuredClone({ ...w, layout: { [BASE_BREAKPOINT]: placement } }) as ResolvedWidgetInstance;
  const slots: Record<string, SlotDefinition> = {};
  for (const name of slotReferences(w)) {
    const slot = own(def.slots, name);
    if (slot) slots[name] = structuredClone(slot);
  }
  return { tenant: origin.tenant, board: origin.board, canvas: structuredClone(def.canvas), widgets: [widget], slots };
}

// serializeWidgetClipboard is the text a copy puts on the system clipboard.
export function serializeWidgetClipboard(clip: WidgetClipboard): string {
  const definition = storedDefinition({
    schemaVersion: CURRENT_SCHEMA_VERSION,
    title: '',
    canvas: clip.canvas,
    widgets: clip.widgets,
    ...(Object.keys(clip.slots).length > 0 ? { slots: clip.slots } : {}),
  });
  return JSON.stringify({
    format: WIDGET_CLIPBOARD_FORMAT,
    version: WIDGET_CLIPBOARD_VERSION,
    tenant: clip.tenant,
    board: clip.board,
    definition,
  });
}

// parseWidgetClipboard reads clipboard text back, validating every widget against the registry
// `options.catalog` names (the built-ins without one). Text that is not a widget clipboard, of
// another version, or whose definition does not parse, is undefined: the caller leaves the paste
// alone, and so is text with no tenant stamp: a paste must know where a copy came from. A widget
// the registry cannot resolve is refused and named.
//
// 🔴 One exception to "undefined": a copy made by a NEWER release (its definition's schemaVersion
// above this build's) THROWS the parser's DashboardDefinitionError, code 'schemaTooNew', exactly as
// a load of that board would. It is a widget clipboard, so "nothing to paste" would be a lie, and
// reading it as the current format would paste it drawn wrong; the caller says why it refused.
export function parseWidgetClipboard(
  text: string,
  options: ParseDefinitionOptions = {},
): { clipboard: WidgetClipboard; refused: RefusedWidget[] } | undefined {
  let env: unknown;
  try {
    env = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof env !== 'object' || env === null) return undefined;
  const { format, version, tenant, board, definition } = env as Record<string, unknown>;
  if (format !== WIDGET_CLIPBOARD_FORMAT || version !== WIDGET_CLIPBOARD_VERSION) return undefined;
  if (typeof tenant !== 'string' || tenant === '' || typeof board !== 'string') return undefined;
  let parsed: DashboardDefinition;
  try {
    parsed = parseDashboardDefinition(definition, options);
  } catch (err) {
    // By `code`, not instanceof: it holds across two bundled copies of this package.
    if ((err as { code?: unknown } | null)?.code === 'schemaTooNew') throw err;
    return undefined;
  }
  const widgets: ResolvedWidgetInstance[] = [];
  const refused: RefusedWidget[] = [];
  for (const w of parsed.widgets) {
    if (w.type !== UNKNOWN_WIDGET_TYPE) {
      widgets.push(w);
      continue;
    }
    const type = typeof w.raw.type === 'string' ? w.raw.type : '';
    refused.push({ type, reason: w.reason?.message ?? '' });
  }
  return { clipboard: { tenant, board, canvas: parsed.canvas, widgets, slots: parsed.slots ?? {} }, refused };
}

// A selector that names its entity directly rather than through a slot.
const CONCRETE_KINDS: ReadonlySet<string> = new Set(['device', 'anchor', 'devices', 'relatedTraversal']);

// pasteWidgets adds the clipboard's widgets to `def`, the board `at`: each with a new id, in the
// first free slot at its span, its slot references kept or unbound as described above.
export function pasteWidgets(def: DashboardDefinition, clip: WidgetClipboard, at: ClipboardOrigin): PasteResult {
  // An empty stamp is never a match: a paste that cannot tell where a copy came from treats it as
  // another tenant's.
  const sameTenant = clip.tenant !== '' && clip.tenant === at.tenant;
  const sameBoard = sameTenant && clip.board === at.board;
  const columns = def.canvas.grid.columns;
  const slots: Record<string, SlotDefinition> = { ...(def.slots ?? {}) };
  // One decision per source slot, so widgets pasted together that shared a slot still share one.
  const mapped = new Map<string, { slot: string; reason?: UnboundPaste['reason'] }>();
  const mapSlot = (from: string) => {
    const known = mapped.get(from);
    if (known) return known;
    const source = own(clip.slots, from);
    const target = own(slots, from);
    const keep =
      sameTenant &&
      target !== undefined &&
      source !== undefined &&
      target.type === source.type &&
      (sameBoard || sameBinding(target.defaultBinding, source.defaultBinding));
    let result: { slot: string; reason?: UnboundPaste['reason'] };
    if (keep) result = { slot: from };
    else {
      const slot = nextSlotName(slots);
      // Another tenant's label is not carried: it is often the token the slot was bound to.
      const label = sameTenant ? source?.label : undefined;
      slots[slot] = { type: source?.type ?? 'device', ...(label !== undefined ? { label } : {}) };
      result = { slot, reason: !sameTenant ? 'tenant' : target ? 'different' : 'missing' };
    }
    mapped.set(from, result);
    return result;
  };

  const occupied = def.widgets.map(basePlacement).filter((p): p is GridPlacement => p.mode === 'grid');
  const added: WidgetInstance[] = [];
  const ids: string[] = [];
  const unbound: UnboundPaste[] = [];
  for (const source of clip.widgets) {
    const id = generateWidgetId();
    const p = basePlacement(source);
    const span = p.mode === 'grid' ? { cols: p.cols, rows: p.rows } : 'fill' in p.size ? { cols: 1, rows: 1 } : p.size;
    const cols = Math.min(span.cols, columns);
    const slot = firstFreeSlot(occupied, cols, span.rows, columns);
    const placement: GridPlacement = { mode: 'grid', col: slot.col, row: slot.row, cols, rows: span.rows };
    occupied.push(placement);
    const w = structuredClone(source) as ResolvedWidgetInstance;
    w.id = id;
    w.layout = { [BASE_BREAKPOINT]: placement };
    for (const from of slotReferences(source)) {
      const m = mapSlot(from);
      if (m.reason) unbound.push({ widgetId: id, slot: m.slot, from, reason: m.reason });
      if (m.slot === from) continue;
      if (w.datasource?.kind === 'slot' && w.datasource.slot === from) w.datasource = { ...w.datasource, slot: m.slot };
      if (w.options?.selectionTarget === from) w.options = { ...w.options, selectionTarget: m.slot };
    }
    // A widget from another tenant that names its device or entity directly keeps the series it
    // reads and loses the entity: it points at a new unbound slot of the matching kind.
    const ds = w.datasource;
    if (!sameTenant && ds && CONCRETE_KINDS.has(ds.kind) && ds.kind !== 'slot' && ds.kind !== 'unbound') {
      const slot = nextSlotName(slots);
      slots[slot] = { type: ds.kind === 'anchor' ? 'anchor' : 'device' };
      w.datasource = {
        kind: 'slot',
        slot,
        measurements: [...ds.measurements],
        ...(ds.location ? { location: { ...ds.location } } : {}),
      };
      unbound.push({ widgetId: id, slot, from: '', reason: 'tenant' });
    }
    added.push(w);
    ids.push(id);
  }
  const next: DashboardDefinition = { ...def, widgets: [...def.widgets, ...added] };
  if (Object.keys(slots).length > 0) next.slots = slots;
  return { definition: resolveEverywhere(next, ids), ids, unbound };
}

// duplicateWidget pastes a copy of widget `id` onto its own board. Undefined for a widget that
// cannot be copied (missing, or a placeholder).
export function duplicateWidget(
  def: DashboardDefinition,
  id: string,
): { definition: DashboardDefinition; id: string; unbound: UnboundPaste[] } | undefined {
  // The widget's own board, in its own tenant.
  const here: ClipboardOrigin = { tenant: 'here', board: 'here' };
  const clip = copyWidget(def, id, here);
  if (!clip) return undefined;
  const r = pasteWidgets(def, clip, here);
  return { definition: r.definition, id: r.ids[0], unbound: r.unbound };
}

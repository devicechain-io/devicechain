// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Slot authoring transforms (ADR-039). A dashboard is a reusable template: every
// widget references a named slot, and each slot carries a default entity binding.
// These pure (React/DOM-free, unit-tested) helpers convert a dashboard's concrete
// device/anchor selectors into that model and drive the editor's rebind/prune. The
// authoring UX stays "pick a device/anchor"; the storage becomes slot-based, and
// identical bindings collapse into ONE shared slot (dedup) — so a host manifest
// binds each real entity once, and CompositeWidget-style sharing falls out for free.

import type {
  AnchorTarget,
  DashboardDefinition,
  DatasourceSelector,
  SlotBinding,
  SlotDefinition,
  WidgetInstance,
} from './types';

// sameBinding — value equality of two entity bindings (drives dedup).
export function sameBinding(a: SlotBinding | undefined, b: SlotBinding | undefined): boolean {
  if (!a || !b) return a === b;
  if (a.kind === 'device' && b.kind === 'device') return a.deviceToken === b.deviceToken;
  if (a.kind === 'anchor' && b.kind === 'anchor') {
    return (
      a.anchor.relationship === b.anchor.relationship &&
      a.anchor.targetType === b.anchor.targetType &&
      a.anchor.targetToken === b.anchor.targetToken
    );
  }
  return false;
}

// bindingLabel — a readable slot label (the bound entity's token).
function bindingLabel(b: SlotBinding): string {
  return b.kind === 'device' ? b.deviceToken : b.anchor.targetToken;
}

// nextSlotName — the lowest unused `slot-N` name in a slots map.
function nextSlotName(slots: Record<string, SlotDefinition>): string {
  let n = 1;
  while (slots[`slot-${n}`]) n += 1;
  return `slot-${n}`;
}

// bindingOfSelector — the entity binding a concrete device/anchor selector names
// (measurements are dropped — a binding is the entity only). undefined for a device
// selector with no token, or a non-concrete selector.
function bindingOfSelector(ds: DatasourceSelector | undefined): SlotBinding | undefined {
  if (ds?.kind === 'device') return ds.deviceToken ? { kind: 'device', deviceToken: ds.deviceToken } : undefined;
  // Guard an empty target token (symmetric with device): a token-less anchor names no
  // entity, so it can't be a binding — and parseSlotBinding would drop it, so slotting it
  // would silently lose the relationship/targetType on reload.
  if (ds?.kind === 'anchor') {
    return ds.anchor.targetToken ? { kind: 'anchor', anchor: ds.anchor } : undefined;
  }
  return undefined;
}

// findOrAddSlot returns the name of the slot bound to `binding`, creating one (in
// the passed, freshly-copied map) when none exists. Dedup lives here.
function findOrAddSlot(slots: Record<string, SlotDefinition>, binding: SlotBinding): string {
  const existing = Object.keys(slots).find((k) => sameBinding(slots[k].defaultBinding, binding));
  if (existing) return existing;
  const name = nextSlotName(slots);
  slots[name] = { type: binding.kind, label: bindingLabel(binding), defaultBinding: binding };
  return name;
}

// migrateToSlots rewrites every widget's concrete device/anchor selector into a slot
// selector (default-bound to that entity), deduping identical bindings into one
// shared slot. Idempotent: slot selectors (and slot-free widgets) pass through
// untouched, so re-running is a no-op. This is the decisive pre-GA cutover applied
// when the console loads a dashboard.
export function migrateToSlots(def: DashboardDefinition): DashboardDefinition {
  const slots: Record<string, SlotDefinition> = { ...(def.slots ?? {}) };
  let changed = false;
  const widgets = def.widgets.map((w) => {
    const ds = w.datasource;
    // Leave an anchor that carries a Phase-2 `aggregation` as a concrete selector: the
    // slot model has no aggregation field, so slotting it would drop that (reserved)
    // config. It coexists fine — the Hub resolves concrete anchors too. (Editing such a
    // widget in the panel still drops aggregation, but that's an explicit action.)
    if (ds?.kind === 'anchor' && ds.aggregation) return w;
    const binding = bindingOfSelector(ds);
    if (!binding || !ds) return w;
    changed = true;
    const slot = findOrAddSlot(slots, binding);
    return { ...w, datasource: { kind: 'slot' as const, slot, measurements: ds.measurements } };
  });
  // Return the SAME reference (no spurious `slots:{}`) when nothing migrated, so the
  // early-out is a true no-op and the result round-trips byte-identically.
  if (!changed) return def;
  return { ...def, widgets, slots };
}

// bindWidgetSlot points a widget at the slot for `binding` (creating/reusing it,
// deduped) with the given measurement names — the editor's rebind + measurements
// operation. Picking a NEW entity forks a fresh slot, leaving other widgets' slots
// alone. A measurements-only edit (binding unchanged) KEEPS the widget's own slot even
// when another slot shares the binding — so a distinct slot the host may override
// separately (I-3 templates) isn't silently collapsed away.
export function bindWidgetSlot(
  def: DashboardDefinition,
  widgetId: string,
  binding: SlotBinding,
  measurements: string[],
): DashboardDefinition {
  const slots: Record<string, SlotDefinition> = { ...(def.slots ?? {}) };
  const current = def.widgets.find((w) => w.id === widgetId)?.datasource;
  const currentSlot = current?.kind === 'slot' ? current.slot : undefined;
  const slot =
    currentSlot && slots[currentSlot] && sameBinding(slots[currentSlot].defaultBinding, binding)
      ? currentSlot // binding unchanged → keep this widget's own slot (don't rehome)
      : findOrAddSlot(slots, binding);
  const widgets = def.widgets.map((w) =>
    w.id === widgetId ? { ...w, datasource: { kind: 'slot' as const, slot, measurements } } : w,
  );
  return { ...def, widgets, slots };
}

// clearWidgetDatasource drops a widget's datasource (the "None" data source). Prune
// afterwards to reclaim a now-orphaned slot.
export function clearWidgetDatasource(def: DashboardDefinition, widgetId: string): DashboardDefinition {
  const widgets = def.widgets.map((w) => {
    if (w.id !== widgetId) return w;
    const { datasource: _drop, ...rest } = w;
    return rest;
  });
  return { ...def, widgets };
}

// pruneSlots removes slots no widget references, and omits the `slots` key entirely
// when none remain (so a dashboard that loses its last slot serializes clean).
export function pruneSlots(def: DashboardDefinition): DashboardDefinition {
  if (!def.slots) return def;
  const used = new Set<string>();
  for (const w of def.widgets) if (w.datasource?.kind === 'slot') used.add(w.datasource.slot);
  const slots: Record<string, SlotDefinition> = {};
  for (const [name, slot] of Object.entries(def.slots)) if (used.has(name)) slots[name] = slot;
  if (Object.keys(slots).length === 0) {
    const { slots: _drop, ...rest } = def;
    return rest;
  }
  return { ...def, slots };
}

// widgetBinding resolves a widget's effective entity binding for the editor: a slot
// widget's default binding, or a (pre-migration) concrete selector's entity.
export function widgetBinding(def: DashboardDefinition, widget: WidgetInstance): SlotBinding | undefined {
  const ds = widget.datasource;
  if (ds?.kind === 'slot') return def.slots?.[ds.slot]?.defaultBinding;
  return bindingOfSelector(ds);
}

// widgetSlotName is the slot a widget references, if any (for an editor hint).
export function widgetSlotName(widget: WidgetInstance): string | undefined {
  return widget.datasource?.kind === 'slot' ? widget.datasource.slot : undefined;
}

// A concrete device/anchor selector — what the config panel edits (slot-agnostic).
// Rebuilt into slot storage by the workspace via bindWidgetSlot.
export type ConcreteSelector =
  | { kind: 'device'; deviceToken: string; measurements: string[] }
  | { kind: 'anchor'; anchor: AnchorTarget; measurements: string[] };

// resolveConcrete gives the config panel a slot-free view of a widget's data source:
// the bound entity + the widget's measurements, or undefined when unbound.
export function resolveConcrete(
  def: DashboardDefinition,
  widget: WidgetInstance,
): ConcreteSelector | undefined {
  const binding = widgetBinding(def, widget);
  if (!binding) return undefined;
  const measurements = widget.datasource?.measurements ?? [];
  return binding.kind === 'device'
    ? { kind: 'device', deviceToken: binding.deviceToken, measurements }
    : { kind: 'anchor', anchor: binding.anchor, measurements };
}

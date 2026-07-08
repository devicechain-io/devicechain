// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The scoped-slot cascade (ADR-039 selection amendment). A dashboard's slots form a
// context FOREST: a scoped slot resolves relative to its parent's binding (a device
// within the selected building). resolveContextBindings walks that forest and produces
// ONE settled slot→binding map the host feeds to the hub + renderer.
//
// It is a host-level overlay, NOT in-hub machinery: the hub resolves its bindings once
// at construction, so selection re-keys the renderer and the hub is rebuilt through the
// shipped path — the overlay lives outside the hub and a rebuild never erases it. The
// pass is async only because enumerating an anchor's members hits the resolver; it is
// otherwise pure, and the host guards it with a monotonic generation so a slow members
// response can't overwrite a newer selection.

import type { DeviceResolver } from './hub';
import type { DashboardDefinition, SelectionTarget, SlotBinding, SlotDefinition } from './types';

// The cascade only needs to enumerate an anchor's member devices; it takes the narrow
// slice of DeviceResolver so a caller (or a test) needn't supply the whole interface.
export type MemberResolver = Pick<DeviceResolver, 'devicesForAnchor'>;

function ownGet<T>(map: Record<string, T> | undefined, key: string): T | undefined {
  return map && Object.prototype.hasOwnProperty.call(map, key) ? map[key] : undefined;
}

// setBinding assigns an OWN property, skipping '__proto__' — a plain `map['__proto__'] =
// v` hits the prototype setter (drops the value AND swaps the map's prototype) rather
// than creating an own key. A slot so named names no real entity, so skipping it is safe;
// this keeps the SCOPED path as prototype-safe as the scope-free spread path.
function setBinding(map: Record<string, SlotBinding>, key: string, value: SlotBinding): void {
  if (key === '__proto__') return;
  map[key] = value;
}

// hasScopedSlots reports whether any slot declares a scope — the host's fast-path gate
// (a scope-free dashboard needs no async cascade, just the synchronous overlay).
export function hasScopedSlots(definition: DashboardDefinition): boolean {
  const slots = definition.slots;
  if (!slots) return false;
  for (const name of Object.keys(slots)) if (slots[name]?.scope) return true;
  return false;
}

// bindingsWithoutScopedSlots returns `map` with every SCOPED-slot key removed — the safe
// interim a host seeds before the async cascade first settles. At mount nothing has been
// derived yet, so showing a scoped slot's (possibly out-of-context) default would be a
// stale/lying binding; omitting it renders an empty placeholder until the cascade fills it
// ("unbound, never stale"). Prototype-safe on write.
export function bindingsWithoutScopedSlots(
  definition: DashboardDefinition,
  map: Record<string, SlotBinding>,
): Record<string, SlotBinding> {
  const slots = definition.slots;
  if (!slots) return map;
  const out: Record<string, SlotBinding> = {};
  for (const name of Object.keys(map)) {
    if (!ownGet(slots, name)?.scope) setBinding(out, name, map[name]);
  }
  return out;
}

// applySelection folds one selection into the overlay, returning a new map (never
// mutating). Computed-key assignment creates an own property even for '__proto__', so
// this is prototype-safe; the cascade reads it back via own-property lookups regardless.
export function applySelection(
  overlay: Record<string, SlotBinding>,
  target: SelectionTarget,
): Record<string, SlotBinding> {
  return { ...overlay, [target.slot]: target.binding };
}

// topoOrder returns slot names parent-before-child (the forest's topological order), so
// a child is derived only after its parent's binding is settled. DFS with a visited set;
// parse already rejected cycles, but the guard keeps a hand-edited definition safe.
function topoOrder(slots: Record<string, SlotDefinition>): string[] {
  const order: string[] = [];
  const done = new Set<string>();
  const visit = (name: string, stack: Set<string>): void => {
    if (done.has(name) || stack.has(name)) return;
    const slot = ownGet(slots, name);
    if (!slot) return;
    stack.add(name);
    const parent = slot.scope?.parent;
    if (parent) visit(parent, stack);
    stack.delete(name);
    done.add(name);
    order.push(name);
  };
  for (const name of Object.keys(slots)) visit(name, new Set());
  return order;
}

// resolveContextBindings computes the effective slot→binding map: root slots take the
// selection overlay over the slot default; a scoped slot derives from its parent's
// resolved binding per its strategy. Unbound slots are OMITTED (the hub renders them as
// an empty placeholder). Fail-safe: a membership error or an unbound/non-anchor parent
// leaves the child unbound rather than throwing. `base` is the sync default+manifest
// layer (effectiveBindings); `overlay` is the accumulated selection (wins over base).
export async function resolveContextBindings(
  definition: DashboardDefinition,
  base: Record<string, SlotBinding>,
  overlay: Record<string, SlotBinding>,
  resolver: MemberResolver,
): Promise<Record<string, SlotBinding>> {
  const slots = definition.slots ?? {};
  const out: Record<string, SlotBinding> = {};

  for (const name of topoOrder(slots)) {
    const slot = ownGet(slots, name);
    const scope = slot?.scope;
    if (!scope) {
      // Root context: the selection wins over the default — but only a TYPE-COMPATIBLE
      // selection (a device pick must not re-bind an anchor slot, and vice versa). A
      // type-incompatible selection is "not applicable": ignore it and keep the prior
      // (default/manifest) binding rather than corrupting the context (ADR decision 7).
      const sel = ownGet(overlay, name);
      const b = sel && (!slot || bindingMatchesType(sel, slot.type)) ? sel : ownGet(base, name);
      if (b) setBinding(out, name, b);
      continue;
    }
    // Scoped: derive from the parent's ALREADY-settled binding (topo order guarantees it).
    // Every strategy binds a DEVICE, so a scoped slot must be device-typed; a hand-edited
    // anchor-typed scoped slot can't be derived (a device binding on an anchor slot mis-types
    // downstream) → leave it unbound (the authoring UI only scopes device slots).
    if (slot && slot.type !== 'device') continue;
    const parentBinding = ownGet(out, scope.parent);
    if (!parentBinding || parentBinding.kind !== 'anchor') continue; // parent unbound → child unbound
    let members: string[];
    try {
      members = [...(await resolver.devicesForAnchor(parentBinding.anchor))].sort();
    } catch {
      continue; // membership error → child unbound (fail-safe)
    }
    if (scope.strategy === 'first') {
      // Auto-follow: the parent's first member (deterministic by token). The slot's own
      // default/selection is ignored — it is fully derived.
      if (members.length > 0) setBinding(out, name, { kind: 'device', deviceToken: members[0] });
    } else {
      // Manual: the current pick (selection over default), kept iff still a member of the
      // (possibly changed) parent; otherwise unbound (a picker prompts a fresh, in-context pick).
      const pick = ownGet(overlay, name) ?? ownGet(base, name);
      if (pick && pick.kind === 'device' && members.includes(pick.deviceToken)) setBinding(out, name, pick);
    }
  }

  // Pass through a manifest binding for a slot the definition does not declare (a host
  // manifest may bind an undeclared slot; effectiveBindings kept it, so we must too). Only
  // BASE keys are carried — a selection targeting an undeclared slot is dropped, so a
  // mis-authored drill target (a slot that doesn't exist) can't churn a spurious rebuild.
  for (const name of Object.keys(base)) {
    if (Object.prototype.hasOwnProperty.call(slots, name) || Object.prototype.hasOwnProperty.call(out, name)) {
      continue;
    }
    const b = ownGet(overlay, name) ?? ownGet(base, name);
    if (b) setBinding(out, name, b);
  }

  return out;
}

// bindingMatchesType reports whether a binding's kind matches a slot's declared type — a
// device binding fits a 'device' slot, an anchor binding an 'anchor' slot.
function bindingMatchesType(binding: SlotBinding, type: SlotDefinition['type']): boolean {
  return binding.kind === type;
}

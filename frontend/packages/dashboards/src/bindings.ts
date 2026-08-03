// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runtime slot binding (ADR-039). A dashboard definition is a reusable TEMPLATE:
// widgets reference named slots, and each slot declares a `defaultBinding` (the
// authoring tenant's entity). At MOUNT the host may supply a manifest that overrides
// any slot's binding — so one definition + two manifests renders as two live
// dashboards on different entities. effectiveBindings computes the manifest the
// DashboardHub actually resolves against: the slot defaults, overlaid by the host's
// overrides.

import { parseSlotBinding } from './definition';
import type { DashboardDefinition, SlotBinding, SlotDefinition } from './types';

// effectiveBindings merges a definition's slot default bindings with an optional
// host manifest (manifest wins). Slots without a default and not in the manifest are
// omitted → the Hub renders them as an empty placeholder.
//
// This is the BASE layer (defaults + manifest), computed synchronously. For a dashboard
// with SCOPED slots it is NOT the final manifest: a scoped slot's default here is a
// fallback that resolveContextBindings (the cascade) supersedes with a value derived
// from the parent. A host with scoped slots feeds this map as `base` into the cascade
// and hands the hub/renderer the cascade's output, not this map directly.
export function effectiveBindings(
  definition: DashboardDefinition,
  manifest?: Record<string, SlotBinding>,
): Record<string, SlotBinding> {
  const out: Record<string, SlotBinding> = {};
  for (const [name, slot] of Object.entries(definition.slots ?? {})) {
    if (slot.defaultBinding) out[name] = slot.defaultBinding;
  }
  if (manifest) {
    for (const [name, binding] of Object.entries(manifest)) out[name] = binding;
  }
  return out;
}

// What parseBindingManifest made of a host manifest: the bindings it accepted, and the
// slot names it did NOT — in manifest order.
export interface ParsedBindingManifest {
  bindings: Record<string, SlotBinding>;
  dropped: string[];
}

// parseBindingManifest validates an untrusted host manifest (slot name → binding)
// into a clean Record<slot, SlotBinding>, dropping malformed entries. The host of an
// exported dashboard passes this to effectiveBindings to bind the definition's slots
// to ITS entities: one definition + two manifests → two live dashboards.
//
// 🔴 IT REPORTS WHAT IT DROPPED, because a silent drop is this API's sharpest edge and
// every host has to handle it. A typo'd binding does not fail — it vanishes, the slot
// stays unbound, and the widgets on it render as empty frames with nothing anywhere
// saying why. Returning the names makes that a thing a host can show a user. The
// alternative every host would otherwise reach for is comparing key counts against its
// own input, which infers the same fact less well: it cannot name the offender, and it
// counts the `__proto__` refusal below as if it were malformed.
export function parseBindingManifest(raw: unknown): ParsedBindingManifest {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    return { bindings: {}, dropped: [] };
  }
  const bindings: Record<string, SlotBinding> = {};
  const dropped: string[] = [];
  for (const [slot, spec] of Object.entries(raw as Record<string, unknown>)) {
    // Skip a `__proto__` key: `out['__proto__'] = …` would hit the prototype setter
    // (lose the binding + swap out's prototype) rather than set an own property.
    // Reported as dropped: it IS a binding the host asked for and did not get.
    if (slot === '__proto__') {
      dropped.push(slot);
      continue;
    }
    const binding = parseSlotBinding(spec);
    if (binding) bindings[slot] = binding;
    else dropped.push(slot);
  }
  return { bindings, dropped };
}

// stripDefaultBindings removes every slot's default binding, turning a concrete
// dashboard into a TEMPLATE — the exported form a host must supply a manifest for
// (each slot renders as a placeholder until bound). Slot names/types/labels are kept
// so the importer knows what to bind. (Caveats: a slot's `label` is the author's
// entity token — a naming hint, not stripped; and an anchor selector carrying a
// Phase-2 `aggregation` stays concrete/un-slotted through migration, so it isn't
// rebindable — neither is reachable through the console's authoring UI today.)
export function stripDefaultBindings(def: DashboardDefinition): DashboardDefinition {
  if (!def.slots) return def;
  const slots: Record<string, SlotDefinition> = {};
  for (const [name, slot] of Object.entries(def.slots)) {
    const { defaultBinding: _drop, ...rest } = slot;
    slots[name] = rest;
  }
  return { ...def, slots };
}

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

// parseBindingManifest validates an untrusted host manifest (slot name → binding)
// into a clean Record<slot, SlotBinding>, dropping malformed entries. The host of an
// exported dashboard passes this to effectiveBindings to bind the definition's slots
// to ITS entities: one definition + two manifests → two live dashboards.
export function parseBindingManifest(raw: unknown): Record<string, SlotBinding> {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return {};
  const out: Record<string, SlotBinding> = {};
  for (const [slot, spec] of Object.entries(raw as Record<string, unknown>)) {
    // Skip a `__proto__` key: `out['__proto__'] = …` would hit the prototype setter
    // (lose the binding + swap out's prototype) rather than set an own property.
    if (slot === '__proto__') continue;
    const binding = parseSlotBinding(spec);
    if (binding) out[slot] = binding;
  }
  return out;
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

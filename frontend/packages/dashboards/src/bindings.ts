// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runtime slot binding (ADR-039). A dashboard definition is a reusable TEMPLATE:
// widgets reference named slots, and each slot declares a `defaultBinding` (the
// authoring tenant's entity). At MOUNT the host may supply a manifest that overrides
// any slot's binding — so one definition + two manifests renders as two live
// dashboards on different entities. effectiveBindings computes the manifest the
// DashboardHub actually resolves against: the slot defaults, overlaid by the host's
// overrides.

import type { DashboardDefinition, SlotBinding } from './types';

// effectiveBindings merges a definition's slot default bindings with an optional
// host manifest (manifest wins). Slots without a default and not in the manifest are
// omitted → the Hub renders them as an empty placeholder.
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

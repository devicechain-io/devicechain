// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The cascade as a VIEWER sees it. resolveContextBindings settles every slot from the board's own
// defaults and the viewer's picks; this adds the one thing only the viewer's side can answer: is
// the area or asset a root anchor slot is bound to one the viewer may pick? A board saved with
// floor tp-tower-f4 and opened by a member who is not placed there would otherwise show an empty
// floor under a name they cannot see. It falls back to the first entity the picker offers, for
// every viewer (an operator's board pointing at a deleted area benefits too), by correcting the
// root binding BEFORE the cascade derives the slots that follow it.
//
// It is a separate function, not a parameter of resolveContextBindings, so that function keeps the
// signature published hosts call.

import { listEntities, slotTypeFilter } from './candidates';
import { resolveContextBindings, type MemberResolver } from './context';
import type { DashboardDefinition, EntityCandidateLister, SlotBinding } from './types';

const ownGet = <T>(map: Record<string, T>, key: string): T | undefined =>
  Object.prototype.hasOwnProperty.call(map, key) ? map[key] : undefined;

// hasRootAnchorSlots reports whether any ROOT slot is an anchor slot: the slots whose binding the
// viewer-side check applies to. A board without one has nothing to correct.
export function hasRootAnchorSlots(definition: DashboardDefinition): boolean {
  return Object.values(definition.slots ?? {}).some((s) => s.type === 'anchor' && !s.scope);
}

// resolveViewerBindings is resolveContextBindings plus the fallback. Fail-safe like the cascade: a
// lister that is absent, fails or lists nothing keeps the binding, so an outage degrades to
// today's behaviour and never to a blank board.
export async function resolveViewerBindings(
  definition: DashboardDefinition,
  base: Record<string, SlotBinding>,
  overlay: Record<string, SlotBinding>,
  resolver: MemberResolver,
  lister?: EntityCandidateLister,
): Promise<Record<string, SlotBinding>> {
  const slots = definition.slots ?? {};
  let corrected: Record<string, SlotBinding> = { ...overlay };
  if (lister) {
    for (const name of Object.keys(slots)) {
      const slot = slots[name];
      if (slot.type !== 'anchor' || slot.scope) continue;
      // The binding the cascade would settle on: a type-compatible selection over the default.
      const picked = ownGet(overlay, name);
      const binding = picked?.kind === 'anchor' ? picked : ownGet(base, name);
      if (binding?.kind !== 'anchor') continue;
      const { targetType, targetToken } = binding.anchor;
      let rows: Array<{ token: string }>;
      try {
        rows = await listEntities(lister, targetType, slotTypeFilter(slot, targetType));
      } catch {
        continue;
      }
      if (rows.length > 0 && !rows.some((r) => r.token === targetToken)) {
        // A computed key in a literal makes an own property even for '__proto__'.
        corrected = { ...corrected, [name]: { kind: 'anchor', anchor: { ...binding.anchor, targetToken: rows[0].token } } };
      }
    }
  }
  return resolveContextBindings(definition, base, corrected, resolver);
}

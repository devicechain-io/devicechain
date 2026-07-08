// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// resolveSlotCandidates — the option set a context/entity-selector widget offers for one
// target slot (ADR-039 selection amendment). It is the picker's half of the cascade: the
// cascade (resolveContextBindings) DERIVES a scoped slot's binding, and this enumerates the
// bindings a viewer may pick FROM — sharing the same member source (DeviceResolver) so the
// picker's list and the strategies can never disagree.
//
//   scoped child slot → the parent anchor's member devices (devicesForAnchor), one option
//                       per member; empty / unbound / non-anchor parent ⇒ no options.
//   root device slot  → the tenant's devices (the injected lister).
//   root anchor slot  → the tenant's entities of the slot's target type; the anchor
//                       relationship + targetType come from the slot's CURRENT binding (or
//                       its default) as a template, only the targetToken varies. A root
//                       anchor slot with no bound/default anchor has no template ⇒ no options.
//
// Pure and fail-safe: a membership/lister error yields [] (an empty picker), never a throw —
// matching resolveContextBindings' degrade-don't-throw contract.

import type { MemberResolver } from './context';
import type {
  DashboardDefinition,
  EntityCandidateLister,
  SelectionCandidate,
  SlotBinding,
} from './types';

function ownGet<T>(map: Record<string, T> | undefined, key: string): T | undefined {
  return map && Object.prototype.hasOwnProperty.call(map, key) ? map[key] : undefined;
}

// sameBinding — value equality of two bindings, so a candidate can flag the current pick.
function sameBinding(a: SlotBinding | undefined, b: SlotBinding): boolean {
  if (!a) return false;
  if (a.kind === 'device' && b.kind === 'device') return a.deviceToken === b.deviceToken;
  if (a.kind === 'anchor' && b.kind === 'anchor') return a.anchor.targetToken === b.anchor.targetToken;
  return false;
}

export async function resolveSlotCandidates(
  definition: DashboardDefinition,
  slot: string,
  bindings: Record<string, SlotBinding>,
  resolver: MemberResolver,
  lister: EntityCandidateLister,
): Promise<SelectionCandidate[]> {
  const slots = definition.slots ?? {};
  const def = ownGet(slots, slot);
  if (!def) return [];
  const current = ownGet(bindings, slot);
  const mark = (binding: SlotBinding, label: string): SelectionCandidate => ({
    binding,
    label,
    selected: sameBinding(current, binding),
  });

  // Scoped child: the parent anchor's members (the same source the cascade's strategies use).
  if (def.scope) {
    // A 'first' slot is fully auto-derived (the cascade always binds the parent's first
    // member and IGNORES any selection), so a picker over it would snap back on every pick.
    // Only a 'manual' slot is user-pickable; offer no options for 'first'.
    if (def.scope.strategy === 'first') return [];
    const parentBinding = ownGet(bindings, def.scope.parent);
    if (!parentBinding || parentBinding.kind !== 'anchor') return [];
    let members: string[];
    try {
      members = [...(await resolver.devicesForAnchor(parentBinding.anchor))].sort();
    } catch {
      return [];
    }
    return members.map((token) => mark({ kind: 'device', deviceToken: token }, token));
  }

  // Root device slot: list the tenant's devices.
  if (def.type === 'device') {
    let rows: Array<{ token: string; name?: string | null }>;
    try {
      rows = await lister('device');
    } catch {
      return [];
    }
    return rows.map((r) => mark({ kind: 'device', deviceToken: r.token }, r.name || r.token));
  }

  // Root anchor slot: list the tenant's entities of the target type, reusing the current/
  // default binding's relationship+targetType as the template (only targetToken varies).
  const template =
    current?.kind === 'anchor'
      ? current.anchor
      : def.defaultBinding?.kind === 'anchor'
        ? def.defaultBinding.anchor
        : undefined;
  if (!template) return [];
  let rows: Array<{ token: string; name?: string | null }>;
  try {
    rows = await lister(template.targetType);
  } catch {
    return [];
  }
  return rows.map((r) =>
    mark(
      {
        kind: 'anchor',
        anchor: {
          relationship: template.relationship,
          targetType: template.targetType,
          targetToken: r.token,
        },
      },
      r.name || r.token,
    ),
  );
}

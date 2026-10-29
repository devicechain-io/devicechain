// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Towerpulse annex board, as a member limited to ONE customer opens it.
//
// Such a member is refused every area anchor and the relationship graph (FORBIDDEN, reason
// SCOPE_UNSUPPORTED). A board anchored on areas therefore opened for them with the context dropped,
// empty Floor and Zone pickers and every aggregate unbound. This drives the REAL context cascade and
// the REAL candidate resolver over the COMMITTED fixture (the document the sim publishes), against a
// backend that answers as the platform does for that member: the scope refusal for an area or asset
// anchor, and the customer's own devices for the `assigned` customer anchor (customerDevices).
//
// The negative control retargets the same board's root at an area. If it did not fail, the harness
// would be reading something other than the refusal and the passing case would prove nothing.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { resolveSlotCandidates } from './candidates';
import { resolveContextBindings } from './context';
import type { DashboardDefinition, EntityCandidateLister, SlotBinding } from './types';

const FIXTURE = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  'testdata',
  'sim-dashboards',
  'tp-annex-dashboard.json',
);

const OWNER_CUSTOMER = 'tp-halden';
const ANNEX_DEVICES = [
  'tp-ahu-annex-1',
  'tp-chiller-annex',
  'tp-meter-annex',
  'tp-vav-annex-f1-north',
  'tp-vav-annex-f1-south',
  'tp-vav-annex-f2-north',
  'tp-vav-annex-f2-south',
];

const load = (): DashboardDefinition => JSON.parse(readFileSync(FIXTURE, 'utf8')) as DashboardDefinition;

function defaults(def: DashboardDefinition): Record<string, SlotBinding> {
  const out: Record<string, SlotBinding> = {};
  for (const [name, slot] of Object.entries(def.slots ?? {})) {
    if (slot.defaultBinding) out[name] = slot.defaultBinding;
  }
  return out;
}

// What the platform answers a customer-scoped member.
const scopedBackend = {
  calls: [] as string[],
  async devicesForAnchor(anchor: { relationship: string; targetType: string; targetToken: string }): Promise<string[]> {
    this.calls.push(`${anchor.targetType}:${anchor.targetToken}`);
    if (anchor.targetType === 'customer' && anchor.targetToken === OWNER_CUSTOMER && anchor.relationship === 'assigned') {
      return [...ANNEX_DEVICES];
    }
    throw Object.assign(new Error('forbidden'), { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' });
  },
};

// The lister is the tenant-wide entity read; a customer-scoped member is refused it for areas.
const refusingLister: EntityCandidateLister = async (kind) => {
  throw new Error(`forbidden: SCOPE_UNSUPPORTED (${kind})`);
};

describe('the annex board for a customer-scoped member', () => {
  it('keeps its context, fills its pickers and binds every slot its widgets read', async () => {
    scopedBackend.calls = [];
    const def = load();
    const effective = await resolveContextBindings(def, defaults(def), {}, scopedBackend);

    // The default zone survives the cascade: it is a member of the customer's devices.
    expect(effective.selectedZone).toEqual({ kind: 'device', deviceToken: 'tp-vav-annex-f1-north' });

    // Every slot is bound, so no aggregate renders as an unbound placeholder.
    for (const name of Object.keys(def.slots ?? {})) {
      expect(effective[name], `slot ${name} is unbound`).toBeDefined();
    }
    for (const w of def.widgets) {
      const slot = (w.datasource as { slot?: string } | undefined)?.slot;
      if (slot) expect(effective[slot], `${w.id} reads unbound slot ${slot}`).toBeDefined();
    }

    // The picker the board offers has options, and flags the current pick.
    const target = def.widgets.find((w) => w.type === 'entity-selector')?.options?.selectionTarget as string;
    expect(target).toBe('selectedZone');
    const options = await resolveSlotCandidates(def, target, effective, scopedBackend, refusingLister);
    expect(options.map((o) => o.label)).toEqual([...ANNEX_DEVICES].sort());
    expect(options.filter((o) => o.selected).map((o) => o.label)).toEqual(['tp-vav-annex-f1-north']);

    // Nothing but the customer anchor was ever asked of the backend.
    expect(new Set(scopedBackend.calls)).toEqual(new Set([`customer:${OWNER_CUSTOMER}`]));
  });

  it('has no picker that needs an area list', () => {
    const def = load();
    for (const w of def.widgets.filter((x) => x.type === 'entity-selector')) {
      const slot = def.slots?.[w.options?.selectionTarget as string];
      expect(slot?.scope, `${w.id} picks a root slot, which lists the tenant's entities`).toBeDefined();
    }
  });

  it('NEGATIVE CONTROL: the same board rooted on an area loses its context and its pickers', async () => {
    const def = load();
    const slots = def.slots ?? {};
    const rootName = Object.keys(slots).find((n) => slots[n].type === 'anchor')!;
    const rooted = slots[rootName].defaultBinding as Extract<SlotBinding, { kind: 'anchor' }>;
    const areaBoard: DashboardDefinition = {
      ...def,
      slots: {
        ...slots,
        [rootName]: {
          ...slots[rootName],
          defaultBinding: { kind: 'anchor', anchor: { ...rooted.anchor, targetType: 'area', targetToken: 'tp-annex' } },
        },
      },
    };
    const effective = await resolveContextBindings(areaBoard, defaults(areaBoard), {}, scopedBackend);
    expect(effective.selectedZone).toBeUndefined();
    expect(await resolveSlotCandidates(areaBoard, 'selectedZone', effective, scopedBackend, refusingLister)).toEqual([]);
  });
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { effectiveBindings } from './bindings';
import type { DashboardDefinition, SlotBinding } from './types';

function def(slots: DashboardDefinition['slots']): DashboardDefinition {
  return {
    schemaVersion: 1,
    title: 'T',
    canvas: { grid: { snap: true, size: 8 }, breakpoints: { base: 0 } },
    widgets: [],
    slots,
  };
}

const devA: SlotBinding = { kind: 'device', deviceToken: 'a' };
const devB: SlotBinding = { kind: 'device', deviceToken: 'b' };

describe('effectiveBindings', () => {
  it('uses slot default bindings when no manifest is given', () => {
    const d = def({ primary: { type: 'device', defaultBinding: devA } });
    expect(effectiveBindings(d)).toEqual({ primary: devA });
  });

  it('omits a slot with no default and no manifest entry (renders as placeholder)', () => {
    const d = def({ primary: { type: 'device' } });
    expect(effectiveBindings(d)).toEqual({});
  });

  it('lets the host manifest override a slot default', () => {
    const d = def({ primary: { type: 'device', defaultBinding: devA } });
    expect(effectiveBindings(d, { primary: devB })).toEqual({ primary: devB });
  });

  it('adds manifest bindings for slots that had no default', () => {
    const d = def({ primary: { type: 'device' } });
    expect(effectiveBindings(d, { primary: devB })).toEqual({ primary: devB });
  });

  it('returns an empty manifest for a slot-free definition', () => {
    expect(effectiveBindings(def(undefined))).toEqual({});
  });
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { effectiveBindings, parseBindingManifest, stripDefaultBindings } from './bindings';
import type { DashboardDefinition, SlotBinding } from './types';

function def(slots: DashboardDefinition['slots']): DashboardDefinition {
  return {
    schemaVersion: 1,
    title: 'T',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
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

describe('parseBindingManifest', () => {
  it('validates a host manifest, dropping malformed entries', () => {
    const raw = {
      primary: { kind: 'device', deviceToken: 'therm-9' },
      area: { kind: 'anchor', anchor: { relationship: 'contains', targetType: 'area', targetToken: 'plant-2' } },
      bad1: { kind: 'device', deviceToken: '' }, // empty token → dropped
      bad2: { kind: 'nonsense' }, // unknown kind → dropped
      bad3: 'not an object',
    };
    expect(parseBindingManifest(raw)).toEqual({
      bindings: {
        primary: { kind: 'device', deviceToken: 'therm-9' },
        area: { kind: 'anchor', anchor: { relationship: 'contains', targetType: 'area', targetToken: 'plant-2' } },
      },
      // NAMED, not counted. A host's only other way to learn an entry vanished is to
      // compare key counts against its own input, which cannot say which one went.
      dropped: ['bad1', 'bad2', 'bad3'],
    });
  });

  it('reports nothing dropped when every entry is good', () => {
    // The counterweight: naming the refusals is only useful while a clean manifest
    // reports an empty list, and a `dropped` that was never empty would make every
    // host's error path fire on correct input.
    const out = parseBindingManifest({ primary: { kind: 'device', deviceToken: 'therm-9' } });
    expect(out.dropped).toEqual([]);
  });

  it('returns nothing for a non-object', () => {
    for (const raw of [null, [], 'x']) {
      expect(parseBindingManifest(raw)).toEqual({ bindings: {}, dropped: [] });
    }
  });

  it('ignores a __proto__ key without polluting the prototype', () => {
    const raw = JSON.parse('{"__proto__": {"kind":"device","deviceToken":"x"}, "ok": {"kind":"device","deviceToken":"y"}}');
    const out = parseBindingManifest(raw);
    expect(out.bindings).toEqual({ ok: { kind: 'device', deviceToken: 'y' } });
    // Reported rather than swallowed: the host asked for a binding and did not get one,
    // which is the same thing that happened to a malformed entry.
    expect(out.dropped).toEqual(['__proto__']);
    expect(Object.getPrototypeOf(out.bindings)).toBe(Object.prototype); // not swapped
    expect(({} as Record<string, unknown>).kind).toBeUndefined(); // no global pollution
  });

  it('a manifest overrides a definition default via effectiveBindings', () => {
    const d = def({ primary: { type: 'device', defaultBinding: devA } });
    const { bindings: manifest } = parseBindingManifest({ primary: { kind: 'device', deviceToken: 'host-device' } });
    expect(effectiveBindings(d, manifest)).toEqual({ primary: { kind: 'device', deviceToken: 'host-device' } });
  });
});

describe('stripDefaultBindings', () => {
  it('removes slot default bindings but keeps slot names/types/labels', () => {
    const d = def({ primary: { type: 'device', label: 'Thermostat', defaultBinding: devA } });
    const t = stripDefaultBindings(d);
    expect(t.slots).toEqual({ primary: { type: 'device', label: 'Thermostat' } });
    // The stripped template has no effective bindings without a host manifest.
    expect(effectiveBindings(t)).toEqual({});
  });

  it('no-ops a slot-free definition', () => {
    const d = def(undefined);
    expect(stripDefaultBindings(d)).toBe(d);
  });

  it('a template + a matching host manifest resolves the slot (the embed contract)', () => {
    const authored = def({ primary: { type: 'device', label: 'therm-001', defaultBinding: devA } });
    const template = stripDefaultBindings(authored);
    const { bindings: hostA } = parseBindingManifest({ primary: { kind: 'device', deviceToken: 'host-a' } });
    const { bindings: hostB } = parseBindingManifest({ primary: { kind: 'device', deviceToken: 'host-b' } });
    // One template, two manifests → two different bindings.
    expect(effectiveBindings(template, hostA)).toEqual({ primary: { kind: 'device', deviceToken: 'host-a' } });
    expect(effectiveBindings(template, hostB)).toEqual({ primary: { kind: 'device', deviceToken: 'host-b' } });
    // A non-template (defaults kept) renders the author's device with no manifest.
    expect(effectiveBindings(authored)).toEqual({ primary: devA });
  });
});

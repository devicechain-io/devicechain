// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import {
  parseTierConfig,
  buildTierConfigPatch,
  invalidTierScalar,
  TIER_SCALARS,
  type ConfigDimension,
} from './tierConfig';

// The platform's dimensions, as the server hands them over.
const DIMS: ConfigDimension[] = [
  { rateField: 'ingestReadingsPerSecond', burstField: 'ingestBurst' },
  { rateField: 'outboundCallsPerSecond', burstField: 'outboundBurst' },
];

// GOLD's seeded packaging — the thing an accidental clear destroys.
const GOLD = JSON.stringify({
  ingestReadingsPerSecond: 2000,
  ingestBurst: 4000,
  outboundCallsPerSecond: 200,
  outboundBurst: 400,
});

describe('parseTierConfig', () => {
  it('reads settings into editable strings', () => {
    expect(parseTierConfig(GOLD)).toEqual({
      ingestReadingsPerSecond: '2000',
      ingestBurst: '4000',
      outboundCallsPerSecond: '200',
      outboundBurst: '400',
    });
  });

  it('treats a tier that declares nothing as empty, not broken', () => {
    // The seeded standard tier carries null: "inherit the platform default
    // everywhere" is a valid, deliberate state.
    expect(parseTierConfig(null)).toEqual({});
    expect(parseTierConfig(undefined)).toEqual({});
    expect(parseTierConfig('')).toEqual({});
    // Garbage must not throw and take the form down with it.
    expect(parseTierConfig('not json')).toEqual({});
    expect(parseTierConfig('[1,2]')).toEqual({});
  });
});

describe('buildTierConfigPatch', () => {
  // THE point of this function. Without the guard, a rename saved while the
  // dimensions query is in flight — or has failed, which is a PERMANENT state, not a
  // race — sends "{}" and clears the tier. Gold's tenants would silently drop from
  // 2000/s to the platform default within the 60s TTL, off a query failure the
  // operator never saw, under a success toast.
  //
  // Undefined means "leave the settings alone", which the server's config-as-PATCH
  // exists to express. The form must not claim to have edited settings it never
  // rendered.
  it('says NOTHING about settings when the editor never rendered', () => {
    expect(buildTierConfigPatch(null, {}, GOLD)).toBeUndefined();
    expect(buildTierConfigPatch(undefined, {}, GOLD)).toBeUndefined();
    expect(buildTierConfigPatch([], {}, GOLD)).toBeUndefined();
  });

  it('round-trips a tier unchanged when nothing was edited', () => {
    const patch = buildTierConfigPatch(DIMS, parseTierConfig(GOLD), GOLD);
    expect(JSON.parse(patch!)).toEqual(JSON.parse(GOLD));
  });

  it('writes an edited ceiling', () => {
    const settings = { ...parseTierConfig(GOLD), ingestReadingsPerSecond: '9000' };
    expect(JSON.parse(buildTierConfigPatch(DIMS, settings, GOLD)!)).toEqual({
      ingestReadingsPerSecond: 9000,
      ingestBurst: 4000,
      outboundCallsPerSecond: 200,
      outboundBurst: 400,
    });
  });

  it('omits a cleared field so the tier inherits the platform default', () => {
    // Blanking a field is the one legitimate way to drop a ceiling: the key is
    // omitted, which is how a tier says "inherit". It is NOT written as 0 — a zero
    // ceiling admits nothing and the server rejects it outright.
    const settings = { ...parseTierConfig(GOLD), ingestReadingsPerSecond: '  ' };
    const out = JSON.parse(buildTierConfigPatch(DIMS, settings, GOLD)!);
    expect(out).not.toHaveProperty('ingestReadingsPerSecond');
    expect(out.ingestBurst).toBe(4000);
  });

  it('clears every setting only when the operator actually emptied the fields', () => {
    // The editor WAS live and every field is blank — so "{}" here is a real
    // instruction, not the absence of one. This is the case the guard above must not
    // over-block.
    expect(buildTierConfigPatch(DIMS, {}, GOLD)).toBe('{}');
  });

  it('preserves a key the editor has no field for', () => {
    // Today every registered key is a numeric rate/burst, so this cannot happen. It
    // can the day a tier's config carries a model menu (ADR-065 S5, the tier↔provider
    // join) — a non-numeric key no rate/burst field renders. Rebuilding config from
    // only the rendered fields would silently delete a tier's model menu on the next
    // rename; preserving is what keeps this correct when that key arrives.
    const withMenu = JSON.stringify({ ingestReadingsPerSecond: 2000, aiProviders: ['anthropic'] });
    const out = JSON.parse(buildTierConfigPatch(DIMS, parseTierConfig(withMenu), withMenu)!);
    expect(out.aiProviders).toEqual(['anthropic']);
    expect(out.ingestReadingsPerSecond).toBe(2000);
  });

  it('ignores a non-numeric entry in a rendered field rather than writing garbage', () => {
    const settings = { ...parseTierConfig(GOLD), ingestBurst: 'lots' };
    const out = JSON.parse(buildTierConfigPatch(DIMS, settings, GOLD)!);
    expect(out).not.toHaveProperty('ingestBurst');
  });
});

describe('tier scalar settings', () => {
  const ALL = {
    heldCommandCeiling: '5000',
    shedPriority: '80',
    geoFenceCeiling: '100',
    geoFencePositionCeiling: '256',
    geoFencePositionBudget: '20000',
  };

  it('carries all five scalar keys the server accepts', () => {
    expect(TIER_SCALARS.map((s) => s.key).sort()).toEqual(Object.keys(ALL).sort());
  });

  it('writes them as numbers beside the dimensions', () => {
    const out = JSON.parse(buildTierConfigPatch(DIMS, { ...parseTierConfig(GOLD), ...ALL }, GOLD)!);
    expect(out).toMatchObject({ ingestBurst: 4000, heldCommandCeiling: 5000, shedPriority: 80, geoFenceCeiling: 100 });
    expect(out.geoFencePositionBudget).toBe(20000);
  });

  it('reads stored scalars back so an unrelated save does not drop them', () => {
    const stored = JSON.stringify({ ingestBurst: 4000, shedPriority: 80, heldCommandCeiling: 5000 });
    const out = JSON.parse(buildTierConfigPatch(DIMS, parseTierConfig(stored), stored)!);
    expect(out).toEqual({ ingestBurst: 4000, shedPriority: 80, heldCommandCeiling: 5000 });
  });

  it('omits a blanked scalar so the tier inherits', () => {
    const stored = JSON.stringify({ shedPriority: 80 });
    const out = JSON.parse(buildTierConfigPatch(DIMS, { shedPriority: ' ' }, stored)!);
    expect(out).not.toHaveProperty('shedPriority');
  });

  it('refuses values the server would refuse', () => {
    expect(invalidTierScalar(ALL)).toBeNull();
    expect(invalidTierScalar({})).toBeNull();
    expect(invalidTierScalar({ shedPriority: '101' })?.key).toBe('shedPriority');
    expect(invalidTierScalar({ shedPriority: '0' })?.key).toBe('shedPriority');
    expect(invalidTierScalar({ geoFencePositionCeiling: '1025' })?.key).toBe('geoFencePositionCeiling');
    expect(invalidTierScalar({ geoFenceCeiling: '4001' })?.key).toBe('geoFenceCeiling');
    expect(invalidTierScalar({ geoFencePositionBudget: '128001' })?.key).toBe('geoFencePositionBudget');
    expect(invalidTierScalar({ heldCommandCeiling: '1.5' })?.key).toBe('heldCommandCeiling');
    expect(invalidTierScalar({ heldCommandCeiling: 'lots' })?.key).toBe('heldCommandCeiling');
    expect(invalidTierScalar({ heldCommandCeiling: '2147483647' })).toBeNull();
    expect(invalidTierScalar({ heldCommandCeiling: '2147483648' })?.key).toBe('heldCommandCeiling');
  });
});

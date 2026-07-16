// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { parseTierConfig, buildTierConfigPatch, type ConfigDimension } from './tierConfig';

// The platform's dimensions, as the server hands them over.
const DIMS: ConfigDimension[] = [
  { rateField: 'ingestMessagesPerSecond', burstField: 'ingestBurst' },
  { rateField: 'outboundMessagesPerSecond', burstField: 'outboundBurst' },
];

// GOLD's seeded packaging — the thing an accidental clear destroys.
const GOLD = JSON.stringify({
  ingestMessagesPerSecond: 2000,
  ingestBurst: 4000,
  outboundMessagesPerSecond: 200,
  outboundBurst: 400,
});

describe('parseTierConfig', () => {
  it('reads settings into editable strings', () => {
    expect(parseTierConfig(GOLD)).toEqual({
      ingestMessagesPerSecond: '2000',
      ingestBurst: '4000',
      outboundMessagesPerSecond: '200',
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
    const settings = { ...parseTierConfig(GOLD), ingestMessagesPerSecond: '9000' };
    expect(JSON.parse(buildTierConfigPatch(DIMS, settings, GOLD)!)).toEqual({
      ingestMessagesPerSecond: 9000,
      ingestBurst: 4000,
      outboundMessagesPerSecond: 200,
      outboundBurst: 400,
    });
  });

  it('omits a cleared field so the tier inherits the platform default', () => {
    // Blanking a field is the one legitimate way to drop a ceiling: the key is
    // omitted, which is how a tier says "inherit". It is NOT written as 0 — a zero
    // ceiling admits nothing and the server rejects it outright.
    const settings = { ...parseTierConfig(GOLD), ingestMessagesPerSecond: '  ' };
    const out = JSON.parse(buildTierConfigPatch(DIMS, settings, GOLD)!);
    expect(out).not.toHaveProperty('ingestMessagesPerSecond');
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
    const withMenu = JSON.stringify({ ingestMessagesPerSecond: 2000, aiProviders: ['anthropic'] });
    const out = JSON.parse(buildTierConfigPatch(DIMS, parseTierConfig(withMenu), withMenu)!);
    expect(out.aiProviders).toEqual(['anthropic']);
    expect(out.ingestMessagesPerSecond).toBe(2000);
  });

  it('ignores a non-numeric entry in a rendered field rather than writing garbage', () => {
    const settings = { ...parseTierConfig(GOLD), ingestBurst: 'lots' };
    const out = JSON.parse(buildTierConfigPatch(DIMS, settings, GOLD)!);
    expect(out).not.toHaveProperty('ingestBurst');
  });
});

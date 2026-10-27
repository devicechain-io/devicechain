// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
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
    calculatedMetricsPerProfile: '4',
    integrationsPerTenant: '12',
  };

  it('carries all seven scalar keys the server accepts', () => {
    expect(TIER_SCALARS.map((s) => s.key).sort()).toEqual(Object.keys(ALL).sort());
  });

  it('writes them as numbers beside the dimensions', () => {
    const out = JSON.parse(buildTierConfigPatch(DIMS, { ...parseTierConfig(GOLD), ...ALL }, GOLD)!);
    expect(out).toMatchObject({ ingestBurst: 4000, heldCommandCeiling: 5000, shedPriority: 80, geoFenceCeiling: 100 });
    expect(out.geoFencePositionBudget).toBe(20000);
    expect(out.calculatedMetricsPerProfile).toBe(4);
  });

  it('writes an explicit 0 rather than dropping it as blank', () => {
    const out = JSON.parse(buildTierConfigPatch(DIMS, { calculatedMetricsPerProfile: '0' }, GOLD)!);
    expect(out.calculatedMetricsPerProfile).toBe(0);
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

  it('accepts 0 only where the server does', () => {
    // 0 is how a tier turns calculated metrics off; every other ceiling refuses it.
    expect(invalidTierScalar({ calculatedMetricsPerProfile: '0' })).toBeNull();
    expect(invalidTierScalar({ calculatedMetricsPerProfile: '32' })).toBeNull();
    expect(invalidTierScalar({ calculatedMetricsPerProfile: '33' })?.key).toBe('calculatedMetricsPerProfile');
    expect(invalidTierScalar({ calculatedMetricsPerProfile: '-1' })?.key).toBe('calculatedMetricsPerProfile');
    expect(invalidTierScalar({ integrationsPerTenant: '0' })).toBeNull();
    expect(invalidTierScalar({ integrationsPerTenant: '100' })).toBeNull();
    expect(invalidTierScalar({ integrationsPerTenant: '101' })?.key).toBe('integrationsPerTenant');
    expect(invalidTierScalar({ integrationsPerTenant: '-1' })?.key).toBe('integrationsPerTenant');
    expect(invalidTierScalar({ heldCommandCeiling: '0' })?.key).toBe('heldCommandCeiling');
  });
});

// The maxima in TIER_SCALARS are copies of server constants no query exposes. Read them off
// the Go source so a raised (or lowered) platform maximum fails here instead of leaving the
// form refusing values the server accepts, or sending ones it refuses.
describe('tier scalar maxima match the server', () => {
  const repo = resolve(__dirname, '../../../../../../..');
  const caps = readFileSync(resolve(repo, 'backend/core/governance/geofence_caps.go'), 'utf8');
  const tierGo = readFileSync(resolve(repo, 'backend/services/user-management/iam/tierconfig.go'), 'utf8');

  // A Go integer constant declared as `Name = 1_024`; missing is a failure, never a skip.
  const goConst = (name: string): number => {
    const m = caps.match(new RegExp(String.raw`^\s*${name}\s*=\s*([0-9_]+)\s*$`, 'm'));
    expect(m, `${name} not found in geofence_caps.go`).not.toBeNull();
    return Number(m![1].replace(/_/g, ''));
  };
  const max = (key: string) => TIER_SCALARS.find((s) => s.key === key)?.max;

  it('geofence maxima', () => {
    expect(max('geoFenceCeiling')).toBe(goConst('MaxGeoFenceCeiling'));
    expect(max('geoFencePositionCeiling')).toBe(goConst('MaxGeoFencePositionCeiling'));
    expect(max('geoFencePositionBudget')).toBe(goConst('MaxTenantGeometryPositions'));
  });

  it('shed priority band', () => {
    const band = tierGo.match(/func validateShedPriority[\s\S]*?if f < (\d+) \|\| f > (\d+) \{/);
    expect(band, 'validateShedPriority band not found in tierconfig.go').not.toBeNull();
    expect(band![1]).toBe('1');
    expect(max('shedPriority')).toBe(Number(band![2]));
  });

  it('calculated metrics: 0 to the platform maximum', () => {
    const calc = readFileSync(resolve(repo, 'backend/core/governance/calculated_metrics_ceiling.go'), 'utf8');
    const m = calc.match(/^\s*MaxCalculatedMetricsPerProfile\s*=\s*([0-9_]+)\s*$/m);
    expect(m, 'MaxCalculatedMetricsPerProfile not found').not.toBeNull();
    const s = TIER_SCALARS.find((x) => x.key === 'calculatedMetricsPerProfile');
    expect(s?.max).toBe(Number(m![1].replace(/_/g, '')));
    expect(tierGo).toMatch(/func validateCalculatedMetricsPerProfile[\s\S]*?if f < 0 \|\| f > governance\.MaxCalculatedMetricsPerProfile \{/);
    expect(s?.min).toBe(0);
  });

  it('integrations per tenant: 0 to the platform maximum', () => {
    const gov = readFileSync(resolve(repo, 'backend/core/governance/integrations_ceiling.go'), 'utf8');
    const m = gov.match(/^\s*MaxIntegrationsPerTenant\s*=\s*([0-9_]+)\s*$/m);
    expect(m, 'MaxIntegrationsPerTenant not found').not.toBeNull();
    const s = TIER_SCALARS.find((x) => x.key === 'integrationsPerTenant');
    expect(s?.max).toBe(Number(m![1].replace(/_/g, '')));
    expect(tierGo).toMatch(/func validateIntegrationsPerTenant[\s\S]*?if f < 0 \|\| f > governance\.MaxIntegrationsPerTenant \{/);
    expect(s?.min).toBe(0);
  });

  it('held ceiling stops at the int32 wall', () => {
    expect(tierGo).toMatch(/func validateHeldCommandCeiling\(v any\) error \{ return validatePositiveBurst\(v\) \}/);
    expect(tierGo).toMatch(/f <= 0 \|\| f > math\.MaxInt32/);
    expect(max('heldCommandCeiling')).toBe(2 ** 31 - 1);
  });
});

describe('the mfaRequired tier flag', () => {
  it('reads true as on, and anything else as off', () => {
    expect(parseTierConfig(JSON.stringify({ mfaRequired: true })).mfaRequired).toBe('true');
    expect(parseTierConfig(JSON.stringify({ mfaRequired: false })).mfaRequired).toBeUndefined();
    expect(parseTierConfig(JSON.stringify({ mfaRequired: 'true' })).mfaRequired).toBeUndefined();
  });

  it('writes a boolean true beside the numbers, and omits it when off', () => {
    const on = JSON.parse(buildTierConfigPatch(DIMS, { ...parseTierConfig(GOLD), mfaRequired: 'true' }, GOLD)!);
    expect(on.mfaRequired).toBe(true);
    const off = JSON.parse(buildTierConfigPatch(DIMS, { ...parseTierConfig(GOLD), mfaRequired: '' }, GOLD)!);
    expect(off).not.toHaveProperty('mfaRequired');
  });

  it('turning it off removes a stored true instead of preserving it as an unknown key', () => {
    const stored = JSON.stringify({ ingestBurst: 4000, mfaRequired: true });
    const settings = parseTierConfig(stored);
    delete settings.mfaRequired;
    const out = JSON.parse(buildTierConfigPatch(DIMS, settings, stored)!);
    expect(out).not.toHaveProperty('mfaRequired');
  });

  it('keeps a stored true when the form is saved untouched', () => {
    const stored = JSON.stringify({ ingestBurst: 4000, mfaRequired: true });
    const out = JSON.parse(buildTierConfigPatch(DIMS, parseTierConfig(stored), stored)!);
    expect(out.mfaRequired).toBe(true);
  });
});

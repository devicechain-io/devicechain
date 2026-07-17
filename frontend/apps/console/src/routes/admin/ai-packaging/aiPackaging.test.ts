// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import {
  buildPackagingTiers,
  tierWarning,
  warningText,
  type PackagingProvider,
  type PackagingTierInput,
} from './aiPackaging';
import type { AiProviderTierGrant } from '@/lib/api/ai-inference-admin';

const grant = (tier: string, token: string, isDefault = false, enabled = true): AiProviderTierGrant => ({
  tier,
  isDefault,
  provider: { token, name: token, enabled },
});

const CATALOG: PackagingTierInput[] = [
  { token: 'gold', name: 'Gold', tenantCount: 2 },
  { token: 'silver', name: 'Silver', tenantCount: 5 },
  { token: 'bronze', name: 'Bronze', tenantCount: 0 },
];

const PROVIDERS: PackagingProvider[] = [
  { token: 'opus', name: 'Opus', enabled: true },
  { token: 'sonnet', name: 'Sonnet', enabled: true },
  { token: 'retired', name: 'Retired', enabled: false },
];

describe('buildPackagingTiers', () => {
  it('keeps the catalog order and folds grants onto their tier', () => {
    const tiers = buildPackagingTiers(CATALOG, [
      grant('silver', 'sonnet', true),
      grant('gold', 'opus', true),
      grant('gold', 'sonnet'),
    ]);

    expect(tiers.map((t) => t.token)).toEqual(['gold', 'silver', 'bronze']);
    expect([...tiers[0].granted].sort()).toEqual(['opus', 'sonnet']);
    expect(tiers[0].defaultProvider).toBe('opus');
    expect(tiers[1].defaultProvider).toBe('sonnet');
    expect(tiers[2].granted.size).toBe(0);
    expect(tiers[2].defaultProvider).toBeNull();
  });

  it('surfaces a grant naming a tier the catalog does not have, rather than dropping it', () => {
    const tiers = buildPackagingTiers(CATALOG, [grant('platinum', 'opus', true)]);

    // Unknown tiers sort after the catalog: they are cleanup, not packaging.
    expect(tiers.map((t) => t.token)).toEqual(['gold', 'silver', 'bronze', 'platinum']);
    const platinum = tiers[3];
    expect(platinum.known).toBe(false);
    // Inert by construction: no tenant can report a tier the catalog does not have.
    expect(platinum.tenantCount).toBe(0);
    expect(platinum.defaultProvider).toBe('opus');
  });

  it('marks catalog tiers known even when nothing is granted to them', () => {
    const tiers = buildPackagingTiers(CATALOG, []);
    expect(tiers.every((t) => t.known)).toBe(true);
    expect(tiers.every((t) => t.defaultProvider === null)).toBe(true);
  });
});

describe('tierWarning', () => {
  const tierNamed = (token: string, grants: AiProviderTierGrant[]) =>
    buildPackagingTiers(CATALOG, grants).find((t) => t.token === token)!;

  it('says nothing about a tier that offers no models — AI off is a package, not a fault', () => {
    expect(tierWarning(tierNamed('gold', []), PROVIDERS)).toBeNull();
  });

  it('says nothing when the tier marks an enabled default', () => {
    const tier = tierNamed('gold', [grant('gold', 'opus', true), grant('gold', 'sonnet')]);
    expect(tierWarning(tier, PROVIDERS)).toBeNull();
  });

  it('warns when a tier offers models but marks no default', () => {
    const tier = tierNamed('gold', [grant('gold', 'opus'), grant('gold', 'sonnet')]);
    expect(tierWarning(tier, PROVIDERS)).toEqual({ kind: 'no-default' });
  });

  // The deliberate server behaviour this whole screen exists to make visible: granting
  // exactly one model still resolves to NONE, because nothing infers a default from the
  // fact that there is only one.
  it('warns when a tier offers exactly one model and marks no default', () => {
    const tier = tierNamed('gold', [grant('gold', 'opus')]);
    expect(tierWarning(tier, PROVIDERS)).toEqual({ kind: 'no-default' });
  });

  // The trap that is invisible from the packaging screen alone: the mark is right, the
  // provider is off, and a menu carries enabled providers only.
  //
  // The tier grants a SECOND, enabled model on purpose. With only the disabled one this
  // still passes, but it passes for two reasons at once — grant count and enabledness —
  // so a rule that short-circuited on "the tier grants exactly one model" would fail this
  // test and the sole-model test together, and the pair would no longer say which rule
  // broke. Two grants isolate this one.
  it('warns when the marked default is disabled', () => {
    const tier = tierNamed('gold', [grant('gold', 'retired', true), grant('gold', 'opus')]);
    expect(tierWarning(tier, PROVIDERS)).toEqual({ kind: 'default-disabled', provider: 'retired' });
  });

  it('does not warn about a disabled provider that is merely granted, not the default', () => {
    const tier = tierNamed('gold', [grant('gold', 'opus', true), grant('gold', 'retired')]);
    expect(tierWarning(tier, PROVIDERS)).toBeNull();
  });

  it('stays quiet when the marked provider is missing from a truncated list', () => {
    const tier = tierNamed('gold', [grant('gold', 'unlisted', true)]);
    expect(tierWarning(tier, PROVIDERS)).toBeNull();
  });

  // An unknown tier would otherwise trip the no-default arm and claim its tenants get no
  // model — when it has none and can have none. Stale config is a different sentence.
  it('does not claim an unknown tier strands tenants, since nothing resolves through it', () => {
    const tier = tierNamed('platinum', [grant('platinum', 'opus'), grant('platinum', 'sonnet')]);
    expect(tier.known).toBe(false);
    expect(tierWarning(tier, PROVIDERS)).toBeNull();
  });
});

describe('warningText', () => {
  it('counts the tenants a tier actually moves', () => {
    const tier = buildPackagingTiers(CATALOG, [grant('silver', 'opus')]).find(
      (t) => t.token === 'silver',
    )!;
    expect(warningText({ kind: 'no-default' }, tier)).toContain('5 tenants packaged here');
  });

  it('singularizes one tenant', () => {
    const tier = buildPackagingTiers([{ token: 'gold', tenantCount: 1 }], [grant('gold', 'opus')]).find(
      (t) => t.token === 'gold',
    )!;
    expect(warningText({ kind: 'no-default' }, tier)).toContain('1 tenant packaged here');
  });

  it('speaks in the future tense for a tier nobody is on yet', () => {
    const tier = buildPackagingTiers(CATALOG, [grant('bronze', 'opus')]).find(
      (t) => t.token === 'bronze',
    )!;
    const text = warningText({ kind: 'no-default' }, tier);
    expect(text).toContain('No tenants are packaged here yet');
    expect(text).toContain('will get');
  });

  it('names the disabled provider so the operator knows what to re-enable', () => {
    const tier = buildPackagingTiers(CATALOG, [grant('gold', 'retired', true, false)]).find(
      (t) => t.token === 'gold',
    )!;
    expect(warningText({ kind: 'default-disabled', provider: 'retired' }, tier)).toContain('retired');
  });
});

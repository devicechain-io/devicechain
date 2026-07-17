// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { buildTenantAiModels } from './tenantAiModels';
import type {
  AiFunction,
  AiFunctionAssignment,
  AiProviderTierGrant,
  AiProviderTenantGrant,
} from '@/lib/api/ai-inference-admin';

const tierGrant = (
  tier: string,
  token: string,
  isDefault = false,
  enabled = true,
): AiProviderTierGrant => ({
  tier,
  isDefault,
  provider: { token, name: token, enabled },
});

const tenantGrant = (
  tenant: string,
  token: string,
  enabled = true,
): AiProviderTenantGrant => ({
  tenant,
  provider: { token, name: token, enabled },
});

const assignment = (fn: string, token: string, enabled = true): AiFunctionAssignment => ({
  function: fn,
  provider: { token, name: token, enabled },
});

const FUNCTIONS: AiFunction[] = [
  { token: 'rule-drafting', name: 'Detection rule drafting', description: 'Drafts a rule.' },
];

const build = (
  assignments: AiFunctionAssignment[],
  tierGrants: AiProviderTierGrant[],
  tenantGrants: AiProviderTenantGrant[],
  tier = 'gold',
) => buildTenantAiModels(FUNCTIONS, assignments, tierGrants, tenantGrants, tier);

describe('buildTenantAiModels — the menu', () => {
  it('is the enabled union of the tenant’s tier grants and its additive grants, deduped', () => {
    const m = build(
      [],
      [tierGrant('gold', 'opus'), tierGrant('gold', 'sonnet'), tierGrant('silver', 'fable')],
      [tenantGrant('acme', 'sonnet'), tenantGrant('acme', 'haiku')],
    );
    // silver's grant (fable) is another tier's and never reaches this tenant; sonnet is
    // granted twice (tier + additive) and appears once.
    expect(m.menu.map((x) => x.token)).toEqual(['haiku', 'opus', 'sonnet']);
  });

  it('excludes disabled providers — a disabled model is off the menu even when granted', () => {
    const m = build(
      [],
      [tierGrant('gold', 'opus'), tierGrant('gold', 'sonnet', false, false)],
      [tenantGrant('acme', 'haiku', false)],
    );
    expect(m.menu.map((x) => x.token)).toEqual(['opus']);
  });
});

describe('buildTenantAiModels — effective model (mirrors ResolveModelForFunction)', () => {
  it('uses the tenant’s assignment when it is on the menu', () => {
    const m = build([assignment('rule-drafting', 'opus')], [tierGrant('gold', 'opus', true)], []);
    expect(m.rows[0].effective).toEqual({ kind: 'assigned', token: 'opus', name: 'opus' });
    expect(m.rows[0].assignedOnMenu).toBe(true);
  });

  it('falls back to the tier’s marked default when the tenant assigned nothing', () => {
    const m = build(
      [],
      [tierGrant('gold', 'opus', true), tierGrant('gold', 'sonnet')],
      [],
    );
    expect(m.rows[0].effective).toEqual({ kind: 'tier-default', token: 'opus', name: 'opus' });
  });

  it('resolves to none — never a substitute — when the assignment is off the menu', () => {
    // Assigned sonnet, but sonnet is no longer granted; the tier defaults to opus. The
    // server does NOT substitute the default for a stale choice, and neither does this.
    const m = build([assignment('rule-drafting', 'sonnet')], [tierGrant('gold', 'opus', true)], []);
    expect(m.rows[0].effective).toEqual({
      kind: 'none-assignment-off-menu',
      token: 'sonnet',
      name: 'sonnet',
    });
    expect(m.rows[0].assignedOnMenu).toBe(false);
    expect(m.rows[0].assigned).toEqual({ token: 'sonnet', name: 'sonnet' });
  });

  it('resolves to none when the assigned provider is disabled (off the menu)', () => {
    const m = build(
      [assignment('rule-drafting', 'opus', false)],
      [tierGrant('gold', 'opus', true, false)],
      [],
    );
    expect(m.rows[0].effective.kind).toBe('none-assignment-off-menu');
    expect(m.rows[0].assignedOnMenu).toBe(false);
  });

  it('distinguishes a disabled tier default from no default at all', () => {
    const disabled = build([], [tierGrant('gold', 'opus', true, false)], []);
    expect(disabled.rows[0].effective).toEqual({
      kind: 'none-default-disabled',
      token: 'opus',
      name: 'opus',
    });

    const noDefault = build([], [tierGrant('gold', 'opus')], []);
    expect(noDefault.rows[0].effective).toEqual({ kind: 'none-no-default' });
  });

  it('resolves to none-no-default when the tier grants nothing', () => {
    const m = build([], [], []);
    expect(m.menu).toEqual([]);
    expect(m.tierDefault).toBeNull();
    expect(m.rows[0].effective).toEqual({ kind: 'none-no-default' });
  });

  it('surfaces the tier default as a fact even when a provider is disabled', () => {
    const m = build([], [tierGrant('gold', 'opus', true, false)], []);
    expect(m.tierDefault).toEqual({ token: 'opus', name: 'opus', enabled: false });
  });
});

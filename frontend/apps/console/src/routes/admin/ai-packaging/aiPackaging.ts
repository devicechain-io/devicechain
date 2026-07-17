// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure shape behind the AI packaging matrix (ADR-065 decision 10): fold the tier
// catalog (user-management/admin) and the tier→provider grants (ai-inference/admin) into
// one row-per-provider, two-columns-per-tier view, and say which tiers will resolve to no
// model.
//
// Kept out of the component so the rules are testable, the same split TierForm's
// tierConfig.ts uses.

import type { AiProviderTierGrant } from '@/lib/api/ai-inference-admin';

// The tier fields this screen needs. Structural rather than the generated
// AdminTenantTierDetail so the tests can build one without the whole catalog type.
export interface PackagingTierInput {
  token: string;
  name?: string | null;
  tenantCount: number;
}

// The provider fields this screen needs.
export interface PackagingProvider {
  token: string;
  name?: string | null;
  enabled: boolean;
}

export interface PackagingTier {
  token: string;
  name: string | null;
  // How many tenants are packaged here — how many this tier's column actually moves.
  // Always 0 for an unknown tier: no tenant can report a tier the catalog does not
  // have, which is precisely why such a grant is inert.
  tenantCount: number;
  // Whether the tier catalog has this token. A grant may name a tier that does not
  // exist (ai-inference cannot validate the token at write — see listAiProviderTierGrants),
  // and this screen is the only place that can show it.
  known: boolean;
  // Provider tokens this tier offers.
  granted: Set<string>;
  // The provider token this tier MARKED as its default, or null when it marked none.
  // Null is an answer an operator can choose, not a gap: at most one grant per tier
  // carries the mark (uix_ai_tier_grant_default), and nothing infers it.
  defaultProvider: string | null;
}

// buildPackagingTiers returns the catalog's tiers in catalog order, followed by any tier
// a grant names that the catalog does not have, sorted by token. Unknown tiers come last
// because they are cleanup, not packaging.
export function buildPackagingTiers(
  catalog: PackagingTierInput[],
  grants: AiProviderTierGrant[],
): PackagingTier[] {
  const byToken = new Map<string, PackagingTier>();

  for (const t of catalog) {
    byToken.set(t.token, {
      token: t.token,
      name: t.name ?? null,
      tenantCount: t.tenantCount,
      known: true,
      granted: new Set(),
      defaultProvider: null,
    });
  }

  for (const g of grants) {
    let tier = byToken.get(g.tier);
    if (!tier) {
      tier = {
        token: g.tier,
        name: null,
        tenantCount: 0,
        known: false,
        granted: new Set(),
        defaultProvider: null,
      };
      byToken.set(g.tier, tier);
    }
    tier.granted.add(g.provider.token);
    if (g.isDefault) tier.defaultProvider = g.provider.token;
  }

  const known = catalog.map((t) => byToken.get(t.token)!);
  const unknown = [...byToken.values()]
    .filter((t) => !t.known)
    .sort((a, b) => a.token.localeCompare(b.token));
  return [...known, ...unknown];
}

// A tier whose tenants will resolve to no model for any function they did not assign
// explicitly — the two ways that happens, told apart because the fix differs.
export type PackagingWarning =
  // The tier offers models but marks none of them default.
  | { kind: 'no-default' }
  // The tier's default is marked correctly but its provider is disabled, so the model is
  // off the menu. Nothing takes the call in its place.
  | { kind: 'default-disabled'; provider: string };

// tierWarning reports whether a tier's non-choosing tenants get no model.
//
// IT MIRRORS THE SERVER, IT DOES NOT DECIDE ANYTHING. model.Api.ResolveModelForFunction
// is the only thing that answers "which model serves this call", and this function
// re-states its two NONE outcomes so an operator can see them coming. The distinction
// matters if you are reading this next to the rule that a default may never be INFERRED
// from a set: that rule binds the server, whose answer is an entitlement. This is a
// console hint, and the worst a wrong hint can do is mislead — it grants nothing, marks
// nothing, and is never read back.
//
// It is still a hand-copied rule, which is the honest cost of there being no query that
// asks the server what a tier resolves to. If ResolveModelForFunction's NONE conditions
// change, this drifts and the console starts lying. Keep it to a re-statement of that
// function's two cases and nothing more; if it needs a third, the server grew a case and
// this is the reminder to look.
export function tierWarning(
  tier: PackagingTier,
  providers: PackagingProvider[],
): PackagingWarning | null {
  // An unknown tier strands nobody: no tenant can report a tier the catalog does not
  // have, so nothing ever resolves through these grants. They are stale config, which is
  // a real thing to tell the operator but not this sentence — the screen surfaces them as
  // unknown separately. Warning here would claim tenants are getting no model when there
  // are no tenants at all.
  if (!tier.known) return null;

  // A tier that offers nothing is AI turned off for its tenants, which is a package an
  // operator can sell — not a misconfiguration to warn about. (Reading `granted.size`
  // here is safe for the reason in the doc above: it picks which sentence to render, not
  // which model to serve.)
  if (tier.granted.size === 0) return null;

  if (tier.defaultProvider === null) return { kind: 'no-default' };

  const marked = providers.find((p) => p.token === tier.defaultProvider);
  // An unfound provider means the list was truncated, not that the mark is broken — the
  // FK (ON DELETE RESTRICT) makes a grant naming a deleted provider unreachable. Say
  // nothing rather than invent a fault.
  if (marked && !marked.enabled) {
    return { kind: 'default-disabled', provider: tier.defaultProvider };
  }
  return null;
}

// warningText renders a warning as the sentence the operator reads. Kept beside the
// warning type so a new kind cannot be added without its wording.
export function warningText(w: PackagingWarning, tier: PackagingTier): string {
  const who =
    tier.tenantCount === 0
      ? 'No tenants are packaged here yet, but once one is, it'
      : `${tier.tenantCount} tenant${tier.tenantCount === 1 ? '' : 's'} packaged here`;
  const verb = tier.tenantCount === 0 ? 'will get' : 'get';

  switch (w.kind) {
    case 'no-default':
      return `${who} ${verb} no model for any AI function they have not assigned themselves — this tier offers models but marks none of them its default.`;
    case 'default-disabled':
      return `${who} ${verb} no model for any AI function they have not assigned themselves — this tier's default (${w.provider}) is disabled, and nothing takes the call in its place.`;
  }
}

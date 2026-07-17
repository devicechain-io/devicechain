// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure shape behind the tenant "AI Models" tab (ADR-065 S5c′): fold a tenant's tier
// grants, its additive grants, its tier's marked default and its stored per-function
// choices into one row per AI function — the menu it may pick from, and what each function
// resolves to today.
//
// Kept out of the component so the rules are testable, the same split aiPackaging.ts uses.
//
// IT MIRRORS THE SERVER, IT DOES NOT DECIDE ANYTHING. model.Api.ResolveModelForFunction
// (ai-inference) is the only thing that answers "which model serves this call"; it refuses
// the admin plane's system context by design, so the console cannot ask it and re-states
// its outcome instead. This is a console hint: the worst a wrong hint can do is mislead —
// it grants nothing, assigns nothing, and is never read back as an entitlement. The
// server re-checks entitlement on every real call regardless of what this computes.
//
// The menu is the SAME union the server resolves (MenuForTenant: enabled providers granted
// to the tier ∪ enabled providers granted to the tenant), composed here from grant facts
// the operator already sees — not a new tenant-plane read. If ResolveModelForFunction's
// NONE conditions change, this drifts; keep it a re-statement of that function's cases and
// nothing more.

import type {
  AiFunction,
  AiFunctionAssignment,
  AiProviderTierGrant,
  AiProviderTenantGrant,
} from '@/lib/api/ai-inference-admin';

// One provider a tenant may assign — an entry on its menu.
export interface MenuModel {
  token: string;
  name: string | null;
}

// What a function resolves to for this tenant, mirroring ResolveModelForFunction. The
// three NONE variants are told apart because the operator's fix differs.
export type EffectiveModel =
  // The tenant assigned this model and it is on the menu — the call uses it.
  | { kind: 'assigned'; token: string; name: string | null }
  // The tenant assigned nothing; the tier's marked default is on the menu and takes it.
  | { kind: 'tier-default'; token: string; name: string | null }
  // The tenant assigned a model that is no longer on its menu (revoked or disabled). It
  // resolves to no model — never a substitute — until the entitlement returns.
  | { kind: 'none-assignment-off-menu'; token: string; name: string | null }
  // No assignment; the tier marks a default, but that default's provider is disabled, so
  // it is off the menu and nothing takes its place.
  | { kind: 'none-default-disabled'; token: string; name: string | null }
  // No assignment and the tier marks no usable default — the tenant must be assigned one.
  | { kind: 'none-no-default' };

export interface FunctionRow {
  token: string;
  name: string;
  description: string;
  // The provider the tenant has chosen for this function (its stored assignment), or null
  // when it has chosen none. Present even when off-menu, so a stale choice is visible.
  assigned: { token: string; name: string | null } | null;
  // Whether the assigned provider is on the tenant's current (enabled) menu. False both
  // when there is no assignment and when the assignment is stale.
  assignedOnMenu: boolean;
  effective: EffectiveModel;
}

export interface TenantAiModels {
  // The tenant's menu: enabled providers it may assign, deduped and sorted by token.
  menu: MenuModel[];
  // The tenant's tier's marked default, or null when the tier marks none. Carries its own
  // enabled flag (a disabled default is marked but off the menu).
  tierDefault: { token: string; name: string | null; enabled: boolean } | null;
  rows: FunctionRow[];
}

// buildTenantAiModels composes the tab's whole model from the four fetched facts plus the
// tenant's tier token. tierGrants is the instance-wide list; only this tenant's tier's
// grants participate.
export function buildTenantAiModels(
  functions: AiFunction[],
  assignments: AiFunctionAssignment[],
  tierGrants: AiProviderTierGrant[],
  tenantGrants: AiProviderTenantGrant[],
  tierToken: string,
): TenantAiModels {
  const myTierGrants = tierGrants.filter((g) => g.tier === tierToken);

  // The menu: enabled providers from the tier's grants ∪ the tenant's additive grants,
  // deduped by token. Enabled-only, matching the server — a disabled provider never serves
  // even where granted, so it must not be assignable here.
  const menuByToken = new Map<string, MenuModel>();
  for (const g of [...myTierGrants, ...tenantGrants]) {
    if (g.provider.enabled && !menuByToken.has(g.provider.token)) {
      menuByToken.set(g.provider.token, { token: g.provider.token, name: g.provider.name ?? null });
    }
  }
  const menu = [...menuByToken.values()].sort((a, b) => a.token.localeCompare(b.token));
  const onMenu = (token: string) => menuByToken.has(token);

  // The tier's marked default (at most one). Kept with its enabled flag rather than
  // filtered out when disabled: "the tier marks X but X is disabled" is a distinct,
  // actionable state from "the tier marks none".
  const defaultGrant = myTierGrants.find((g) => g.isDefault) ?? null;
  const tierDefault = defaultGrant
    ? {
        token: defaultGrant.provider.token,
        name: defaultGrant.provider.name ?? null,
        enabled: defaultGrant.provider.enabled,
      }
    : null;

  const rows: FunctionRow[] = functions.map((fn) => {
    const assignment = assignments.find((a) => a.function === fn.token) ?? null;
    const assigned = assignment
      ? { token: assignment.provider.token, name: assignment.provider.name ?? null }
      : null;
    const assignedOnMenu = assigned != null && onMenu(assigned.token);

    let effective: EffectiveModel;
    if (assigned) {
      // The tenant chose. Honour it iff still entitled; otherwise NONE — never the tier's
      // default, matching the server's "an assignment off the menu resolves to none, never
      // to a substitute".
      effective = assignedOnMenu
        ? { kind: 'assigned', token: assigned.token, name: assigned.name }
        : { kind: 'none-assignment-off-menu', token: assigned.token, name: assigned.name };
    } else if (tierDefault && tierDefault.enabled) {
      effective = { kind: 'tier-default', token: tierDefault.token, name: tierDefault.name };
    } else if (tierDefault) {
      effective = {
        kind: 'none-default-disabled',
        token: tierDefault.token,
        name: tierDefault.name,
      };
    } else {
      effective = { kind: 'none-no-default' };
    }

    return {
      token: fn.token,
      name: fn.name,
      description: fn.description,
      assigned,
      assignedOnMenu,
      effective,
    };
  });

  return { menu, tierDefault, rows };
}

// modelLabel renders a provider for display: its name when it has one, else its token,
// with the token as a muted suffix when both exist. Shared by the picker and the status
// lines so a model reads the same everywhere.
export function modelLabel(model: { token: string; name: string | null }): string {
  return model.name ? `${model.name} (${model.token})` : model.token;
}

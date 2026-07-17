// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the ai-inference ADMIN plane (ADR-056 §4,
// re-homed by ADR-065). A provider is an INSTANCE-scoped, operator-managed {kind,
// endpoint, model, params, write-only API key} config. Which tenants may USE a
// provider is a separate question answered by the tier↔provider grants (ADR-065
// decision 10) — registering a model and selling it are different acts. The API key
// is write-only: it goes in on create/update (`secret`) and never comes back out —
// the read side exposes only `hasSecret`.
//
// These calls ride the IDENTITY lane (`ai-inference/admin` → /admin/graphql), like
// user-management's admin API. That is the ADR-065 correction: the provider list is
// instance config an operator owns, but it used to be served on the tenant data
// plane and authorized on `ai:admin` alone — which the seeded `tenant-admin` role's
// "*" satisfied, so it was reachable from inside a tenant. The lane is derived from
// the area (see isIdentityArea), so no call here opts into it by hand.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/ai-inference-admin';
import type {
  AiProvidersQuery,
  AiProviderQuery,
  AiProviderTierGrantsQuery,
  InferenceRequest,
} from '@/gql/ai-inference-admin/graphql';

// Public types derive from the generated operation results so they can never drift
// from the schema.
export type AiProviderListItem = AiProvidersQuery['aiProviders']['results'][number];
export type AiProviderSearchResults = AiProvidersQuery['aiProviders'];
export type AiProvider = NonNullable<AiProviderQuery['aiProvider']>;
export type AiProviderTierGrant = AiProviderTierGrantsQuery['aiProviderTierGrants'][number];

// ── Queries ─────────────────────────────────────────────────────────────

// The list omits `params` and `endpoint` — the table only needs identity + status.
const AI_PROVIDERS = graphql(`
  query AiProviders($criteria: AiProviderSearchCriteria!) {
    aiProviders(criteria: $criteria) {
      results {
        token
        name
        kind
        model
        enabled
        hasSecret
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listAiProviders(opts: {
  pageNumber: number;
  pageSize: number;
  kind?: string;
}): Promise<AiProviderSearchResults> {
  const data = await gql('ai-inference/admin', AI_PROVIDERS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      kind: opts.kind ?? null,
    },
  });
  return data.aiProviders;
}

const AI_PROVIDER_BY_TOKEN = graphql(`
  query AiProvider($token: String!) {
    aiProvider(token: $token) {
      id
      token
      name
      description
      kind
      endpoint
      model
      params
      enabled
      hasSecret
      updatedAt
    }
  }
`);

export async function getAiProvider(token: string): Promise<AiProvider | null> {
  const data = await gql('ai-inference/admin', AI_PROVIDER_BY_TOKEN, { token });
  return data.aiProvider ?? null;
}

const AI_PROVIDER_KINDS = graphql(`
  query AiProviderKinds {
    aiProviderKinds
  }
`);

export async function listAiProviderKinds(): Promise<string[]> {
  const data = await gql('ai-inference/admin', AI_PROVIDER_KINDS);
  return data.aiProviderKinds;
}

// The packaging matrix (ADR-065 decision 10): every tier→provider offer on the
// instance, each carrying whether it is that tier's marked default.
//
// It includes grants naming a tier the catalog no longer has. That is deliberate on the
// server (ai-inference cannot validate a tier token — the catalog lives on
// user-management's identity-only admin plane and this service holds a service token),
// so an unknown grant is inert rather than rejected at write. Visibility is what
// replaces validation: the packaging screen renders those grants as unknown instead of
// filtering them away, because this query is the only door that can reveal one.
const AI_PROVIDER_TIER_GRANTS = graphql(`
  query AiProviderTierGrants {
    aiProviderTierGrants {
      tier
      isDefault
      provider {
        token
        name
        enabled
      }
    }
  }
`);

export async function listAiProviderTierGrants(): Promise<AiProviderTierGrant[]> {
  const data = await gql('ai-inference/admin', AI_PROVIDER_TIER_GRANTS);
  return data.aiProviderTierGrants;
}

// ── Mutations ───────────────────────────────────────────────────────────

const CREATE_AI_PROVIDER = graphql(`
  mutation CreateAiProvider($request: AiProviderCreateRequest!) {
    createAiProvider(request: $request) {
      token
    }
  }
`);

// A `secret` of undefined means "no key yet"; a non-empty string seals one.
export async function createAiProvider(opts: {
  token: string;
  name?: string;
  description?: string;
  kind: string;
  endpoint?: string;
  model: string;
  params?: string;
  enabled: boolean;
  secret?: string;
}): Promise<{ token: string }> {
  const data = await gql('ai-inference/admin', CREATE_AI_PROVIDER, {
    request: {
      token: opts.token,
      name: opts.name ?? null,
      description: opts.description ?? null,
      kind: opts.kind,
      endpoint: opts.endpoint ?? null,
      model: opts.model,
      params: opts.params ?? null,
      enabled: opts.enabled,
      secret: opts.secret ?? null,
    },
  });
  return data.createAiProvider;
}

// updateAiProvider is a full replacement of {name, description, kind, endpoint,
// model, params, enabled}. `secret` follows the store's write-only contract: omit
// (null) to PRESERVE the stored key, send a non-empty string to REPLACE it, or send
// an empty string to CLEAR it — so a caller not touching the key must pass null,
// never "". A provider's GRANTS are not changed here. expectedUpdatedAt
// is the optimistic-concurrency precondition: pass the updatedAt the editor loaded so
// a save fails (CONFLICT) if another writer changed the provider since.
const UPDATE_AI_PROVIDER = graphql(`
  mutation UpdateAiProvider(
    $token: String!
    $request: AiProviderCreateRequest!
    $expectedUpdatedAt: String
  ) {
    updateAiProvider(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {
      token
      updatedAt
    }
  }
`);

export async function updateAiProvider(
  token: string,
  input: {
    name?: string | null;
    description?: string | null;
    kind: string;
    endpoint?: string | null;
    model: string;
    params?: string | null;
    enabled: boolean;
    // null ⇒ preserve, "" ⇒ clear, value ⇒ replace.
    secret?: string | null;
    expectedUpdatedAt?: string | null;
  },
): Promise<{ token: string; updatedAt: string | null }> {
  const data = await gql('ai-inference/admin', UPDATE_AI_PROVIDER, {
    token,
    request: {
      token,
      name: input.name ?? null,
      description: input.description ?? null,
      kind: input.kind,
      endpoint: input.endpoint ?? null,
      model: input.model,
      params: input.params ?? null,
      enabled: input.enabled,
      secret: input.secret ?? null,
    },
    expectedUpdatedAt: input.expectedUpdatedAt ?? null,
  });
  return data.updateAiProvider;
}

const DELETE_AI_PROVIDER = graphql(`
  mutation DeleteAiProvider($token: String!) {
    deleteAiProvider(token: $token)
  }
`);

export async function deleteAiProvider(token: string): Promise<boolean> {
  const data = await gql('ai-inference/admin', DELETE_AI_PROVIDER, { token });
  return data.deleteAiProvider;
}

// ── Packaging: grants and tier defaults (ADR-065 decision 10) ───────────

// GRANTING AND DEFAULTING ARE SEPARATE CALLS, AND THIS FILE MUST KEEP THEM SEPARATE.
// grantAiProviderToTier carries no makeDefault argument by design: fusing the two made
// the default a property of the grant call, which is how a later grant could silently
// overturn an operator's explicit "no default" (the bug shipped five times — see
// model/grant.go). Do not add a convenience wrapper here that grants and marks in one
// function; the packaging screen issues both, visibly, from one operator gesture, which
// is where that convenience belongs.

const GRANT_AI_PROVIDER_TO_TIER = graphql(`
  mutation GrantAiProviderToTier($tier: String!, $provider: String!) {
    grantAiProviderToTier(tier: $tier, provider: $provider)
  }
`);

// Offers a provider to every tenant at the tier — an entitlement, and nothing about
// which model anything uses. Idempotent; never marks a default, not even for a tier's
// first grant.
export async function grantAiProviderToTier(tier: string, provider: string): Promise<boolean> {
  const data = await gql('ai-inference/admin', GRANT_AI_PROVIDER_TO_TIER, { tier, provider });
  return data.grantAiProviderToTier;
}

const REVOKE_AI_PROVIDER_FROM_TIER = graphql(`
  mutation RevokeAiProviderFromTier($tier: String!, $provider: String!) {
    revokeAiProviderFromTier(tier: $tier, provider: $provider)
  }
`);

// Withdraws a tier's offer. Returns whether a grant was removed. Revoking the tier's
// DEFAULT leaves the tier with none — nothing is promoted in its place, so every tenant
// at the tier that never chose a model resolves to no model. The caller confirms first.
export async function revokeAiProviderFromTier(tier: string, provider: string): Promise<boolean> {
  const data = await gql('ai-inference/admin', REVOKE_AI_PROVIDER_FROM_TIER, { tier, provider });
  return data.revokeAiProviderFromTier;
}

const SET_AI_TIER_DEFAULT = graphql(`
  mutation SetAiTierDefault($tier: String!, $provider: String!) {
    setAiTierDefault(tier: $tier, provider: $provider)
  }
`);

// Marks an ALREADY-GRANTED provider as the tier's default. Refused when the pair is not
// granted — it never creates the grant as a side effect, so the screen must grant first
// and mark second.
export async function setAiTierDefault(tier: string, provider: string): Promise<boolean> {
  const data = await gql('ai-inference/admin', SET_AI_TIER_DEFAULT, { tier, provider });
  return data.setAiTierDefault;
}

const CLEAR_AI_TIER_DEFAULT = graphql(`
  mutation ClearAiTierDefault($tier: String!) {
    clearAiTierDefault(tier: $tier)
  }
`);

// Marks none of the tier's grants default, leaving every grant in place. Idempotent.
// A legitimate package, not a broken state: afterwards the tier's tenants must each
// choose a model explicitly.
export async function clearAiTierDefault(tier: string): Promise<boolean> {
  const data = await gql('ai-inference/admin', CLEAR_AI_TIER_DEFAULT, { tier });
  return data.clearAiTierDefault;
}

// ── Operator smoke test (ai:admin) ──────────────────────────────────────

const TEST_AI_PROVIDER = graphql(`
  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {
    testAiProvider(token: $token, request: $request) {
      candidate
      model
      provider
    }
  }
`);

export type InferenceResult = { candidate: string; model: string; provider: string };

// testAiProvider validates a SPECIFIC provider's endpoint + key with an operator
// prompt, bypassing the tenant-consent gate (it is operator config, not a tenant's
// NL→rule request). Fails when the provider has no resolvable key or the endpoint
// rejects the call.
export async function testAiProvider(
  token: string,
  request: InferenceRequest,
): Promise<InferenceResult> {
  const data = await gql('ai-inference/admin', TEST_AI_PROVIDER, { token, request });
  return data.testAiProvider;
}

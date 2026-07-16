// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the ai-inference service (ADR-056). A provider
// is an INSTANCE-scoped, operator-managed {kind, endpoint, model, params, write-only
// API key} config; at most one is the ACTIVE provider used for NL→rule authoring
// ("default of none" when none is active). The API key is write-only: it goes in on
// create/update (`secret`) and never comes back out — the read side exposes only
// `hasSecret`. This service authenticates on the tenant access-token data plane and
// authorizes on `ai:admin` (an operator authority a normal tenant never holds), so
// these calls ride the DEFAULT lane — not the identity/admin lane.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/ai-inference';
import type {
  AiProvidersQuery,
  AiProviderQuery,
  ActiveAiProviderQuery,
  InferenceRequest,
} from '@/gql/ai-inference/graphql';

// Public types derive from the generated operation results so they can never drift
// from the schema.
export type AiProviderListItem = AiProvidersQuery['aiProviders']['results'][number];
export type AiProviderSearchResults = AiProvidersQuery['aiProviders'];
export type AiProvider = NonNullable<AiProviderQuery['aiProvider']>;
// The active-provider read is a deliberately narrow projection (just enough to name
// which model answers) — its own type, not the full provider.
export type ActiveAiProvider = NonNullable<ActiveAiProviderQuery['activeAiProvider']>;

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
        active
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
  const data = await gql('ai-inference', AI_PROVIDERS, {
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
      active
      hasSecret
      updatedAt
    }
  }
`);

export async function getAiProvider(token: string): Promise<AiProvider | null> {
  const data = await gql('ai-inference', AI_PROVIDER_BY_TOKEN, { token });
  return data.aiProvider ?? null;
}

const AI_PROVIDER_KINDS = graphql(`
  query AiProviderKinds {
    aiProviderKinds
  }
`);

export async function listAiProviderKinds(): Promise<string[]> {
  const data = await gql('ai-inference', AI_PROVIDER_KINDS);
  return data.aiProviderKinds;
}

const ACTIVE_AI_PROVIDER = graphql(`
  query ActiveAiProvider {
    activeAiProvider {
      token
      name
      kind
      model
      hasSecret
    }
  }
`);

// The active provider, or null when none is active ("default of none" — the NL→rule
// feature is simply off until an operator promotes one).
export async function getActiveAiProvider(): Promise<ActiveAiProvider | null> {
  const data = await gql('ai-inference', ACTIVE_AI_PROVIDER);
  return data.activeAiProvider ?? null;
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
  const data = await gql('ai-inference', CREATE_AI_PROVIDER, {
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
// never "". `active` is NOT changed here (use setActiveAiProvider). expectedUpdatedAt
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
  const data = await gql('ai-inference', UPDATE_AI_PROVIDER, {
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

// CONFLICT_MARKER matches the backend's optimistic-concurrency error text (the
// ai-inference model mirrors dashboard/connector ErrConflict).
export const CONFLICT_MARKER = 'modified by another writer';

const SET_ACTIVE_AI_PROVIDER = graphql(`
  mutation SetActiveAiProvider($token: String!) {
    setActiveAiProvider(token: $token) {
      token
      active
      updatedAt
    }
  }
`);

// Promote a provider to THE active one, clearing any previous. Returns the promoted
// provider's {token, active, updatedAt} — the fresh updatedAt matters because the
// promote touches the row, so a stale editor baseline would otherwise CONFLICT on the
// next save.
export async function setActiveAiProvider(
  token: string,
): Promise<{ token: string; active: boolean; updatedAt: string | null }> {
  const data = await gql('ai-inference', SET_ACTIVE_AI_PROVIDER, { token });
  return data.setActiveAiProvider;
}

const CLEAR_ACTIVE_AI_PROVIDER = graphql(`
  mutation ClearActiveAiProvider {
    clearActiveAiProvider
  }
`);

// Clear the active provider (return to "default of none"). Always returns true.
export async function clearActiveAiProvider(): Promise<boolean> {
  const data = await gql('ai-inference', CLEAR_ACTIVE_AI_PROVIDER);
  return data.clearActiveAiProvider;
}

const DELETE_AI_PROVIDER = graphql(`
  mutation DeleteAiProvider($token: String!) {
    deleteAiProvider(token: $token)
  }
`);

export async function deleteAiProvider(token: string): Promise<boolean> {
  const data = await gql('ai-inference', DELETE_AI_PROVIDER, { token });
  return data.deleteAiProvider;
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
  const data = await gql('ai-inference', TEST_AI_PROVIDER, { token, request });
  return data.testAiProvider;
}

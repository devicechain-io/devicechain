/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/**
 * Fields for a new provider. The kind must be one returned by aiProviderKinds. A new
 * provider is offered to no tenant until it is granted.
 */
export type AiProviderCreateRequest = {
  /** Free-text description of the provider. */
  description?: string | null | undefined;
  /** Whether the provider may be used. A disabled provider never serves a tenant call. */
  enabled: boolean;
  /**
   * Base URL of the provider's API: an absolute http or https URL with a host and no query
   * or fragment, to which the provider's API path is appended. Optional for anthropic,
   * where it overrides the built-in address; required for openai-compatible. A blank value
   * counts as omitted.
   */
  endpoint?: string | null | undefined;
  /** The provider kind: one of the values returned by aiProviderKinds. */
  kind: string;
  /** The model identifier to request from the provider. Required and must not be blank. */
  model: string;
  /** Human-readable name shown in provider lists. */
  name?: string | null | undefined;
  /** Free-form settings for the kind, as a JSON object serialized to a string of at most 16 KiB. Currently stored but not read by inference calls. A blank value counts as omitted. */
  params?: string | null | undefined;
  /** The provider's API key. Write-only; stored encrypted and never returned. Omit it, or send an empty string, to store no key yet; a provider with no key cannot serve calls. */
  secret?: string | null | undefined;
  /**
   * Unique identifier for the new provider. Letters, digits, hyphens and underscores,
   * starting with a letter or digit, at most 128 characters.
   */
  token: string;
};

/** Filter and paging for the aiProviders query. */
export type AiProviderSearchCriteria = {
  /** Return only providers of this kind. Omit it to return every kind. */
  kind?: string | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Providers per page. A value below 1 is treated as 100; a value above 1000 is capped at 1000. */
  pageSize: number;
};

/**
 * A partial update to a provider. Omit a field to leave the stored value alone, send a
 * value to set it, or send an explicit null to clear it (except where noted). The provider
 * is named by the mutation's token argument, so there is no token here; use renameAiProvider
 * to change the token. Grants are not affected.
 */
export type AiProviderUpdateRequest = {
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** Whether the provider may be used. Omit it to keep the stored value; an explicit null is refused. */
  enabled?: boolean | null | undefined;
  /**
   * New base URL, in the format described on AiProviderCreateRequest.endpoint, or null to
   * remove the override and use the kind's built-in address. Null is refused for
   * openai-compatible, which has no built-in address; a change of kind is checked against
   * the stored endpoint in the same way.
   */
  endpoint?: string | null | undefined;
  /** New provider kind. Omit it to keep the stored kind; an explicit null is refused. */
  kind?: string | null | undefined;
  /** New model identifier. Omit it to keep the stored one; an explicit null is refused. */
  model?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
  /** New free-form settings as a JSON object serialized to a string of at most 16 KiB, or null to clear them. */
  params?: string | null | undefined;
  /**
   * The write-only API key. Omit it to keep the stored key, send a value to replace it, or
   * send null or an empty string to delete it.
   */
  secret?: string | null | undefined;
};

/** A prompt to send to the model. The output-token limit, endpoint and timeout are fixed by the server. */
export type InferenceRequest = {
  /** The user prompt. Required, and must not be blank. The prompt and system prompt together may not exceed 128 KiB unless the operator configured a different limit. */
  prompt: string;
  /** Optional system prompt (instructions or persona) sent ahead of the prompt. */
  system?: string | null | undefined;
};

export type AiProvidersQueryVariables = Exact<{
  criteria: AiProviderSearchCriteria;
}>;


export type AiProvidersQuery = { aiProviders: { results: Array<{ token: string, name: string | null, kind: string, model: string, enabled: boolean, hasSecret: boolean }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AiProviderQueryVariables = Exact<{
  token: string;
}>;


export type AiProviderQuery = { aiProvider: { id: string, token: string, name: string | null, description: string | null, kind: string, endpoint: string | null, model: string, params: string | null, enabled: boolean, hasSecret: boolean, updatedAt: string | null } | null };

export type AiProviderKindsQueryVariables = Exact<{ [key: string]: never; }>;


export type AiProviderKindsQuery = { aiProviderKinds: Array<string> };

export type AiProviderTierGrantsQueryVariables = Exact<{ [key: string]: never; }>;


export type AiProviderTierGrantsQuery = { aiProviderTierGrants: Array<{ tier: string, isDefault: boolean, provider: { token: string, name: string | null, enabled: boolean } }> };

export type AiProviderTenantGrantsQueryVariables = Exact<{
  tenant: string;
}>;


export type AiProviderTenantGrantsQuery = { aiProviderTenantGrants: Array<{ tenant: string, provider: { token: string, name: string | null, enabled: boolean } }> };

export type AiFunctionsQueryVariables = Exact<{ [key: string]: never; }>;


export type AiFunctionsQuery = { aiFunctions: Array<{ token: string, name: string, description: string }> };

export type AiFunctionAssignmentsQueryVariables = Exact<{
  tenant: string;
}>;


export type AiFunctionAssignmentsQuery = { aiFunctionAssignments: Array<{ function: string, provider: { token: string, name: string | null, enabled: boolean } }> };

export type CreateAiProviderMutationVariables = Exact<{
  request: AiProviderCreateRequest;
}>;


export type CreateAiProviderMutation = { createAiProvider: { token: string } };

export type UpdateAiProviderMutationVariables = Exact<{
  token: string;
  request: AiProviderUpdateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateAiProviderMutation = { updateAiProvider: { token: string, updatedAt: string | null } };

export type RenameAiProviderMutationVariables = Exact<{
  token: string;
  newToken: string;
}>;


export type RenameAiProviderMutation = { renameAiProvider: { token: string, updatedAt: string | null } };

export type DeleteAiProviderMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAiProviderMutation = { deleteAiProvider: boolean };

export type GrantAiProviderToTierMutationVariables = Exact<{
  tier: string;
  provider: string;
}>;


export type GrantAiProviderToTierMutation = { grantAiProviderToTier: boolean };

export type RevokeAiProviderFromTierMutationVariables = Exact<{
  tier: string;
  provider: string;
}>;


export type RevokeAiProviderFromTierMutation = { revokeAiProviderFromTier: boolean };

export type SetAiTierDefaultMutationVariables = Exact<{
  tier: string;
  provider: string;
}>;


export type SetAiTierDefaultMutation = { setAiTierDefault: boolean };

export type ClearAiTierDefaultMutationVariables = Exact<{
  tier: string;
}>;


export type ClearAiTierDefaultMutation = { clearAiTierDefault: boolean };

export type SetAiFunctionModelMutationVariables = Exact<{
  tenant: string;
  function: string;
  provider: string;
}>;


export type SetAiFunctionModelMutation = { setAiFunctionModel: boolean };

export type ClearAiFunctionModelMutationVariables = Exact<{
  tenant: string;
  function: string;
}>;


export type ClearAiFunctionModelMutation = { clearAiFunctionModel: boolean };

export type TestAiProviderMutationVariables = Exact<{
  token: string;
  request: InferenceRequest;
}>;


export type TestAiProviderMutation = { testAiProvider: { candidate: string, model: string, provider: string } };

export class TypedDocumentString<TResult, TVariables>
  extends String
  implements DocumentTypeDecoration<TResult, TVariables>
{
  __apiType?: NonNullable<DocumentTypeDecoration<TResult, TVariables>['__apiType']>;
  private value: string;
  public __meta__?: Record<string, any> | undefined;

  constructor(value: string, __meta__?: Record<string, any> | undefined) {
    super(value);
    this.value = value;
    this.__meta__ = __meta__;
  }

  override toString(): string & DocumentTypeDecoration<TResult, TVariables> {
    return this.value;
  }
}

export const AiProvidersDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<AiProvidersQuery, AiProvidersQueryVariables>;
export const AiProviderDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<AiProviderQuery, AiProviderQueryVariables>;
export const AiProviderKindsDocument = new TypedDocumentString(`
    query AiProviderKinds {
  aiProviderKinds
}
    `) as unknown as TypedDocumentString<AiProviderKindsQuery, AiProviderKindsQueryVariables>;
export const AiProviderTierGrantsDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<AiProviderTierGrantsQuery, AiProviderTierGrantsQueryVariables>;
export const AiProviderTenantGrantsDocument = new TypedDocumentString(`
    query AiProviderTenantGrants($tenant: String!) {
  aiProviderTenantGrants(tenant: $tenant) {
    tenant
    provider {
      token
      name
      enabled
    }
  }
}
    `) as unknown as TypedDocumentString<AiProviderTenantGrantsQuery, AiProviderTenantGrantsQueryVariables>;
export const AiFunctionsDocument = new TypedDocumentString(`
    query AiFunctions {
  aiFunctions {
    token
    name
    description
  }
}
    `) as unknown as TypedDocumentString<AiFunctionsQuery, AiFunctionsQueryVariables>;
export const AiFunctionAssignmentsDocument = new TypedDocumentString(`
    query AiFunctionAssignments($tenant: String!) {
  aiFunctionAssignments(tenant: $tenant) {
    function
    provider {
      token
      name
      enabled
    }
  }
}
    `) as unknown as TypedDocumentString<AiFunctionAssignmentsQuery, AiFunctionAssignmentsQueryVariables>;
export const CreateAiProviderDocument = new TypedDocumentString(`
    mutation CreateAiProvider($request: AiProviderCreateRequest!) {
  createAiProvider(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateAiProviderMutation, CreateAiProviderMutationVariables>;
export const UpdateAiProviderDocument = new TypedDocumentString(`
    mutation UpdateAiProvider($token: String!, $request: AiProviderUpdateRequest!, $expectedUpdatedAt: String) {
  updateAiProvider(
    token: $token
    request: $request
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateAiProviderMutation, UpdateAiProviderMutationVariables>;
export const RenameAiProviderDocument = new TypedDocumentString(`
    mutation RenameAiProvider($token: String!, $newToken: String!) {
  renameAiProvider(token: $token, newToken: $newToken) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<RenameAiProviderMutation, RenameAiProviderMutationVariables>;
export const DeleteAiProviderDocument = new TypedDocumentString(`
    mutation DeleteAiProvider($token: String!) {
  deleteAiProvider(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAiProviderMutation, DeleteAiProviderMutationVariables>;
export const GrantAiProviderToTierDocument = new TypedDocumentString(`
    mutation GrantAiProviderToTier($tier: String!, $provider: String!) {
  grantAiProviderToTier(tier: $tier, provider: $provider)
}
    `) as unknown as TypedDocumentString<GrantAiProviderToTierMutation, GrantAiProviderToTierMutationVariables>;
export const RevokeAiProviderFromTierDocument = new TypedDocumentString(`
    mutation RevokeAiProviderFromTier($tier: String!, $provider: String!) {
  revokeAiProviderFromTier(tier: $tier, provider: $provider)
}
    `) as unknown as TypedDocumentString<RevokeAiProviderFromTierMutation, RevokeAiProviderFromTierMutationVariables>;
export const SetAiTierDefaultDocument = new TypedDocumentString(`
    mutation SetAiTierDefault($tier: String!, $provider: String!) {
  setAiTierDefault(tier: $tier, provider: $provider)
}
    `) as unknown as TypedDocumentString<SetAiTierDefaultMutation, SetAiTierDefaultMutationVariables>;
export const ClearAiTierDefaultDocument = new TypedDocumentString(`
    mutation ClearAiTierDefault($tier: String!) {
  clearAiTierDefault(tier: $tier)
}
    `) as unknown as TypedDocumentString<ClearAiTierDefaultMutation, ClearAiTierDefaultMutationVariables>;
export const SetAiFunctionModelDocument = new TypedDocumentString(`
    mutation SetAiFunctionModel($tenant: String!, $function: String!, $provider: String!) {
  setAiFunctionModel(tenant: $tenant, function: $function, provider: $provider)
}
    `) as unknown as TypedDocumentString<SetAiFunctionModelMutation, SetAiFunctionModelMutationVariables>;
export const ClearAiFunctionModelDocument = new TypedDocumentString(`
    mutation ClearAiFunctionModel($tenant: String!, $function: String!) {
  clearAiFunctionModel(tenant: $tenant, function: $function)
}
    `) as unknown as TypedDocumentString<ClearAiFunctionModelMutation, ClearAiFunctionModelMutationVariables>;
export const TestAiProviderDocument = new TypedDocumentString(`
    mutation TestAiProvider($token: String!, $request: InferenceRequest!) {
  testAiProvider(token: $token, request: $request) {
    candidate
    model
    provider
  }
}
    `) as unknown as TypedDocumentString<TestAiProviderMutation, TestAiProviderMutationVariables>;
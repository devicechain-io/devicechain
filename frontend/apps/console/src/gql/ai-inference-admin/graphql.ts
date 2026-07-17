/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type AiProviderCreateRequest = {
  description?: string | null | undefined;
  enabled: boolean;
  endpoint?: string | null | undefined;
  kind: string;
  model: string;
  name?: string | null | undefined;
  params?: string | null | undefined;
  secret?: string | null | undefined;
  token: string;
};

export type AiProviderSearchCriteria = {
  kind?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type InferenceRequest = {
  prompt: string;
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

export type CreateAiProviderMutationVariables = Exact<{
  request: AiProviderCreateRequest;
}>;


export type CreateAiProviderMutation = { createAiProvider: { token: string } };

export type UpdateAiProviderMutationVariables = Exact<{
  token: string;
  request: AiProviderCreateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateAiProviderMutation = { updateAiProvider: { token: string, updatedAt: string | null } };

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
export const CreateAiProviderDocument = new TypedDocumentString(`
    mutation CreateAiProvider($request: AiProviderCreateRequest!) {
  createAiProvider(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateAiProviderMutation, CreateAiProviderMutationVariables>;
export const UpdateAiProviderDocument = new TypedDocumentString(`
    mutation UpdateAiProvider($token: String!, $request: AiProviderCreateRequest!, $expectedUpdatedAt: String) {
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
export const TestAiProviderDocument = new TypedDocumentString(`
    mutation TestAiProvider($token: String!, $request: InferenceRequest!) {
  testAiProvider(token: $token, request: $request) {
    candidate
    model
    provider
  }
}
    `) as unknown as TypedDocumentString<TestAiProviderMutation, TestAiProviderMutationVariables>;
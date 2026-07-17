/* eslint-disable */
import * as types from './graphql';



/**
 * Map of all GraphQL operations in the project.
 *
 * This map has several performance disadvantages:
 * 1. It is not tree-shakeable, so it will include all operations in the project.
 * 2. It is not minifiable, so the string of a GraphQL query will be multiple times inside the bundle.
 * 3. It does not support dead code elimination, so it will add unused operations.
 *
 * Therefore it is highly recommended to use the babel or swc plugin for production.
 * Learn more about it here: https://the-guild.dev/graphql/codegen/plugins/presets/preset-client#reducing-bundle-size
 */
type Documents = {
    "\n  query AiProviders($criteria: AiProviderSearchCriteria!) {\n    aiProviders(criteria: $criteria) {\n      results {\n        token\n        name\n        kind\n        model\n        enabled\n        hasSecret\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AiProvidersDocument,
    "\n  query AiProvider($token: String!) {\n    aiProvider(token: $token) {\n      id\n      token\n      name\n      description\n      kind\n      endpoint\n      model\n      params\n      enabled\n      hasSecret\n      updatedAt\n    }\n  }\n": typeof types.AiProviderDocument,
    "\n  query AiProviderKinds {\n    aiProviderKinds\n  }\n": typeof types.AiProviderKindsDocument,
    "\n  query AiProviderTierGrants {\n    aiProviderTierGrants {\n      tier\n      isDefault\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n": typeof types.AiProviderTierGrantsDocument,
    "\n  query AiProviderTenantGrants($tenant: String!) {\n    aiProviderTenantGrants(tenant: $tenant) {\n      tenant\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n": typeof types.AiProviderTenantGrantsDocument,
    "\n  query AiFunctions {\n    aiFunctions {\n      token\n      name\n      description\n    }\n  }\n": typeof types.AiFunctionsDocument,
    "\n  query AiFunctionAssignments($tenant: String!) {\n    aiFunctionAssignments(tenant: $tenant) {\n      function\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n": typeof types.AiFunctionAssignmentsDocument,
    "\n  mutation CreateAiProvider($request: AiProviderCreateRequest!) {\n    createAiProvider(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateAiProviderDocument,
    "\n  mutation UpdateAiProvider(\n    $token: String!\n    $request: AiProviderCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateAiProvider(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": typeof types.UpdateAiProviderDocument,
    "\n  mutation DeleteAiProvider($token: String!) {\n    deleteAiProvider(token: $token)\n  }\n": typeof types.DeleteAiProviderDocument,
    "\n  mutation GrantAiProviderToTier($tier: String!, $provider: String!) {\n    grantAiProviderToTier(tier: $tier, provider: $provider)\n  }\n": typeof types.GrantAiProviderToTierDocument,
    "\n  mutation RevokeAiProviderFromTier($tier: String!, $provider: String!) {\n    revokeAiProviderFromTier(tier: $tier, provider: $provider)\n  }\n": typeof types.RevokeAiProviderFromTierDocument,
    "\n  mutation SetAiTierDefault($tier: String!, $provider: String!) {\n    setAiTierDefault(tier: $tier, provider: $provider)\n  }\n": typeof types.SetAiTierDefaultDocument,
    "\n  mutation ClearAiTierDefault($tier: String!) {\n    clearAiTierDefault(tier: $tier)\n  }\n": typeof types.ClearAiTierDefaultDocument,
    "\n  mutation SetAiFunctionModel($tenant: String!, $function: String!, $provider: String!) {\n    setAiFunctionModel(tenant: $tenant, function: $function, provider: $provider)\n  }\n": typeof types.SetAiFunctionModelDocument,
    "\n  mutation ClearAiFunctionModel($tenant: String!, $function: String!) {\n    clearAiFunctionModel(tenant: $tenant, function: $function)\n  }\n": typeof types.ClearAiFunctionModelDocument,
    "\n  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {\n    testAiProvider(token: $token, request: $request) {\n      candidate\n      model\n      provider\n    }\n  }\n": typeof types.TestAiProviderDocument,
};
const documents: Documents = {
    "\n  query AiProviders($criteria: AiProviderSearchCriteria!) {\n    aiProviders(criteria: $criteria) {\n      results {\n        token\n        name\n        kind\n        model\n        enabled\n        hasSecret\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AiProvidersDocument,
    "\n  query AiProvider($token: String!) {\n    aiProvider(token: $token) {\n      id\n      token\n      name\n      description\n      kind\n      endpoint\n      model\n      params\n      enabled\n      hasSecret\n      updatedAt\n    }\n  }\n": types.AiProviderDocument,
    "\n  query AiProviderKinds {\n    aiProviderKinds\n  }\n": types.AiProviderKindsDocument,
    "\n  query AiProviderTierGrants {\n    aiProviderTierGrants {\n      tier\n      isDefault\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n": types.AiProviderTierGrantsDocument,
    "\n  query AiProviderTenantGrants($tenant: String!) {\n    aiProviderTenantGrants(tenant: $tenant) {\n      tenant\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n": types.AiProviderTenantGrantsDocument,
    "\n  query AiFunctions {\n    aiFunctions {\n      token\n      name\n      description\n    }\n  }\n": types.AiFunctionsDocument,
    "\n  query AiFunctionAssignments($tenant: String!) {\n    aiFunctionAssignments(tenant: $tenant) {\n      function\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n": types.AiFunctionAssignmentsDocument,
    "\n  mutation CreateAiProvider($request: AiProviderCreateRequest!) {\n    createAiProvider(request: $request) {\n      token\n    }\n  }\n": types.CreateAiProviderDocument,
    "\n  mutation UpdateAiProvider(\n    $token: String!\n    $request: AiProviderCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateAiProvider(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": types.UpdateAiProviderDocument,
    "\n  mutation DeleteAiProvider($token: String!) {\n    deleteAiProvider(token: $token)\n  }\n": types.DeleteAiProviderDocument,
    "\n  mutation GrantAiProviderToTier($tier: String!, $provider: String!) {\n    grantAiProviderToTier(tier: $tier, provider: $provider)\n  }\n": types.GrantAiProviderToTierDocument,
    "\n  mutation RevokeAiProviderFromTier($tier: String!, $provider: String!) {\n    revokeAiProviderFromTier(tier: $tier, provider: $provider)\n  }\n": types.RevokeAiProviderFromTierDocument,
    "\n  mutation SetAiTierDefault($tier: String!, $provider: String!) {\n    setAiTierDefault(tier: $tier, provider: $provider)\n  }\n": types.SetAiTierDefaultDocument,
    "\n  mutation ClearAiTierDefault($tier: String!) {\n    clearAiTierDefault(tier: $tier)\n  }\n": types.ClearAiTierDefaultDocument,
    "\n  mutation SetAiFunctionModel($tenant: String!, $function: String!, $provider: String!) {\n    setAiFunctionModel(tenant: $tenant, function: $function, provider: $provider)\n  }\n": types.SetAiFunctionModelDocument,
    "\n  mutation ClearAiFunctionModel($tenant: String!, $function: String!) {\n    clearAiFunctionModel(tenant: $tenant, function: $function)\n  }\n": types.ClearAiFunctionModelDocument,
    "\n  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {\n    testAiProvider(token: $token, request: $request) {\n      candidate\n      model\n      provider\n    }\n  }\n": types.TestAiProviderDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProviders($criteria: AiProviderSearchCriteria!) {\n    aiProviders(criteria: $criteria) {\n      results {\n        token\n        name\n        kind\n        model\n        enabled\n        hasSecret\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AiProvidersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProvider($token: String!) {\n    aiProvider(token: $token) {\n      id\n      token\n      name\n      description\n      kind\n      endpoint\n      model\n      params\n      enabled\n      hasSecret\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').AiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProviderKinds {\n    aiProviderKinds\n  }\n"): typeof import('./graphql').AiProviderKindsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProviderTierGrants {\n    aiProviderTierGrants {\n      tier\n      isDefault\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n"): typeof import('./graphql').AiProviderTierGrantsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProviderTenantGrants($tenant: String!) {\n    aiProviderTenantGrants(tenant: $tenant) {\n      tenant\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n"): typeof import('./graphql').AiProviderTenantGrantsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiFunctions {\n    aiFunctions {\n      token\n      name\n      description\n    }\n  }\n"): typeof import('./graphql').AiFunctionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiFunctionAssignments($tenant: String!) {\n    aiFunctionAssignments(tenant: $tenant) {\n      function\n      provider {\n        token\n        name\n        enabled\n      }\n    }\n  }\n"): typeof import('./graphql').AiFunctionAssignmentsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateAiProvider($request: AiProviderCreateRequest!) {\n    createAiProvider(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateAiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateAiProvider(\n    $token: String!\n    $request: AiProviderCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateAiProvider(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateAiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAiProvider($token: String!) {\n    deleteAiProvider(token: $token)\n  }\n"): typeof import('./graphql').DeleteAiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation GrantAiProviderToTier($tier: String!, $provider: String!) {\n    grantAiProviderToTier(tier: $tier, provider: $provider)\n  }\n"): typeof import('./graphql').GrantAiProviderToTierDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RevokeAiProviderFromTier($tier: String!, $provider: String!) {\n    revokeAiProviderFromTier(tier: $tier, provider: $provider)\n  }\n"): typeof import('./graphql').RevokeAiProviderFromTierDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetAiTierDefault($tier: String!, $provider: String!) {\n    setAiTierDefault(tier: $tier, provider: $provider)\n  }\n"): typeof import('./graphql').SetAiTierDefaultDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ClearAiTierDefault($tier: String!) {\n    clearAiTierDefault(tier: $tier)\n  }\n"): typeof import('./graphql').ClearAiTierDefaultDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetAiFunctionModel($tenant: String!, $function: String!, $provider: String!) {\n    setAiFunctionModel(tenant: $tenant, function: $function, provider: $provider)\n  }\n"): typeof import('./graphql').SetAiFunctionModelDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ClearAiFunctionModel($tenant: String!, $function: String!) {\n    clearAiFunctionModel(tenant: $tenant, function: $function)\n  }\n"): typeof import('./graphql').ClearAiFunctionModelDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {\n    testAiProvider(token: $token, request: $request) {\n      candidate\n      model\n      provider\n    }\n  }\n"): typeof import('./graphql').TestAiProviderDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

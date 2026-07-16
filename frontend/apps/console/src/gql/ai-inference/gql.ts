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
    "\n  query AiProviders($criteria: AiProviderSearchCriteria!) {\n    aiProviders(criteria: $criteria) {\n      results {\n        token\n        name\n        kind\n        model\n        enabled\n        active\n        hasSecret\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AiProvidersDocument,
    "\n  query AiProvider($token: String!) {\n    aiProvider(token: $token) {\n      id\n      token\n      name\n      description\n      kind\n      endpoint\n      model\n      params\n      enabled\n      active\n      hasSecret\n      updatedAt\n    }\n  }\n": typeof types.AiProviderDocument,
    "\n  query AiProviderKinds {\n    aiProviderKinds\n  }\n": typeof types.AiProviderKindsDocument,
    "\n  query ActiveAiProvider {\n    activeAiProvider {\n      token\n      name\n      kind\n      model\n      hasSecret\n    }\n  }\n": typeof types.ActiveAiProviderDocument,
    "\n  mutation CreateAiProvider($request: AiProviderCreateRequest!) {\n    createAiProvider(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateAiProviderDocument,
    "\n  mutation UpdateAiProvider(\n    $token: String!\n    $request: AiProviderCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateAiProvider(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": typeof types.UpdateAiProviderDocument,
    "\n  mutation SetActiveAiProvider($token: String!) {\n    setActiveAiProvider(token: $token) {\n      token\n      active\n    }\n  }\n": typeof types.SetActiveAiProviderDocument,
    "\n  mutation ClearActiveAiProvider {\n    clearActiveAiProvider\n  }\n": typeof types.ClearActiveAiProviderDocument,
    "\n  mutation DeleteAiProvider($token: String!) {\n    deleteAiProvider(token: $token)\n  }\n": typeof types.DeleteAiProviderDocument,
    "\n  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {\n    testAiProvider(token: $token, request: $request) {\n      candidate\n      model\n      provider\n    }\n  }\n": typeof types.TestAiProviderDocument,
};
const documents: Documents = {
    "\n  query AiProviders($criteria: AiProviderSearchCriteria!) {\n    aiProviders(criteria: $criteria) {\n      results {\n        token\n        name\n        kind\n        model\n        enabled\n        active\n        hasSecret\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AiProvidersDocument,
    "\n  query AiProvider($token: String!) {\n    aiProvider(token: $token) {\n      id\n      token\n      name\n      description\n      kind\n      endpoint\n      model\n      params\n      enabled\n      active\n      hasSecret\n      updatedAt\n    }\n  }\n": types.AiProviderDocument,
    "\n  query AiProviderKinds {\n    aiProviderKinds\n  }\n": types.AiProviderKindsDocument,
    "\n  query ActiveAiProvider {\n    activeAiProvider {\n      token\n      name\n      kind\n      model\n      hasSecret\n    }\n  }\n": types.ActiveAiProviderDocument,
    "\n  mutation CreateAiProvider($request: AiProviderCreateRequest!) {\n    createAiProvider(request: $request) {\n      token\n    }\n  }\n": types.CreateAiProviderDocument,
    "\n  mutation UpdateAiProvider(\n    $token: String!\n    $request: AiProviderCreateRequest!\n    $expectedUpdatedAt: String\n  ) {\n    updateAiProvider(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {\n      token\n      updatedAt\n    }\n  }\n": types.UpdateAiProviderDocument,
    "\n  mutation SetActiveAiProvider($token: String!) {\n    setActiveAiProvider(token: $token) {\n      token\n      active\n    }\n  }\n": types.SetActiveAiProviderDocument,
    "\n  mutation ClearActiveAiProvider {\n    clearActiveAiProvider\n  }\n": types.ClearActiveAiProviderDocument,
    "\n  mutation DeleteAiProvider($token: String!) {\n    deleteAiProvider(token: $token)\n  }\n": types.DeleteAiProviderDocument,
    "\n  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {\n    testAiProvider(token: $token, request: $request) {\n      candidate\n      model\n      provider\n    }\n  }\n": types.TestAiProviderDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProviders($criteria: AiProviderSearchCriteria!) {\n    aiProviders(criteria: $criteria) {\n      results {\n        token\n        name\n        kind\n        model\n        enabled\n        active\n        hasSecret\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AiProvidersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProvider($token: String!) {\n    aiProvider(token: $token) {\n      id\n      token\n      name\n      description\n      kind\n      endpoint\n      model\n      params\n      enabled\n      active\n      hasSecret\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').AiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AiProviderKinds {\n    aiProviderKinds\n  }\n"): typeof import('./graphql').AiProviderKindsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query ActiveAiProvider {\n    activeAiProvider {\n      token\n      name\n      kind\n      model\n      hasSecret\n    }\n  }\n"): typeof import('./graphql').ActiveAiProviderDocument;
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
export function graphql(source: "\n  mutation SetActiveAiProvider($token: String!) {\n    setActiveAiProvider(token: $token) {\n      token\n      active\n    }\n  }\n"): typeof import('./graphql').SetActiveAiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ClearActiveAiProvider {\n    clearActiveAiProvider\n  }\n"): typeof import('./graphql').ClearActiveAiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAiProvider($token: String!) {\n    deleteAiProvider(token: $token)\n  }\n"): typeof import('./graphql').DeleteAiProviderDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation TestAiProvider($token: String!, $request: InferenceRequest!) {\n    testAiProvider(token: $token, request: $request) {\n      candidate\n      model\n      provider\n    }\n  }\n"): typeof import('./graphql').TestAiProviderDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

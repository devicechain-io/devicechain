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
    "\n  query Commands($criteria: CommandSearchCriteria!) {\n    commands(criteria: $criteria) {\n      results {\n        id\n        token\n        deviceToken\n        name\n        payload\n        status\n        queuedTime\n        sentTime\n        respondedTime\n        expiresAt\n        responsePayload\n        error\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.CommandsDocument,
    "\n  mutation CreateCommand($request: CommandCreateRequest!) {\n    createCommand(request: $request) {\n      command {\n        id\n        token\n        status\n      }\n      rejection {\n        code\n        reason\n      }\n    }\n  }\n": typeof types.CreateCommandDocument,
    "\n  query CommandBatches($criteria: CommandBatchSearchCriteria!) {\n    commandBatches(criteria: $criteria) {\n      results {\n        id\n        token\n        createdAt\n        name\n        targetKind\n        groupToken\n        groupVersion\n        allowPartial\n        resolved\n        accepted\n        cancelledAt\n        cancelledCount\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.CommandBatchesDocument,
    "\n  query CommandBatchesByToken($tokens: [String!]!) {\n    commandBatchesByToken(tokens: $tokens) {\n      id\n      token\n      createdAt\n      name\n      payload\n      targetKind\n      groupToken\n      groupVersion\n      allowPartial\n      resolved\n      accepted\n      refusals {\n        deviceToken\n        code\n        reason\n      }\n      refusalCounts {\n        code\n        count\n      }\n      cancelledAt\n      cancelledCount\n    }\n  }\n": typeof types.CommandBatchesByTokenDocument,
    "\n  mutation CancelCommand($token: String!) {\n    cancelCommand(token: $token) {\n      id\n      token\n      status\n    }\n  }\n": typeof types.CancelCommandDocument,
};
const documents: Documents = {
    "\n  query Commands($criteria: CommandSearchCriteria!) {\n    commands(criteria: $criteria) {\n      results {\n        id\n        token\n        deviceToken\n        name\n        payload\n        status\n        queuedTime\n        sentTime\n        respondedTime\n        expiresAt\n        responsePayload\n        error\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.CommandsDocument,
    "\n  mutation CreateCommand($request: CommandCreateRequest!) {\n    createCommand(request: $request) {\n      command {\n        id\n        token\n        status\n      }\n      rejection {\n        code\n        reason\n      }\n    }\n  }\n": types.CreateCommandDocument,
    "\n  query CommandBatches($criteria: CommandBatchSearchCriteria!) {\n    commandBatches(criteria: $criteria) {\n      results {\n        id\n        token\n        createdAt\n        name\n        targetKind\n        groupToken\n        groupVersion\n        allowPartial\n        resolved\n        accepted\n        cancelledAt\n        cancelledCount\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.CommandBatchesDocument,
    "\n  query CommandBatchesByToken($tokens: [String!]!) {\n    commandBatchesByToken(tokens: $tokens) {\n      id\n      token\n      createdAt\n      name\n      payload\n      targetKind\n      groupToken\n      groupVersion\n      allowPartial\n      resolved\n      accepted\n      refusals {\n        deviceToken\n        code\n        reason\n      }\n      refusalCounts {\n        code\n        count\n      }\n      cancelledAt\n      cancelledCount\n    }\n  }\n": types.CommandBatchesByTokenDocument,
    "\n  mutation CancelCommand($token: String!) {\n    cancelCommand(token: $token) {\n      id\n      token\n      status\n    }\n  }\n": types.CancelCommandDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Commands($criteria: CommandSearchCriteria!) {\n    commands(criteria: $criteria) {\n      results {\n        id\n        token\n        deviceToken\n        name\n        payload\n        status\n        queuedTime\n        sentTime\n        respondedTime\n        expiresAt\n        responsePayload\n        error\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').CommandsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateCommand($request: CommandCreateRequest!) {\n    createCommand(request: $request) {\n      command {\n        id\n        token\n        status\n      }\n      rejection {\n        code\n        reason\n      }\n    }\n  }\n"): typeof import('./graphql').CreateCommandDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CommandBatches($criteria: CommandBatchSearchCriteria!) {\n    commandBatches(criteria: $criteria) {\n      results {\n        id\n        token\n        createdAt\n        name\n        targetKind\n        groupToken\n        groupVersion\n        allowPartial\n        resolved\n        accepted\n        cancelledAt\n        cancelledCount\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').CommandBatchesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CommandBatchesByToken($tokens: [String!]!) {\n    commandBatchesByToken(tokens: $tokens) {\n      id\n      token\n      createdAt\n      name\n      payload\n      targetKind\n      groupToken\n      groupVersion\n      allowPartial\n      resolved\n      accepted\n      refusals {\n        deviceToken\n        code\n        reason\n      }\n      refusalCounts {\n        code\n        count\n      }\n      cancelledAt\n      cancelledCount\n    }\n  }\n"): typeof import('./graphql').CommandBatchesByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CancelCommand($token: String!) {\n    cancelCommand(token: $token) {\n      id\n      token\n      status\n    }\n  }\n"): typeof import('./graphql').CancelCommandDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

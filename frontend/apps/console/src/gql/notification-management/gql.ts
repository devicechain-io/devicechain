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
    "\n  query NotificationPolicies($criteria: NotificationPolicySearchCriteria!) {\n    notificationPolicies(criteria: $criteria) {\n      results {\n        token\n        name\n        enabled\n        deviceTypeToken\n        rules {\n          severity\n          recipients\n          channel {\n            token\n            enabled\n            channelType\n          }\n        }\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": typeof types.NotificationPoliciesDocument,
    "\n  query NotificationChannels($criteria: NotificationChannelSearchCriteria!) {\n    notificationChannels(criteria: $criteria) {\n      results {\n        token\n        name\n        channelType\n        enabled\n      }\n    }\n  }\n": typeof types.NotificationChannelsDocument,
    "\n  mutation CreateNotificationPolicy($request: NotificationPolicyCreateRequest!) {\n    createNotificationPolicy(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateNotificationPolicyDocument,
};
const documents: Documents = {
    "\n  query NotificationPolicies($criteria: NotificationPolicySearchCriteria!) {\n    notificationPolicies(criteria: $criteria) {\n      results {\n        token\n        name\n        enabled\n        deviceTypeToken\n        rules {\n          severity\n          recipients\n          channel {\n            token\n            enabled\n            channelType\n          }\n        }\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": types.NotificationPoliciesDocument,
    "\n  query NotificationChannels($criteria: NotificationChannelSearchCriteria!) {\n    notificationChannels(criteria: $criteria) {\n      results {\n        token\n        name\n        channelType\n        enabled\n      }\n    }\n  }\n": types.NotificationChannelsDocument,
    "\n  mutation CreateNotificationPolicy($request: NotificationPolicyCreateRequest!) {\n    createNotificationPolicy(request: $request) {\n      token\n    }\n  }\n": types.CreateNotificationPolicyDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query NotificationPolicies($criteria: NotificationPolicySearchCriteria!) {\n    notificationPolicies(criteria: $criteria) {\n      results {\n        token\n        name\n        enabled\n        deviceTypeToken\n        rules {\n          severity\n          recipients\n          channel {\n            token\n            enabled\n            channelType\n          }\n        }\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').NotificationPoliciesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query NotificationChannels($criteria: NotificationChannelSearchCriteria!) {\n    notificationChannels(criteria: $criteria) {\n      results {\n        token\n        name\n        channelType\n        enabled\n      }\n    }\n  }\n"): typeof import('./graphql').NotificationChannelsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateNotificationPolicy($request: NotificationPolicyCreateRequest!) {\n    createNotificationPolicy(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateNotificationPolicyDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

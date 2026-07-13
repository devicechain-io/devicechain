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
    "\n  query ValidateDetectionRules($rules: [DetectionRuleInput!]!) {\n    validateDetectionRules(rules: $rules) {\n      valid\n      errors {\n        index\n        token\n        message\n      }\n    }\n  }\n": typeof types.ValidateDetectionRulesDocument,
    "\n  query CompileCanvas($graph: String!, $profileToken: String!) {\n    compileCanvas(graph: $graph, profileToken: $profileToken) {\n      ok\n      definition\n      estimatedCost\n      diagnostics {\n        nodeId\n        severity\n        message\n      }\n    }\n  }\n": typeof types.CompileCanvasDocument,
    "\n  query PreviewRule($input: PreviewRuleInput!) {\n    previewRule(input: $input) {\n      ok\n      firings {\n        occurredAt\n        series\n        signal\n        trace {\n          nodeId\n          kind\n          disposition\n          detail\n        }\n      }\n      stats {\n        eventsScanned\n        firingCount\n        evalErrors\n        wallMs\n      }\n      degraded\n      diagnostics {\n        nodeId\n        severity\n        message\n      }\n    }\n  }\n": typeof types.PreviewRuleDocument,
    "\n  query RuleHealth($profileToken: String!) {\n    ruleHealth(profileToken: $profileToken) {\n      ruleId\n      ruleToken\n      name\n      status\n      lastFiredAt\n      fireCount\n      lastSignal\n      message\n    }\n  }\n": typeof types.RuleHealthDocument,
    "\n  subscription DetectionStream($profileToken: String!) {\n    detectionStream(profileToken: $profileToken) {\n      ruleId\n      ruleToken\n      kind\n      edge\n      series\n      occurredTime\n      severity\n      value\n    }\n  }\n": typeof types.DetectionStreamDocument,
};
const documents: Documents = {
    "\n  query ValidateDetectionRules($rules: [DetectionRuleInput!]!) {\n    validateDetectionRules(rules: $rules) {\n      valid\n      errors {\n        index\n        token\n        message\n      }\n    }\n  }\n": types.ValidateDetectionRulesDocument,
    "\n  query CompileCanvas($graph: String!, $profileToken: String!) {\n    compileCanvas(graph: $graph, profileToken: $profileToken) {\n      ok\n      definition\n      estimatedCost\n      diagnostics {\n        nodeId\n        severity\n        message\n      }\n    }\n  }\n": types.CompileCanvasDocument,
    "\n  query PreviewRule($input: PreviewRuleInput!) {\n    previewRule(input: $input) {\n      ok\n      firings {\n        occurredAt\n        series\n        signal\n        trace {\n          nodeId\n          kind\n          disposition\n          detail\n        }\n      }\n      stats {\n        eventsScanned\n        firingCount\n        evalErrors\n        wallMs\n      }\n      degraded\n      diagnostics {\n        nodeId\n        severity\n        message\n      }\n    }\n  }\n": types.PreviewRuleDocument,
    "\n  query RuleHealth($profileToken: String!) {\n    ruleHealth(profileToken: $profileToken) {\n      ruleId\n      ruleToken\n      name\n      status\n      lastFiredAt\n      fireCount\n      lastSignal\n      message\n    }\n  }\n": types.RuleHealthDocument,
    "\n  subscription DetectionStream($profileToken: String!) {\n    detectionStream(profileToken: $profileToken) {\n      ruleId\n      ruleToken\n      kind\n      edge\n      series\n      occurredTime\n      severity\n      value\n    }\n  }\n": types.DetectionStreamDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query ValidateDetectionRules($rules: [DetectionRuleInput!]!) {\n    validateDetectionRules(rules: $rules) {\n      valid\n      errors {\n        index\n        token\n        message\n      }\n    }\n  }\n"): typeof import('./graphql').ValidateDetectionRulesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CompileCanvas($graph: String!, $profileToken: String!) {\n    compileCanvas(graph: $graph, profileToken: $profileToken) {\n      ok\n      definition\n      estimatedCost\n      diagnostics {\n        nodeId\n        severity\n        message\n      }\n    }\n  }\n"): typeof import('./graphql').CompileCanvasDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query PreviewRule($input: PreviewRuleInput!) {\n    previewRule(input: $input) {\n      ok\n      firings {\n        occurredAt\n        series\n        signal\n        trace {\n          nodeId\n          kind\n          disposition\n          detail\n        }\n      }\n      stats {\n        eventsScanned\n        firingCount\n        evalErrors\n        wallMs\n      }\n      degraded\n      diagnostics {\n        nodeId\n        severity\n        message\n      }\n    }\n  }\n"): typeof import('./graphql').PreviewRuleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query RuleHealth($profileToken: String!) {\n    ruleHealth(profileToken: $profileToken) {\n      ruleId\n      ruleToken\n      name\n      status\n      lastFiredAt\n      fireCount\n      lastSignal\n      message\n    }\n  }\n"): typeof import('./graphql').RuleHealthDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  subscription DetectionStream($profileToken: String!) {\n    detectionStream(profileToken: $profileToken) {\n      ruleId\n      ruleToken\n      kind\n      edge\n      series\n      occurredTime\n      severity\n      value\n    }\n  }\n"): typeof import('./graphql').DetectionStreamDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

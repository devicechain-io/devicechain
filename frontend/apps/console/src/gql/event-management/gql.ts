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
    "\n  query Events($criteria: EventSearchCriteria!) {\n    events(criteria: $criteria) {\n      results {\n        id\n        deviceToken\n        eventType\n        occurredTime\n        source\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": typeof types.EventsDocument,
    "\n  query CalculatedEvents(\n    $deviceToken: String!\n    $expression: String!\n    $dataType: String!\n    $inputs: [CalculatedInput!]!\n    $minValue: Float\n    $maxValue: Float\n    $constants: [CalculatedConstant!]\n    $from: String!\n    $to: String!\n  ) {\n    calculatedEvents(\n      deviceToken: $deviceToken\n      expression: $expression\n      dataType: $dataType\n      inputs: $inputs\n      minValue: $minValue\n      maxValue: $maxValue\n      constants: $constants\n      from: $from\n      to: $to\n    ) {\n      samples {\n        occurredTime\n        eventId\n        inputs {\n          key\n          value\n        }\n        value\n        skippedReason\n      }\n      computed\n      skipped {\n        reason\n        count\n      }\n      inputDecimalPlaces\n    }\n  }\n": typeof types.CalculatedEventsDocument,
};
const documents: Documents = {
    "\n  query Events($criteria: EventSearchCriteria!) {\n    events(criteria: $criteria) {\n      results {\n        id\n        deviceToken\n        eventType\n        occurredTime\n        source\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": types.EventsDocument,
    "\n  query CalculatedEvents(\n    $deviceToken: String!\n    $expression: String!\n    $dataType: String!\n    $inputs: [CalculatedInput!]!\n    $minValue: Float\n    $maxValue: Float\n    $constants: [CalculatedConstant!]\n    $from: String!\n    $to: String!\n  ) {\n    calculatedEvents(\n      deviceToken: $deviceToken\n      expression: $expression\n      dataType: $dataType\n      inputs: $inputs\n      minValue: $minValue\n      maxValue: $maxValue\n      constants: $constants\n      from: $from\n      to: $to\n    ) {\n      samples {\n        occurredTime\n        eventId\n        inputs {\n          key\n          value\n        }\n        value\n        skippedReason\n      }\n      computed\n      skipped {\n        reason\n        count\n      }\n      inputDecimalPlaces\n    }\n  }\n": types.CalculatedEventsDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Events($criteria: EventSearchCriteria!) {\n    events(criteria: $criteria) {\n      results {\n        id\n        deviceToken\n        eventType\n        occurredTime\n        source\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').EventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CalculatedEvents(\n    $deviceToken: String!\n    $expression: String!\n    $dataType: String!\n    $inputs: [CalculatedInput!]!\n    $minValue: Float\n    $maxValue: Float\n    $constants: [CalculatedConstant!]\n    $from: String!\n    $to: String!\n  ) {\n    calculatedEvents(\n      deviceToken: $deviceToken\n      expression: $expression\n      dataType: $dataType\n      inputs: $inputs\n      minValue: $minValue\n      maxValue: $maxValue\n      constants: $constants\n      from: $from\n      to: $to\n    ) {\n      samples {\n        occurredTime\n        eventId\n        inputs {\n          key\n          value\n        }\n        value\n        skippedReason\n      }\n      computed\n      skipped {\n        reason\n        count\n      }\n      inputDecimalPlaces\n    }\n  }\n"): typeof import('./graphql').CalculatedEventsDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

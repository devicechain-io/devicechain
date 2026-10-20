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
    "\n  mutation StartCalculatedMetricBackfill($input: CalculatedMetricBackfillInput!) {\n    startCalculatedMetricBackfill(input: $input) {\n      token\n      status\n    }\n  }\n": typeof types.StartCalculatedMetricBackfillDocument,
    "\n  query CalculatedMetricBackfillAvailability {\n    calculatedMetricBackfillAvailability {\n      available\n      reason\n    }\n  }\n": typeof types.CalculatedMetricBackfillAvailabilityDocument,
    "\n  mutation CancelCalculatedMetricBackfill($token: String!) {\n    cancelCalculatedMetricBackfill(token: $token) {\n      token\n      status\n      cancelRequestedAt\n    }\n  }\n": typeof types.CancelCalculatedMetricBackfillDocument,
    "\n  query CalculatedMetricBackfills($profileToken: String, $first: Int) {\n    calculatedMetricBackfills(profileToken: $profileToken, first: $first) {\n      nodes {\n        token\n        status\n        profileToken\n        profileVersion\n        metricKeys\n        warnings\n        from\n        to\n        devicesTotal\n        devicesDone\n        progress\n        samples\n        computed\n        written\n        alreadyPresent\n        windowsCompressed\n        windowsBeyondRetention\n        windowsRefreshed\n        skipped {\n          reason\n          count\n        }\n        storedInputPrecision\n        failure\n        createdAt\n        createdBy\n        startedAt\n        finishedAt\n        cancelRequestedAt\n      }\n      nextCursor\n    }\n  }\n": typeof types.CalculatedMetricBackfillsDocument,
};
const documents: Documents = {
    "\n  query Events($criteria: EventSearchCriteria!) {\n    events(criteria: $criteria) {\n      results {\n        id\n        deviceToken\n        eventType\n        occurredTime\n        source\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": types.EventsDocument,
    "\n  query CalculatedEvents(\n    $deviceToken: String!\n    $expression: String!\n    $dataType: String!\n    $inputs: [CalculatedInput!]!\n    $minValue: Float\n    $maxValue: Float\n    $constants: [CalculatedConstant!]\n    $from: String!\n    $to: String!\n  ) {\n    calculatedEvents(\n      deviceToken: $deviceToken\n      expression: $expression\n      dataType: $dataType\n      inputs: $inputs\n      minValue: $minValue\n      maxValue: $maxValue\n      constants: $constants\n      from: $from\n      to: $to\n    ) {\n      samples {\n        occurredTime\n        eventId\n        inputs {\n          key\n          value\n        }\n        value\n        skippedReason\n      }\n      computed\n      skipped {\n        reason\n        count\n      }\n      inputDecimalPlaces\n    }\n  }\n": types.CalculatedEventsDocument,
    "\n  mutation StartCalculatedMetricBackfill($input: CalculatedMetricBackfillInput!) {\n    startCalculatedMetricBackfill(input: $input) {\n      token\n      status\n    }\n  }\n": types.StartCalculatedMetricBackfillDocument,
    "\n  query CalculatedMetricBackfillAvailability {\n    calculatedMetricBackfillAvailability {\n      available\n      reason\n    }\n  }\n": types.CalculatedMetricBackfillAvailabilityDocument,
    "\n  mutation CancelCalculatedMetricBackfill($token: String!) {\n    cancelCalculatedMetricBackfill(token: $token) {\n      token\n      status\n      cancelRequestedAt\n    }\n  }\n": types.CancelCalculatedMetricBackfillDocument,
    "\n  query CalculatedMetricBackfills($profileToken: String, $first: Int) {\n    calculatedMetricBackfills(profileToken: $profileToken, first: $first) {\n      nodes {\n        token\n        status\n        profileToken\n        profileVersion\n        metricKeys\n        warnings\n        from\n        to\n        devicesTotal\n        devicesDone\n        progress\n        samples\n        computed\n        written\n        alreadyPresent\n        windowsCompressed\n        windowsBeyondRetention\n        windowsRefreshed\n        skipped {\n          reason\n          count\n        }\n        storedInputPrecision\n        failure\n        createdAt\n        createdBy\n        startedAt\n        finishedAt\n        cancelRequestedAt\n      }\n      nextCursor\n    }\n  }\n": types.CalculatedMetricBackfillsDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Events($criteria: EventSearchCriteria!) {\n    events(criteria: $criteria) {\n      results {\n        id\n        deviceToken\n        eventType\n        occurredTime\n        source\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').EventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CalculatedEvents(\n    $deviceToken: String!\n    $expression: String!\n    $dataType: String!\n    $inputs: [CalculatedInput!]!\n    $minValue: Float\n    $maxValue: Float\n    $constants: [CalculatedConstant!]\n    $from: String!\n    $to: String!\n  ) {\n    calculatedEvents(\n      deviceToken: $deviceToken\n      expression: $expression\n      dataType: $dataType\n      inputs: $inputs\n      minValue: $minValue\n      maxValue: $maxValue\n      constants: $constants\n      from: $from\n      to: $to\n    ) {\n      samples {\n        occurredTime\n        eventId\n        inputs {\n          key\n          value\n        }\n        value\n        skippedReason\n      }\n      computed\n      skipped {\n        reason\n        count\n      }\n      inputDecimalPlaces\n    }\n  }\n"): typeof import('./graphql').CalculatedEventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation StartCalculatedMetricBackfill($input: CalculatedMetricBackfillInput!) {\n    startCalculatedMetricBackfill(input: $input) {\n      token\n      status\n    }\n  }\n"): typeof import('./graphql').StartCalculatedMetricBackfillDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CalculatedMetricBackfillAvailability {\n    calculatedMetricBackfillAvailability {\n      available\n      reason\n    }\n  }\n"): typeof import('./graphql').CalculatedMetricBackfillAvailabilityDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CancelCalculatedMetricBackfill($token: String!) {\n    cancelCalculatedMetricBackfill(token: $token) {\n      token\n      status\n      cancelRequestedAt\n    }\n  }\n"): typeof import('./graphql').CancelCalculatedMetricBackfillDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CalculatedMetricBackfills($profileToken: String, $first: Int) {\n    calculatedMetricBackfills(profileToken: $profileToken, first: $first) {\n      nodes {\n        token\n        status\n        profileToken\n        profileVersion\n        metricKeys\n        warnings\n        from\n        to\n        devicesTotal\n        devicesDone\n        progress\n        samples\n        computed\n        written\n        alreadyPresent\n        windowsCompressed\n        windowsBeyondRetention\n        windowsRefreshed\n        skipped {\n          reason\n          count\n        }\n        storedInputPrecision\n        failure\n        createdAt\n        createdBy\n        startedAt\n        finishedAt\n        cancelRequestedAt\n      }\n      nextCursor\n    }\n  }\n"): typeof import('./graphql').CalculatedMetricBackfillsDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

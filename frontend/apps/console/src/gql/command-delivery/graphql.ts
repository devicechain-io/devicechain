/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/**
 * Fields for issuing one command to many devices. Supply exactly one of deviceTokens and
 * groupToken; both or neither is refused with BATCH_TARGET_AMBIGUOUS.
 */
export type CommandBatchCreateRequest = {
  /**
   * Required. When true, devices that cannot receive the command are skipped and recorded as
   * refusals. When false, a batch in which any device cannot receive the command is refused
   * whole (BATCH_PARTIAL_REFUSED) and nothing is created.
   */
  allowPartial: boolean;
  /**
   * Tokens of the devices to command, at most 10000. The order is the order devices are admitted
   * in when the batch is partially admitted. An empty list counts as not supplied.
   */
  deviceTokens?: Array<string> | null | undefined;
  /**
   * RFC 3339 timestamp after which the batch's undelivered commands expire. Optional; when
   * omitted the platform's default time-to-live applies.
   */
  expiresAt?: string | null | undefined;
  /**
   * Token of an entity group of devices, resolved to its members when the batch fires. A dynamic
   * group must have been published. Requires device:read in addition to command:write.
   */
  groupToken?: string | null | undefined;
  /**
   * A specific frozen version of a dynamic group to resolve against. Omit for the group's active
   * published version. Naming a version without a group, or for a static group, is refused.
   */
  groupVersion?: number | null | undefined;
  /** Free-form metadata as a JSON document serialized to a string. Must be valid JSON. Optional. */
  metadata?: string | null | undefined;
  /**
   * Name of the command every targeted device receives, as defined in each device's command
   * vocabulary.
   */
  name: string;
  /**
   * The payload every device receives, as a JSON document serialized to a string. Must be valid
   * JSON. Optional.
   */
  payload?: string | null | undefined;
  /**
   * Idempotency key for the whole batch, unique within the tenant. Re-sending a token that
   * already names a batch returns that batch unchanged and admits no further devices.
   */
  token: string;
};

/**
 * Filters and paging for searching command batches. All supplied filters must match. Results are
 * newest first.
 */
export type CommandBatchSearchCriteria = {
  /** Only batches fired at this entity group. Device-list batches never match. */
  groupToken?: string | null | undefined;
  /** Only batches that fanned out this command name. */
  name?: string | null | undefined;
  /** Page to return, starting at 1; values below 1 are treated as 1. */
  pageNumber: number;
  /** Results per page. Values below 1 become 100; values above 1000 are capped at 1000. */
  pageSize: number;
  /**
   * Only batches of this kind: DEVICE_LIST or GROUP. An unrecognized value matches nothing
   * rather than raising an error.
   */
  targetKind?: string | null | undefined;
};

/** Fields for issuing a command to one device. */
export type CommandCreateRequest = {
  /** Token of the device to command. */
  deviceToken: string;
  /**
   * RFC 3339 timestamp after which the command expires if undelivered. Optional; when omitted
   * the platform's default time-to-live applies.
   */
  expiresAt?: string | null | undefined;
  /** Free-form metadata as a JSON document serialized to a string. Must be valid JSON. Optional. */
  metadata?: string | null | undefined;
  /**
   * Name of the command, which must exist in the command vocabulary of the device's profile;
   * otherwise the request is refused with COMMAND_NOT_IN_VOCABULARY.
   */
  name: string;
  /**
   * The command's arguments as a JSON document serialized to a string. Must be valid JSON and,
   * when the command definition declares a schema, conform to it. Optional.
   */
  payload?: string | null | undefined;
  /**
   * Idempotency key for the command, unique within the tenant. Re-sending a token you already
   * used returns the original command instead of creating a second one.
   */
  token: string;
};

/**
 * Filters and paging for searching commands. All supplied filters must match. Results are newest
 * first.
 */
export type CommandSearchCriteria = {
  /**
   * Only commands created by this batch. This is how to ask what a fleet write is doing now,
   * since the batch record keeps only counts from the moment it fired. Combine with status or
   * statuses. Rows come back in the order the batch admitted its devices.
   */
  batchToken?: string | null | undefined;
  /** Only commands addressed to this device. */
  deviceToken?: string | null | undefined;
  /** Page to return, starting at 1; values below 1 are treated as 1. */
  pageNumber: number;
  /** Results per page. Values below 1 become 100; values above 1000 are capped at 1000. */
  pageSize: number;
  /** Only commands in exactly this lifecycle state (for example HELD). */
  status?: string | null | undefined;
  /**
   * Only commands in any of these lifecycle states. Combined with status, a command must satisfy
   * both. An empty list is ignored rather than matching nothing.
   */
  statuses?: Array<string> | null | undefined;
};

export type CommandsQueryVariables = Exact<{
  criteria: CommandSearchCriteria;
}>;


export type CommandsQuery = { commands: { results: Array<{ id: string, token: string, deviceToken: string, batchToken: string | null, name: string, payload: string | null, status: string, queuedTime: string | null, sentTime: string | null, respondedTime: string | null, expiresAt: string | null, responsePayload: string | null, error: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateCommandMutationVariables = Exact<{
  request: CommandCreateRequest;
}>;


export type CreateCommandMutation = { createCommand: { command: { id: string, token: string, status: string } | null, rejection: { code: string, reason: string } | null } };

export type CommandBatchesQueryVariables = Exact<{
  criteria: CommandBatchSearchCriteria;
}>;


export type CommandBatchesQuery = { commandBatches: { results: Array<{ id: string, token: string, createdAt: string | null, name: string, targetKind: string, groupToken: string | null, groupVersion: number | null, allowPartial: boolean, resolved: number, accepted: number, cancelledAt: string | null, cancelledCount: number | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CommandBatchesByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CommandBatchesByTokenQuery = { commandBatchesByToken: Array<{ id: string, token: string, createdAt: string | null, name: string, payload: string | null, targetKind: string, groupToken: string | null, groupVersion: number | null, allowPartial: boolean, resolved: number, accepted: number, cancelledAt: string | null, cancelledCount: number | null, refusals: Array<{ deviceToken: string, code: string, reason: string }>, refusalCounts: Array<{ code: string, count: number }> }> };

export type CreateCommandBatchMutationVariables = Exact<{
  request: CommandBatchCreateRequest;
}>;


export type CreateCommandBatchMutation = { createCommandBatch: { batch: { id: string, token: string, name: string, targetKind: string, resolved: number, accepted: number } | null, rejection: { code: string, reason: string, resolved: number | null, refusals: Array<{ deviceToken: string, code: string, reason: string }>, refusalCounts: Array<{ code: string, count: number }> } | null } };

export type CancelCommandMutationVariables = Exact<{
  token: string;
}>;


export type CancelCommandMutation = { cancelCommand: { id: string, token: string, status: string } };

export type CancelCommandBatchMutationVariables = Exact<{
  token: string;
}>;


export type CancelCommandBatchMutation = { cancelCommandBatch: { cancelled: number, alreadySent: number, alreadyFinished: number, matched: number } };

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

export const CommandsDocument = new TypedDocumentString(`
    query Commands($criteria: CommandSearchCriteria!) {
  commands(criteria: $criteria) {
    results {
      id
      token
      deviceToken
      batchToken
      name
      payload
      status
      queuedTime
      sentTime
      respondedTime
      expiresAt
      responsePayload
      error
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CommandsQuery, CommandsQueryVariables>;
export const CreateCommandDocument = new TypedDocumentString(`
    mutation CreateCommand($request: CommandCreateRequest!) {
  createCommand(request: $request) {
    command {
      id
      token
      status
    }
    rejection {
      code
      reason
    }
  }
}
    `) as unknown as TypedDocumentString<CreateCommandMutation, CreateCommandMutationVariables>;
export const CommandBatchesDocument = new TypedDocumentString(`
    query CommandBatches($criteria: CommandBatchSearchCriteria!) {
  commandBatches(criteria: $criteria) {
    results {
      id
      token
      createdAt
      name
      targetKind
      groupToken
      groupVersion
      allowPartial
      resolved
      accepted
      cancelledAt
      cancelledCount
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CommandBatchesQuery, CommandBatchesQueryVariables>;
export const CommandBatchesByTokenDocument = new TypedDocumentString(`
    query CommandBatchesByToken($tokens: [String!]!) {
  commandBatchesByToken(tokens: $tokens) {
    id
    token
    createdAt
    name
    payload
    targetKind
    groupToken
    groupVersion
    allowPartial
    resolved
    accepted
    refusals {
      deviceToken
      code
      reason
    }
    refusalCounts {
      code
      count
    }
    cancelledAt
    cancelledCount
  }
}
    `) as unknown as TypedDocumentString<CommandBatchesByTokenQuery, CommandBatchesByTokenQueryVariables>;
export const CreateCommandBatchDocument = new TypedDocumentString(`
    mutation CreateCommandBatch($request: CommandBatchCreateRequest!) {
  createCommandBatch(request: $request) {
    batch {
      id
      token
      name
      targetKind
      resolved
      accepted
    }
    rejection {
      code
      reason
      resolved
      refusals {
        deviceToken
        code
        reason
      }
      refusalCounts {
        code
        count
      }
    }
  }
}
    `) as unknown as TypedDocumentString<CreateCommandBatchMutation, CreateCommandBatchMutationVariables>;
export const CancelCommandDocument = new TypedDocumentString(`
    mutation CancelCommand($token: String!) {
  cancelCommand(token: $token) {
    id
    token
    status
  }
}
    `) as unknown as TypedDocumentString<CancelCommandMutation, CancelCommandMutationVariables>;
export const CancelCommandBatchDocument = new TypedDocumentString(`
    mutation CancelCommandBatch($token: String!) {
  cancelCommandBatch(token: $token) {
    cancelled
    alreadySent
    alreadyFinished
    matched
  }
}
    `) as unknown as TypedDocumentString<CancelCommandBatchMutation, CancelCommandBatchMutationVariables>;
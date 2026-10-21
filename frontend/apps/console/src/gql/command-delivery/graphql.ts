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
   * group must have been published. A group that resolves to more than 10000 devices is refused
   * with BATCH_TOO_LARGE. Requires device:read in addition to command:write.
   */
  groupToken?: string | null | undefined;
  /**
   * A specific frozen version of a dynamic group to resolve against. Omit for the group's active
   * published version. Naming a version without a group, or for a static group, is refused.
   */
  groupVersion?: number | null | undefined;
  /**
   * Free-form metadata as a JSON document serialized to a string. Must be valid JSON. At most
   * 65536 bytes (METADATA_TOO_LARGE). Optional.
   */
  metadata?: string | null | undefined;
  /**
   * Name of the command every targeted device receives, as defined in each device's command
   * vocabulary. At most 128 bytes, or the request is refused with COMMAND_NAME_TOO_LONG.
   */
  name: string;
  /**
   * The payload every device receives, as a JSON document serialized to a string. Must be valid
   * JSON. At most 65536 bytes (PAYLOAD_TOO_LARGE). Optional.
   */
  payload?: string | null | undefined;
  /**
   * Idempotency key for the whole batch, unique within the tenant. Re-sending a token that
   * already names a batch returns that batch unchanged and admits no further devices.
   */
  token: string;
};

/**
 * Fields for re-issuing a batch's command to a chosen subset of its devices. The command name and
 * payload are copied from the original batch; they cannot be changed here.
 */
export type CommandBatchRetryRequest = {
  /**
   * Required. As for createCommandBatch: when true, devices that cannot receive the command now
   * are skipped and recorded as refusals; when false, any such device refuses the whole retry.
   */
  allowPartial: boolean;
  /**
   * Tokens of the devices to retry, at most 10000. Each must be a member of the original batch
   * and unsuccessful (see includeUnknown). The order is the order devices are admitted in when the
   * retry is partially admitted.
   */
  deviceTokens: Array<string>;
  /**
   * RFC 3339 timestamp after which the retry's undelivered commands expire. Optional; when omitted
   * the platform's default time-to-live applies.
   */
  expiresAt?: string | null | undefined;
  /**
   * Whether to also retry devices whose outcome is UNKNOWN: commands that may already have reached
   * the device and acted. Devices with that outcome are refused with RETRY_UNKNOWN_NOT_ACKNOWLEDGED
   * unless this is true. Only set it after confirming that acting twice is harmless.
   */
  includeUnknown: boolean;
  /** Free-form metadata for the retry batch, as a JSON document serialized to a string. Optional. */
  metadata?: string | null | undefined;
  /** Token of the batch to retry. It may itself be a retry. */
  of: string;
  /**
   * Idempotency key for the retry, unique within the tenant and different from the original
   * batch's. Re-sending it returns the retry unchanged and admits nothing further. Re-sending the
   * ORIGINAL batch's token through createCommandBatch still returns the original and executes
   * nothing.
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
  /**
   * Free-form metadata as a JSON document serialized to a string. Must be valid JSON. At most
   * 65536 bytes (METADATA_TOO_LARGE). Optional.
   */
  metadata?: string | null | undefined;
  /**
   * Name of the command, which must exist in the command vocabulary of the device's profile;
   * otherwise the request is refused with COMMAND_NOT_IN_VOCABULARY. At most 128 bytes, or the
   * request is refused with COMMAND_NAME_TOO_LONG.
   */
  name: string;
  /**
   * The command's arguments as a JSON document serialized to a string. Must be valid JSON and,
   * when the command definition declares a schema, conform to it. At most 65536 bytes
   * (PAYLOAD_TOO_LARGE). Optional.
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
  /**
   * Only commands sent at or before this instant (inclusive), or queued at or before it when
   * never sent, as an RFC 3339 timestamp. Omit for no upper bound. A value that is not RFC 3339
   * is an error.
   */
  endTime?: string | null | undefined;
  /**
   * Only finished commands whose outcome is one of these (SUCCEEDED, NOT_DELIVERED, REJECTED or
   * UNKNOWN). Live commands have no outcome and never match. An empty list is ignored.
   */
  outcomes?: Array<string> | null | undefined;
  /** Page to return, starting at 1; values below 1 are treated as 1. */
  pageNumber: number;
  /** Results per page. Values below 1 become 100; values above 1000 are capped at 1000. */
  pageSize: number;
  /**
   * Only commands sent at or after this instant (inclusive), as an RFC 3339 timestamp. A
   * command never sent (queued, held, or expired or cancelled first) is placed at the time it
   * was queued instead. Omit for no lower bound. A value that is not RFC 3339 is an error.
   */
  startTime?: string | null | undefined;
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


export type CommandsQuery = { commands: { results: Array<{ id: string, token: string, deviceToken: string, batchToken: string | null, name: string, payload: string | null, status: string, queuedTime: string | null, sentTime: string | null, respondedTime: string | null, expiresAt: string | null, dispatchAfter: string | null, maintenanceWindowToken: string | null, responsePayload: string | null, error: string | null, outcome: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateCommandMutationVariables = Exact<{
  request: CommandCreateRequest;
}>;


export type CreateCommandMutation = { createCommand: { command: { id: string, token: string, status: string } | null, rejection: { code: string, reason: string } | null } };

export type CommandBatchesQueryVariables = Exact<{
  criteria: CommandBatchSearchCriteria;
}>;


export type CommandBatchesQuery = { commandBatches: { results: Array<{ id: string, token: string, createdAt: string | null, name: string, targetKind: string, groupToken: string | null, groupVersion: number | null, allowPartial: boolean, resolved: number, accepted: number, waiting: number | null, cancelledAt: string | null, cancelledCount: number | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CommandBatchesByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CommandBatchesByTokenQuery = { commandBatchesByToken: Array<{ id: string, token: string, createdAt: string | null, name: string, payload: string | null, targetKind: string, groupToken: string | null, groupVersion: number | null, allowPartial: boolean, resolved: number, accepted: number, waiting: number | null, cancelledAt: string | null, cancelledCount: number | null, retryOf: string | null, retries: Array<string>, refusals: Array<{ deviceToken: string, code: string, reason: string }>, refusalCounts: Array<{ code: string, count: number }> }> };

export type CreateCommandBatchMutationVariables = Exact<{
  request: CommandBatchCreateRequest;
}>;


export type CreateCommandBatchMutation = { createCommandBatch: { batch: { id: string, token: string, name: string, targetKind: string, resolved: number, accepted: number } | null, rejection: { code: string, reason: string, resolved: number | null, refusals: Array<{ deviceToken: string, code: string, reason: string }>, refusalCounts: Array<{ code: string, count: number }> } | null } };

export type RetryCommandBatchMutationVariables = Exact<{
  request: CommandBatchRetryRequest;
}>;


export type RetryCommandBatchMutation = { retryCommandBatch: { batch: { id: string, token: string, retryOf: string | null, resolved: number, accepted: number } | null, rejection: { code: string, reason: string, resolved: number | null, refusals: Array<{ deviceToken: string, code: string, reason: string }> } | null } };

export type CancelCommandMutationVariables = Exact<{
  token: string;
}>;


export type CancelCommandMutation = { cancelCommand: { id: string, token: string, status: string } };

export type CancelCommandBatchMutationVariables = Exact<{
  token: string;
}>;


export type CancelCommandBatchMutation = { cancelCommandBatch: { cancelled: number, alreadySent: number, alreadyFinished: number, matched: number } };

export type DeviceCommandHoldQueryVariables = Exact<{
  deviceToken: string;
}>;


export type DeviceCommandHoldQuery = { deviceCommandHold: { disabled: boolean, heldCount: number } };

export type CancelDeviceCommandsMutationVariables = Exact<{
  deviceToken: string;
}>;


export type CancelDeviceCommandsMutation = { cancelDeviceCommands: { cancelled: number } };

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
      dispatchAfter
      maintenanceWindowToken
      responsePayload
      error
      outcome
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
      waiting
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
    waiting
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
    retryOf
    retries
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
export const RetryCommandBatchDocument = new TypedDocumentString(`
    mutation RetryCommandBatch($request: CommandBatchRetryRequest!) {
  retryCommandBatch(request: $request) {
    batch {
      id
      token
      retryOf
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
    }
  }
}
    `) as unknown as TypedDocumentString<RetryCommandBatchMutation, RetryCommandBatchMutationVariables>;
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
export const DeviceCommandHoldDocument = new TypedDocumentString(`
    query DeviceCommandHold($deviceToken: String!) {
  deviceCommandHold(deviceToken: $deviceToken) {
    disabled
    heldCount
  }
}
    `) as unknown as TypedDocumentString<DeviceCommandHoldQuery, DeviceCommandHoldQueryVariables>;
export const CancelDeviceCommandsDocument = new TypedDocumentString(`
    mutation CancelDeviceCommands($deviceToken: String!) {
  cancelDeviceCommands(deviceToken: $deviceToken) {
    cancelled
  }
}
    `) as unknown as TypedDocumentString<CancelDeviceCommandsMutation, CancelDeviceCommandsMutationVariables>;
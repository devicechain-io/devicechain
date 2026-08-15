// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the command-delivery service (ADR-012):
// persistent, two-way command dispatch with a guarded lifecycle.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/command-delivery';
import type {
  CommandsQuery,
  CommandCreateRequest,
  CreateCommandMutation,
  CommandBatchesQuery,
  CommandBatchesByTokenQuery,
  CommandBatchCreateRequest,
  CreateCommandBatchMutation,
} from '@/gql/command-delivery/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection set and can never drift from the schema.
export type Command = CommandsQuery['commands']['results'][number];
export type CommandSearchResults = CommandsQuery['commands'];
export type { CommandCreateRequest };

// The outcome of an enqueue attempt: exactly one of `command` / `rejection` is
// populated (see createCommand below).
export type CreateCommandResult = CreateCommandMutation['createCommand'];
export type CommandEnqueueRejection = NonNullable<CreateCommandResult['rejection']>;

// A batch as the LIST sees it (no payload, no refusal detail) and as the DETAIL page
// sees it (everything). They are separate selections on purpose: a list of batches
// fired at large groups would otherwise carry a refusal sample per row.
export type CommandBatchSummary = CommandBatchesQuery['commandBatches']['results'][number];
export type CommandBatchSearchResults = CommandBatchesQuery['commandBatches'];
export type CommandBatch = CommandBatchesByTokenQuery['commandBatchesByToken'][number];
export type CommandBatchDeviceRefusal = CommandBatch['refusals'][number];
export type CommandBatchRefusalCount = CommandBatch['refusalCounts'][number];

// The outcome of a batch attempt, and the two shapes it can carry. Both fields are
// NULLABLE and this is NOT a union, so a caller has to reckon with all four shapes —
// see createCommandBatch below.
export type CreateCommandBatchResult = CreateCommandBatchMutation['createCommandBatch'];
export type CreatedCommandBatch = NonNullable<CreateCommandBatchResult['batch']>;
export type CommandBatchRejection = NonNullable<CreateCommandBatchResult['rejection']>;
export type { CommandBatchCreateRequest };

const COMMANDS = graphql(`
  query Commands($criteria: CommandSearchCriteria!) {
    commands(criteria: $criteria) {
      results {
        id
        token
        deviceToken
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
`);

const CREATE_COMMAND = graphql(`
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
`);

// The batch LIST selection. It carries the stored creation-time facts (`resolved`,
// `accepted`) and the cancellation stamp, which is everything the list column set
// needs — and deliberately not `payload`, `refusals` or `refusalCounts`, which are
// per-batch detail nobody reads from a table of twenty.
const COMMAND_BATCHES = graphql(`
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
`);

// The batch DETAIL selection. It adds the payload and BOTH refusal shapes, which
// answer different questions and are not interchangeable: `refusalCounts` is the
// complete per-code total, while `refusals` is a bounded sample (capped per code) of
// the individual devices. Rendering the sample as though it were the whole set is
// the misreading this pair exists to prevent — see the Refusals panel.
const COMMAND_BATCHES_BY_TOKEN = graphql(`
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
`);

// Firing a batch. The result selection takes BOTH arms in full, because both are the
// answer: `batch` is the record the console then navigates to, and `rejection` is a
// decided verdict that has to be read on the spot — it is never stored anywhere, so a
// caller that did not select its refusal detail could not tell the operator WHICH
// devices refused a whole-batch refusal.
//
// The batch arm selects only the identity and the creation-time counts. Everything else
// about a created batch is already read in full by the detail page's own query, and
// duplicating that selection here is how the two would drift.
const CREATE_COMMAND_BATCH = graphql(`
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
`);

const CANCEL_COMMAND = graphql(`
  mutation CancelCommand($token: String!) {
    cancelCommand(token: $token) {
      id
      token
      status
    }
  }
`);

// List the commands issued to a device, newest first (server default), paged.
// Requires the command:read authority.
export async function listCommands(opts: {
  deviceToken: string;
  pageNumber: number;
  pageSize: number;
}): Promise<CommandSearchResults> {
  return (
    await gql('command-delivery', COMMANDS, {
      criteria: {
        pageNumber: opts.pageNumber,
        pageSize: opts.pageSize,
        deviceToken: opts.deviceToken,
      },
    })
  ).commands;
}

// List the per-device commands ONE BATCH created, paged. Requires command:read.
//
// 🔴 THE ORDER IS THE BATCH'S OWN, not "newest first". Every row of a batch shares one
// creation instant, so the newest-first sort ties across the whole batch and the
// tiebreak — a token minted from each device's position — carries the ADMITTED order,
// which is the order the caller named the devices in. Page 1 is therefore the head of
// the batch and must not be re-sorted client-side.
//
// This is also the only way to ask a present-tense question about a fleet write. The
// batch record's `resolved`/`accepted` are stored facts about the moment it fired; "how
// many have actually gone out?" is answerable only from these rows.
export async function listBatchCommands(opts: {
  batchToken: string;
  pageNumber: number;
  pageSize: number;
  status?: string;
}): Promise<CommandSearchResults> {
  return (
    await gql('command-delivery', COMMANDS, {
      criteria: {
        pageNumber: opts.pageNumber,
        pageSize: opts.pageSize,
        batchToken: opts.batchToken,
        status: opts.status,
      },
    })
  ).commands;
}

// List command batches, newest first (server default), paged. Requires command:read.
export async function listCommandBatches(opts: {
  pageNumber: number;
  pageSize: number;
  name?: string;
  groupToken?: string;
  targetKind?: string;
}): Promise<CommandBatchSearchResults> {
  return (
    await gql('command-delivery', COMMAND_BATCHES, {
      criteria: {
        pageNumber: opts.pageNumber,
        pageSize: opts.pageSize,
        name: opts.name,
        groupToken: opts.groupToken,
        targetKind: opts.targetKind,
      },
    })
  ).commandBatches;
}

// Read one batch by token. The query takes a LIST and answers with the batches it
// found, so an unknown token is an empty array rather than an error — hence the
// `?? null`, which the detail page renders as "no such batch".
export async function getCommandBatch(token: string): Promise<CommandBatch | null> {
  const found = (await gql('command-delivery', COMMAND_BATCHES_BY_TOKEN, { tokens: [token] }))
    .commandBatchesByToken;
  return found[0] ?? null;
}

// Issue a command to a device. The caller supplies a fresh unique token (an
// idempotency key for the dispatch); requires the command:write authority.
//
// 🔴 IT RESOLVES ON A REFUSAL. The result carries EITHER the created `command` OR
// the `rejection` that refused it — the device is unknown, the command is not in the
// device's published vocabulary, the payload violates its parameter schema, the
// timestamp/JSON is malformed, or the tenant is at its held-command ceiling. A
// rejection is a decided verdict about the request, so it is a value here and the
// caller must branch on it; only a failure to DECIDE (the service unreachable, a
// database error) throws. The two are not interchangeable: a rejection carries a
// reason the operator can act on, while a thrown error means the platform could not
// answer and says nothing about the command.
//
// The rejection's `code` is the stable classification (PAYLOAD_NOT_JSON,
// COMMAND_NOT_IN_VOCABULARY, HELD_CEILING_EXCEEDED, …) and its `reason` is
// client-safe prose. The code set is OPEN — the enqueue gate that owns the device
// vocabulary relays its own codes through — so a caller must never treat an
// unrecognized code as anything but a rejection.
export async function createCommand(request: CommandCreateRequest): Promise<CreateCommandResult> {
  return (await gql('command-delivery', CREATE_COMMAND, { request })).createCommand;
}

// Fan one command out to many devices as a single recorded operation. Requires
// `command:write`; targeting a GROUP additionally requires `device:read`, because
// resolving a group to its members is a read of the device registry.
//
// 🔴 THE CALLER MINTS A FRESH TOKEN PER ATTEMPT. It is the idempotency key for the whole
// fan-out, and re-issuing one that already names a batch returns THAT batch UNCHANGED —
// it is never topped up. So a reused token is not a harmless retry: it answers with a
// previous batch's stored counts, which a UI would render as the outcome of the write it
// just made. Fresh token, every submit.
//
// 🔴 IT RESOLVES ON A REFUSAL, and `CreateCommandBatchResult` is a pair of NULLABLE
// fields rather than a union — so the type permits four shapes and only two of them are
// contractual. Branch on `rejection` first, then require `batch`, and treat "neither" as
// a failure: a batch that reports success with no record is a fleet actuation nobody can
// audit. A thrown error means the batch could not be DECIDED, and the service guarantees
// nothing was created — the token is unspent and the request can simply be retried.
export async function createCommandBatch(
  request: CommandBatchCreateRequest,
): Promise<CreateCommandBatchResult> {
  return (await gql('command-delivery', CREATE_COMMAND_BATCH, { request })).createCommandBatch;
}

// Cancel a command by token (QUEUED / HELD / PARKED -> CANCELLED). Requires command:write.
//
// 🔴 NOT "any non-terminal command". SENT is non-terminal and is NOT cancellable: the
// command is already at the device and the service will not call it back, because doing
// so would make it discard the response the device is about to send. The service gates
// this on a positive list — but 🔴 OUTSIDE THAT LIST IT DOES NOT FAIL. SENT, a terminal
// state, or a status this build has never heard of all come back as a 200 carrying the
// command UNCHANGED, with no error. Only an unknown token errors.
//
// Two consequences, and neither is optional:
//   • Gate the control on isCancellableCommandStatus, so the button is never offered
//     where pressing it would do nothing.
//   • Believe the RETURNED status, never the resolved promise. A command can move from
//     QUEUED to SENT between the render that offered the button and the click, and this
//     call still succeeds — reporting success off `await` alone tells the operator a
//     command was cancelled when it is on its way to the device.
//
// CANCELLED, not EXPIRED: the two were one value until recently, and cancellation
// wrote EXPIRED. Historical rows keep it — there is no backfill — so both values
// appear in real data, meaning different things (EXPIRED = the platform ran out of
// time to send it; CANCELLED = somebody called it off).
export async function cancelCommand(token: string) {
  return (await gql('command-delivery', CANCEL_COMMAND, { token })).cancelCommand;
}

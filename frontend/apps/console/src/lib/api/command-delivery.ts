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

// Cancel a non-terminal command by token (QUEUED / HELD / SENT -> CANCELLED).
// Requires command:write.
//
// CANCELLED, not EXPIRED: the two were one value until recently, and cancellation
// wrote EXPIRED. Historical rows keep it — there is no backfill — so both values
// appear in real data, meaning different things (EXPIRED = the platform ran out of
// time to send it; CANCELLED = somebody called it off).
export async function cancelCommand(token: string) {
  return (await gql('command-delivery', CANCEL_COMMAND, { token })).cancelCommand;
}

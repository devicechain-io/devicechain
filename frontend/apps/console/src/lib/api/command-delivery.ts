// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the command-delivery service (ADR-012):
// persistent, two-way command dispatch with a guarded lifecycle.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/command-delivery';
import type {
  CommandsQuery,
  CommandCreateRequest,
} from '@/gql/command-delivery/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection set and can never drift from the schema.
export type Command = CommandsQuery['commands']['results'][number];
export type CommandSearchResults = CommandsQuery['commands'];
export type { CommandCreateRequest };

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
      id
      token
      status
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
export async function createCommand(request: CommandCreateRequest) {
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

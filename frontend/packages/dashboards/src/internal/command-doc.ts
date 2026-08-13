// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The typed command documents the hub's control channel drives, hand-authored.
//
// Packages carry no graphql-codegen (only apps do), so — like measurement-doc and
// alarm-doc — these are written by hand and cast to TypedDocument. The control channel
// is poll-then-emit: command-delivery exposes NO subscription, so COMMANDS_QUERY is
// polled (device-scoped) for the live delivery lifecycle, and CREATE_COMMAND issues a
// new command (requires command:write). The command-button widget bakes its parameter
// schema from the console at author time; there is no definition query here.

import type { TypedDocument } from '@devicechain/client';

import type { CommandRow } from '../types';

// ── Query (lifecycle poll) ───────────────────────────────────────────────

export interface CommandSearchCriteriaInput {
  pageNumber: number;
  pageSize: number;
  deviceToken?: string | null;
  // Match a single lifecycle state exactly.
  status?: string | null;
  // Match ANY of several lifecycle states. ANDed with `status` when both are given; an
  // empty list is ignored rather than matching nothing. The states a caller cares about
  // are usually a set — "still outstanding" is HELD ∪ SENT ∪ QUEUED, not one value.
  statuses?: string[] | null;
}

export interface CommandsQueryResult {
  commands: {
    results: CommandRow[];
    pagination: { totalRecords: number };
  };
}

export interface CommandsQueryVariables {
  criteria: CommandSearchCriteriaInput;
}

export const COMMANDS_QUERY = `
  query DashboardCommands($criteria: CommandSearchCriteria!) {
    commands(criteria: $criteria) {
      results {
        token
        name
        status
        payload
        responsePayload
        error
        queuedTime
        sentTime
        respondedTime
      }
      pagination {
        totalRecords
      }
    }
  }
` as unknown as TypedDocument<CommandsQueryResult, CommandsQueryVariables>;

// ── Mutation (issue a command) ───────────────────────────────────────────
// createCommand persists a new command to a device (command-delivery, requires
// command:write). The caller mints a fresh unique `token` per dispatch (the
// idempotency key + cancel handle); `payload` is the request body the device receives
// verbatim (the widget serializes its typed form to JSON). Only the token + status are
// selected back — the hub re-reads the lifecycle via the poll query afterward.
//
// 🔴 THE RESULT HAS TWO ARMS, and they mean opposite things. `command` is the command
// that was created; `rejection` is a decided REFUSAL of the request (unknown device,
// command not in the device's published vocabulary, payload violating its parameter
// schema, malformed JSON/timestamp, the tenant's held-command ceiling full), carrying a
// stable `code` to branch on and a client-safe `reason` for a person. Exactly one is
// non-null. A GraphQL error instead of either means the enqueue could not be DECIDED —
// an availability failure that says nothing about the command — which is why a
// rejection is a value here rather than something thrown.
//
// The code set is OPEN (the enqueue gate that owns the vocabulary relays its own codes
// through), so an unrecognized code is still a rejection, never a success.

export interface CreateCommandRequestInput {
  token: string;
  deviceToken: string;
  name: string;
  payload?: string | null;
}

export interface CreateCommandResult {
  createCommand: {
    command: { token: string; status: string } | null;
    rejection: { code: string; reason: string } | null;
  };
}

export interface CreateCommandVariables {
  request: CreateCommandRequestInput;
}

export const CREATE_COMMAND = `
  mutation DashboardCreateCommand($request: CommandCreateRequest!) {
    createCommand(request: $request) {
      command {
        token
        status
      }
      rejection {
        code
        reason
      }
    }
  }
` as unknown as TypedDocument<CreateCommandResult, CreateCommandVariables>;

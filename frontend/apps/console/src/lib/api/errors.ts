// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Classification of transport failures the UI has to render differently.
//
// Most errors are one shape to a screen ("something went wrong"), but a refusal
// is not: "you may not see this" and "this could not be loaded" call for
// different copy, and collapsing them makes a permission boundary look like an
// outage.
import { GraphQLRequestError } from '@devicechain/client';

// The backend's authorization gate returns core/auth's ErrForbidden, whose text
// is "forbidden: missing required authority"; graphql-go surfaces a resolver
// error verbatim as the entry's `message`, over HTTP 200. Anchoring the match at
// the START of the message keeps it from firing on an unrelated error that
// merely mentions the word (an entity literally named "forbidden-zone", say).
const FORBIDDEN_PREFIX = /^forbidden\b/i;

/**
 * True when the server REFUSED the request for lack of an authority, as opposed
 * to failing it. Callers use this to render a permission state rather than an
 * error state.
 */
export function isForbiddenError(err: unknown): boolean {
  if (!(err instanceof GraphQLRequestError)) return false;
  // A transport-level 403 counts even with no GraphQL body to read.
  if (err.status === 403) return true;
  const messages = err.errors?.length ? err.errors.map((e) => e.message) : [err.message];
  return messages.some((m) => FORBIDDEN_PREFIX.test(m.trim()));
}

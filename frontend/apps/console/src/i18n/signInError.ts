// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which message the sign-in screen shows when signing in fails.
//
// 🔴 THE BUG THIS REPLACES TOLD USERS TO RE-TYPE A CORRECT PASSWORD DURING AN OUTAGE.
// Login.tsx folded every failure with `err instanceof GraphQLRequestError ? badCreds :
// unreachable`, and the SDK throws that same class with `status: 0` when `fetch` itself
// rejects — so an API that was down, a broken network, or a bad ingress all rendered
// "Invalid email or password." on the one screen that decides whether anyone can get in.
//
// This is a copy of apps/dashboard/src/i18n/signInError.ts, and the duplication is the
// same deliberate one recorded in config.ts for the locale registry: the two apps ship
// independently and /dash is the reference external embedder a third party clones, so it
// must not depend on console internals. What keeps the copies honest is that neither is a
// judgement call — both describe ONE contract, the throw sites in
// packages/client/src/transport.ts, and each app's test reads that contract out of the
// transport source rather than imagining it.

import { GraphQLRequestError } from '@devicechain/client';

/**
 * Did the server EVALUATE this request and reject it, or did the request never get an
 * answer at all?
 *
 * 🔴 THE TEST IS THE `errors` ARRAY, NOT THE ERROR CLASS AND NOT THE STATUS, and both of
 * the more obvious predicates are wrong in the same direction — they report an OUTAGE as
 * a rejected credential:
 *
 *   - `err instanceof GraphQLRequestError` alone: the transport throws that same class
 *     with `status: 0` when `fetch` itself fails, so an unplugged network reads as a bad
 *     password. This is the defect that was live here.
 *   - `… && err.status !== 0`: fixes the network case and keeps the class of bug. A 503
 *     from the ingress, or a 502 from a service that is down, carries a real HTTP status
 *     and no `errors` array — so it is `!== 0`, and it still reports as a bad password.
 *
 * The `errors` array is present on exactly one throw in the transport: the one taken
 * after the server returned a GraphQL body containing errors. That is the same as saying
 * "the server received this, ran it, and said no" — which is the actual question.
 *
 * 🔴 VERIFIED AGAINST THE SERVER, NOT REASONED FROM THE CLIENT. Both of this screen's
 * calls — `login` and `selectTenant` — pass `{ anonymous: true }`, so they carry no
 * Authorization header; the GraphQL handler's 401 branches all require a bearer token to
 * verify, and user-management's data plane does not require one (login has to stay
 * reachable). A rejected login therefore arrives as HTTP 200 with an `errors` array,
 * which is what makes this predicate the right one rather than merely the safer one. An
 * area that DID require a token would 401 with no `errors`, and would need its own
 * expired-session handling — which is why this helper is scoped to sign-in.
 */
export function serverRejectedRequest(err: unknown): boolean {
  return err instanceof GraphQLRequestError && (err.errors?.length ?? 0) > 0;
}

/**
 * The catalog key for a sign-in failure. `rejectedKey` is what to say when the server
 * evaluated the request and refused it — different for the two calls, since "we could not
 * sign you in" and "we could not enter that tenant" are different facts. Anything else
 * means the request never got an answer, which is one message for every cause.
 *
 * Keys are namespace-qualified so a caller whose default namespace is not `login` cannot
 * silently resolve a different string.
 *
 * Deliberately coarse: distinguishing a rate-limit from a bad password would mean
 * pattern-matching the server's prose, which is a contract nobody has agreed to and which
 * breaks the moment that text is reworded.
 */
export function signInErrorKey(err: unknown, rejectedKey: string): string {
  return serverRejectedRequest(err) ? rejectedKey : 'login:serverUnreachable';
}

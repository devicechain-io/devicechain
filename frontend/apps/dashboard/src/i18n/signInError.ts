// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which message step 1 shows when signing in fails.
//
// 🔴 THE SIGN-IN SCREEN IS THE ONE PLACE A RAW ERROR MUST NOT REACH THE VIEWER, and
// the first version of this app's localization let it. `setError(errorDetail(err))`
// rendered whatever the SDK threw, which is English from three different sources: the
// server's own text for a bad password, our client package's `Request failed (503)`,
// and the browser's `Failed to fetch`. So a Spanish viewer who mistyped a password read
// English on the one line that decides whether they can get in.
//
// The "a runtime message stays untranslated" rule that governs load.ts does NOT reach
// here, and the difference is worth stating because the two look alike. A parser
// diagnostic names a position in JSON the viewer pasted — it is evidence about their
// own input, and translating it would make it harder to match against the document in
// front of them. A sign-in failure names nothing the viewer can look at. It IS the
// chrome, and it has exactly two useful shapes.

import { GraphQLRequestError } from '@devicechain/client';

/**
 * Did the server EVALUATE this request and reject it, or did the request never get an
 * answer at all?
 *
 * 🔴 THE TEST IS THE `errors` ARRAY, NOT THE ERROR CLASS AND NOT THE STATUS, and both
 * of the more obvious predicates are wrong in the same direction — they report an
 * OUTAGE as a rejected credential, which tells a viewer to go re-type a password that
 * was never the problem:
 *
 *   - `err instanceof GraphQLRequestError` alone: the transport throws that same class
 *     with `status: 0` when `fetch` itself fails, so an unplugged network reads as a
 *     bad password. (The console's Login.tsx has exactly this bug today; see the
 *     residual on this app's PR. It is not inherited here.)
 *   - `… && err.status !== 0`: fixes the network case and keeps the class of bug. A 503
 *     from the ingress, or a 502 from a service that is down, carries a real HTTP status
 *     and no `errors` array — so it is `!== 0`, and it still reports as a bad password.
 *
 * The `errors` array is present on exactly one throw in the transport: the one taken
 * after the server returned a GraphQL body containing errors. That is the same as
 * saying "the server received this, ran it, and said no" — which is the actual question.
 *
 * This app's two sign-in calls are both anonymous, so the 401 branch of the GraphQL
 * handler (which needs a bearer token to reject) cannot fire on them: a rejected login
 * arrives as HTTP 200 with an `errors` array, which is what makes this predicate the
 * right one rather than merely the safer one.
 */
export function serverRejectedRequest(err: unknown): boolean {
  return err instanceof GraphQLRequestError && (err.errors?.length ?? 0) > 0;
}

/**
 * The catalog key for a sign-in failure. `rejectedKey` is what to say when the server
 * evaluated the request and refused it — different for the two calls, since "we could
 * not sign you in" and "we could not enter that tenant" are different facts. Anything
 * else means the request never got an answer, which is one message for every cause.
 *
 * Deliberately coarse: distinguishing a rate-limit from a bad password would mean
 * pattern-matching the server's prose, which is a contract nobody has agreed to and
 * which breaks the moment that text is reworded.
 */
export function signInErrorKey(err: unknown, rejectedKey: string): string {
  return serverRejectedRequest(err) ? rejectedKey : 'signIn:serverUnreachable';
}

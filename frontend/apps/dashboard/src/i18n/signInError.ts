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
 *     bad password. (This was live in the console's Login.tsx, which is where it was
 *     found; that screen now asks the same question through its own copy of this file,
 *     apps/console/src/i18n/signInError.ts.)
 *   - `… && err.status !== 0`: fixes the network case and keeps the class of bug. A 503
 *     from the ingress, or a 502 from a service that is down, carries a real HTTP status
 *     and no `errors` array — so it is `!== 0`, and it still reports as a bad password.
 *
 * The `errors` array is present on exactly one throw in the transport: the one taken
 * after the server returned a GraphQL body containing errors. That is the same as saying
 * "the resolver ran and returned an error" — the question this screen can actually ask.
 *
 * 🔴 IT IS NOT THE SAME AS "the server decided about these credentials". Two failures
 * that are not about the password also arrive as HTTP 200 with an `errors` array, and the
 * server labels both with an `extensions.code` so they are not read as one:
 *
 *   - `THROTTLED` — the account has failed too many times recently and this attempt was
 *     not evaluated at all. Saying "invalid password" here would tell someone who typed
 *     the RIGHT password that it is wrong.
 *   - `UNAVAILABLE` — the server could not count the attempt (its attempt store was
 *     unreachable), so it refused to check the password. That is an outage, not a verdict.
 *
 * signInErrorKey reads those codes before this predicate. What still has no code is a
 * database failure during the account lookup, which is therefore still reported as a
 * rejection — the one remaining case where this screen can blame the password for an
 * outage.
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
 * The server's codes are read FIRST, because they are the cases where "the server
 * answered" and "the server rejected these credentials" come apart: a throttled attempt
 * gets its own message, and an unavailable check reads as the outage it is. They are
 * matched on `extensions.code`, never on the message text, which is prose and may be
 * reworded.
 */
export function signInErrorKey(err: unknown, rejectedKey: string): string {
  switch (serverErrorCode(err)) {
    case 'THROTTLED':
      return 'signIn:tooManyAttempts';
    case 'UNAVAILABLE':
      return 'signIn:signInUnavailable';
    // A password sign-in refused because the tenant requires single sign-on. Matched on
    // the code like the two above, never on the message text.
    case 'SSO_REQUIRED':
      return 'signIn:ssoRequired';
    // The tenant needs a second factor and this sign-in had none. The account has one
    // (MFA_REQUIRED): signing in again and entering the code is the remedy. It has none yet
    // (MFA_ENROLLMENT_REQUIRED): this viewer cannot set one up, the console can.
    case 'MFA_REQUIRED':
      return 'signIn:mfaRequired';
    case 'MFA_ENROLLMENT_REQUIRED':
      return 'signIn:mfaEnrollmentRequired';
  }
  return serverRejectedRequest(err) ? rejectedKey : 'signIn:serverUnreachable';
}

/** What login's MFA_CHALLENGE error carries: the handle verifyMfa needs, and when it lapses. */
export interface MfaChallenge {
  challenge: string;
  /** RFC 3339; the challenge accepts attempts until then. */
  expiresAt: string;
  /** `TOTP` and `RECOVERY_CODE`. */
  methods: string[];
}

/**
 * The challenge login held the sign-in for, or null when the error is anything else. An
 * account with two-factor authentication on does not get a token from its password alone:
 * login fails with the code MFA_CHALLENGE, and the handle rides in the error's extensions.
 * It lives in component state only: never in the address, never in storage.
 */
export function mfaChallengeOf(err: unknown): MfaChallenge | null {
  if (serverErrorCode(err) !== 'MFA_CHALLENGE') return null;
  const ext = (err as GraphQLRequestError).errors?.[0]?.extensions as Record<string, unknown> | undefined;
  const challenge = ext?.challenge;
  const expiresAt = ext?.expiresAt;
  if (typeof challenge !== 'string' || challenge === '' || typeof expiresAt !== 'string') return null;
  const methods = Array.isArray(ext?.methods)
    ? (ext.methods as unknown[]).filter((m): m is string => typeof m === 'string')
    : [];
  return { challenge, expiresAt, methods };
}

// 🔴 THE ONE PLACE THIS FEATURE MATCHES PROSE. verifyMfa's per-source rate limit is the only
// refusal in the feature with no `extensions.code` (every other failure of a code check is
// deliberately the same uniform "invalid code"). Until the server gives it a code, the exact
// message is the only way to tell "slow down" from "wrong code". If the match goes stale the
// rate limit reads as "invalid code", which is the safe direction.
const RATE_LIMITED_MESSAGE = 'too many attempts; try again shortly';

/**
 * The catalog key for a failed verifyMfa. A wrong, replayed or locked code are ONE answer on
 * the server and one message here; an expired challenge means signing in again; a rate limit
 * and an outage read as themselves.
 */
export function mfaVerifyErrorKey(err: unknown): string {
  switch (serverErrorCode(err)) {
    case 'MFA_CHALLENGE_EXPIRED':
      return 'signIn:mfaChallengeExpired';
    case 'THROTTLED':
      return 'signIn:tooManyAttempts';
    case 'UNAVAILABLE':
      return 'signIn:signInUnavailable';
    case undefined:
      if (
        err instanceof GraphQLRequestError &&
        err.errors?.[0]?.message?.trim().toLowerCase() === RATE_LIMITED_MESSAGE
      ) {
        return 'signIn:mfaRateLimited';
      }
  }
  return serverRejectedRequest(err) ? 'signIn:mfaInvalidCode' : 'signIn:serverUnreachable';
}

/** The machine-readable code on the server's first error, when it sent one. */
export function serverErrorCode(err: unknown): string | undefined {
  if (!(err instanceof GraphQLRequestError)) return undefined;
  const code = err.errors?.[0]?.extensions?.code;
  return typeof code === 'string' ? code : undefined;
}

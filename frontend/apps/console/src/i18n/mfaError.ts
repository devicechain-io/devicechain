// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Which message the two-factor screens show when a call fails. Same contract as
// signInError.ts (did the server EVALUATE the request, or did it never answer?), with the
// codes this feature adds.
import { GraphQLRequestError } from '@devicechain/client';
import { MFA_CODE } from '@/lib/api/mfa';
import { serverErrorCode, serverRejectedRequest } from '@/i18n/signInError';

// 🔴 THE ONE PLACE THIS FEATURE MATCHES PROSE. verifyMfa's per-source rate limit is the only
// refusal in the feature that carries no `extensions.code` (every other failure of a code
// check is deliberately the same uniform "invalid code"). Until the server gives it a code,
// the exact message is the only way to tell "slow down" from "wrong code". The failure mode of
// the match going stale is benign and in the safe direction: the rate limit then reads as
// "invalid code", which is what the server would have said for a locked account anyway.
const RATE_LIMITED_MESSAGE = 'too many attempts; try again shortly';

/** True when verifyMfa refused because its source address sent too many attempts. */
export function isMfaRateLimited(err: unknown): boolean {
  if (!(err instanceof GraphQLRequestError) || serverErrorCode(err) !== undefined) return false;
  return err.errors?.[0]?.message?.trim().toLowerCase() === RATE_LIMITED_MESSAGE;
}

/**
 * The catalog key for a failed two-factor call. `rejectedKey` is what to say when the server
 * evaluated the request and refused it without a code of its own (a wrong password, a wrong
 * code): the server deliberately does not say which, and neither does this.
 */
export function mfaErrorKey(err: unknown, rejectedKey: string): string {
  switch (serverErrorCode(err)) {
    case MFA_CODE.challengeExpired:
      return 'security:challengeExpired';
    case MFA_CODE.alreadyEnrolled:
      return 'security:alreadyEnrolled';
    case MFA_CODE.notEnrolled:
      return 'security:notEnrolled';
    case 'THROTTLED':
      return 'login:tooManyAttempts';
    case 'UNAVAILABLE':
      return 'login:signInUnavailable';
  }
  if (isMfaRateLimited(err)) return 'login:mfaRateLimited';
  return serverRejectedRequest(err) ? rejectedKey : 'login:serverUnreachable';
}

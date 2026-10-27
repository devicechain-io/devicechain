// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the sign-in screens need to recognise a held sign-in, kept apart from lib/api/mfa.ts
// (which carries every two-factor GraphQL document) so importing it costs nothing.
import type { GraphQLRequestError } from '@devicechain/client';
import { serverErrorCode } from '@/i18n/signInError';

/** The error codes this feature adds, matched on `extensions.code` and never on the prose. */
export const MFA_CODE = {
  /** login held the sign-in for a code: ask for it. */
  challenge: 'MFA_CHALLENGE',
  /** The challenge lapsed or was used up: sign in again. */
  challengeExpired: 'MFA_CHALLENGE_EXPIRED',
  /** The tenant needs a second factor and this sign-in had none, but the account has one. */
  required: 'MFA_REQUIRED',
  /** The tenant needs a second factor and the account has none: send them to set one up. */
  enrollmentRequired: 'MFA_ENROLLMENT_REQUIRED',
  alreadyEnrolled: 'MFA_ALREADY_ENROLLED',
  notEnrolled: 'MFA_NOT_ENROLLED',
  /** Resetting a factor with failures on record needs a new password with it. */
  resetNeedsPassword: 'MFA_RESET_NEEDS_PASSWORD',
} as const;

/** What login's MFA_CHALLENGE error carries in its extensions. */
export interface MfaChallenge {
  challenge: string;
  /** RFC 3339; the challenge accepts attempts until then. */
  expiresAt: string;
  /** `TOTP` and `RECOVERY_CODE`. */
  methods: string[];
}

/**
 * The challenge login held the sign-in for, or null when the error is anything else. The
 * handle is read from the error's extensions and nowhere else, and it never goes to a URL
 * or to storage.
 */
export function mfaChallengeOf(err: unknown): MfaChallenge | null {
  if (serverErrorCode(err) !== MFA_CODE.challenge) return null;
  const ext = (err as GraphQLRequestError).errors?.[0]?.extensions as Record<string, unknown> | undefined;
  const challenge = ext?.challenge;
  const expiresAt = ext?.expiresAt;
  if (typeof challenge !== 'string' || challenge === '' || typeof expiresAt !== 'string') return null;
  const methods = Array.isArray(ext?.methods)
    ? (ext.methods as unknown[]).filter((m): m is string => typeof m === 'string')
    : [];
  return { challenge, expiresAt, methods };
}

/** The server's challenge lifetime: five minutes from when login issues it. */
export const CHALLENGE_TTL_MS = 5 * 60_000;

/**
 * How long, from the moment the challenge was RECEIVED, to wait before telling the person it
 * has probably lapsed. A browser's clock can be minutes off the server's, so `expiresAt` is
 * never compared with Date.now() to REFUSE anything (the server decides, and answers
 * MFA_CHALLENGE_EXPIRED); it only shortens this notice when it plainly says less than the
 * full lifetime remains. A time already in the past, or unparseable, means the clock is
 * ahead (or the value junk), so the full lifetime applies. Never longer than the lifetime.
 */
export function challengeNoticeDelayMs(expiresAt: string, now: number = Date.now()): number {
  const left = Date.parse(expiresAt) - now;
  return Number.isFinite(left) && left > 0 ? Math.min(left, CHALLENGE_TTL_MS) : CHALLENGE_TTL_MS;
}

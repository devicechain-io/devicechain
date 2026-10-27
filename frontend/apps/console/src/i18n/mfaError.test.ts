// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { GraphQLRequestError } from '@devicechain/client';
import { describe, expect, it } from 'vitest';

import { MFA_CODE, mfaChallengeOf } from '@/lib/api/mfa';
import { isMfaRateLimited, mfaErrorKey } from './mfaError';
import { signInErrorKey } from './signInError';

const coded = (code: string, extra: Record<string, unknown> = {}) =>
  new GraphQLRequestError(code, 200, [{ message: code, extensions: { code, ...extra } }]);
const uncoded = (message: string) => new GraphQLRequestError(message, 200, [{ message }]);

describe('mfaChallengeOf', () => {
  it('reads the handle, expiry and methods from the extensions', () => {
    const err = coded('MFA_CHALLENGE', { challenge: 'c1', expiresAt: '2026-10-27T10:00:00Z', methods: ['TOTP', 'RECOVERY_CODE'] });
    expect(mfaChallengeOf(err)).toEqual({
      challenge: 'c1',
      expiresAt: '2026-10-27T10:00:00Z',
      methods: ['TOTP', 'RECOVERY_CODE'],
    });
  });

  it('is null for any other error, and for a challenge with no handle', () => {
    expect(mfaChallengeOf(coded('THROTTLED'))).toBeNull();
    expect(mfaChallengeOf(new Error('x'))).toBeNull();
    expect(mfaChallengeOf(coded('MFA_CHALLENGE', { expiresAt: 'x' }))).toBeNull();
    expect(mfaChallengeOf(coded('MFA_CHALLENGE', { challenge: '', expiresAt: 'x' }))).toBeNull();
  });
});

describe('mfaErrorKey', () => {
  const REJECTED = 'login:mfaInvalidCode';

  it('is the same answer for every uncoded refusal of a code', () => {
    expect(mfaErrorKey(uncoded('invalid code'), REJECTED)).toBe(REJECTED);
    expect(mfaErrorKey(uncoded('anything else the server says'), REJECTED)).toBe(REJECTED);
  });

  it('reads the per-source rate limit as one', () => {
    const limited = uncoded('too many attempts; try again shortly');
    expect(isMfaRateLimited(limited)).toBe(true);
    expect(mfaErrorKey(limited, REJECTED)).toBe('login:mfaRateLimited');
  });

  it('does not take a CODED error for the rate limit, whatever its text', () => {
    expect(isMfaRateLimited(coded('FORBIDDEN'))).toBe(false);
  });

  it('maps the codes this feature adds', () => {
    expect(mfaErrorKey(coded(MFA_CODE.challengeExpired), REJECTED)).toBe('security:challengeExpired');
    expect(mfaErrorKey(coded(MFA_CODE.alreadyEnrolled), REJECTED)).toBe('security:alreadyEnrolled');
    expect(mfaErrorKey(coded(MFA_CODE.notEnrolled), REJECTED)).toBe('security:notEnrolled');
    expect(mfaErrorKey(coded('THROTTLED'), REJECTED)).toBe('login:tooManyAttempts');
    expect(mfaErrorKey(coded('UNAVAILABLE'), REJECTED)).toBe('login:signInUnavailable');
  });

  it('says unreachable, never "wrong code", when the request got no answer', () => {
    expect(mfaErrorKey(new GraphQLRequestError('Failed to fetch', 0), REJECTED)).toBe('login:serverUnreachable');
    expect(mfaErrorKey(new GraphQLRequestError('Request failed (503)', 503), REJECTED)).toBe('login:serverUnreachable');
  });
});

describe('signInErrorKey with the tenant requirement', () => {
  it('maps MFA_REQUIRED and MFA_ENROLLMENT_REQUIRED to their own messages', () => {
    expect(signInErrorKey(coded('MFA_REQUIRED'), 'x')).toBe('login:mfaRequired');
    expect(signInErrorKey(coded('MFA_ENROLLMENT_REQUIRED'), 'x')).toBe('login:mfaEnrollmentRequired');
  });
});

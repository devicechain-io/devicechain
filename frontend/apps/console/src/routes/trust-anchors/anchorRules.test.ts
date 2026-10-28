// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import {
  EXPIRY_WARN_DAYS,
  certificateCount,
  checkPem,
  classifyX509Error,
  daysUntil,
  deleteBlockers,
  derivedDeviceToken,
  isAnchorExpired,
} from './anchorRules';

const CERT = '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----';

const refusal = (code: string, message = 'refused') =>
  new GraphQLRequestError(message, 200, [{ message, extensions: { code } }]);

describe('checkPem', () => {
  it('refuses empty text', () => {
    expect(checkPem('  \n', { exactlyOne: true })).toBe('pemEmpty');
  });

  it('refuses a private key before it can be sent, even with a certificate beside it', () => {
    const key = '-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----';
    expect(checkPem(key, { exactlyOne: true })).toBe('pemLooksLikeKey');
    expect(checkPem(`${CERT}\n${key}`, { exactlyOne: false })).toBe('pemLooksLikeKey');
    expect(checkPem('-----BEGIN RSA PRIVATE KEY-----\nAA', { exactlyOne: false })).toBe('pemLooksLikeKey');
  });

  it('refuses text that is not a certificate', () => {
    expect(checkPem('hello', { exactlyOne: false })).toBe('pemNotCertificate');
  });

  it('refuses an oversized paste', () => {
    expect(checkPem(`${CERT}\n${'A'.repeat(70 * 1024)}`, { exactlyOne: false })).toBe('pemTooLarge');
  });

  it('holds an anchor to exactly one certificate and lets a device chain carry intermediates', () => {
    const two = `${CERT}\n${CERT}`;
    expect(checkPem(two, { exactlyOne: true })).toBe('pemNeedsOne');
    expect(checkPem(two, { exactlyOne: false })).toBeNull();
    expect(checkPem(CERT, { exactlyOne: true })).toBeNull();
    expect(certificateCount(two)).toBe(2);
  });
});

describe('deleteBlockers', () => {
  it('is null only when nothing relies on the anchor', () => {
    expect(deleteBlockers({ credentialCount: 0, profileCount: 0 })).toBeNull();
  });

  it('reports each kind of reliance, so either one alone blocks the delete', () => {
    expect(deleteBlockers({ credentialCount: 2, profileCount: 0 })).toEqual({ credentials: 2, profiles: 0 });
    expect(deleteBlockers({ credentialCount: 0, profileCount: 1 })).toEqual({ credentials: 0, profiles: 1 });
  });
});

describe('classifyX509Error', () => {
  it('picks the explanation by the server code, never by its text', () => {
    expect(classifyX509Error(refusal('REFERENCE_VIOLATION', 'whatever'), 'x')).toEqual({ key: 'errInUse' });
    expect(classifyX509Error(refusal('CONFLICT'), 'x')).toEqual({ key: 'errConflict' });
    expect(classifyX509Error(refusal('NOT_FOUND'), 'x')).toEqual({ key: 'errNotFound' });
  });

  it('keeps the server sentence for the families where the specifics matter', () => {
    expect(classifyX509Error(refusal('INVALID_VALUE', 'the certificate has expired'), 'the certificate has expired')).toEqual({
      key: 'errInvalid',
      detail: 'the certificate has expired',
    });
    expect(classifyX509Error(refusal('LIMIT_EXCEEDED'), 'at most 32')).toEqual({ key: 'errLimit', detail: 'at most 32' });
  });

  it('passes anything else through as it was said', () => {
    expect(classifyX509Error(new Error('boom'), 'boom')).toEqual({ key: 'errOther', detail: 'boom' });
  });
});

describe('expiry', () => {
  const now = Date.parse('2026-10-28T00:00:00Z');

  it('counts whole days to expiry and goes negative once past', () => {
    expect(daysUntil('2026-11-27T00:00:00Z', now)).toBe(30);
    expect(daysUntil('2026-10-27T00:00:00Z', now)).toBe(-1);
    expect(daysUntil(null, now)).toBeNull();
    expect(daysUntil('not a date', now)).toBeNull();
    expect(EXPIRY_WARN_DAYS).toBe(30);
  });

  it('calls an anchor expired from its notAfter', () => {
    expect(isAnchorExpired('2026-10-27T00:00:00Z', now)).toBe(true);
    expect(isAnchorExpired('2026-10-29T00:00:00Z', now)).toBe(false);
  });
});

describe('derivedDeviceToken', () => {
  it('is pv- plus the common name', () => {
    expect(derivedDeviceToken('sensor-001')).toBe('pv-sensor-001');
    expect(derivedDeviceToken('  sensor-001 ')).toBe('pv-sensor-001');
  });
});

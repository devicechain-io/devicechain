// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  GENERATED_PASSWORD_BYTES,
  MIN_PASSWORD_BYTES,
  generatePassword,
  passwordBelowMinimum,
  passwordByteLength,
} from './credentialPassword';

function decodeBase64url(s: string): Uint8Array {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4);
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

describe('generatePassword', () => {
  it('encodes at least 24 random bytes as unpadded base64url', () => {
    const pw = generatePassword();
    expect(pw).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(decodeBase64url(pw).length).toBeGreaterThanOrEqual(24);
    expect(GENERATED_PASSWORD_BYTES).toBeGreaterThanOrEqual(24);
  });

  it('draws from the browser CSPRNG, not a constant', () => {
    const seen = new Set(Array.from({ length: 50 }, () => generatePassword()));
    expect(seen.size).toBe(50);
    // A weak source would leave long runs of one byte; 24 random bytes essentially never do.
    expect(new Set(decodeBase64url(generatePassword())).size).toBeGreaterThan(8);
  });

  it('passes the length hint', () => {
    expect(passwordBelowMinimum(generatePassword())).toBe(false);
  });
});

describe('the length hint counts UTF-8 bytes, not characters', () => {
  it('flags a short ASCII password and not a 16-byte one', () => {
    expect(passwordBelowMinimum('a'.repeat(MIN_PASSWORD_BYTES - 1))).toBe(true);
    expect(passwordBelowMinimum('a'.repeat(MIN_PASSWORD_BYTES))).toBe(false);
  });

  it('accepts 8 characters that are 24 bytes', () => {
    const pw = '€'.repeat(8); // 8 characters, 3 bytes each
    expect(pw.length).toBe(8);
    expect(passwordByteLength(pw)).toBe(24);
    expect(passwordBelowMinimum(pw)).toBe(false);
  });

  it('counts a two-byte character as two', () => {
    expect(passwordBelowMinimum('é'.repeat(7))).toBe(true); // 7 chars, 14 bytes
    expect(passwordBelowMinimum('é'.repeat(8))).toBe(false); // 16 bytes
  });

  it('does not nag about an empty field', () => {
    expect(passwordBelowMinimum('')).toBe(false);
  });
});

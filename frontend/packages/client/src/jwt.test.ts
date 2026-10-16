// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { decodeToken, isScoped, scopeAnchorRefs } from './jwt';

// A JWT with the given payload; the signature is irrelevant to a client-side decode.
function jwt(payload: Record<string, unknown>): string {
  const b64 = (o: unknown) => btoa(JSON.stringify(o)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
  return `${b64({ alg: 'none' })}.${b64(payload)}.sig`;
}

describe('scope claim', () => {
  it('reads the membership scope from scp, not from the OAuth scope claim', () => {
    const claims = decodeToken(
      jwt({ tenant: 'oem', authorities: ['device:read'], scp: ['customer:acme', 'customer:globex'], scope: 'mcp:read' }),
    );
    expect(claims?.scopeAnchors).toEqual(['customer:acme', 'customer:globex']);
    expect(isScoped(claims)).toBe(true);
    expect(scopeAnchorRefs(claims)).toEqual([
      { kind: 'customer', token: 'acme' },
      { kind: 'customer', token: 'globex' },
    ]);
  });

  it('is unscoped when scp is absent, even when an OAuth scope is present', () => {
    const claims = decodeToken(jwt({ tenant: 'oem', authorities: ['*'], scope: 'mcp:read' }));
    expect(claims?.scopeAnchors).toEqual([]);
    expect(isScoped(claims)).toBe(false);
    expect(isScoped(null)).toBe(false);
  });

  it('keeps a token whose own text holds a colon whole', () => {
    const claims = decodeToken(jwt({ scp: ['customer:a:b', 'malformed'] }));
    expect(scopeAnchorRefs(claims)).toEqual([{ kind: 'customer', token: 'a:b' }]);
  });
});

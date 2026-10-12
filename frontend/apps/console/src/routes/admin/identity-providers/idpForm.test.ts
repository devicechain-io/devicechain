// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The create form's rules, on their own. The instance-connection rule is the one with
// consequences: an instance connection can reach a superuser, so the form must not let one
// through without a required authentication method or the operator's recorded statement.

import { describe, expect, it } from 'vitest';
import {
  createRequest,
  needsReapproval,
  newCreateState,
  splitList,
  validateCreate,
  type IdpCreateState,
} from './idpForm';

function valid(patch: Partial<IdpCreateState> = {}): IdpCreateState {
  return {
    ...newCreateState(),
    token: 'acme-sso',
    name: 'Acme',
    issuer: 'https://login.example.com',
    clientId: 'client-1',
    ...patch,
  };
}

describe('validateCreate', () => {
  it('accepts a well-formed tenant connection', () => {
    expect(validateCreate(valid())).toBeNull();
  });

  it.each<[Partial<IdpCreateState>, string]>([
    [{ token: '' }, 'tokenRequiredError'],
    [{ token: 'has space' }, 'tokenInvalidError'],
    [{ token: '-leading' }, 'tokenInvalidError'],
    [{ name: '  ' }, 'nameRequiredError'],
    [{ issuer: '' }, 'issuerRequiredError'],
    [{ issuer: 'http://login.example.com' }, 'issuerNotHttpsError'],
    [{ clientId: '' }, 'clientIdRequiredError'],
    [{ claims: [{ name: 'hd', value: '' }] }, 'claimIncompleteError'],
  ])('refuses %j with %s', (patch, key) => {
    expect(validateCreate(valid(patch))).toBe(key);
  });

  it('requires an instance connection to carry amr or the MFA acknowledgement', () => {
    expect(validateCreate(valid({ kind: 'instance' }))).toBe('instanceNeedsMfaError');
    expect(validateCreate(valid({ kind: 'instance', requiredAmr: 'mfa' }))).toBeNull();
    expect(validateCreate(valid({ kind: 'instance', mfaEnforcedByIdp: true }))).toBeNull();
  });

  it('does not ask a tenant connection for either', () => {
    expect(validateCreate(valid({ kind: 'tenant' }))).toBeNull();
  });
});

describe('createRequest', () => {
  it('omits blank optionals so server defaults apply, and the secret when empty', () => {
    const req = createRequest(valid());
    expect(req).toEqual({
      token: 'acme-sso',
      name: 'Acme',
      kind: 'tenant',
      issuer: 'https://login.example.com',
      clientId: 'client-1',
    });
    expect('clientSecret' in req).toBe(false);
  });

  it('sends the secret, lists and claims when given', () => {
    const req = createRequest(
      valid({
        clientSecret: 's3cret',
        scopes: 'openid, email',
        requiredAmr: 'mfa hwk',
        emailClaim: 'upn',
        claims: [{ name: ' hd ', value: ' example.com ' }],
      }),
    );
    expect(req.clientSecret).toBe('s3cret');
    expect(req.scopes).toEqual(['openid', 'email']);
    expect(req.requiredAmr).toEqual(['mfa', 'hwk']);
    expect(req.emailClaim).toBe('upn');
    expect(req.requiredClaims).toEqual([{ name: 'hd', value: 'example.com' }]);
  });

  it('sends the MFA acknowledgement only for an instance connection with no required methods', () => {
    expect(createRequest(valid({ kind: 'instance', mfaEnforcedByIdp: true })).mfaEnforcedByIdp).toBe(true);
    // Stale tick after switching back to tenant, or once methods are required: not sent.
    expect(createRequest(valid({ kind: 'tenant', mfaEnforcedByIdp: true })).mfaEnforcedByIdp).toBeUndefined();
    expect(
      createRequest(valid({ kind: 'instance', requiredAmr: 'mfa', mfaEnforcedByIdp: true }))
        .mfaEnforcedByIdp,
    ).toBeUndefined();
  });
});

describe('splitList', () => {
  it('splits on commas and whitespace and drops blanks and repeats', () => {
    expect(splitList(' a,b  c,, a ')).toEqual(['a', 'b', 'c']);
    expect(splitList('')).toEqual([]);
  });
});

describe('needsReapproval', () => {
  it('is true only for an enabled connection whose snapshot is unapproved', () => {
    expect(needsReapproval({ enabled: true, discovery: { approved: false } })).toBe(true);
    expect(needsReapproval({ enabled: true, discovery: { approved: true } })).toBe(false);
    // A brand-new connection: disabled, never approved — first approval, not re-approval.
    expect(needsReapproval({ enabled: false, discovery: { approved: false } })).toBe(false);
  });
});

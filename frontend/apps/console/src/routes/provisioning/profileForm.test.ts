// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The partial-update rule of the provisioning-profile form: an edit names ONLY what changed.
// updateProvisioningProfile omits = leaves alone, so a request that restated an unchanged or
// hidden field would write a stale snapshot back over whatever another operator did since.

import { describe, expect, it } from 'vitest';
import type { ProvisioningProfile } from '@/lib/api/provisioning';
import {
  draftOf,
  emptyDraft,
  fromLocalInput,
  isExpired,
  parseMaxDevices,
  profileCreateRequest,
  profileEditRequest,
  profileProblems,
  toLocalInput,
} from './profileForm';

function profile(over: Partial<ProvisioningProfile> = {}): ProvisioningProfile {
  return {
    id: 'p-1',
    token: 'acme-fleet',
    name: 'Acme fleet',
    description: 'Dock sensors',
    provisionKey: 'acme-sensors-2026',
    strategy: 'CHECK_PRE_PROVISIONED',
    credentialType: 'ACCESS_TOKEN',
    enabled: true,
    expiresAt: '2027-01-02T03:04:05Z',
    maxDevices: 50,
    provisionedDeviceCount: 7,
    secretIssued: true,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-02T00:00:00Z',
    deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
    ...over,
  } as ProvisioningProfile;
}

describe('profileEditRequest', () => {
  it('sends nothing when nothing changed', () => {
    const initial = draftOf(profile());
    expect(profileEditRequest(initial, { ...initial })).toEqual({});
  });

  it('names only the field that changed, and never the ones the form does not show', () => {
    const initial = draftOf(profile());
    const request = profileEditRequest(initial, { ...initial, name: 'Acme fleet (north)' });
    expect(request).toEqual({ name: 'Acme fleet (north)' });
    // The hidden fields of the contract: metadata and the credential type are not inputs.
    for (const key of ['metadata', 'credentialType', 'provisionSecret', 'expiresAt', 'maxDevices', 'enabled']) {
      expect(key in request, key).toBe(false);
    }
  });

  it('does not restate an unchanged expiry when the rest is renamed (a datetime-local round trip)', () => {
    // The stored instant has seconds; the input holds minutes. A save that re-sent the input's
    // value would silently move the expiry by those seconds.
    const initial = draftOf(profile({ expiresAt: '2027-01-02T03:04:59Z' }));
    const request = profileEditRequest(initial, { ...initial, description: 'Renamed' });
    expect('expiresAt' in request).toBe(false);
  });

  it('clears what the operator emptied, with an explicit null', () => {
    const initial = draftOf(profile());
    const request = profileEditRequest(initial, { ...initial, description: '  ', expiresAt: '', maxDevices: '' });
    expect(request).toEqual({ description: null, expiresAt: null, maxDevices: null });
  });

  it('sets a cap, a strategy, a type and the enabled flag when they change', () => {
    const initial = draftOf(profile());
    const request = profileEditRequest(initial, {
      ...initial,
      maxDevices: ' 120 ',
      strategy: 'ALLOW_NEW',
      deviceTypeToken: 'sensor',
      enabled: false,
    });
    expect(request).toEqual({ maxDevices: 120, strategy: 'ALLOW_NEW', deviceTypeToken: 'sensor', enabled: false });
  });

  it('sets a new expiry as an RFC 3339 instant', () => {
    const initial = draftOf(profile({ expiresAt: null }));
    const request = profileEditRequest(initial, { ...initial, expiresAt: '2030-05-06T07:08' });
    expect(request.expiresAt).toBe(new Date('2030-05-06T07:08').toISOString());
  });

  it('sends the provision key exactly as typed', () => {
    const initial = draftOf(profile());
    expect(profileEditRequest(initial, { ...initial, provisionKey: ' padded-key ' })).toEqual({
      provisionKey: ' padded-key ',
    });
  });
});

describe('profileCreateRequest', () => {
  it('carries what the server needs and leaves the secret, metadata and credential type to it', () => {
    const request = profileCreateRequest(' acme-fleet ', {
      ...emptyDraft(),
      name: 'Acme',
      provisionKey: 'acme-sensors-2026',
      deviceTypeToken: 'gateway',
      strategy: 'ALLOW_NEW',
      maxDevices: '10',
    });
    expect(request).toEqual({
      token: 'acme-fleet',
      name: 'Acme',
      description: null,
      provisionKey: 'acme-sensors-2026',
      strategy: 'ALLOW_NEW',
      deviceTypeToken: 'gateway',
      enabled: true,
      maxDevices: 10,
    });
    expect('provisionSecret' in request).toBe(false);
    expect('metadata' in request).toBe(false);
    expect('credentialType' in request).toBe(false);
  });

  it('omits an empty cap and expiry rather than sending an empty value', () => {
    const request = profileCreateRequest('p', { ...emptyDraft(), provisionKey: 'k', deviceTypeToken: 't' });
    expect('maxDevices' in request).toBe(false);
    expect('expiresAt' in request).toBe(false);
  });
});

describe('profileProblems', () => {
  it('flags a missing key and type, and a cap that is not a whole number of at least one', () => {
    expect(profileProblems(emptyDraft())).toEqual(['key', 'type']);
    const ok = { ...emptyDraft(), provisionKey: 'k', deviceTypeToken: 't' };
    expect(profileProblems(ok)).toEqual([]);
    for (const bad of ['0', '-3', '1.5', 'many']) {
      expect(profileProblems({ ...ok, maxDevices: bad }), bad).toEqual(['cap']);
    }
    expect(profileProblems({ ...ok, maxDevices: '' })).toEqual([]);
  });
});

describe('small helpers', () => {
  it('reads the cap', () => {
    expect(parseMaxDevices('')).toBeNull();
    expect(parseMaxDevices(' 5 ')).toBe(5);
    expect(parseMaxDevices('0')).toBeUndefined();
  });

  it('round-trips an instant through the input without changing the minute', () => {
    const iso = '2027-01-02T03:04:00.000Z';
    expect(fromLocalInput(toLocalInput(iso))).toBe(iso);
    expect(toLocalInput(null)).toBe('');
    expect(fromLocalInput('')).toBeNull();
  });

  it('knows when an expiry has passed', () => {
    expect(isExpired('2020-01-01T00:00:00Z', Date.parse('2026-01-01T00:00:00Z'))).toBe(true);
    expect(isExpired('2030-01-01T00:00:00Z', Date.parse('2026-01-01T00:00:00Z'))).toBe(false);
    expect(isExpired(null)).toBe(false);
  });
});

describe('a certificate profile draft', () => {
  const certProfile = profile({
    trustAnchor: { token: 'acme-ca', name: 'Acme CA' },
    secretIssued: false,
  });

  it('is seeded as a certificate profile naming its anchor', () => {
    const d = draftOf(certProfile);
    expect(d.certificate).toBe(true);
    expect(d.trustAnchorToken).toBe('acme-ca');
    expect(draftOf(profile()).certificate).toBe(false);
  });

  it('needs an anchor and no provision key', () => {
    const d = { ...emptyDraft(), deviceTypeToken: 'gateway', certificate: true };
    expect(profileProblems(d)).toEqual(['anchor']);
    expect(profileProblems({ ...d, trustAnchorToken: 'acme-ca' })).toEqual([]);
  });

  it('creates with the anchor and the certificate credential type, never a secret', () => {
    const d = { ...emptyDraft(), deviceTypeToken: 'gateway', certificate: true, trustAnchorToken: 'acme-ca' };
    const r = profileCreateRequest('cert-fleet', d);
    expect(r.trustAnchorToken).toBe('acme-ca');
    expect(r.credentialType).toBe('X509_CERTIFICATE');
    expect(r.provisionKey).toBe('cert-fleet');
    expect(Object.keys(r)).not.toContain('provisionSecret');
  });

  it('sends no trust anchor for a secret profile', () => {
    const r = profileCreateRequest('p', { ...emptyDraft(), deviceTypeToken: 'gateway', provisionKey: 'k' });
    expect('trustAnchorToken' in r).toBe(false);
  });

  it('an edit names the anchor only when it changed, and never the hidden key', () => {
    const initial = draftOf(certProfile);
    expect(profileEditRequest(initial, initial)).toEqual({});
    expect(profileEditRequest(initial, { ...initial, trustAnchorToken: 'other-ca' })).toEqual({ trustAnchorToken: 'other-ca' });
    // The key field is not shown for a certificate profile, so a stray change to it is not sent.
    expect(profileEditRequest(initial, { ...initial, provisionKey: 'changed' })).toEqual({});
  });

  it('an edit of a secret profile never names an anchor', () => {
    const initial = draftOf(profile());
    expect(profileEditRequest(initial, { ...initial, trustAnchorToken: 'sneaky' })).toEqual({});
  });
});

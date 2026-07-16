// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { areaPath, isIdentityArea, type Area } from './transport';

describe('areaPath', () => {
  it('routes an area to its ingress path', () => {
    expect(areaPath('device-management')).toBe('/api/device-management/graphql');
  });

  it('carries an admin area through as a second path on the same service', () => {
    // The ingress matches /api/<area-segment>(/|$)(.*) and rewrites to $2, so the
    // "/admin" here lands as /admin/graphql on the user-management Service. Nothing
    // in the ingress knows the admin plane exists — this is why adding one costs no
    // deploy change.
    expect(areaPath('user-management/admin')).toBe('/api/user-management/admin/graphql');
    expect(areaPath('ai-inference/admin')).toBe('/api/ai-inference/admin/graphql');
  });
});

describe('isIdentityArea', () => {
  // This is a security default, not a convenience. An /admin endpoint accepts ONLY
  // an identity token (ADR-033), so sending a tenant access token there is never
  // right. Deriving the lane from the area is what stops a new admin call from
  // silently authenticating with the wrong credential — the mistake is easy to make
  // because the area string and the token choice used to be independent, and it
  // fails as a 401 far from its cause.
  it('puts every admin and settings lane on the identity token', () => {
    const identityAreas: Area[] = [
      'user-management/admin',
      'user-management/settings',
      'ai-inference/admin',
    ];
    for (const area of identityAreas) {
      expect(isIdentityArea(area), `${area} must authenticate with the identity token`).toBe(true);
    }
  });

  it('leaves every tenant data plane on the tenant access token', () => {
    const tenantAreas: Area[] = [
      'user-management',
      'device-management',
      'event-management',
      'event-processing',
      'device-state',
      'command-delivery',
      'dashboard-management',
      'outbound-connectors',
    ];
    for (const area of tenantAreas) {
      expect(isIdentityArea(area), `${area} must authenticate with the tenant access token`).toBe(
        false,
      );
    }
  });

  it('keys off the lane suffix, not the service name', () => {
    // user-management serves BOTH lanes, so the service name cannot decide this —
    // only the area's suffix can.
    expect(isIdentityArea('user-management')).toBe(false);
    expect(isIdentityArea('user-management/admin')).toBe(true);
  });
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { mayWrite, scopedRouteAllowed } from './scope';

describe('the routes a scoped member may open', () => {
  it('admits home, devices, alarms and dashboards, list and detail', () => {
    for (const path of ['/', '/devices', '/devices/pump-7', '/devices/pump-7/', '/alarms', '/dashboards', '/dashboards/fleet']) {
      expect(scopedRouteAllowed(path), path).toBe(true);
    }
  });

  // Every one of these is a real route in App.tsx whose page reads something the server
  // refuses a scoped member; reaching it by a bookmark must send them home.
  it('refuses every configuration and administration route', () => {
    for (const path of [
      '/device-types',
      '/device-profiles/p1',
      '/provisioning-profiles',
      '/provisioning-profiles/acme-sensors',
      '/customers',
      '/customers/acme',
      '/members',
      '/roles',
      '/roles/new',
      '/audit',
      '/branding',
      '/connectors',
      '/command-batches',
      '/updates/artifacts',
      '/browse',
      '/devicesx',
      '/devices/a/b',
    ]) {
      expect(scopedRouteAllowed(path), path).toBe(false);
    }
  });
});

describe('mayWrite', () => {
  it('needs the authority and no scope', () => {
    expect(mayWrite({ tenant: 't', username: 'u', roles: [], authorities: ['*'], typ: 'access' }, 'dashboard:write')).toBe(true);
    expect(
      mayWrite({ tenant: 't', username: 'u', roles: [], authorities: ['*'], typ: 'access', scopeAnchors: ['customer:acme'] }, 'dashboard:write'),
    ).toBe(false);
    expect(mayWrite({ tenant: 't', username: 'u', roles: [], authorities: ['dashboard:read'], typ: 'access' }, 'dashboard:write')).toBe(false);
    expect(mayWrite(null, 'dashboard:write')).toBe(false);
  });
});

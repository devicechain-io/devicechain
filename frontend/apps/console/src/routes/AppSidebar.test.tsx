// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The sidebar's authority gating, and specifically the Settings group.
//
// 🔴 THIS EXISTS BECAUSE THE GATING IS THE ARGUMENT. Settings is a sidebar group
// rather than a tabbed page for one concrete reason: `branding:write` and
// `basemap:write` are deliberately SEPARATE grants — bundling them would make each
// imply the other, which is the entire reason basemap:write exists — and a group gets
// every combination right through machinery that already exists (`visibleNav` filters
// children by `requires` and drops a group left with none).
//
// An argument of the form "this shape handles the permission cases correctly" is worth
// nothing unasserted. Each combination below is one the reasoning claimed.
//
// The sidebar had NO test at all before this, so the CRUD-side control at the bottom
// is not ceremony: without it, "renders nothing for anyone" would satisfy most of the
// cases above it.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { authorities, scope, area } = vi.hoisted(() => ({
  authorities: { value: [] as string[] },
  // The membership's scope anchors (the `scp` claim); empty means the whole tenant.
  scope: { value: [] as string[] },
  // What area discovery answered for update-management; the Updates group reads it.
  area: { status: 'unknown' as 'available' | 'absent' | 'unknown' },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value, scopeAnchors: scope.value } }),
}));
vi.mock('@/auth/TenantProvider', () => ({
  useCurrentTenant: () => ({ token: 'acme', branding: null }),
}));
vi.mock('@/lib/capabilities', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/capabilities')>();
  return { ...actual, useAreaStatus: () => area.status };
});
vi.mock('@/lib/useBrandingLogo', () => ({ useBrandingLogoSrc: () => null }));
vi.mock('@/routes/NavUser', () => ({ NavUser: () => null }));

import { SidebarProvider } from '@/components/ui/sidebar';
import { AppSidebar } from './AppSidebar';

afterEach(cleanup);
beforeEach(() => {
  authorities.value = [];
  scope.value = [];
  area.status = 'unknown';
});

// The sidebar is an accordion: a group renders its label always, and its CHILDREN
// only while expanded. It auto-expands the group owning the current route, so landing
// on a route inside Settings is both how a deep link behaves and the only way to see
// the children — which makes `at` a real part of the fixture rather than a detail.
function renderWith(auths: string[], at = '/') {
  authorities.value = auths;
  render(
    <MemoryRouter initialEntries={[at]}>
      <SidebarProvider>
        <AppSidebar />
      </SidebarProvider>
    </MemoryRouter>,
  );
}

// Nav entries are rendered as their localized label; matched exactly so "Map" cannot
// be satisfied by "Basemap" or by a map-adjacent entry elsewhere in the tree.
const entry = (label: string) => screen.queryByText(label, { exact: true });

describe('the Settings group', () => {
  it('shows every child to someone holding all three authorities', () => {
    renderWith(['branding:write', 'basemap:write', 'locale:write'], '/branding');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Branding')).toBeTruthy();
    expect(entry('Map')).toBeTruthy();
    expect(entry('Language')).toBeTruthy();
  });

  it('shows only Branding to a holder of branding:write', () => {
    renderWith(['branding:write'], '/branding');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Branding')).toBeTruthy();
    expect(entry('Map')).toBeNull();
    expect(entry('Language')).toBeNull();
  });

  // 🔴 The one that matters most, and the one the grants exist to keep apart:
  // holding the map key must not advertise the ability to restyle the console.
  it('shows only Map to a holder of basemap:write', () => {
    renderWith(['basemap:write'], '/basemap');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Map')).toBeTruthy();
    expect(entry('Branding')).toBeNull();
    expect(entry('Language')).toBeNull();
  });

  // The same argument for the third grant: setting the tenant's language must not
  // come with the brand or the map key attached.
  it('shows only Language to a holder of locale:write', () => {
    renderWith(['locale:write'], '/locale');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Language')).toBeTruthy();
    expect(entry('Branding')).toBeNull();
    expect(entry('Map')).toBeNull();
  });

  // 🔴 The empty-group case. A group whose children are all filtered away must
  // disappear rather than sit there opening onto nothing — this is the failure a tab
  // strip would have had to re-derive, and it is why the group shape was chosen.
  it('disappears entirely for someone holding none of them', () => {
    renderWith(['device:read']);

    expect(entry('Settings')).toBeNull();
    expect(entry('Branding')).toBeNull();
    expect(entry('Map')).toBeNull();
    expect(entry('Language')).toBeNull();
  });

  it('is not labelled Basemap any more, at any authority', () => {
    renderWith(['branding:write', 'basemap:write'], '/basemap');
    expect(entry('Basemap')).toBeNull();
  });

  // The nav label is wayfinding; the identifier is not. "Language" is what the group
  // shows, while the authority, the GraphQL field and the route all stay "locale".
  it('labels the locale child Language rather than Locale', () => {
    renderWith(['locale:write'], '/locale');
    expect(entry('Locale')).toBeNull();
  });
});

describe('the record-management nav is untouched by the grouping', () => {
  // 🔴 THE CONTROL. Every assertion above is either "an entry is present" under a
  // permissive fixture or "an entry is absent" under a restrictive one, and a sidebar
  // that rendered nothing at all would pass four of the five. This pins that the rest
  // of the nav still works and still gates independently.
  it('still shows the CRUD groups to a device:read holder, and no config group', () => {
    renderWith(['device:read']);

    expect(entry('Devices')).toBeTruthy();
    expect(entry('Areas')).toBeTruthy();
    expect(entry('Facets')).toBeTruthy();
    expect(entry('Settings')).toBeNull();
  });

  it('hides the CRUD groups from someone with only config authorities', () => {
    renderWith(['branding:write', 'basemap:write', 'locale:write'], '/basemap');

    expect(entry('Devices')).toBeNull();
    expect(entry('Areas')).toBeNull();
    expect(entry('Settings')).toBeTruthy();
  });

  it('always shows Home, which requires nothing', () => {
    renderWith([]);
    expect(entry('Home')).toBeTruthy();
  });
});

describe('the Updates group', () => {
  // Opt-in area: the group needs the area KNOWN to be deployed AND artifact:read. Each
  // of the two is withheld in turn, so neither can satisfy the other's test.
  it('shows Artifacts and Signers when the area is deployed and the caller can read', () => {
    area.status = 'available';
    renderWith(['artifact:read'], '/updates/artifacts');

    expect(entry('Updates')).toBeTruthy();
    expect(entry('Artifacts')).toBeTruthy();
    expect(entry('Signers')).toBeTruthy();
  });

  it('is hidden without artifact:read even when the area is deployed', () => {
    area.status = 'available';
    renderWith(['device:read']);
    expect(entry('Updates')).toBeNull();
  });

  it.each(['unknown', 'absent'] as const)('is hidden while the area is %s, even for a reader', (status) => {
    area.status = status;
    renderWith(['artifact:read']);
    expect(entry('Updates')).toBeNull();
  });
});

describe('the Notification Policies entry', () => {
  it('shows to a notification:read holder and to nobody else', () => {
    renderWith(['notification:read']);
    expect(entry('Notification Policies')).toBeTruthy();
    cleanup();
    renderWith(['alarm:read', 'device:read']);
    expect(entry('Notification Policies')).toBeNull();
  });
});

// A member limited to part of the tenant (a customer user) is offered only the pages the
// server serves them: the home page, devices, alarms and dashboards. Everything else is
// refused to them by the server whatever their role names, so a nav that offered it would
// be a list of errors.
describe('a scoped member', () => {
  const EVERYTHING = ['*'];

  // Whatever the scope names — a customer, a group of devices, or both — the cut is the same.
  const SCOPES: string[][][] = [[['customer:acme']], [['group:north']], [['customer:acme', 'group:north']]];

  it.each(SCOPES)('sees only Home, Dashboards, Alarms and Devices, even holding every authority (scope %j)', (anchors) => {
    scope.value = anchors;
    renderWith(EVERYTHING, '/devices');

    expect(entry('Home')).toBeTruthy();
    expect(entry('Dashboards')).toBeTruthy();
    expect(entry('Alarms')).toBeTruthy();
    // The Devices group, and inside it only the Devices leaf.
    expect(screen.getAllByText('Devices', { exact: true }).length).toBeGreaterThan(0);
    for (const hidden of [
      'Device Types',
      'Device Profiles',
      'Device Groups',
      'Provisioning Profiles',
      'Maintenance Windows',
      'Command Batches',
      'Assets',
      'Customers',
      'Areas',
      'Facets',
      'Browse',
      'Audit',
      'Connectors',
      'Notification Policies',
      'Settings',
      'Updates',
    ]) {
      expect(entry(hidden), hidden).toBeNull();
    }
  });

  // The negative control: the same authorities WITHOUT a scope see the tenant's whole
  // navigation, so the cut above is the scope's doing and not the fixture's.
  it('is the scope, not the authorities: the same token unscoped sees everything', () => {
    renderWith(EVERYTHING, '/devices');
    for (const shown of ['Device Types', 'Provisioning Profiles', 'Customers', 'Audit', 'Settings', 'Connectors']) {
      expect(entry(shown), shown).toBeTruthy();
    }
  });

  it('still needs the authority for a leaf it may see', () => {
    scope.value = ['customer:acme'];
    renderWith(['device:read'], '/');
    expect(entry('Alarms')).toBeNull();
    expect(entry('Dashboards')).toBeNull();
    expect(entry('Devices')).toBeTruthy();
  });
});

describe('Members and Roles', () => {
  it('sit in Settings, each behind its own authority', () => {
    renderWith(['member:read'], '/members');
    expect(entry('Members')).toBeTruthy();
    expect(entry('Roles')).toBeNull();
    cleanup();
    renderWith(['role:read'], '/roles');
    expect(entry('Roles')).toBeTruthy();
    expect(entry('Members')).toBeNull();
  });
});

// Provisioning profiles live in the Devices group, behind the device authority, and are never
// offered to a scoped member (the server refuses them the profile list and every profile field).
describe('the Provisioning Profiles entry', () => {
  it('sits in the Devices group for a device:read holder and for nobody without it', () => {
    renderWith(['device:read'], '/devices');
    expect(entry('Provisioning Profiles')).toBeTruthy();
    cleanup();
    renderWith(['alarm:read'], '/');
    expect(entry('Provisioning Profiles')).toBeNull();
  });
});

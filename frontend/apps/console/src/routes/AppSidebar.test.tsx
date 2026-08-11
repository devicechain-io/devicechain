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

const { authorities } = vi.hoisted(() => ({ authorities: { value: [] as string[] } }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));
vi.mock('@/auth/TenantProvider', () => ({
  useCurrentTenant: () => ({ token: 'acme', branding: null }),
}));
vi.mock('@/lib/useBrandingLogo', () => ({ useBrandingLogoSrc: () => null }));
vi.mock('@/routes/NavUser', () => ({ NavUser: () => null }));

import { SidebarProvider } from '@/components/ui/sidebar';
import { AppSidebar } from './AppSidebar';

afterEach(cleanup);
beforeEach(() => {
  authorities.value = [];
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
  it('shows both children to someone holding both authorities', () => {
    renderWith(['branding:write', 'basemap:write'], '/branding');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Branding')).toBeTruthy();
    expect(entry('Map')).toBeTruthy();
  });

  it('shows only Branding to a holder of branding:write', () => {
    renderWith(['branding:write'], '/branding');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Branding')).toBeTruthy();
    expect(entry('Map')).toBeNull();
  });

  // 🔴 The one that matters most, and the one the two grants exist to keep apart:
  // holding the map key must not advertise the ability to restyle the console.
  it('shows only Map to a holder of basemap:write', () => {
    renderWith(['basemap:write'], '/basemap');

    expect(entry('Settings')).toBeTruthy();
    expect(entry('Map')).toBeTruthy();
    expect(entry('Branding')).toBeNull();
  });

  // 🔴 The empty-group case. A group whose children are all filtered away must
  // disappear rather than sit there opening onto nothing — this is the failure a tab
  // strip would have had to re-derive, and it is why the group shape was chosen.
  it('disappears entirely for someone holding neither', () => {
    renderWith(['device:read']);

    expect(entry('Settings')).toBeNull();
    expect(entry('Branding')).toBeNull();
    expect(entry('Map')).toBeNull();
  });

  it('is not labelled Basemap any more, at any authority', () => {
    renderWith(['branding:write', 'basemap:write'], '/basemap');
    expect(entry('Basemap')).toBeNull();
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
    renderWith(['branding:write', 'basemap:write'], '/basemap');

    expect(entry('Devices')).toBeNull();
    expect(entry('Areas')).toBeNull();
    expect(entry('Settings')).toBeTruthy();
  });

  it('always shows Dashboard, which requires nothing', () => {
    renderWith([]);
    expect(entry('Dashboard')).toBeTruthy();
  });
});

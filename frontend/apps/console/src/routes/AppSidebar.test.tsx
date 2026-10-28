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
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
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
import { AppSidebar, NAV, navLeaves, isLeaf } from './AppSidebar';
import { NAV_STATE_KEY } from '@/lib/navState';

const ALL_IDS = [
  ...NAV.flatMap((n) => (isLeaf(n) ? [] : [n.id])),
  ...['updates', 'assets', 'customers', 'areas'].map((g) => `group:${g}`),
];

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
beforeEach(() => {
  window.localStorage.clear();
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
// A label can appear twice (the Devices section and its Devices leaf), so this answers whether it
// appears at all.
const entry = (label: string) => screen.queryAllByText(label, { exact: true })[0] ?? null;

// Every section and sub-group remembered as expanded, so a test can see all of the navigation.
function openEverything() {
  window.localStorage.setItem(
    NAV_STATE_KEY,
    JSON.stringify(Object.fromEntries(ALL_IDS.map((id) => [id, true]))),
  );
}

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

  // Whether the tenant requires a second factor changes who can get in, so it is gated on
  // the member grant and not on a cosmetic one.
  it('shows Sign-in Security to a holder of member:write, and not to the cosmetic grants', () => {
    renderWith(['member:write'], '/tenant-security');
    expect(entry('Sign-in Security')).toBeTruthy();
    cleanup();
    renderWith(['branding:write', 'basemap:write', 'locale:write'], '/branding');
    expect(entry('Sign-in Security')).toBeNull();
  });

  // The page refuses a member limited to part of the tenant, so the entry is not advertised to one.
  it('hides Sign-in Security from a scoped member even with member:write', () => {
    scope.value = ['customer:acme'];
    renderWith(['member:write', 'member:read'], '/members');
    expect(entry('Sign-in Security')).toBeNull();
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
    expect(entry('Connect')).toBeTruthy();
    expect(entry('Organization')).toBeTruthy();
    expect(entry('Settings')).toBeNull();
    expect(entry('Access')).toBeNull();
  });

  it('hides the CRUD groups from someone with only config authorities', () => {
    renderWith(['branding:write', 'basemap:write', 'locale:write'], '/basemap');

    expect(entry('Devices')).toBeNull();
    expect(entry('Organization')).toBeNull();
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
      'Connect',
      'Organization',
      'Access',
    ]) {
      expect(entry(hidden), hidden).toBeNull();
    }
  });

  // The negative control: the same authorities WITHOUT a scope see the tenant's whole
  // navigation, so the cut above is the scope's doing and not the fixture's.
  it('is the scope, not the authorities: the same token unscoped sees everything', () => {
    openEverything();
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

// Provisioning profiles live in the Connect section, behind the device authority, and are never
// offered to a scoped member (the server refuses them the profile list and every profile field).
describe('the Provisioning Profiles entry', () => {
  it('sits in the Connect section for a device:read holder and for nobody without it', () => {
    renderWith(['device:read'], '/provisioning-profiles');
    expect(entry('Provisioning Profiles')).toBeTruthy();
    cleanup();
    renderWith(['alarm:read'], '/');
    expect(entry('Provisioning Profiles')).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------
// The collapsible sections.

const header = (name: string) => screen.queryByRole('button', { name });
const sectionHeaders = () =>
  screen
    .queryAllByRole('button')
    .filter((b) => b.getAttribute('aria-controls')?.match(/^nav-(?!group-)/))
    .map((b) => b.textContent?.trim());
const link = (name: string | RegExp) => screen.queryByRole('link', { name });
const expanded = (name: string) => header(name)?.getAttribute('aria-expanded');
const ALL_SECTIONS = ['Monitor', 'Devices', 'Connect', 'Organization', 'Access', 'Settings'];

describe('section visibility', () => {
  // Each row: what the caller holds, and the ONLY sections they are offered. A section with
  // nothing visible inside is not rendered at all.
  it.each<[string[], string[]]>([
    [[], []],
    [['dashboard:read'], ['Monitor']],
    [['alarm:read'], ['Monitor']],
    [['notification:read'], ['Monitor']],
    [['device:read'], ['Devices', 'Connect', 'Organization']],
    [['command:read'], ['Devices']],
    [['connector:read'], ['Connect']],
    [['member:read'], ['Access']],
    [['role:read'], ['Access']],
    [['audit:read'], ['Access']],
    [['member:write'], ['Access']],
    [['branding:write'], ['Settings']],
    [['basemap:write'], ['Settings']],
    [['locale:write'], ['Settings']],
    [['dashboard:read', 'audit:read', 'locale:write'], ['Monitor', 'Access', 'Settings']],
  ])('holding %j offers exactly %j', (auths, expected) => {
    openEverything();
    renderWith(auths, '/');
    expect(sectionHeaders()).toEqual(expected);
  });

  it('offers every section, in order, to a holder of everything', () => {
    renderWith(['*'], '/');
    expect(sectionHeaders()).toEqual(ALL_SECTIONS);
    expect(link('Home')).toBeTruthy();
  });

  it('shows Updates only when the OTA area is deployed, inside Devices', () => {
    area.status = 'available';
    renderWith(['*'], '/devices');
    expect(entry('Updates')).toBeTruthy();
    cleanup();
    area.status = 'absent';
    renderWith(['*'], '/devices');
    expect(entry('Updates')).toBeNull();
    expect(header('Devices')).toBeTruthy();
  });

  // The customer-scoped member: Home, Monitor (Dashboards, Alarms) and Devices (Devices), and nothing else.
  it.each([[['customer:acme']], [['group:north']]])('a scoped member (%j) sees Home, Monitor and Devices only', (anchors) => {
    scope.value = anchors;
    renderWith(['*'], '/');

    expect(sectionHeaders()).toEqual(['Monitor', 'Devices']);
    expect(link('Home')).toBeTruthy();
    const monitor = within(header('Monitor')!.closest('li') as HTMLElement);
    expect(monitor.getAllByRole('link').map((a) => a.textContent)).toEqual(['Dashboards', 'Alarms']);
    const devices = within(header('Devices')!.closest('li') as HTMLElement);
    expect(devices.getAllByRole('link').map((a) => a.textContent)).toEqual(['Devices']);
  });
});

describe('expansion defaults and the active section', () => {
  it('opens Monitor and Devices, and closes the rest, when nothing is stored', () => {
    renderWith(['*'], '/');
    expect(Object.fromEntries(ALL_SECTIONS.map((n) => [n, expanded(n)]))).toEqual({
      Monitor: 'true',
      Devices: 'true',
      Connect: 'false',
      Organization: 'false',
      Access: 'false',
      Settings: 'false',
    });
    // Collapsed sections do not render their links.
    expect(link('Trust Anchors')).toBeNull();
    expect(link('Dashboards')).toBeTruthy();
  });

  it.each([
    ['/audit', 'Access', 'Audit'],
    ['/trust-anchors', 'Connect', 'Trust Anchors'],
    ['/facets', 'Organization', 'Facets'],
    ['/branding', 'Settings', 'Branding'],
    ['/tenant-security', 'Access', 'Sign-in Security'],
  ])('expands the section holding %s', (route, section, leaf) => {
    renderWith(['*'], route);
    expect(expanded(section)).toBe('true');
    expect(link(leaf)).toBeTruthy();
  });

  // The section holding the page is open whatever the user last chose, so they never land on a
  // page whose own entry is hidden.
  it('keeps the active section open even when it was stored as collapsed', () => {
    window.localStorage.setItem(NAV_STATE_KEY, JSON.stringify({ access: false, devices: false }));
    renderWith(['*'], '/audit');
    expect(expanded('Access')).toBe('true');
    expect(link('Audit')).toBeTruthy();
    // ...while a stored collapse of a section that is NOT active is honoured.
    expect(expanded('Devices')).toBe('false');
  });

  it('expands a nested group holding the route, and not its siblings', () => {
    renderWith(['*'], '/geofences');
    expect(expanded('Organization')).toBe('true');
    expect(expanded('Areas')).toBe('true');
    expect(link('Geofences')).toBeTruthy();
    expect(expanded('Assets')).toBe('false');
    expect(link('Asset Types')).toBeNull();
  });
});

describe('remembering the expansion', () => {
  it('persists a toggle and restores it on the next render', () => {
    renderWith(['*'], '/');
    fireEvent.click(header('Connect')!);
    expect(expanded('Connect')).toBe('true');
    expect(link('Trust Anchors')).toBeTruthy();
    fireEvent.click(header('Monitor')!);
    expect(expanded('Monitor')).toBe('false');
    expect(JSON.parse(window.localStorage.getItem(NAV_STATE_KEY) as string)).toEqual({
      connect: true,
      monitor: false,
    });

    cleanup();
    renderWith(['*'], '/');
    expect(expanded('Connect')).toBe('true');
    expect(expanded('Monitor')).toBe('false');
    expect(expanded('Devices')).toBe('true');
  });

  it('remembers a nested group too', () => {
    renderWith(['*'], '/');
    fireEvent.click(header('Organization')!);
    fireEvent.click(header('Customers')!);
    cleanup();
    renderWith(['*'], '/');
    expect(expanded('Customers')).toBe('true');
  });

  it.each([['not json'], ['["monitor"]'], ['null'], ['{"monitor":"no","connect":1}']])(
    'ignores unusable stored state (%s) and uses the defaults',
    (raw) => {
      window.localStorage.setItem(NAV_STATE_KEY, raw);
      renderWith(['*'], '/');
      expect(expanded('Monitor')).toBe('true');
      expect(expanded('Connect')).toBe('false');
    },
  );

  // Storage can be missing entirely (blocked site data, some embedded and private contexts).
  it('renders correctly, and still toggles, with no storage at all', () => {
    vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError');
    });
    renderWith(['*'], '/');

    expect(sectionHeaders()).toEqual(ALL_SECTIONS);
    expect(expanded('Monitor')).toBe('true');
    expect(expanded('Connect')).toBe('false');

    fireEvent.click(header('Connect')!);
    expect(expanded('Connect')).toBe('true');
    expect(link('Trust Anchors')).toBeTruthy();
  });
});

describe('section headers as accessible disclosure buttons', () => {
  it('are buttons with aria-expanded that control their list', () => {
    renderWith(['*'], '/');
    const open = header('Monitor')!;
    expect(open.tagName).toBe('BUTTON');
    expect(document.getElementById(open.getAttribute('aria-controls') as string)).toBeTruthy();
    expect(open.getAttribute('aria-expanded')).toBe('true');
    expect(expanded('Access')).toBe('false');
  });

  // Same as the groups they replace: toggling does not move focus or remount the control.
  it('keeps focus on the header it toggled', () => {
    renderWith(['*'], '/');
    const btn = header('Connect')!;
    btn.focus();
    fireEvent.click(btn);
    expect(header('Connect')).toBe(btn);
    expect(document.activeElement).toBe(btn);
    fireEvent.click(btn);
    expect(document.activeElement).toBe(btn);
    expect(btn.getAttribute('aria-expanded')).toBe('false');
  });
});

describe('Device Profiles', () => {
  it('says what is inside a profile', () => {
    renderWith(['device:read'], '/');
    expect(screen.getByText('rules, metrics, commands')).toBeTruthy();
    expect(link(/^Device Profiles/)).toBeTruthy();
  });
});

describe('the navigation tree', () => {
  it('lists every route once', () => {
    const hrefs = navLeaves().map((l) => l.href);
    expect(new Set(hrefs).size).toBe(hrefs.length);
  });
});

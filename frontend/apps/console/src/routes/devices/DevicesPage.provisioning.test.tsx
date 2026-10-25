// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device list's self-registration filters and provenance column. Only the transport is
// faked. The filters ride the URL (so the profile page can link to them) and become criteria
// of the device search; a member limited to part of the tenant gets none of it, and the page
// does not even name the fields, which the server answers with an error for such a member.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  gql: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import DevicesPage from './DevicesPage';

const TYPE = { id: 'dt-1', token: 'gateway', name: 'Gateway', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null };
const device = (token: string) => ({
  id: token,
  token,
  name: `Device ${token}`,
  description: null,
  externalId: null,
  metadata: null,
  createdAt: '2026-10-01T00:00:00Z',
  enabled: true,
  disabledAt: null,
  deviceType: TYPE,
});
const profile = (token: string, name: string) => ({
  id: token,
  token,
  name,
  description: null,
  provisionKey: `${token}-key`,
  strategy: 'ALLOW_NEW',
  credentialType: 'ACCESS_TOKEN',
  enabled: true,
  expiresAt: null,
  maxDevices: null,
  provisionedDeviceCount: 0,
  secretIssued: true,
  createdAt: null,
  updatedAt: null,
  deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
});

let sent: { op: string; variables: any }[] = [];
let deviceRows: string[] = [];

beforeEach(() => {
  sent = [];
  deviceRows = ['pump-1', 'pump-2'];
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.gql.mockImplementation((service: string, doc: unknown, variables: any) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'Devices':
        return Promise.resolve({
          devices: {
            results: deviceRows.map(device),
            pagination: { pageStart: 1, pageEnd: deviceRows.length, totalRecords: deviceRows.length },
          },
        });
      case 'ProvisioningProfiles':
        return Promise.resolve({
          provisioningProfiles: {
            results: [profile('acme-fleet', 'Acme fleet'), profile('beta-fleet', 'Beta fleet')],
            pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 },
          },
        });
      case 'DeviceEarmarks':
        return Promise.resolve({
          devicesByToken: [
            { token: 'pump-1', expectedProvisioningProfile: { token: 'beta-fleet', name: 'Beta fleet' } },
            { token: 'pump-2', expectedProvisioningProfile: null },
          ],
        });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

const renderPage = (search = '') =>
  render(
    <MemoryRouter initialEntries={[`/devices${search}`]}>
      <DevicesPage />
    </MemoryRouter>,
  );

const deviceSearches = () => sent.filter((s) => s.op === 'Devices').map((s) => s.variables.criteria);
const lastSearch = () => deviceSearches()[deviceSearches().length - 1];

describe('the device list provisioning filters', () => {
  it('searches without the fields when no filter is set', async () => {
    renderPage();
    await screen.findByText('Device pump-1');
    expect(deviceSearches()[0]).toEqual({ pageNumber: 1, pageSize: 20, deviceType: null });
  });

  it('filters by the registering profile named in the URL', async () => {
    renderPage('?provisioningProfile=acme-fleet');
    await screen.findByText('Device pump-1');
    expect(deviceSearches()[0]).toMatchObject({ provisioningProfile: 'acme-fleet' });
    expect(deviceSearches()[0]).not.toHaveProperty('expectedProvisioningProfile');
    // Every row of a provenance-filtered list was registered by that profile, so it says so.
    await waitFor(() => expect(screen.getAllByText('Registered by Acme fleet')).toHaveLength(2));
  });

  it('filters by the expected profile named in the URL', async () => {
    renderPage('?expectedProvisioningProfile=beta-fleet');
    await screen.findByText('Device pump-1');
    expect(deviceSearches()[0]).toMatchObject({ expectedProvisioningProfile: 'beta-fleet' });
    expect(deviceSearches()[0]).not.toHaveProperty('provisioningProfile');
  });

  it('shows each device’s expected profile in the Provisioning column', async () => {
    renderPage();
    expect(await screen.findByText('Expected: Beta fleet')).toBeTruthy();
    // pump-2 is earmarked for nothing, and the unfiltered list claims no provenance.
    expect(screen.queryByText(/^Registered by/, { selector: 'td span' })).toBeNull();
  });

  it('applies a filter picked in the bar, and clears it', async () => {
    renderPage();
    await screen.findByText('Device pump-1');
    await waitFor(() => expect(document.getElementById('dev-filter-expected')).not.toBeNull());

    fireEvent.click(document.getElementById('dev-filter-expected') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Acme fleet'));
    await waitFor(() =>
      expect(lastSearch()).toMatchObject({ expectedProvisioningProfile: 'acme-fleet' }),
    );

    fireEvent.click(await screen.findByRole('button', { name: 'Clear filters' }));
    await waitFor(() => expect(lastSearch()).not.toHaveProperty('expectedProvisioningProfile'));
  });

  it('says so when a filter matches nothing', async () => {
    deviceRows = [];
    renderPage('?provisioningProfile=ghost');
    expect(await screen.findByText('No devices match these filters.')).toBeTruthy();
  });
});

describe('the device list for a member limited to part of the tenant', () => {
  beforeEach(() => {
    h.claims = { username: 'cust@acme.example', authorities: ['*'], scopeAnchors: ['customer:acme'] };
  });

  it('shows no filters or provenance column, and never names the profile fields', async () => {
    renderPage('?provisioningProfile=acme-fleet&expectedProvisioningProfile=beta-fleet');
    await screen.findByText('Device pump-1');

    expect(screen.queryByLabelText('Registered by profile')).toBeNull();
    expect(screen.queryByLabelText('Expected profile')).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Provisioning' })).toBeNull();
    // Even with the URL naming them, the search does not, and no profile query is made.
    expect(deviceSearches()[0]).toEqual({ pageNumber: 1, pageSize: 20, deviceType: null });
    expect(sent.some((s) => s.op === 'ProvisioningProfiles' || s.op === 'DeviceEarmarks')).toBe(false);
  });
});

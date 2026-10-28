// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A certificate provisioning profile: one that names a trust anchor and so authenticates its
// devices by client certificate. It has NO secret, so everywhere the console reads the issued
// secret it must cope with null: nothing to reveal, no rotate action, no false "Rotate required"
// alarm. The derived-token rule (`pv-` plus the certificate CN) is stated where devices are
// earmarked. Only the transport is faked.

import '@/i18n/config';
import { useState } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  gql: vi.fn(),
  toast: vi.fn(),
  confirm: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => h.confirm }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import ProvisioningProfilesPage from './ProvisioningProfilesPage';
import ProvisioningProfileDetailPage from './ProvisioningProfileDetailPage';
import { ProvisioningProfilePicker } from './ProvisioningProfilePicker';

const CERT_PROFILE = {
  id: 'p-9',
  token: 'cert-fleet',
  name: 'Certificate fleet',
  description: null,
  provisionKey: 'cert-fleet',
  strategy: 'ALLOW_NEW',
  credentialType: 'X509_CERTIFICATE',
  enabled: true,
  expiresAt: null,
  maxDevices: null,
  provisionedDeviceCount: 0,
  // Always false for a certificate profile: there is no secret to have issued.
  secretIssued: false,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
  trustAnchor: { token: 'acme-ca', name: 'Acme device CA' },
};

const ANCHORS = [
  { id: 'ta-1', token: 'acme-ca', name: 'Acme device CA', enabled: true, notAfter: '2036-01-01T00:00:00Z' },
  { id: 'ta-2', token: 'old-ca', name: 'Withdrawn CA', enabled: false, notAfter: '2036-01-01T00:00:00Z' },
];

let profiles: unknown[] = [];
let sent: { op: string; variables: Record<string, unknown> }[] = [];

function install() {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: Record<string, unknown>) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'ProvisioningProfiles':
        return Promise.resolve({
          provisioningProfiles: {
            results: profiles,
            pagination: { pageStart: 1, pageEnd: profiles.length, totalRecords: profiles.length },
          },
        });
      case 'ProvisioningProfileByToken':
        return Promise.resolve({ provisioningProfilesByToken: profiles.length ? [profiles[0]] : [] });
      case 'DeviceTrustAnchors':
        return Promise.resolve({
          deviceTrustAnchors: { results: ANCHORS, pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 } },
        });
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: {
            results: [{ id: 'dt-1', token: 'gateway', name: 'Gateway' }],
            pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
          },
        });
      case 'EarmarkedDeviceCount':
        return Promise.resolve({ devices: { pagination: { totalRecords: 2 } } });
      case 'CreateProvisioningProfile':
        profiles = [CERT_PROFILE];
        // The server issues no secret for a certificate profile.
        return Promise.resolve({ createProvisioningProfile: { issuedSecret: null, profile: CERT_PROFILE } });
      default:
        return Promise.resolve({});
    }
  });
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  h.confirm.mockReset();
  profiles = [];
  install();
});
afterEach(cleanup);

describe('creating a certificate profile', () => {
  it('names the anchor, sends no secret fields, and opens no reveal dialog for the null secret', async () => {
    render(
      <MemoryRouter initialEntries={['/provisioning-profiles']}>
        <Routes>
          <Route path="/provisioning-profiles" element={<ProvisioningProfilesPage />} />
        </Routes>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'New provisioning profile' }));
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'cert-fleet' } });
    fireEvent.click(screen.getByRole('radio', { name: 'Certificate' }));
    // No key to choose for a certificate profile.
    expect(screen.queryByLabelText('Provision key')).toBeNull();
    fireEvent.click(document.getElementById('pp-type') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Gateway'));
    fireEvent.click(document.getElementById('pp-anchor') as HTMLButtonElement);
    // Only an enabled anchor can be named.
    expect(await screen.findByText('Acme device CA')).toBeTruthy();
    expect(screen.queryByText('Withdrawn CA')).toBeNull();
    fireEvent.click(screen.getByText('Acme device CA'));

    const create = screen.getByRole('button', { name: 'Create provisioning profile' }) as HTMLButtonElement;
    await waitFor(() => expect(create.disabled).toBe(false));
    fireEvent.click(create);

    await waitFor(() => expect(sent.some((s) => s.op === 'CreateProvisioningProfile')).toBe(true));
    const request = sent.find((s) => s.op === 'CreateProvisioningProfile')!.variables.request as Record<string, unknown>;
    expect(request).toMatchObject({
      token: 'cert-fleet',
      trustAnchorToken: 'acme-ca',
      credentialType: 'X509_CERTIFICATE',
      deviceTypeToken: 'gateway',
    });
    // The new profile is on the list, badged as a certificate profile and NOT as needing a rotate.
    expect(await screen.findByText('Certificate fleet')).toBeTruthy();
    expect(screen.queryByText('Rotate required')).toBeNull();
    expect(document.querySelector('[data-shot="provisioning-secret"]')).toBeNull();
    expect(h.toast).toHaveBeenCalledWith(expect.stringContaining('cert-fleet'));
  });

  it('keeps Create off until an anchor is chosen', async () => {
    render(
      <MemoryRouter>
        <ProvisioningProfilesPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'New provisioning profile' }));
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'cert-fleet' } });
    fireEvent.click(screen.getByRole('radio', { name: 'Certificate' }));
    fireEvent.click(document.getElementById('pp-type') as HTMLButtonElement);
    fireEvent.click(await screen.findByText('Gateway'));
    const create = screen.getByRole('button', { name: 'Create provisioning profile' }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
  });
});

describe('a certificate profile on screen', () => {
  const renderDetail = () =>
    render(
      <MemoryRouter initialEntries={['/provisioning-profiles/cert-fleet']}>
        <Routes>
          <Route path="/provisioning-profiles/:token" element={<ProvisioningProfileDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

  beforeEach(() => {
    profiles = [CERT_PROFILE];
  });

  it('offers no secret rotation and raises no Rotate required alarm', async () => {
    renderDetail();
    expect(await screen.findByText('Certificate fleet')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Rotate secret/ })).toBeNull();
    expect(screen.queryByText('Rotate required')).toBeNull();
    expect(screen.queryByText(/chosen before the platform generated secrets/)).toBeNull();
    expect(screen.getAllByText('Certificate').length).toBeGreaterThan(0);
  });

  it('links the trust anchor and states the derived-token rule', async () => {
    renderDetail();
    const link = await screen.findByRole('link', { name: 'Acme device CA' });
    expect(link.getAttribute('href')).toBe('/trust-anchors/acme-ca');
    expect(screen.getByText(/takes its token from its certificate: pv- followed by the common name/)).toBeTruthy();
  });

  it('shows the anchor picker instead of a provision key, and does not offer a withdrawn anchor', async () => {
    renderDetail();
    await screen.findByText('Certificate fleet');
    expect(screen.queryByLabelText('Provision key')).toBeNull();
    fireEvent.click(document.getElementById('pp-anchor') as HTMLButtonElement);
    // The disabled anchor is not offered as a new target.
    expect(screen.queryByText('Withdrawn CA')).toBeNull();
  });
});

describe('the earmark picker on the device form', () => {
  it('states the pv- derived-token rule when the chosen profile is a certificate profile', async () => {
    profiles = [CERT_PROFILE, { ...CERT_PROFILE, id: 'p-1', token: 'secret-fleet', name: 'Secret fleet', trustAnchor: null }];
    const Harness = () => {
      const [v, setV] = useState('cert-fleet');
      return <ProvisioningProfilePicker value={v} onChange={setV} deviceTypeToken="gateway" />;
    };
    render(<Harness />);
    expect(await screen.findByText(/derived from its certificate's common name as pv- followed by the CN/)).toBeTruthy();
  });

  it('keeps the ordinary help for a secret profile', async () => {
    profiles = [{ ...CERT_PROFILE, token: 'secret-fleet', trustAnchor: null }];
    render(<ProvisioningProfilePicker value="secret-fleet" onChange={() => {}} deviceTypeToken="gateway" />);
    expect(await screen.findByText(/Only this profile can adopt the device/)).toBeTruthy();
    expect(screen.queryByText(/pv-/)).toBeNull();
  });
});

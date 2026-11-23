// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provisioning-profile form on its own (the list page's tests drive create and the
// reveal-once secret through the page): its starting state, what keeps the button off, the exact
// create request for a secret profile and a certificate profile, the partial update an edit
// sends, the read-only view and a server refusal. Only the transport is faked.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { ProvisioningProfileForm } from './ProvisioningProfileForm';
import type { ProvisioningProfile } from '@/lib/api/provisioning';

const DEVICE_TYPE = { id: 'dt-1', token: 'gateway', name: 'Gateway', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null };

const ANCHORS = [
  { id: 'ta-1', token: 'acme-ca', name: 'Acme device CA', enabled: true },
  { id: 'ta-2', token: 'old-ca', name: 'Retired CA', enabled: false },
];

const STORED = {
  id: 'p-1',
  token: 'acme-fleet',
  name: 'Acme fleet',
  description: null,
  provisionKey: 'acme-sensors-2026',
  strategy: 'CHECK_PRE_PROVISIONED',
  credentialType: 'ACCESS_TOKEN',
  enabled: true,
  expiresAt: null,
  maxDevices: 50,
  provisionedDeviceCount: 7,
  secretIssued: true,
  trustAnchor: null,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
} as unknown as ProvisioningProfile;

let failWith: Error | null = null;
let writes: { op: string; variables: Record<string, any> }[] = [];

beforeEach(() => {
  failWith = null;
  writes = [];
  h.gql.mockReset();
  h.gql.mockImplementation((service: string, doc: unknown, variables: Record<string, any>) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    switch (op) {
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: { results: [DEVICE_TYPE], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
        });
      case 'DeviceTrustAnchors':
        return Promise.resolve({
          deviceTrustAnchors: { results: ANCHORS, pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 } },
        });
      case 'ProvisioningProfileByToken':
        return Promise.resolve({ provisioningProfilesByToken: [] });
      case 'CreateProvisioningProfile':
      case 'UpdateProvisioningProfile':
        writes.push({ op, variables });
        if (failWith) return Promise.reject(failWith);
        return Promise.resolve({
          createProvisioningProfile: { issuedSecret: 'issued-once', profile: STORED },
          updateProvisioningProfile: STORED,
        });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

const submit = (name: string) => screen.getByRole('button', { name }) as HTMLButtonElement;
const fill = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });

async function chooseType() {
  fireEvent.click(document.getElementById('pp-type') as HTMLButtonElement);
  fireEvent.click(await screen.findByText('Gateway'));
}

describe('creating a provisioning profile', () => {
  it('starts as an enabled secret profile that registers only pre-registered devices, with nothing to submit', () => {
    render(<ProvisioningProfileForm onCreated={vi.fn()} />);
    expect(screen.getByRole('radio', { name: 'Shared secret' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Only pre-registered devices' }).getAttribute('aria-checked')).toBe('true');
    expect((screen.getByLabelText('Accepting registrations') as HTMLElement).getAttribute('aria-checked')).toBe('true');
    expect((screen.getByLabelText('Provision key') as HTMLInputElement).value).toBe('');
    expect((screen.getByLabelText('Device limit') as HTMLInputElement).value).toBe('');
    expect(submit('Create provisioning profile').disabled).toBe(true);
  });

  it('needs a token, a key and a device type before it can be submitted', async () => {
    render(<ProvisioningProfileForm onCreated={vi.fn()} />);
    fill('Token', 'acme-fleet');
    expect(submit('Create provisioning profile').disabled).toBe(true);
    fill('Provision key', 'acme-sensors-2026');
    expect(submit('Create provisioning profile').disabled).toBe(true);
    await chooseType();
    await waitFor(() => expect(submit('Create provisioning profile').disabled).toBe(false));
    // A whitespace-only key is not a key.
    fill('Provision key', '   ');
    expect(submit('Create provisioning profile').disabled).toBe(true);
  });

  it('refuses a device limit that is not a whole number of at least one, and says so', async () => {
    render(<ProvisioningProfileForm onCreated={vi.fn()} />);
    fill('Token', 'acme-fleet');
    fill('Provision key', 'k');
    await chooseType();
    for (const bad of ['0', '-3', '2.5', 'many']) {
      fill('Device limit', bad);
      expect(screen.getByText('Use a whole number of at least 1, or leave it empty for no limit.')).toBeTruthy();
      expect(submit('Create provisioning profile').disabled).toBe(true);
    }
    fill('Device limit', '25');
    await waitFor(() => expect(submit('Create provisioning profile').disabled).toBe(false));
  });

  it('sends exactly the request a filled secret profile describes, and hands the issued secret to the caller', async () => {
    const onCreated = vi.fn();
    render(<ProvisioningProfileForm onCreated={onCreated} />);
    fill('Token', ' acme-fleet ');
    fill('Name', ' Acme fleet ');
    fill('Provision key', 'acme-sensors-2026');
    await chooseType();
    fireEvent.click(screen.getByRole('radio', { name: 'Allow new devices' }));
    fill('Device limit', '50');
    fireEvent.click(screen.getByLabelText('Accepting registrations'));
    await waitFor(() => expect(submit('Create provisioning profile').disabled).toBe(false));
    fireEvent.click(submit('Create provisioning profile'));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(writes).toEqual([
      {
        op: 'CreateProvisioningProfile',
        variables: {
          request: {
            token: 'acme-fleet',
            name: 'Acme fleet',
            description: null,
            provisionKey: 'acme-sensors-2026',
            strategy: 'ALLOW_NEW',
            deviceTypeToken: 'gateway',
            enabled: false,
            maxDevices: 50,
          },
        },
      },
    ]);
    // The one-time secret goes to the caller and is not rendered by the form.
    expect(onCreated.mock.calls[0][0].issuedSecret).toBe('issued-once');
    expect(document.body.textContent).not.toContain('issued-once');
  });

  it('a certificate profile asks for a usable trust anchor instead of a key, and sends its own token as the key', async () => {
    const onCreated = vi.fn();
    render(<ProvisioningProfileForm onCreated={onCreated} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Certificate' }));
    expect(screen.queryByLabelText('Provision key')).toBeNull();
    fill('Token', 'acme-certs');
    await chooseType();
    // No anchor yet: still blocked.
    expect(submit('Create provisioning profile').disabled).toBe(true);
    fireEvent.click(document.getElementById('pp-anchor') as HTMLButtonElement);
    // Only enabled anchors are offered.
    expect(await screen.findByText('Acme device CA')).toBeTruthy();
    expect(screen.queryByText('Retired CA')).toBeNull();
    fireEvent.click(screen.getByText('Acme device CA'));
    await waitFor(() => expect(submit('Create provisioning profile').disabled).toBe(false));
    fireEvent.click(submit('Create provisioning profile'));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0].variables.request).toEqual({
      token: 'acme-certs',
      name: null,
      description: null,
      provisionKey: 'acme-certs',
      strategy: 'CHECK_PRE_PROVISIONED',
      deviceTypeToken: 'gateway',
      enabled: true,
      trustAnchorToken: 'acme-ca',
      credentialType: 'X509_CERTIFICATE',
    });
  });

  it('shows the server refusal and keeps what was typed', async () => {
    failWith = new GraphQLRequestError('provision key is already used by another profile', 200, [
      { message: 'provision key is already used by another profile', extensions: { code: 'CONFLICT' } },
    ]);
    const onCreated = vi.fn();
    render(<ProvisioningProfileForm onCreated={onCreated} />);
    fill('Token', 'acme-fleet');
    fill('Provision key', 'acme-sensors-2026');
    await chooseType();
    await waitFor(() => expect(submit('Create provisioning profile').disabled).toBe(false));
    fireEvent.click(submit('Create provisioning profile'));
    expect(await screen.findByText(/already used by another profile/)).toBeTruthy();
    expect(onCreated).not.toHaveBeenCalled();
    expect((screen.getByLabelText('Provision key') as HTMLInputElement).value).toBe('acme-sensors-2026');
    expect(submit('Create provisioning profile').disabled).toBe(false);
  });
});

describe('editing a provisioning profile', () => {
  it('is prefilled, fixes the token and the authentication method, and offers no save until something changes', () => {
    render(<ProvisioningProfileForm entity={STORED} onSaved={vi.fn()} />);
    const token = screen.getByLabelText('Token') as HTMLInputElement;
    expect(token.value).toBe('acme-fleet');
    expect(token.disabled).toBe(true);
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Acme fleet');
    expect((screen.getByLabelText('Provision key') as HTMLInputElement).value).toBe('acme-sensors-2026');
    expect((screen.getByLabelText('Device limit') as HTMLInputElement).value).toBe('50');
    expect(screen.queryByRole('radio', { name: 'Certificate' })).toBeNull();
    expect(submit('Save changes').disabled).toBe(true);
  });

  it('sends a partial update that names only what changed', async () => {
    const onSaved = vi.fn();
    render(<ProvisioningProfileForm entity={STORED} onSaved={onSaved} />);
    fill('Device limit', '');
    fireEvent.click(screen.getByRole('radio', { name: 'Allow new devices' }));
    await waitFor(() => expect(submit('Save changes').disabled).toBe(false));
    fireEvent.click(submit('Save changes'));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith('Provisioning profile “acme-fleet” updated'));
    expect(writes).toEqual([
      {
        op: 'UpdateProvisioningProfile',
        variables: { token: 'acme-fleet', request: { strategy: 'ALLOW_NEW', maxDevices: null } },
      },
    ]);
  });

  it('warns that a secret chosen before generated secrets must be rotated', () => {
    render(<ProvisioningProfileForm entity={{ ...STORED, secretIssued: false } as ProvisioningProfile} />);
    expect(screen.getByText(/Rotate the secret to bring it into service/)).toBeTruthy();
  });

  it('is shown read-only without a save button for a member who cannot write', () => {
    render(<ProvisioningProfileForm entity={STORED} readOnly />);
    expect(screen.queryByRole('button', { name: 'Save changes' })).toBeNull();
    expect((screen.getByLabelText('Name') as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText('Provision key') as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText('Device limit') as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText('Accepting registrations') as HTMLButtonElement).disabled).toBe(true);
  });
});

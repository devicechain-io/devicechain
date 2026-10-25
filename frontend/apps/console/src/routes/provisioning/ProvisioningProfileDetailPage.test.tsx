// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A provisioning profile's detail page: the confirmations that name what an action does to the
// fleet, the partial update, the reveal after a rotation, and who is offered what. Only the
// transport is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

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

import ProvisioningProfileDetailPage from './ProvisioningProfileDetailPage';

const NEW_SECRET = 'Rotated-0123456789-abcdefghijklmnopqrstuvwxyz';

const BASE_PROFILE = {
  id: 'p-1',
  token: 'acme-fleet',
  name: 'Acme fleet',
  description: 'Dock sensors',
  provisionKey: 'acme-sensors-2026',
  strategy: 'CHECK_PRE_PROVISIONED',
  credentialType: 'ACCESS_TOKEN',
  enabled: true,
  expiresAt: '2027-01-02T03:04:59Z',
  maxDevices: 50,
  provisionedDeviceCount: 7,
  secretIssued: true,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-02T00:00:00Z',
  deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
};

let profile: typeof BASE_PROFILE;
let sent: { op: string; variables: Record<string, unknown> }[] = [];
let freshCount = 7;
let deleteError: Error | null = null;

const writes = (op: string) => sent.filter((s) => s.op === op);

function install() {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: Record<string, unknown>) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    sent.push({ op, variables });
    switch (op) {
      case 'ProvisioningProfileByToken':
        // A profile read after the first one is the revoke confirmation's fresh count.
        return Promise.resolve({
          provisioningProfilesByToken: [
            sent.filter((s) => s.op === op).length > 1
              ? { ...profile, provisionedDeviceCount: freshCount }
              : profile,
          ],
        });
      case 'EarmarkedDeviceCount':
        return Promise.resolve({ devices: { pagination: { totalRecords: 4 } } });
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: {
            results: [{ id: 'dt-1', token: 'gateway', name: 'Gateway' }],
            pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
          },
        });
      case 'UpdateProvisioningProfile': {
        profile = { ...profile, ...(variables.request as object), updatedAt: '2026-10-03T00:00:00Z' };
        return Promise.resolve({ updateProvisioningProfile: profile });
      }
      case 'RotateProvisioningProfileSecret':
        profile = { ...profile, secretIssued: true, updatedAt: '2026-10-03T00:00:00Z' };
        return Promise.resolve({ rotateProvisioningProfileSecret: { issuedSecret: NEW_SECRET, profile } });
      case 'RevokeProvisionedDevices':
        return Promise.resolve({ revokeProvisionedDevices: 9 });
      case 'DeleteProvisioningProfile':
        return deleteError ? Promise.reject(deleteError) : Promise.resolve({ deleteProvisioningProfile: true });
      default:
        return Promise.resolve({});
    }
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/provisioning-profiles/acme-fleet']}>
      <Routes>
        <Route path="/provisioning-profiles/:token" element={<ProvisioningProfileDetailPage />} />
        <Route path="/provisioning-profiles" element={<p>the list</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const revealDialog = () => document.querySelector('[data-shot="provisioning-secret"]') as HTMLElement | null;

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  h.confirm.mockReset();
  h.confirm.mockResolvedValue(true);
  profile = { ...BASE_PROFILE };
  freshCount = 7;
  deleteError = null;
  install();
});
afterEach(cleanup);

describe('the profile summary', () => {
  it('shows the registered and expected device counts with links to the filtered device lists', async () => {
    renderPage();
    expect(await screen.findByText('7 of 50')).toBeTruthy();
    await waitFor(() => expect(screen.getByText('4')).toBeTruthy());
    const hrefs = screen.getAllByRole('link', { name: 'View devices' }).map((a) => a.getAttribute('href'));
    expect(hrefs).toEqual([
      '/devices?provisioningProfile=acme-fleet',
      '/devices?expectedProvisioningProfile=acme-fleet',
    ]);
  });

  it('shows the Rotate required badge and says why for a profile with no generated secret', async () => {
    profile = { ...profile, secretIssued: false };
    renderPage();
    expect(await screen.findByText('Rotate required')).toBeTruthy();
    expect(screen.getByText(/refuses every device\. Rotate the secret to bring it into service/)).toBeTruthy();
  });

  it('shows no such badge for a profile with an issued secret', async () => {
    renderPage();
    await screen.findByText('7 of 50');
    expect(screen.queryByText('Rotate required')).toBeNull();
  });
});

describe('editing is a partial update', () => {
  it('offers no save until something changes', async () => {
    renderPage();
    const save = (await screen.findByRole('button', { name: 'Save changes' })) as HTMLButtonElement;
    await waitFor(() => expect(save.disabled).toBe(true));
  });

  it('sends only the field the operator changed', async () => {
    renderPage();
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Acme fleet (north)' } });
    const save = screen.getByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);

    await waitFor(() => expect(writes('UpdateProvisioningProfile')).toHaveLength(1));
    const { variables } = writes('UpdateProvisioningProfile')[0];
    expect(variables.token).toBe('acme-fleet');
    // Paired control: the field IS sent. Without it, "sends nothing else" is satisfied by a form
    // that saves nothing.
    expect(variables.request).toEqual({ name: 'Acme fleet (north)' });
    // The expiry carries seconds the datetime input cannot show, and the form hides metadata
    // and the credential type altogether: none of them travels.
    for (const key of ['expiresAt', 'maxDevices', 'metadata', 'credentialType', 'provisionSecret', 'enabled', 'strategy']) {
      expect(variables.request).not.toHaveProperty(key);
    }
  });

  it('clears the cap with an explicit null when the field is emptied', async () => {
    renderPage();
    fireEvent.change(await screen.findByLabelText('Device limit'), { target: { value: '' } });
    const save = screen.getByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);
    await waitFor(() => expect(writes('UpdateProvisioningProfile')).toHaveLength(1));
    expect(writes('UpdateProvisioningProfile')[0].variables.request).toEqual({ maxDevices: null });
  });

  it('refuses to save a cap that is not a whole number, before any request', async () => {
    renderPage();
    fireEvent.change(await screen.findByLabelText('Device limit'), { target: { value: '0' } });
    expect(screen.getByText(/Use a whole number of at least 1/)).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('rotating the secret', () => {
  it('asks first, naming the immediate cutover and the unaffected registered devices', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate secret' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalledTimes(1));
    const options = h.confirm.mock.calls[0][0] as { description: string };
    expect(options.description).toContain('stops working immediately');
    expect(options.description).toContain('7 device(s) already registered keep their credentials');
    expect(options.description).toContain('shown once');
  });

  it('does nothing when the operator declines', async () => {
    h.confirm.mockResolvedValue(false);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate secret' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalled());
    expect(writes('RotateProvisioningProfileSecret')).toHaveLength(0);
    expect(revealDialog()).toBeNull();
  });

  it('reveals the new secret once, and not after the dialog is closed', async () => {
    profile = { ...profile, secretIssued: false };
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate secret' }));

    await waitFor(() => expect(revealDialog()).not.toBeNull());
    expect((screen.getByLabelText('Provision secret') as HTMLInputElement).value).toBe(NEW_SECRET);
    expect(revealDialog()!.textContent).toContain('The previous secret no longer works');
    // The profile reloaded behind the dialog is now in service: the badge is gone.
    await waitFor(() => expect(screen.queryByText('Rotate required')).toBeNull());

    fireEvent.click(screen.getByRole('button', { name: 'I have copied it, close' }));
    await waitFor(() => expect(revealDialog()).toBeNull());
    expect(document.body.innerHTML).not.toContain(NEW_SECRET);
    // No request after the rotation selects the secret: nothing could bring it back.
    const afterRotate = sent.slice(sent.findIndex((s) => s.op === 'RotateProvisioningProfileSecret') + 1);
    expect(afterRotate.map((s) => JSON.stringify(s.variables)).join('')).not.toContain(NEW_SECRET);
  });
});

describe('revoking the registered devices', () => {
  it('states the count read just now and what follows, then revokes', async () => {
    freshCount = 9; // two devices registered since the page loaded
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke devices' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalledTimes(1));
    const options = h.confirm.mock.calls[0][0] as { description: string };
    expect(options.description).toContain('up to 9 device(s)');
    expect(options.description).toContain('refused until you reset each one');
    expect(options.description).toContain('keeps accepting new registrations');

    await waitFor(() => expect(writes('RevokeProvisionedDevices')).toHaveLength(1));
    expect(writes('RevokeProvisionedDevices')[0].variables).toEqual({ profileToken: 'acme-fleet' });
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('Revoked 9 devices'));
  });

  it('revokes nothing when the operator declines', async () => {
    h.confirm.mockResolvedValue(false);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke devices' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalled());
    expect(writes('RevokeProvisionedDevices')).toHaveLength(0);
  });
});

describe('deleting', () => {
  it('goes back to the list on success', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('the list')).toBeTruthy();
    expect(writes('DeleteProvisioningProfile')[0].variables).toEqual({ token: 'acme-fleet' });
  });

  it("shows the server's refusal and stays on the page", async () => {
    const refusal = "in use: 7 credential(s) were issued by provisioning profile \"acme-fleet\"; revoke the profile's devices first";
    deleteError = new GraphQLRequestError(refusal, 200, [{ message: refusal, extensions: { code: 'IN_USE' } }]);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    expect(await screen.findByText(/revoke the profile's devices first/)).toBeTruthy();
    expect(screen.queryByText('the list')).toBeNull();
    expect(h.toast).toHaveBeenCalledWith(expect.stringContaining("revoke the profile's devices first"), 'error');
  });

  it('asks first and deletes nothing when declined', async () => {
    h.confirm.mockResolvedValue(false);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalled());
    expect(writes('DeleteProvisioningProfile')).toHaveLength(0);
  });
});

describe('who is offered what', () => {
  it('shows a member without device:write the profile, disabled, with no actions', async () => {
    h.claims = { username: 'reader@oem.example', authorities: ['device:read'], scopeAnchors: [] };
    renderPage();
    const name = (await screen.findByLabelText('Name')) as HTMLInputElement;
    expect(name.disabled).toBe(true);
    for (const label of ['Rotate secret', 'Revoke devices', 'Delete', 'Save changes']) {
      expect(screen.queryByRole('button', { name: label }), label).toBeNull();
    }
  });

  it('offers a member limited to part of the tenant no actions, whatever authorities the role names', async () => {
    h.claims = { username: 'cust@acme.example', authorities: ['*'], scopeAnchors: ['customer:acme'] };
    renderPage();
    await screen.findByLabelText('Name');
    for (const label of ['Rotate secret', 'Revoke devices', 'Delete', 'Save changes']) {
      expect(screen.queryByRole('button', { name: label }), label).toBeNull();
    }
  });
});

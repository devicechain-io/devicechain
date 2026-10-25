// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provisioning profiles list and its create flow, with the reveal-once secret.
//
// 🔴 What is proven about the secret, and how. Only the transport is faked, so the page, the
// form, the dialog and the API wrappers are real. The fake returns the secret in exactly one
// response (the create mutation) and in no other; the assertions then read BOTH sides:
//   * the DOM: the secret is on screen while the dialog is open, with its warning, and gone from
//     the document the moment it is closed;
//   * the wire: nothing the page sends afterwards (the list reload) selects `issuedSecret`, so
//     there is no request that could bring the value back.
// The paired positive controls (it IS shown, it IS copyable) are what make the negative ones mean
// anything: a dialog that never rendered the secret would also "not show it after close".

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  gql: vi.fn(),
  toast: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import ProvisioningProfilesPage from './ProvisioningProfilesPage';

const SECRET = 'S3cr3t-shown-once-0123456789abcdefghijklmnop';

const PROFILE = {
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
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  deviceType: { id: 'dt-1', token: 'gateway', name: 'Gateway' },
};

const DEVICE_TYPE = { id: 'dt-1', token: 'gateway', name: 'Gateway', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null };

/** The documents the page sent, by operation name, in order. */
let sent: { op: string; text: string; variables: unknown }[] = [];
let profiles: unknown[] = [];

function install() {
  sent = [];
  h.gql.mockImplementation((service: string, doc: unknown, variables: unknown) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const text = String(doc);
    const op = /(?:query|mutation)\s+(\w+)/.exec(text)?.[1] ?? '';
    sent.push({ op, text, variables });
    switch (op) {
      case 'ProvisioningProfiles':
        return Promise.resolve({
          provisioningProfiles: {
            results: profiles,
            pagination: { pageStart: 1, pageEnd: profiles.length, totalRecords: profiles.length },
          },
        });
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: { results: [DEVICE_TYPE], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
        });
      case 'ProvisioningProfileByToken':
        return Promise.resolve({ provisioningProfilesByToken: [] });
      case 'CreateProvisioningProfile':
        profiles = [PROFILE];
        return Promise.resolve({ createProvisioningProfile: { issuedSecret: SECRET, profile: PROFILE } });
      default:
        return Promise.resolve({});
    }
  });
}

function renderPage(path = '/provisioning-profiles') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/provisioning-profiles" element={<ProvisioningProfilesPage />} />
        <Route path="/elsewhere" element={<p>elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

// The reveal dialog, found by its capture anchor: the create drawer is also a dialog role and
// may still be animating out when this one appears.
const revealDialog = () => document.querySelector('[data-shot="provisioning-secret"]') as HTMLElement | null;
async function findRevealDialog(): Promise<HTMLElement> {
  await waitFor(() => expect(revealDialog()).not.toBeNull());
  return revealDialog()!;
}

async function createProfile() {
  fireEvent.click(await screen.findByRole('button', { name: 'New provisioning profile' }));
  fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'acme-fleet' } });
  fireEvent.change(screen.getByLabelText('Provision key'), { target: { value: 'acme-sensors-2026' } });
  fireEvent.click(document.getElementById('pp-type') as HTMLButtonElement);
  fireEvent.click(await screen.findByText('Gateway'));
  const create = screen.getByRole('button', { name: 'Create provisioning profile' });
  await waitFor(() => expect((create as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(create);
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.toast.mockReset();
  profiles = [];
  install();
});
afterEach(cleanup);

describe('the provisioning profiles list', () => {
  it('shows each profile with its type, strategy, usage and badges', async () => {
    profiles = [
      PROFILE,
      { ...PROFILE, id: 'p-2', token: 'legacy', name: null, secretIssued: false, enabled: false, maxDevices: null, provisionedDeviceCount: 3 },
    ];
    renderPage();
    expect(await screen.findByText('Acme fleet')).toBeTruthy();
    expect(screen.getByText('7 of 50')).toBeTruthy();
    expect(screen.getByText('Rotate required')).toBeTruthy();
    expect(screen.getByText('Disabled')).toBeTruthy();
    // An uncapped profile states its count alone.
    expect(screen.getByText('3')).toBeTruthy();
  });

  it('offers no create to a member without device:write', async () => {
    h.claims = { username: 'reader@oem.example', authorities: ['device:read'], scopeAnchors: [] };
    profiles = [PROFILE];
    renderPage();
    await screen.findByText('Acme fleet');
    expect(screen.queryByRole('button', { name: 'New provisioning profile' })).toBeNull();
  });

  it('offers no create to a member limited to part of the tenant, even holding device:write', async () => {
    h.claims = { username: 'cust@acme.example', authorities: ['*'], scopeAnchors: ['customer:acme'] };
    profiles = [PROFILE];
    renderPage();
    await screen.findByText('Acme fleet');
    expect(screen.queryByRole('button', { name: 'New provisioning profile' })).toBeNull();
  });
});

describe('creating a profile reveals the secret exactly once', () => {
  it('shows the secret with a copy button and the warning, after the profile is created', async () => {
    renderPage();
    await createProfile();

    const dialog = await findRevealDialog();
    const field = screen.getByLabelText('Provision secret') as HTMLInputElement;
    expect(field.value).toBe(SECRET);
    expect(dialog.textContent).toContain("You won't see this secret again");
    expect(screen.getByRole('button', { name: 'Copy provision secret' })).toBeTruthy();
    // The key is shown beside it, since the device needs the pair.
    expect((screen.getByLabelText('Provision key', { selector: '#pp-reveal-key' }) as HTMLInputElement).value).toBe(
      'acme-sensors-2026',
    );
  });

  it('copies the secret to the clipboard when asked', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    renderPage();
    await createProfile();
    fireEvent.click(await screen.findByRole('button', { name: 'Copy provision secret' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(SECRET));
  });

  it('is gone from the document once the dialog is closed', async () => {
    renderPage();
    await createProfile();
    await findRevealDialog();
    expect(document.body.textContent).not.toContain('S3cr3t');
    // (the field's value is an attribute of an input, not text: look at inputs too)
    expect(Array.from(document.querySelectorAll('input')).some((i) => i.value === SECRET)).toBe(true);

    fireEvent.click(screen.getByRole('button', { name: 'I have copied it, close' }));

    await waitFor(() => expect(revealDialog()).toBeNull());
    expect(Array.from(document.querySelectorAll('input')).some((i) => i.value === SECRET)).toBe(false);
    expect(document.body.innerHTML).not.toContain(SECRET);
    // The new profile is on the list behind it, so the close lost nothing but the secret.
    expect(await screen.findByText('Acme fleet')).toBeTruthy();
  });

  it('is never in a refetch: nothing sent after the create can return it', async () => {
    renderPage();
    await createProfile();
    await findRevealDialog();
    // The create reloads the list. Give any other request the chance to fire.
    await waitFor(() => expect(sent.filter((s) => s.op === 'ProvisioningProfiles').length).toBeGreaterThanOrEqual(2));

    const creates = sent.filter((s) => s.text.includes('issuedSecret'));
    // Exactly one document selects the secret: the create mutation. The reload does not.
    expect(creates.map((s) => s.op)).toEqual(['CreateProvisioningProfile']);
    // And no request carries the value back to the server either.
    expect(JSON.stringify(sent.map((s) => s.variables))).not.toContain(SECRET);
  });

  it('does not close on a stray click or Esc: the secret cannot be read again', async () => {
    renderPage();
    await createProfile();
    const dialog = await findRevealDialog();
    fireEvent.keyDown(dialog, { key: 'Escape' });
    fireEvent.pointerDown(document.body);
    fireEvent.click(document.body);
    expect(revealDialog()).not.toBeNull();
    expect((screen.getByLabelText('Provision secret') as HTMLInputElement).value).toBe(SECRET);
  });

  it('is cleared by navigating away, not only by closing', async () => {
    const view = renderPage();
    await createProfile();
    await findRevealDialog();
    view.unmount();
    expect(document.body.innerHTML).not.toContain(SECRET);
  });
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Identity providers admin screens.
//
// What is faked is the SEAM — the admin API module, the toast and the confirm dialog.
// Every form, validator and gate runs for real, because those are the thing under test.
//
// 🔴 The assertions are about the properties a careless page would lack: the secret field
// can never be pre-filled, approval sends the fingerprint that is ON SCREEN and cannot be
// clicked before it is acknowledged, an instance connection cannot be submitted without
// amr or the MFA statement, a server refusal reaches the form, and no write is clickable
// while a re-read is in flight.

import i18n from '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  listIdpConnections: vi.fn(),
  getIdpConnection: vi.fn(),
  listSsoLinkRequests: vi.fn(),
  createIdpConnection: vi.fn(),
  updateIdpConnection: vi.fn(),
  setIdpRequiredClaims: vi.fn(),
  deleteIdpConnection: vi.fn(),
  setIdpConnectionSecret: vi.fn(),
  setIdpConnectionTenants: vi.fn(),
  addIdpDomain: vi.fn(),
  removeIdpDomain: vi.fn(),
  refreshIdpDiscovery: vi.fn(),
  approveIdpDiscovery: vi.fn(),
  approveSsoLinkRequest: vi.fn(),
  rejectSsoLinkRequest: vi.fn(),
  toast: vi.fn(),
  confirm: vi.fn(),
}));

vi.mock('@/lib/api/identity-providers', () => ({
  listIdpConnections: (...a: unknown[]) => api.listIdpConnections(...a),
  getIdpConnection: (...a: unknown[]) => api.getIdpConnection(...a),
  listSsoLinkRequests: (...a: unknown[]) => api.listSsoLinkRequests(...a),
  createIdpConnection: (...a: unknown[]) => api.createIdpConnection(...a),
  updateIdpConnection: (...a: unknown[]) => api.updateIdpConnection(...a),
  setIdpRequiredClaims: (...a: unknown[]) => api.setIdpRequiredClaims(...a),
  deleteIdpConnection: (...a: unknown[]) => api.deleteIdpConnection(...a),
  setIdpConnectionSecret: (...a: unknown[]) => api.setIdpConnectionSecret(...a),
  setIdpConnectionTenants: (...a: unknown[]) => api.setIdpConnectionTenants(...a),
  addIdpDomain: (...a: unknown[]) => api.addIdpDomain(...a),
  removeIdpDomain: (...a: unknown[]) => api.removeIdpDomain(...a),
  refreshIdpDiscovery: (...a: unknown[]) => api.refreshIdpDiscovery(...a),
  approveIdpDiscovery: (...a: unknown[]) => api.approveIdpDiscovery(...a),
  approveSsoLinkRequest: (...a: unknown[]) => api.approveSsoLinkRequest(...a),
  rejectSsoLinkRequest: (...a: unknown[]) => api.rejectSsoLinkRequest(...a),
}));
vi.mock('@/lib/api/admin', () => ({ listTenants: () => Promise.resolve([{ token: 'acme', name: 'Acme' }]) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: api.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => api.confirm }));

import IdentityProvidersPage from './IdentityProvidersPage';
import IdentityProviderDetailPage from './IdentityProviderDetailPage';

const FP_OLD = 'sha256:aaaa1111';
const FP_NEW = 'sha256:bbbb2222';

function discovery(over: Record<string, unknown> = {}) {
  return {
    authorizationEndpoint: 'https://login.example.com/authorize',
    tokenEndpoint: 'https://login.example.com/token',
    jwksUri: 'https://login.example.com/keys',
    endSessionEndpoint: null,
    fetchedAt: '2026-10-01T10:00:00Z',
    fingerprint: FP_OLD,
    approved: false,
    approvedBy: null,
    approvedAt: null,
    ...over,
  };
}

function connection(over: Record<string, unknown> = {}) {
  return {
    token: 'acme-sso',
    name: 'Acme SSO',
    kind: 'tenant',
    enabled: false,
    issuer: 'https://login.example.com',
    clientId: 'client-1',
    hasSecret: true,
    scopes: ['openid', 'email'],
    emailClaim: 'email',
    requiredClaims: [{ name: 'hd', value: 'example.com' }],
    requiredAmr: [],
    domains: [{ domain: 'example.com', verifiedAt: '2026-10-01T10:00:00Z', verifiedBy: 'root@example.com' }],
    tenants: ['acme'],
    callbackUrl: 'https://dc.example.com/api/user-management/sso/callback/acme-sso',
    providerSignOut: false,
    signOutReturnUrls: ['https://dc.example.com/login', 'https://dc.example.com/dash/'],
    discovery: discovery(),
    createdAt: null,
    updatedAt: null,
    ...over,
  };
}

function listItem(over: Record<string, unknown> = {}) {
  const c = connection(over) as ReturnType<typeof connection>;
  return { ...c, domains: c.domains.map((d) => ({ domain: d.domain })) };
}

function renderList() {
  render(
    <MemoryRouter initialEntries={['/admin/identity-providers']}>
      <Routes>
        <Route path="/admin/identity-providers" element={<IdentityProvidersPage />} />
        <Route path="/admin/identity-providers/:token" element={<div>detail-page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderDetail() {
  render(
    <MemoryRouter initialEntries={['/admin/identity-providers/acme-sso']}>
      <Routes>
        <Route path="/admin/identity-providers" element={<div>list-page</div>} />
        <Route path="/admin/identity-providers/:token" element={<IdentityProviderDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  // jsdom has no ResizeObserver; Radix's checkbox measures its form-bubbling input with one.
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  api.listIdpConnections.mockResolvedValue([listItem()]);
  api.listSsoLinkRequests.mockResolvedValue([]);
  api.getIdpConnection.mockResolvedValue(connection());
  api.confirm.mockResolvedValue(true);
  for (const k of [
    'createIdpConnection', 'updateIdpConnection', 'setIdpRequiredClaims', 'deleteIdpConnection',
    'setIdpConnectionSecret', 'setIdpConnectionTenants', 'addIdpDomain', 'removeIdpDomain',
    'refreshIdpDiscovery', 'approveIdpDiscovery', 'approveSsoLinkRequest', 'rejectSsoLinkRequest',
  ] as const) {
    api[k].mockResolvedValue(k === 'createIdpConnection' ? { token: 'acme-sso' } : true);
  }
});
afterEach(cleanup);

// Radix Tabs switch on mousedown, so a bare click does nothing.
function openTab(name: RegExp) {
  fireEvent.mouseDown(screen.getByRole('tab', { name }));
}

describe('the connection list', () => {
  it('renders each connection with its kind, domains, endpoint state and status', async () => {
    renderList();
    expect(await screen.findByText('Acme SSO')).toBeTruthy();
    expect(screen.getByText('https://login.example.com')).toBeTruthy();
    expect(screen.getByText('example.com')).toBeTruthy();
    expect(screen.getByText('Needs approval')).toBeTruthy();
  });

  it('marks an enabled connection whose approval was withdrawn as needing RE-approval', async () => {
    api.listIdpConnections.mockResolvedValue([listItem({ enabled: true })]);
    renderList();
    expect(await screen.findByText('Re-approval needed')).toBeTruthy();
  });

  it('shows an empty state when there are no connections', async () => {
    api.listIdpConnections.mockResolvedValue([]);
    renderList();
    expect(await screen.findByText(/No identity providers yet/i)).toBeTruthy();
  });
});

describe('creating a connection', () => {
  async function openCreate() {
    renderList();
    await screen.findByText('Acme SSO');
    fireEvent.click(screen.getByRole('button', { name: /new connection/i }));
    return screen.findByLabelText('Issuer');
  }

  function fill(label: string | RegExp, value: string) {
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
  }

  async function fillValid() {
    await openCreate();
    fill('Token', 'acme-sso');
    fill('Name', 'Acme SSO');
    fill('Issuer', 'https://login.example.com');
    fill('Client ID', 'client-1');
  }

  it('starts with an empty, masked client-secret field', async () => {
    await openCreate();
    const secret = screen.getByLabelText('Client secret') as HTMLInputElement;
    expect(secret.type).toBe('password');
    expect(secret.value).toBe('');
  });

  it('refuses a non-https issuer before any request is made', async () => {
    await openCreate();
    fill('Token', 'acme-sso');
    fill('Name', 'Acme SSO');
    fill('Issuer', 'http://login.example.com');
    fill('Client ID', 'client-1');
    fireEvent.click(screen.getByRole('button', { name: 'Create connection' }));
    expect(await screen.findByText('Issuer must be an https address.')).toBeTruthy();
    expect(api.createIdpConnection).not.toHaveBeenCalled();
  });

  it('shows the server refusal verbatim in the form', async () => {
    api.createIdpConnection.mockRejectedValue(
      new GraphQLRequestError('issuer cannot be pinned to one organisation: a google connection needs an hd requirement', 200),
    );
    await fillValid();
    fireEvent.click(screen.getByRole('button', { name: 'Create connection' }));
    expect(await screen.findByText(/needs an hd requirement/)).toBeTruthy();
    expect(screen.queryByText('detail-page')).toBeNull();
  });

  it('sends the secret the operator typed, then lands on the new connection', async () => {
    await fillValid();
    fill('Client secret', 's3cret');
    fireEvent.click(screen.getByRole('button', { name: 'Create connection' }));
    await waitFor(() => expect(api.createIdpConnection).toHaveBeenCalledTimes(1));
    expect(api.createIdpConnection.mock.calls[0][0]).toMatchObject({
      token: 'acme-sso',
      kind: 'tenant',
      clientSecret: 's3cret',
    });
    expect(await screen.findByText('detail-page')).toBeTruthy();
  });

  it('will not submit an instance connection without amr or the MFA statement', async () => {
    await fillValid();
    fireEvent.click(screen.getByRole('radio', { name: 'Instance' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create connection' }));
    expect(await screen.findByText(/must require an authentication method/i)).toBeTruthy();
    expect(api.createIdpConnection).not.toHaveBeenCalled();

    // The acknowledgement unblocks it, and is what gets sent.
    fireEvent.click(screen.getByRole('checkbox', { name: /enforces multi-factor/i }));
    fireEvent.click(screen.getByRole('button', { name: 'Create connection' }));
    await waitFor(() => expect(api.createIdpConnection).toHaveBeenCalledTimes(1));
    expect(api.createIdpConnection.mock.calls[0][0]).toMatchObject({
      kind: 'instance',
      mfaEnforcedByIdp: true,
    });
  });

  it('accepts required amr in place of the acknowledgement, and hides the checkbox', async () => {
    await fillValid();
    fireEvent.click(screen.getByRole('radio', { name: 'Instance' }));
    fill('Required authentication methods', 'mfa');
    expect(screen.queryByRole('checkbox', { name: /enforces multi-factor/i })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Create connection' }));
    await waitFor(() => expect(api.createIdpConnection).toHaveBeenCalledTimes(1));
    expect(api.createIdpConnection.mock.calls[0][0]).toMatchObject({ requiredAmr: ['mfa'] });
  });
});

describe('the discovery snapshot', () => {
  it('shows the fingerprint and keeps Approve disabled until it is acknowledged', async () => {
    renderDetail();
    expect(await screen.findByText(FP_OLD)).toBeTruthy();
    const approve = screen.getByRole('button', { name: 'Approve endpoints' }) as HTMLButtonElement;
    expect(approve.disabled).toBe(true);
    fireEvent.click(approve);
    expect(api.approveIdpDiscovery).not.toHaveBeenCalled();
  });

  it('approves with the fingerprint that is on screen', async () => {
    renderDetail();
    await screen.findByText(FP_OLD);
    fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(FP_OLD) }));
    fireEvent.click(screen.getByRole('button', { name: 'Approve endpoints' }));
    await waitFor(() => expect(api.approveIdpDiscovery).toHaveBeenCalledWith('acme-sso', FP_OLD));
  });

  it('prompts for re-approval when the snapshot changed, and an old acknowledgement does not carry over', async () => {
    api.getIdpConnection.mockResolvedValue(connection({ enabled: true, discovery: discovery({ fingerprint: FP_OLD }) }));
    renderDetail();
    expect(await screen.findByText(/approval withdrawn/i)).toBeTruthy();
    fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(FP_OLD) }));

    // A refresh swaps in a different snapshot.
    api.getIdpConnection.mockResolvedValue(connection({ enabled: true, discovery: discovery({ fingerprint: FP_NEW }) }));
    fireEvent.click(screen.getByRole('button', { name: /refresh from provider/i }));
    expect(await screen.findByText(FP_NEW)).toBeTruthy();

    const box = screen.getByRole('checkbox', { name: new RegExp(FP_NEW) });
    expect(box.getAttribute('aria-checked')).toBe('false');
    expect((screen.getByRole('button', { name: 'Approve new endpoints' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('shows a server refusal of the approval, and re-reads so the CURRENT snapshot is what is offered next', async () => {
    api.approveIdpDiscovery.mockRejectedValue(new GraphQLRequestError('the discovery snapshot changed', 200));
    renderDetail();
    await screen.findByText(FP_OLD);
    fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(FP_OLD) }));

    // The snapshot moved on the server between the operator's look and their click.
    api.getIdpConnection.mockResolvedValue(connection({ discovery: discovery({ fingerprint: FP_NEW }) }));
    fireEvent.click(screen.getByRole('button', { name: 'Approve endpoints' }));
    expect(await screen.findByText('the discovery snapshot changed')).toBeTruthy();

    // Without the re-read every retry would send FP_OLD and be refused forever. The refusal
    // survives the re-read, and the new snapshot needs its own acknowledgement.
    expect(await screen.findByText(FP_NEW)).toBeTruthy();
    expect(screen.getByText('the discovery snapshot changed')).toBeTruthy();
    const box = screen.getByRole('checkbox', { name: new RegExp(FP_NEW) });
    expect(box.getAttribute('aria-checked')).toBe('false');
  });

  it('does not offer Enable until the endpoints are approved', async () => {
    renderDetail();
    await screen.findByText(FP_OLD);
    expect((screen.getByRole('button', { name: 'Enable' }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('writes during a re-read', () => {
  it('disables Disable and Delete while the connection is being re-read', async () => {
    api.getIdpConnection.mockResolvedValue(
      connection({ enabled: true, discovery: discovery({ approved: true, approvedBy: 'root' }) }),
    );
    renderDetail();
    const disable = (await screen.findByRole('button', { name: 'Disable' })) as HTMLButtonElement;
    expect(disable.disabled).toBe(false);

    // The next read never lands: the page must show the old data, with writes locked.
    api.getIdpConnection.mockReturnValue(new Promise(() => undefined));
    fireEvent.click(screen.getByRole('button', { name: /refresh from provider/i }));
    await waitFor(() => expect(api.refreshIdpDiscovery).toHaveBeenCalled());
    await waitFor(() =>
      expect((screen.getByRole('button', { name: 'Disable' }) as HTMLButtonElement).disabled).toBe(true),
    );
    expect((screen.getByRole('button', { name: 'Delete' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('disables an acknowledged Approve while the connection is being re-read', async () => {
    renderDetail();
    await screen.findByText(FP_OLD);
    fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(FP_OLD) }));
    const approve = screen.getByRole('button', { name: 'Approve endpoints' }) as HTMLButtonElement;
    expect(approve.disabled).toBe(false);

    api.getIdpConnection.mockReturnValue(new Promise(() => undefined));
    fireEvent.click(screen.getByRole('button', { name: /refresh from provider/i }));
    await waitFor(() => expect(api.refreshIdpDiscovery).toHaveBeenCalled());
    await waitFor(() => expect(approve.disabled).toBe(true));
  });

  // The header's Disable is the write that starts the re-read; the panel's own save, with an
  // edit already pending, must lock for it.
  async function stallAfterDisable() {
    api.getIdpConnection.mockResolvedValue(
      connection({ enabled: true, discovery: discovery({ approved: true }) }),
    );
    renderDetail();
    await screen.findByRole('button', { name: 'Disable' });
  }
  async function disableAndStall() {
    api.getIdpConnection.mockReturnValue(new Promise(() => undefined));
    fireEvent.click(screen.getByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(api.updateIdpConnection).toHaveBeenCalled());
  }

  it('disables a dirty settings save while the connection is being re-read', async () => {
    await stallAfterDisable();
    openTab(/settings/i);
    fireEvent.change(await screen.findByLabelText('Client ID'), { target: { value: 'client-2' } });
    const save = screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement;
    expect(save.disabled).toBe(false);
    await disableAndStall();
    await waitFor(() => expect(save.disabled).toBe(true));
  });

  it('disables a dirty tenants save while the connection is being re-read', async () => {
    await stallAfterDisable();
    openTab(/tenants/i);
    fireEvent.click(await screen.findByRole('button', { name: 'Remove Acme' }));
    const save = screen.getByRole('button', { name: 'Save tenants' }) as HTMLButtonElement;
    expect(save.disabled).toBe(false);
    await disableAndStall();
    await waitFor(() => expect(save.disabled).toBe(true));
  });

  it('disables link-request decisions while the list is being re-read', async () => {
    api.listSsoLinkRequests.mockResolvedValue([
      { id: 'r1', connection: 'acme-sso', subject: 's1', email: 'a@example.com', createdAt: null,
        identityExists: true, subjectChanged: false, manualMemberships: [] },
      { id: 'r2', connection: 'acme-sso', subject: 's2', email: 'b@example.com', createdAt: null,
        identityExists: true, subjectChanged: false, manualMemberships: [] },
    ]);
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: /approve the link request from a@example.com/i }));
    api.listSsoLinkRequests.mockReturnValue(new Promise(() => undefined));
    await waitFor(() => expect(api.approveSsoLinkRequest).toHaveBeenCalled());
    const other = screen.getByRole('button', { name: /approve the link request from b@example.com/i });
    await waitFor(() => expect((other as HTMLButtonElement).disabled).toBe(true));
  });
});

describe('server refusals reach the screen', () => {
  const refuse = (fn: (typeof api)[keyof typeof api], msg: string) =>
    fn.mockRejectedValue(new GraphQLRequestError(msg, 200));

  it('a settings save', async () => {
    refuse(api.updateIdpConnection, 'client id is out of bounds');
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    fireEvent.change(await screen.findByLabelText('Client ID'), { target: { value: 'client-2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    expect(await screen.findByText('client id is out of bounds')).toBeTruthy();
  });

  it('a secret save, and the typed secret is cleared once it is stored', async () => {
    refuse(api.setIdpConnectionSecret, 'secret store unavailable');
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    fireEvent.click(await screen.findByRole('button', { name: 'Replace' }));
    fireEvent.change(screen.getByLabelText('Client secret'), { target: { value: 'rotated' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save secret' }));
    expect(await screen.findByText('secret store unavailable')).toBeTruthy();

    api.setIdpConnectionSecret.mockResolvedValue(true);
    fireEvent.click(screen.getByRole('button', { name: 'Save secret' }));
    await waitFor(() => expect(api.toast).toHaveBeenCalled());
    // The re-read returns the same connection (it already had a secret), so nothing
    // remounts: only the explicit clear keeps the old keystrokes out of the next Replace.
    fireEvent.click(await screen.findByRole('button', { name: 'Replace' }));
    expect((screen.getByLabelText('Client secret') as HTMLInputElement).value).toBe('');
  });

  it('enable and disable', async () => {
    refuse(api.updateIdpConnection, 'approve the discovery snapshot first');
    api.getIdpConnection.mockResolvedValue(connection({ discovery: discovery({ approved: true }) }));
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Enable' }));
    expect(await screen.findByText('approve the discovery snapshot first')).toBeTruthy();
  });

  it('a delete, which stays on the page', async () => {
    refuse(api.deleteIdpConnection, 'secret store unavailable');
    renderDetail();
    await screen.findByText(FP_OLD);
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('secret store unavailable')).toBeTruthy();
    expect(screen.queryByText('list-page')).toBeNull();
  });

  it('a link approval', async () => {
    refuse(api.approveSsoLinkRequest, 'this identity already has manual memberships');
    api.listSsoLinkRequests.mockResolvedValue([
      { id: 'r1', connection: 'acme-sso', subject: 's1', email: 'a@example.com', createdAt: null,
        identityExists: true, subjectChanged: false, manualMemberships: [] },
    ]);
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: /approve the link request from a@example.com/i }));
    expect(await screen.findByText('this identity already has manual memberships')).toBeTruthy();
  });
});

describe('the client secret', () => {
  it('is never pre-filled, even when one is stored', async () => {
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    expect(await screen.findByText('Configured')).toBeTruthy();
    // Stored: no input until the operator chooses to replace it.
    expect(screen.queryByLabelText('Client secret')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }));
    const input = screen.getByLabelText('Client secret') as HTMLInputElement;
    expect(input.type).toBe('password');
    expect(input.value).toBe('');
    fireEvent.change(input, { target: { value: 'rotated' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save secret' }));
    await waitFor(() => expect(api.setIdpConnectionSecret).toHaveBeenCalledWith('acme-sso', 'rotated'));
  });

  it('shows an empty input when none is stored', async () => {
    api.getIdpConnection.mockResolvedValue(connection({ hasSecret: false }));
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    expect(((await screen.findByLabelText('Client secret')) as HTMLInputElement).value).toBe('');
    expect(screen.getByText('Not set')).toBeTruthy();
  });

  it('sends only the fields that changed when saving settings', async () => {
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    fireEvent.change(await screen.findByLabelText('Client ID'), { target: { value: 'client-2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(api.updateIdpConnection).toHaveBeenCalledTimes(1));
    expect(api.updateIdpConnection).toHaveBeenCalledWith('acme-sso', { clientId: 'client-2' });
  });
});

describe('signing out at the provider', () => {
  it('starts off, lists the addresses to register, and saves only that one field', async () => {
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    const box = await screen.findByRole('checkbox', { name: 'Also sign people out at the provider' });
    expect(box.getAttribute('aria-checked')).toBe('false');
    expect(screen.getAllByText('https://dc.example.com/login').length).toBeGreaterThan(0);
    expect(screen.getAllByText('https://dc.example.com/dash/').length).toBeGreaterThan(0);
    fireEvent.click(box);
    await waitFor(() => expect(api.updateIdpConnection).toHaveBeenCalledTimes(1));
    expect(api.updateIdpConnection).toHaveBeenCalledWith('acme-sso', { providerSignOut: true });
  });

  it('says when it can have no effect: the approved snapshot names no sign-out endpoint', async () => {
    api.getIdpConnection.mockResolvedValue(
      connection({ providerSignOut: true, discovery: discovery({ approved: true, endSessionEndpoint: null }) }),
    );
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    expect(await screen.findByText(/no approved sign-out endpoint/)).toBeTruthy();
  });

  it('says the same when the snapshot names one but has not been approved', async () => {
    api.getIdpConnection.mockResolvedValue(
      connection({
        providerSignOut: true,
        discovery: discovery({ approved: false, endSessionEndpoint: 'https://login.example.com/logout' }),
      }),
    );
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    const note = await screen.findByText(/no approved sign-out endpoint/);
    expect(note.textContent).toMatch(/has not been approved/);
  });

  it('says nothing more once the approved snapshot names one', async () => {
    api.getIdpConnection.mockResolvedValue(
      connection({
        providerSignOut: true,
        discovery: discovery({ approved: true, endSessionEndpoint: 'https://login.example.com/logout' }),
      }),
    );
    renderDetail();
    await screen.findByText(FP_OLD);
    openTab(/settings/i);
    const box = await screen.findByRole('checkbox', { name: 'Also sign people out at the provider' });
    expect(box.getAttribute('aria-checked')).toBe('true');
    expect(screen.queryByText(/no approved sign-out endpoint/)).toBeNull();
  });
});

describe('disable and delete', () => {
  it('states the consequences of deleting and does nothing if declined', async () => {
    api.confirm.mockResolvedValue(false);
    renderDetail();
    await screen.findByText(FP_OLD);
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(api.confirm).toHaveBeenCalled());
    const text = JSON.stringify(api.confirm.mock.calls[0][0]);
    expect(text).toMatch(/Memberships it managed are disabled, not removed/);
    expect(text).toMatch(/cannot be undone/);
    expect(api.deleteIdpConnection).not.toHaveBeenCalled();
  });

  it('states the consequences of disabling before it disables', async () => {
    api.getIdpConnection.mockResolvedValue(
      connection({ enabled: true, discovery: discovery({ approved: true }) }),
    );
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(api.updateIdpConnection).toHaveBeenCalledWith('acme-sso', { enabled: false }));
    expect(JSON.stringify(api.confirm.mock.calls[0][0])).toMatch(/end at their next token exchange/);
  });
});

describe('link requests', () => {
  const request = (over: Record<string, unknown> = {}) => ({
    id: 'r1',
    connection: 'acme-sso',
    subject: 'sub-123',
    email: 'pat@example.com',
    createdAt: '2026-10-02T09:00:00Z',
    identityExists: true,
    subjectChanged: false,
    manualMemberships: [],
    ...over,
  });

  it('approves a plain request without a disposition', async () => {
    api.listSsoLinkRequests.mockResolvedValue([request()]);
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: /approve the link request from pat@example.com/i }));
    await waitFor(() => expect(api.approveSsoLinkRequest).toHaveBeenCalledWith('r1', undefined));
  });

  it('blocks approval until the membership disposition is chosen, then sends it', async () => {
    api.listSsoLinkRequests.mockResolvedValue([request({ manualMemberships: ['acme'] })]);
    renderList();
    const approve = (await screen.findByRole('button', {
      name: /approve the link request from pat@example.com/i,
    })) as HTMLButtonElement;
    expect(approve.disabled).toBe(true);
    fireEvent.click(screen.getByRole('radio', { name: 'Keep manual' }));
    expect(approve.disabled).toBe(false);
    fireEvent.click(approve);
    await waitFor(() => expect(api.approveSsoLinkRequest).toHaveBeenCalledWith('r1', 'keep_manual'));
  });

  it('flags a changed subject and blocks approval when no identity exists', async () => {
    api.listSsoLinkRequests.mockResolvedValue([
      request({ subjectChanged: true }),
      request({ id: 'r2', email: 'gone@example.com', identityExists: false }),
    ]);
    renderList();
    expect(await screen.findByText('Subject changed')).toBeTruthy();
    // The identity already holds a link on this connection and the server allows one per
    // identity per connection, so approving could only fail.
    const changed = screen.getByRole('button', { name: /approve the link request from pat@example.com/i });
    expect((changed as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/remove the existing link first/i)).toBeTruthy();
    const gone = screen.getByRole('button', { name: /approve the link request from gone@example.com/i });
    expect((gone as HTMLButtonElement).disabled).toBe(true);
  });

  it('rejects after confirmation and surfaces a server refusal', async () => {
    api.listSsoLinkRequests.mockResolvedValue([request()]);
    api.rejectSsoLinkRequest.mockRejectedValue(new GraphQLRequestError('not permitted', 200));
    renderList();
    const row = (await screen.findByText('pat@example.com')).closest('tr') as HTMLElement;
    fireEvent.click(within(row).getByRole('button', { name: /reject/i }));
    expect(await screen.findByText('not permitted')).toBeTruthy();
    expect(api.confirm).toHaveBeenCalled();
  });
});

// A disable, a delete and a dropped tenant can each be refused because they would leave a
// tenant that requires single sign-on with no enabled connection. The server names the
// tenants in extensions.tenants; the page says what to do about it in the operator's language
// instead of showing the server's English prose.
describe('the stranded-tenant refusal', () => {
  const PROSE =
    'this is the last enabled identity provider connection serving a tenant that requires single sign-on (tenants: acme, globex)';
  const STRANDED = new GraphQLRequestError(PROSE, 200, [
    {
      message: PROSE,
      extensions: { code: 'SSO_ENFORCED_TENANT_STRANDED', tenants: ['acme', 'globex'] },
    },
  ]);
  const enabledConnection = () =>
    connection({ enabled: true, discovery: discovery({ approved: true }) });

  beforeEach(() => {
    api.getIdpConnection.mockResolvedValue(enabledConnection());
  });
  afterEach(async () => {
    await i18n.changeLanguage('en');
  });

  const expected = () => i18n.t('identityProviders:strandedRefusal', { tenants: 'acme, globex' });

  // The assertions compare against what i18next resolves, and an unknown key resolves to its
  // own name, so a missing catalog entry would pass silently without this.
  it.each(['en', 'es'])('has its text in %s, naming the tenants and the fix', async (lng) => {
    await i18n.changeLanguage(lng);
    const text = expected();
    expect(text).not.toContain('strandedRefusal');
    expect(text).toContain('acme, globex');
  });

  const flows: [string, (label: (k: string) => string) => Promise<void>, () => void][] = [
    [
      'disable',
      async (label) => {
        fireEvent.click(await screen.findByRole('button', { name: label('identityProviders:disableButton') }));
      },
      () => expect(api.updateIdpConnection).toHaveBeenCalledWith('acme-sso', { enabled: false }),
    ],
    [
      'delete',
      async (label) => {
        fireEvent.click(await screen.findByRole('button', { name: label('common:delete') }));
      },
      () => expect(api.deleteIdpConnection).toHaveBeenCalledWith('acme-sso'),
    ],
    [
      'dropping a tenant',
      async (label) => {
        openTab(new RegExp(label('identityProviders:tenantsTab'), 'i'));
        fireEvent.click(await screen.findByRole('button', { name: label('common:removeItem').replace('{{label}}', 'Acme') }));
        fireEvent.click(screen.getByRole('button', { name: label('identityProviders:saveTenantsButton') }));
      },
      () => expect(api.setIdpConnectionTenants).toHaveBeenCalledWith('acme-sso', []),
    ],
  ];

  describe.each(flows)('%s', (_name, act, called) => {
    it.each(['en', 'es'])('shows the mapped text in %s, not the server prose', async (lng) => {
      await i18n.changeLanguage(lng);
      api.updateIdpConnection.mockRejectedValue(STRANDED);
      api.deleteIdpConnection.mockRejectedValue(STRANDED);
      api.setIdpConnectionTenants.mockRejectedValue(STRANDED);
      renderDetail();
      await screen.findByText(FP_OLD);
      await act((k) => (k === 'common:removeItem' ? i18n.t(k, { label: '{{label}}' }) : i18n.t(k)));
      await waitFor(called);
      expect(await screen.findByText(expected())).toBeTruthy();
      expect(screen.queryByText(PROSE)).toBeNull();
    });

    it('falls back to the server message for an unknown code', async () => {
      const other = new GraphQLRequestError('something else refused', 200, [
        { message: 'something else refused', extensions: { code: 'SOMETHING_ELSE', tenants: ['acme'] } },
      ]);
      api.updateIdpConnection.mockRejectedValue(other);
      api.deleteIdpConnection.mockRejectedValue(other);
      api.setIdpConnectionTenants.mockRejectedValue(other);
      renderDetail();
      await screen.findByText(FP_OLD);
      await act((k) => (k === 'common:removeItem' ? i18n.t(k, { label: '{{label}}' }) : i18n.t(k)));
      await waitFor(called);
      expect(await screen.findByText('something else refused')).toBeTruthy();
      expect(screen.queryByText(expected())).toBeNull();
    });
  });
});

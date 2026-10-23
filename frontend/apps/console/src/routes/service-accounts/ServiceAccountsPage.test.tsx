// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's Service accounts page: what a row shows, who is offered changes, and the
// client secret an admin is shown exactly once. The transport is faked; the page and the
// error mapping are real.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  accounts: [] as unknown[],
  endpoint: 'https://dc.example.com/api/user-management/oauth/token' as string | null,
  create: vi.fn(),
  rotate: vi.fn(),
  setAccess: vi.fn(),
  setEnabled: vi.fn(),
  list: vi.fn(),
  toast: vi.fn(),
  confirm: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => h.confirm }));
vi.mock('@/lib/api/customers', () => ({
  listCustomers: vi.fn(async () => ({
    results: [{ token: 'acme', name: 'Acme Corp' }],
    pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
  })),
  getCustomerNames: vi.fn(async () => ({ acme: 'Acme Corp' })),
}));
vi.mock('@/lib/api/device-management', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/device-management')>()),
  listAccessScopeGroups: vi.fn(async () => []),
  getGroupNames: vi.fn(async () => ({})),
}));
vi.mock('@/lib/api/members', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/members')>()),
  listTenantRoles: vi.fn(async () => [
    { token: 'integrator', name: 'Integrator', description: null, authorities: ['device:read'], owner: 'TENANT' },
  ]),
}));
vi.mock('@/lib/api/service-accounts', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/service-accounts')>()),
  listServiceAccounts: h.list,
  createServiceAccount: h.create,
  rotateServiceAccountSecret: h.rotate,
  setServiceAccountAccess: h.setAccess,
  setServiceAccountEnabled: h.setEnabled,
}));

import ServiceAccountsPage from './ServiceAccountsPage';

const account = (over: Record<string, unknown>) => ({
  token: 'ci-bot',
  name: 'CI',
  description: null,
  clientId: 'dcsa_abc',
  enabled: true,
  createdBy: 'boss@oem.example',
  createdAt: '2026-10-22T00:00:00Z',
  roles: [{ token: 'integrator', name: 'Integrator', owner: 'TENANT' }],
  scope: [],
  secrets: [{ hint: 'wxyz', createdAt: '2026-10-22T00:00:00Z', expiresAt: '2027-10-22T00:00:00Z', lastUsedAt: null, active: true }],
  ...over,
});

const credentials = {
  clientId: 'dcsa_abc',
  clientSecret: 'dcsas_the-one-time-secret',
  tokenEndpoint: 'https://dc.example.com/api/user-management/oauth/token',
  serviceAccount: { token: 'ci-bot' },
};

function renderPage() {
  return render(
    <MemoryRouter>
      <ServiceAccountsPage />
    </MemoryRouter>,
  );
}

function openActions(name: string) {
  fireEvent.pointerDown(screen.getByRole('button', { name: `Actions for ${name}` }), {
    button: 0,
    ctrlKey: false,
    pointerType: 'mouse',
  });
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.endpoint = 'https://dc.example.com/api/user-management/oauth/token';
  h.create.mockReset();
  h.rotate.mockReset();
  h.setAccess.mockReset();
  h.setEnabled.mockReset();
  h.toast.mockReset();
  h.confirm.mockReset();
  h.confirm.mockResolvedValue(true);
  h.list.mockReset();
  h.list.mockImplementation(async () => ({
    page: { results: h.accounts, pagination: { pageStart: 1, pageEnd: h.accounts.length, totalRecords: h.accounts.length } },
    tokenEndpoint: h.endpoint,
  }));
});
afterEach(cleanup);

describe('the service accounts list', () => {
  it('shows each account with its client ID, roles, scope and working secrets', async () => {
    h.accounts = [account({}), account({ token: 'etl', name: null, clientId: 'dcsa_etl', scope: [{ kind: 'CUSTOMER', token: 'acme' }], secrets: [] })];
    renderPage();
    expect(await screen.findByText('dcsa_abc')).toBeTruthy();
    expect(screen.getByText('1 working (…wxyz)')).toBeTruthy();
    expect(screen.getByText('No working secret')).toBeTruthy();
    await waitFor(() => expect(screen.getByText('Acme Corp')).toBeTruthy());
    expect(screen.getByText(/grant_type=client_credentials/)).toBeTruthy();
  });

  it('offers no changes to a member without member:write', async () => {
    h.claims = { username: 'reader@oem.example', authorities: ['member:read'], scopeAnchors: [] };
    h.accounts = [account({})];
    renderPage();
    await screen.findByText('dcsa_abc');
    expect(screen.queryByRole('button', { name: 'New service account' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Actions for ci-bot' })).toBeNull();
  });

  it('says when the instance has no token endpoint', async () => {
    h.accounts = [];
    h.endpoint = null;
    renderPage();
    expect(await screen.findByText(/no OAuth issuer configured/)).toBeTruthy();
  });

  it('a scoped caller who types the URL is told why, in words, not the raw refusal', async () => {
    h.claims = { username: 'cust@acme.example', authorities: ['member:read'], scopeAnchors: ['customer:acme'] };
    const refusal = 'forbidden: this operation is not available to a member whose access is limited to part of the tenant';
    h.list.mockRejectedValue(
      new GraphQLRequestError(refusal, 200, [{ message: refusal, extensions: { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' } }]),
    );
    renderPage();
    expect(await screen.findByText(/managed by administrators of the whole tenant/)).toBeTruthy();
    expect(screen.queryByText(/^forbidden:/)).toBeNull();
    expect(screen.queryByRole('button', { name: /Actions for/ })).toBeNull();
  });

  it('asks before disabling, and does nothing when the admin declines', async () => {
    h.accounts = [account({})];
    h.confirm.mockResolvedValue(false);
    renderPage();
    await screen.findByText('dcsa_abc');
    openActions('ci-bot');
    fireEvent.click(await screen.findByText('Disable'));
    await waitFor(() => expect(h.confirm).toHaveBeenCalledTimes(1));
    expect(h.setEnabled).not.toHaveBeenCalled();
  });

  it('enables without asking', async () => {
    h.accounts = [account({ enabled: false })];
    h.setEnabled.mockResolvedValue(undefined);
    renderPage();
    await screen.findByText('dcsa_abc');
    openActions('ci-bot');
    fireEvent.click(await screen.findByText('Enable'));
    await waitFor(() => expect(h.setEnabled).toHaveBeenCalledWith('ci-bot', true));
    expect(h.confirm).not.toHaveBeenCalled();
  });

  it('holds row actions while a reload is in flight', async () => {
    h.accounts = [account({})];
    renderPage();
    await screen.findByText('dcsa_abc');
    expect((screen.getByRole('button', { name: 'Actions for ci-bot' }) as HTMLButtonElement).disabled).toBe(false);
    h.list.mockImplementation(() => new Promise(() => {}));
    h.setEnabled.mockResolvedValue(undefined);
    openActions('ci-bot');
    fireEvent.click(await screen.findByText('Disable'));
    await waitFor(() => expect(h.setEnabled).toHaveBeenCalledWith('ci-bot', false));
    // The reload after the change never answers: the sticky row still shows, its actions do not act.
    await waitFor(() =>
      expect((screen.getByRole('button', { name: 'Actions for ci-bot' }) as HTMLButtonElement).disabled).toBe(true),
    );
  });
});

describe('creating and rotating', () => {
  it('creates an account and shows its secret once', async () => {
    h.accounts = [];
    h.create.mockResolvedValue(credentials);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'New service account' }));
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: ' ci-bot ' } });
    fireEvent.click(screen.getByLabelText('Roles'));
    fireEvent.click(await screen.findByText('Integrator'));
    fireEvent.click(screen.getByRole('button', { name: 'Create service account' }));
    await waitFor(() =>
      expect(h.create).toHaveBeenCalledWith({
        token: 'ci-bot',
        name: undefined,
        description: undefined,
        roleTokens: ['integrator'],
        scope: [],
      }),
    );
    const secret = (await screen.findByLabelText('Client secret')) as HTMLInputElement;
    expect(secret.value).toBe('dcsas_the-one-time-secret');
    expect(screen.getByText(/shown only once/)).toBeTruthy();
    // Closing the panel drops the secret: nothing on the page can show it again.
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    await waitFor(() => expect(screen.queryByDisplayValue('dcsas_the-one-time-secret')).toBeNull());
  });

  it('rotates with the default overlap and refuses an overlap out of range', async () => {
    h.accounts = [account({})];
    h.rotate.mockResolvedValue(credentials);
    renderPage();
    await screen.findByText('dcsa_abc');
    openActions('ci-bot');
    fireEvent.click(await screen.findByText('Rotate secret'));
    const overlap = (await screen.findByLabelText('Overlap (hours)')) as HTMLInputElement;
    expect(overlap.value).toBe('24');
    fireEvent.change(overlap, { target: { value: '500' } });
    expect((screen.getByRole('button', { name: 'Rotate secret' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(overlap, { target: { value: '24' } });
    fireEvent.click(screen.getByRole('button', { name: 'Rotate secret' }));
    await waitFor(() => expect(h.rotate).toHaveBeenCalledWith('ci-bot', 24));
    expect(((await screen.findByLabelText('Client secret')) as HTMLInputElement).value).toBe('dcsas_the-one-time-secret');
  });

  it('maps an escalation refusal to words that say what to do', async () => {
    h.accounts = [];
    h.create.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'escalation', extensions: { code: 'ESCALATION' } }]),
    );
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'New service account' }));
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'x' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create service account' }));
    expect(await screen.findByText(/only if you hold every authority it holds/i)).toBeTruthy();
  });

  it('a refused rotation says why', async () => {
    h.accounts = [account({})];
    h.rotate.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'escalation', extensions: { code: 'ESCALATION' } }]),
    );
    renderPage();
    await screen.findByText('dcsa_abc');
    openActions('ci-bot');
    fireEvent.click(await screen.findByText('Rotate secret'));
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate secret' }));
    expect(await screen.findByText(/only if you hold every authority it holds/i)).toBeTruthy();
  });
});

describe('copying the secret', () => {
  const clipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
  const exec = Object.getOwnPropertyDescriptor(document, 'execCommand');
  afterEach(() => {
    if (clipboard) Object.defineProperty(navigator, 'clipboard', clipboard);
    else Reflect.deleteProperty(navigator, 'clipboard');
    if (exec) Object.defineProperty(document, 'execCommand', exec);
    else Reflect.deleteProperty(document, 'execCommand');
  });

  async function reveal() {
    h.accounts = [];
    h.create.mockResolvedValue(credentials);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'New service account' }));
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'ci-bot' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create service account' }));
    await screen.findByLabelText('Client secret');
  }

  it('uses the async clipboard, and the button never carries the secret', async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    await reveal();
    const button = screen.getByRole('button', { name: 'Copy client secret' });
    expect(button.outerHTML).not.toContain('dcsas_the-one-time-secret');
    fireEvent.click(button);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('dcsas_the-one-time-secret'));
    expect(h.toast).not.toHaveBeenCalled();
  });

  it('falls back to copying the selected field where there is no async clipboard', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    const copy = vi.fn(() => true);
    Object.defineProperty(document, 'execCommand', { value: copy, configurable: true });
    await reveal();
    fireEvent.click(screen.getByRole('button', { name: 'Copy client secret' }));
    await waitFor(() => expect(copy).toHaveBeenCalledWith('copy'));
    expect(document.activeElement).toBe(screen.getByLabelText('Client secret'));
    expect(h.toast).not.toHaveBeenCalled();
  });

  it('when nothing can copy, says so without the secret in the toast', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    Object.defineProperty(document, 'execCommand', { value: vi.fn(() => false), configurable: true });
    await reveal();
    fireEvent.click(screen.getByRole('button', { name: 'Copy client secret' }));
    await waitFor(() => expect(h.toast).toHaveBeenCalledTimes(1));
    expect(JSON.stringify(h.toast.mock.calls)).not.toContain('dcsas_');
  });
});

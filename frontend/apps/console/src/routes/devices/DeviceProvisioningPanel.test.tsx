// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A device's self-registration provenance and the reset that brings a revoked device back.
// Only the transport is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  gql: vi.fn(),
  toast: vi.fn(),
  confirm: vi.fn(),
}));

vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => h.confirm }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { DeviceProvisioningPanel } from './DeviceProvisioningPanel';

let state: { earmark: unknown; credentials: unknown[] };
let resets: unknown[] = [];

beforeEach(() => {
  resets = [];
  state = { earmark: null, credentials: [] };
  h.toast.mockReset();
  h.confirm.mockReset();
  h.confirm.mockResolvedValue(true);
  h.gql.mockImplementation((_service: string, doc: unknown, variables: unknown) => {
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    if (op === 'DeviceProvisioning') {
      return Promise.resolve({
        devicesByToken: [{ token: 'pump-7', expectedProvisioningProfile: state.earmark }],
        deviceCredentials: { results: state.credentials },
      });
    }
    if (op === 'ResetDeviceProvisioning') {
      resets.push(variables);
      state = { ...state, credentials: [] };
      return Promise.resolve({ resetDeviceProvisioning: 1 });
    }
    return Promise.resolve({});
  });
});
afterEach(cleanup);

const renderPanel = (canWrite = true) =>
  render(
    <MemoryRouter>
      <DeviceProvisioningPanel deviceToken="pump-7" canWrite={canWrite} />
    </MemoryRouter>,
  );

const issued = (over: Record<string, unknown> = {}) => ({
  token: 'cred-1',
  enabled: true,
  provisioningProfile: { token: 'acme-fleet', name: 'Acme fleet' },
  ...over,
});

describe('the device provisioning panel', () => {
  it('shows which profile registered the device and which it is expected for, with links', async () => {
    state = { earmark: { token: 'acme-fleet', name: 'Acme fleet' }, credentials: [issued()] };
    renderPanel();
    expect(await screen.findByText('Registered by')).toBeTruthy();
    const links = screen.getAllByRole('link', { name: 'Acme fleet' });
    expect(links.map((a) => a.getAttribute('href'))).toEqual([
      '/provisioning-profiles/acme-fleet',
      '/provisioning-profiles/acme-fleet',
    ]);
    expect(screen.getByText('Expected profile')).toBeTruthy();
  });

  it('marks a revoked registration', async () => {
    state = { earmark: null, credentials: [issued({ enabled: false })] };
    renderPanel();
    expect(await screen.findByText('Revoked')).toBeTruthy();
  });

  it('renders nothing for a device that was never involved in self-registration', async () => {
    renderPanel();
    await waitFor(() => expect(h.gql).toHaveBeenCalled());
    expect(document.body.textContent).toBe('');
  });

  it('ignores a credential an operator made', async () => {
    state = { earmark: null, credentials: [issued({ provisioningProfile: null })] };
    renderPanel();
    await waitFor(() => expect(h.gql).toHaveBeenCalled());
    expect(document.body.textContent).toBe('');
  });
});

describe('Reset provisioning', () => {
  it('asks first, naming the profile and the device, then resets', async () => {
    state = { earmark: { token: 'acme-fleet', name: 'Acme fleet' }, credentials: [issued({ enabled: false })] };
    renderPanel();
    fireEvent.click(await screen.findByRole('button', { name: 'Reset provisioning' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalledTimes(1));
    const options = h.confirm.mock.calls[0][0] as { description: string };
    expect(options.description).toContain('Acme fleet');
    expect(options.description).toContain('pump-7');
    expect(options.description).toContain('registers again');

    await waitFor(() => expect(resets).toEqual([{ deviceToken: 'pump-7' }]));
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('Provisioning of “pump-7” reset'));
    // The panel re-reads: with the credential gone and the earmark kept, it shows the earmark only.
    await waitFor(() => expect(screen.queryByText('Registered by')).toBeNull());
    expect(screen.getByText('Expected profile')).toBeTruthy();
  });

  it('resets nothing when the operator declines', async () => {
    h.confirm.mockResolvedValue(false);
    state = { earmark: null, credentials: [issued()] };
    renderPanel();
    fireEvent.click(await screen.findByRole('button', { name: 'Reset provisioning' }));
    await waitFor(() => expect(h.confirm).toHaveBeenCalled());
    expect(resets).toHaveLength(0);
  });

  it('is not offered to a member who cannot write', async () => {
    state = { earmark: null, credentials: [issued()] };
    renderPanel(false);
    await screen.findByText('Registered by');
    expect(screen.queryByRole('button', { name: 'Reset provisioning' })).toBeNull();
  });
});

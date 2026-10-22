// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. Three seams are faked: the GraphQL transport,
// the toast and the confirm dialog. The real API module shapes every request.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, confirmMock } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  confirmMock: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => confirmMock }));

import { DeviceGatewayPanel } from './DeviceGatewayPanel';

const page = (over: Record<string, unknown> = {}) => ({
  devicesByToken: [
    {
      token: 'pump-7',
      gateway: null,
      children: { results: [], pagination: { totalRecords: 0 } },
      ...over,
    },
  ],
});

function mount(canWrite = true) {
  return render(
    <MemoryRouter>
      <DeviceGatewayPanel deviceToken="pump-7" canWrite={canWrite} />
    </MemoryRouter>,
  );
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  confirmMock.mockReset();
});

describe('DeviceGatewayPanel', () => {
  it('says a device with no gateway is behind none, and offers to assign one', async () => {
    gqlMock.mockResolvedValue(page());
    mount();
    expect(await screen.findByText('This device is not behind a gateway.')).toBeTruthy();
    expect(screen.getByRole('button', { name: /Assign gateway/ })).toBeTruthy();
    expect(screen.getByText('No devices are assigned to this device as their gateway.')).toBeTruthy();
  });

  it('assigns the gateway token it was given, trimmed, and reloads', async () => {
    gqlMock.mockResolvedValueOnce(page());
    gqlMock.mockResolvedValueOnce({ assignDeviceGateway: { token: 'pump-7' } });
    gqlMock.mockResolvedValue(page({ gateway: { token: 'site-gw', name: null, enabled: true } }));
    mount();

    fireEvent.change(await screen.findByPlaceholderText('site-gateway-1'), { target: { value: '  site-gw ' } });
    fireEvent.click(screen.getByRole('button', { name: /Assign gateway/ }));

    await waitFor(() =>
      expect(gqlMock).toHaveBeenCalledWith('device-management', expect.anything(), {
        deviceToken: 'pump-7',
        gatewayToken: 'site-gw',
      }),
    );
    expect(toastMock).toHaveBeenCalledWith('Gateway “site-gw” assigned');
    expect(await screen.findByText('site-gw')).toBeTruthy();
  });

  it('shows the gateway the device is behind and clears it after confirming', async () => {
    gqlMock.mockResolvedValueOnce(page({ gateway: { token: 'site-gw', name: 'Site gateway', enabled: true } }));
    gqlMock.mockResolvedValueOnce({ clearDeviceGateway: { token: 'pump-7' } });
    gqlMock.mockResolvedValue(page());
    confirmMock.mockResolvedValue(true);
    mount();

    expect(await screen.findByText('site-gw')).toBeTruthy();
    expect(screen.getByText('Site gateway')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /Clear gateway/ }));

    await waitFor(() =>
      expect(gqlMock).toHaveBeenCalledWith('device-management', expect.anything(), { deviceToken: 'pump-7' }),
    );
    expect(confirmMock).toHaveBeenCalledWith(expect.objectContaining({ title: 'Clear gateway', destructive: true }));
    expect(toastMock).toHaveBeenCalledWith('Gateway cleared');
  });

  it('does not clear when the confirmation is declined', async () => {
    gqlMock.mockResolvedValueOnce(page({ gateway: { token: 'site-gw', name: null, enabled: true } }));
    confirmMock.mockResolvedValue(false);
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /Clear gateway/ }));
    await waitFor(() => expect(confirmMock).toHaveBeenCalled());
    expect(gqlMock).toHaveBeenCalledTimes(1); // the read only
  });

  it('lists the devices behind a gateway, noting when the page is partial', async () => {
    gqlMock.mockResolvedValue(
      page({
        children: {
          results: [
            { token: 'c-1', name: 'Pump 1', enabled: true },
            { token: 'c-2', name: null, enabled: false },
          ],
          pagination: { totalRecords: 250 },
        },
      }),
    );
    mount();
    expect(await screen.findByText('c-1')).toBeTruthy();
    expect(screen.getByText('c-2')).toBeTruthy();
    expect(screen.getByText('Showing 2 of 250 devices.')).toBeTruthy();
    expect(screen.getAllByText('Disabled').length).toBeGreaterThan(0);
  });

  it('offers no change to a member who may not manage credentials', async () => {
    gqlMock.mockResolvedValue(page({ gateway: { token: 'site-gw', name: null, enabled: true } }));
    mount(false);
    expect(await screen.findByText('site-gw')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Clear gateway/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Move/ })).toBeNull();
  });

  it('refuses an empty gateway token without calling the server', async () => {
    gqlMock.mockResolvedValue(page());
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /Assign gateway/ }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Enter the token of the gateway device.', 'error'));
    expect(gqlMock).toHaveBeenCalledTimes(1);
  });
});

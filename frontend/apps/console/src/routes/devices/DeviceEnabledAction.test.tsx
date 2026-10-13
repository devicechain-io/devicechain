// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. Three seams are faked: the GraphQL
// transport, the toast and the confirm dialog. The real API module shapes the request.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

import { DeviceEnabledAction } from './DeviceEnabledAction';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  confirmMock.mockReset();
  gqlMock.mockResolvedValue({ updateDevice: { token: 'pump-7' } });
});

describe('DeviceEnabledAction', () => {
  it('disables an enabled device after confirming, sending only the enabled flag', async () => {
    confirmMock.mockResolvedValue(true);
    const onChanged = vi.fn();
    render(<DeviceEnabledAction device={{ token: 'pump-7', enabled: true }} onChanged={onChanged} />);

    fireEvent.click(screen.getByRole('button', { name: /Disable/ }));

    await waitFor(() => expect(onChanged).toHaveBeenCalledOnce());
    expect(confirmMock).toHaveBeenCalledWith(expect.objectContaining({ title: 'Disable device', destructive: true }));
    // A partial update that names `enabled` and nothing else: re-sending fields the
    // action never showed would write back a stale copy of them.
    expect(gqlMock).toHaveBeenCalledWith('device-management', expect.anything(), {
      token: 'pump-7',
      request: { enabled: false },
    });
    expect(toastMock).toHaveBeenCalledWith('Device “pump-7” disabled');
  });

  it('enables a disabled device', async () => {
    confirmMock.mockResolvedValue(true);
    const onChanged = vi.fn();
    render(<DeviceEnabledAction device={{ token: 'pump-7', enabled: false }} onChanged={onChanged} />);

    fireEvent.click(screen.getByRole('button', { name: /Enable/ }));

    await waitFor(() => expect(onChanged).toHaveBeenCalledOnce());
    expect(gqlMock).toHaveBeenCalledWith('device-management', expect.anything(), {
      token: 'pump-7',
      request: { enabled: true },
    });
  });

  it('changes nothing when the operator cancels', async () => {
    confirmMock.mockResolvedValue(false);
    const onChanged = vi.fn();
    render(<DeviceEnabledAction device={{ token: 'pump-7', enabled: true }} onChanged={onChanged} />);

    fireEvent.click(screen.getByRole('button', { name: /Disable/ }));

    await waitFor(() => expect(confirmMock).toHaveBeenCalledOnce());
    expect(gqlMock).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();
  });
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The operator's view of the hold on a DISABLED device: its queued commands are neither sent nor
// cancelled, and this notice says how many and offers the one action that is theirs to take.
// Only the GraphQL wire (`gql`) and the claims are faked; the confirm dialog, the real catalogs
// and the real authority check are genuine, so the assertions are on what an operator sees.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, authorities } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  authorities: { value: ['command:read', 'command:write'] as string[] },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { HeldWhileDisabledNotice } from './HeldWhileDisabledNotice';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authorities.value = ['command:read', 'command:write'];
});

function holdIs(hold: { disabled: boolean; heldCount: number } | Error) {
  gqlMock.mockImplementation((_service: string, document: unknown) => {
    if (String(document).includes('mutation CancelDeviceCommands')) {
      return Promise.resolve({ cancelDeviceCommands: { cancelled: 3 } });
    }
    return hold instanceof Error
      ? Promise.reject(hold)
      : Promise.resolve({ deviceCommandHold: hold });
  });
}

function mount(onCancelled = vi.fn()) {
  render(
    <ConfirmProvider>
      <HeldWhileDisabledNotice deviceToken="therm-1" reloadKey={0} onCancelled={onCancelled} />
    </ConfirmProvider>,
  );
  return onCancelled;
}

describe('HeldWhileDisabledNotice', () => {
  it('states the count of queued commands held while the device is disabled', async () => {
    holdIs({ disabled: true, heldCount: 3 });
    mount();

    expect(
      await screen.findByText('3 queued commands held while this device is disabled'),
    ).toBeTruthy();
  });

  it('uses the singular for one command', async () => {
    holdIs({ disabled: true, heldCount: 1 });
    mount();

    expect(
      await screen.findByText('1 queued command held while this device is disabled'),
    ).toBeTruthy();
  });

  // The three states in which a banner would assert a hold the platform is not keeping. Each is
  // awaited by the read having been made, not by a timeout: a notice that never rendered
  // because the read had not landed yet would pass this for the wrong reason.
  it.each([
    ['an enabled device', { disabled: false, heldCount: 5 }],
    ['a disabled device holding nothing', { disabled: true, heldCount: 0 }],
    ['a read that failed', new Error('boom')],
  ])('shows nothing for %s', async (_name, hold) => {
    holdIs(hold as { disabled: boolean; heldCount: number } | Error);
    mount();

    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByTestId('held-while-disabled')).toBeNull();
  });

  it('cancels every held command after confirmation, and reports how many', async () => {
    holdIs({ disabled: true, heldCount: 3 });
    const onCancelled = mount();

    fireEvent.click(await screen.findByRole('button', { name: 'Cancel all' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel the commands' }));

    await waitFor(() => expect(onCancelled).toHaveBeenCalled());
    const mutation = gqlMock.mock.calls.find((c) => String(c[1]).includes('mutation CancelDeviceCommands'));
    expect(mutation?.[2]).toEqual({ deviceToken: 'therm-1' });
    expect(toastMock).toHaveBeenCalledWith('3 held commands cancelled');
  });

  it('cancels nothing when the confirmation is dismissed', async () => {
    holdIs({ disabled: true, heldCount: 3 });
    const onCancelled = mount();

    fireEvent.click(await screen.findByRole('button', { name: 'Cancel all' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Keep them' }));

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(onCancelled).not.toHaveBeenCalled();
    expect(
      gqlMock.mock.calls.some((c) => String(c[1]).includes('mutation CancelDeviceCommands')),
    ).toBe(false);
  });

  it('offers no cancel action without command:write, but still states the hold', async () => {
    authorities.value = ['command:read'];
    holdIs({ disabled: true, heldCount: 2 });
    mount();

    await screen.findByText('2 queued commands held while this device is disabled');
    expect(screen.queryByRole('button', { name: 'Cancel all' })).toBeNull();
  });
});

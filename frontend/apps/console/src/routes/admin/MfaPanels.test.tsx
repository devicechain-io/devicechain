// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The operator's two-factor controls. The tenant panel locks when the tier requires a second
// factor (no tenant setting can go below the floor). The identity reset tries the plain reset
// first and asks for a NEW PASSWORD only when the server answers MFA_RESET_NEEDS_PASSWORD, so an
// operator is never asked for a password the server does not need and a refused reset always
// says what to do next.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

const h = vi.hoisted(() => ({ setMfa: vi.fn(), reset: vi.fn() }));
vi.mock('@/lib/api/admin', () => ({
  setTenantMfaRequired: (...a: unknown[]) => h.setMfa(...a),
  resetIdentityMfa: (...a: unknown[]) => h.reset(...a),
}));

import { TenantMfaPanel } from './tenants/TenantMfaPanel';
import { IdentityMfaPanel } from './identities/IdentityMfaPanel';

const t = (key: string) => i18n.t(key);
const wrap = (ui: React.ReactNode) =>
  render(
    <ToastProvider>
      <ConfirmProvider>{ui}</ConfirmProvider>
    </ToastProvider>,
  );

beforeEach(async () => {
  h.setMfa.mockReset();
  h.reset.mockReset();
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('the tenant panel', () => {
  it('requires after a confirmation', async () => {
    h.setMfa.mockResolvedValue({});
    const onChanged = vi.fn();
    wrap(<TenantMfaPanel tenant="acme" required={false} byTier={false} onChanged={onChanged} />);
    fireEvent.click(screen.getByRole('button', { name: t('tenants:mfaRequire') }));
    const dialog = await screen.findByRole('dialog');
    expect(h.setMfa).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: t('tenants:mfaRequire') }));
    await waitFor(() => expect(h.setMfa).toHaveBeenCalledWith('acme', true));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it('is locked, with the reason, when the tier requires it', () => {
    wrap(<TenantMfaPanel tenant="acme" required={true} byTier={true} onChanged={vi.fn()} />);
    expect((screen.getByRole('button', { name: t('tenants:mfaStopRequiring') }) as HTMLButtonElement).disabled).toBe(true);
    screen.getByText(t('tenants:mfaByTierNote'));
    expect(screen.getByText(t('tenants:mfaRequiredBadge'))).toBeTruthy();
  });

  it('reads as required when only the tier requires it', () => {
    wrap(<TenantMfaPanel tenant="acme" required={false} byTier={true} onChanged={vi.fn()} />);
    expect(screen.getByText(t('tenants:mfaRequiredBadge'))).toBeTruthy();
  });
});

describe('the identity reset', () => {
  const needsPassword = new GraphQLRequestError('needs password', 200, [
    { message: 'needs password', extensions: { code: 'MFA_RESET_NEEDS_PASSWORD' } },
  ]);

  const confirmReset = async () => {
    fireEvent.click(screen.getByRole('button', { name: t('identities:mfaReset') }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: t('identities:mfaReset') }));
  };

  it('is offered only when the identity has a factor', () => {
    wrap(<IdentityMfaPanel email="a@b.c" enabled={false} onChanged={vi.fn()} />);
    expect(screen.queryByRole('button', { name: t('identities:mfaReset') })).toBeNull();
  });

  it('resets without a password when the server does not ask for one', async () => {
    h.reset.mockResolvedValue({});
    const onChanged = vi.fn();
    wrap(<IdentityMfaPanel email="a@b.c" enabled={true} onChanged={onChanged} />);
    await confirmReset();
    await waitFor(() => expect(h.reset).toHaveBeenCalledWith('a@b.c', undefined));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(screen.queryByLabelText(t('identities:mfaNewPasswordLabel'))).toBeNull();
  });

  it('asks for a new password on MFA_RESET_NEEDS_PASSWORD, then resets with it', async () => {
    h.reset.mockRejectedValueOnce(needsPassword).mockResolvedValueOnce({});
    const onChanged = vi.fn();
    wrap(<IdentityMfaPanel email="a@b.c" enabled={true} onChanged={onChanged} />);
    await confirmReset();

    const field = await screen.findByLabelText(t('identities:mfaNewPasswordLabel'));
    screen.getByText(t('identities:mfaResetNeedsPassword'));
    const submit = screen.getByRole('button', { name: t('identities:mfaResetWithPassword') }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true); // a password is required to proceed
    fireEvent.change(field, { target: { value: 'N3w-passw0rd!' } });
    fireEvent.click(submit);
    await waitFor(() => expect(h.reset).toHaveBeenLastCalledWith('a@b.c', 'N3w-passw0rd!'));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it('shows any other refusal as written, without asking for a password', async () => {
    const msg = 'only a superuser may reset this identity';
    h.reset.mockRejectedValue(new GraphQLRequestError(msg, 200, [{ message: msg }]));
    wrap(<IdentityMfaPanel email="a@b.c" enabled={true} onChanged={vi.fn()} />);
    await confirmReset();
    await screen.findByText(msg);
    expect(screen.queryByLabelText(t('identities:mfaNewPasswordLabel'))).toBeNull();
  });
});

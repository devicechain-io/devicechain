// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's "Require SSO" control. Turning it on ends every member's password session,
// so it asks first; turning it off does not. The server refuses to require single sign-on
// for a tenant no enabled connection serves (it would lock its people out); that refusal
// reaches the operator by its code, in their language, not as the server's English prose.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

const setTenantSsoEnforced = vi.fn();
vi.mock('@/lib/api/admin', () => ({
  setTenantSsoEnforced: (...a: unknown[]) => setTenantSsoEnforced(...a),
}));

import { TenantSsoPanel } from './TenantSsoPanel';

const PROSE =
  'no enabled identity provider connection serves this tenant, so requiring single sign-on would lock its people out; connect one first';
const NO_CONNECTION = new GraphQLRequestError(PROSE, 200, [
  { message: PROSE, extensions: { code: 'SSO_ENFORCE_NO_CONNECTION' } },
]);

beforeEach(async () => {
  setTenantSsoEnforced.mockReset();
  await i18n.changeLanguage('en');
});
afterEach(async () => {
  cleanup();
  await i18n.changeLanguage('en');
});

function renderPanel(enforced: boolean, onChanged = vi.fn()) {
  render(
    <ToastProvider>
      <ConfirmProvider>
        <TenantSsoPanel tenant="acme" enforced={enforced} onChanged={onChanged} />
      </ConfirmProvider>
    </ToastProvider>,
  );
  return onChanged;
}

const clickRequire = () => fireEvent.click(screen.getByRole('button', { name: i18n.t('tenants:ssoRequire') }));

async function answerConfirm(confirmIt: boolean) {
  const dialog = await screen.findByRole('dialog');
  expect(within(dialog).getByText(i18n.t('tenants:ssoEnableConfirm', { token: 'acme' }))).toBeTruthy();
  fireEvent.click(
    within(dialog).getByRole('button', {
      name: confirmIt ? i18n.t('tenants:ssoRequire') : i18n.t('common:cancel'),
    }),
  );
}

describe('TenantSsoPanel', () => {
  it('asks before requiring SSO, then requires it and reloads', async () => {
    setTenantSsoEnforced.mockResolvedValue({ token: 'acme', ssoEnforced: true });
    const onChanged = renderPanel(false);
    clickRequire();
    await answerConfirm(true);
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(setTenantSsoEnforced).toHaveBeenCalledWith('acme', true);
  });

  it('changes nothing when the confirmation is declined', async () => {
    const onChanged = renderPanel(false);
    clickRequire();
    await answerConfirm(false);
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(setTenantSsoEnforced).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();
  });

  it('stops requiring it without asking', async () => {
    setTenantSsoEnforced.mockResolvedValue({ token: 'acme', ssoEnforced: false });
    const onChanged = renderPanel(true);
    expect(screen.getByText(i18n.t('tenants:ssoRequiredBadge'))).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: i18n.t('tenants:ssoStopRequiring') }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(setTenantSsoEnforced).toHaveBeenCalledWith('acme', false);
  });

  it.each(['en', 'es'])('shows the no-connection refusal by its code, in %s, and changes nothing', async (lng) => {
    await i18n.changeLanguage(lng);
    setTenantSsoEnforced.mockRejectedValue(NO_CONNECTION);
    const onChanged = renderPanel(false);
    clickRequire();
    await answerConfirm(true);
    expect(await screen.findByText(i18n.t('tenants:ssoRefusedNoConnection'))).toBeTruthy();
    expect(screen.queryByText(PROSE)).toBeNull();
    expect(onChanged).not.toHaveBeenCalled();
    expect(screen.getByText(i18n.t('tenants:ssoNotRequiredBadge'))).toBeTruthy();
  });

  it('falls back to the server message for a refusal without a known code', async () => {
    setTenantSsoEnforced.mockRejectedValue(
      new GraphQLRequestError('tenant not found', 200, [{ message: 'tenant not found' }]),
    );
    renderPanel(false);
    clickRequire();
    await answerConfirm(true);
    expect(await screen.findByText('tenant not found')).toBeTruthy();
  });

  // The assertions above compare against whatever i18next resolves; an unknown key resolves
  // to its own name, so a missing catalog entry would pass silently without this.
  it.each(['en', 'es'])('the new strings exist in %s', async (lng) => {
    await i18n.changeLanguage(lng);
    for (const key of ['ssoEnableConfirm', 'ssoEnableConfirmTitle', 'ssoRefusedNoConnection']) {
      expect(i18n.t(`tenants:${key}`)).not.toBe(key);
    }
  });
});

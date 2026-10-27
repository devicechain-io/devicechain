// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's "Require two-factor authentication" setting. The requirement in force is the
// tenant's own setting OR its tier's floor, and no tenant setting can go below the floor: so
// when the tier requires it the control must be ON, LOCKED, and say why. A control that
// silently refuses a click, or one showing "off" while a requirement is in force, would both
// misreport the state.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

const h = vi.hoisted(() => ({
  get: vi.fn(),
  set: vi.fn(),
  authorities: ['member:write'] as string[],
  scp: [] as string[],
}));

vi.mock('@/lib/api/mfa', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/mfa')>()),
  getTenantMfa: () => h.get(),
  setTenantMfaRequired: (...a: unknown[]) => h.set(...a),
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities, scopeAnchors: h.scp } }),
}));

import TenantSecurityPage from './TenantSecurityPage';

const t = (key: string) => i18n.t(key);

function renderPage() {
  render(
    <ToastProvider>
      <ConfirmProvider>
        <TenantSecurityPage />
      </ConfirmProvider>
    </ToastProvider>,
  );
}

const toggle = () => screen.findByRole('checkbox', { name: t('security:tenantRequireLabel') });

beforeEach(async () => {
  h.get.mockReset();
  h.set.mockReset();
  h.authorities = ['member:write'];
  h.scp = [];
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('the tenant requirement', () => {
  it('is off and can be turned on after a confirmation', async () => {
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: false, mfaRequiredByTier: false });
    h.set.mockResolvedValue({ token: 'acme', mfaRequired: true, mfaRequiredByTier: false });
    renderPage();
    const box = await toggle();
    expect(box.getAttribute('aria-checked')).toBe('false');
    fireEvent.click(box);
    const dialog = await screen.findByRole('dialog');
    // Nothing happens until it is confirmed.
    expect(h.set).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: t('security:tenantRequire') }));
    await waitFor(() => expect(h.set).toHaveBeenCalledWith(true));
  });

  it('does not change anything when the confirmation is declined', async () => {
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: false, mfaRequiredByTier: false });
    renderPage();
    fireEvent.click(await toggle());
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: t('common:cancel') }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(h.set).not.toHaveBeenCalled();
  });

  it('turns off without a confirmation', async () => {
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: true, mfaRequiredByTier: false });
    h.set.mockResolvedValue({ token: 'acme', mfaRequired: false, mfaRequiredByTier: false });
    renderPage();
    const box = await toggle();
    expect(box.getAttribute('aria-checked')).toBe('true');
    fireEvent.click(box);
    await waitFor(() => expect(h.set).toHaveBeenCalledWith(false));
  });

  it('is locked ON, with the reason, when the tier requires it', async () => {
    // The tenant's own setting is off; the floor alone makes it required.
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: false, mfaRequiredByTier: true });
    renderPage();
    const box = await toggle();
    expect(box.getAttribute('aria-checked')).toBe('true');
    expect((box as HTMLButtonElement).disabled).toBe(true);
    screen.getByText(t('security:tenantLockedByTier'));
    fireEvent.click(box);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(h.set).not.toHaveBeenCalled();
  });

  it('shows the server refusal instead of pretending it worked', async () => {
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: true, mfaRequiredByTier: false });
    h.set.mockRejectedValue(new Error('forbidden'));
    renderPage();
    fireEvent.click(await toggle());
    await screen.findByText('forbidden');
  });
});

describe('who may change it', () => {
  it('needs member:write', async () => {
    h.authorities = ['member:read'];
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: false, mfaRequiredByTier: false });
    renderPage();
    await screen.findByText(t('security:tenantNoPermission'));
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  it('is refused to a member limited to part of the tenant', async () => {
    h.scp = ['customer:acme'];
    h.get.mockResolvedValue({ token: 'acme', mfaRequired: false, mfaRequiredByTier: false });
    renderPage();
    await screen.findByText(t('security:tenantNoPermission'));
  });
});

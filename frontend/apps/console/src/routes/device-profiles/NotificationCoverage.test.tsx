// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An instance without the notification area must show NO coverage panel: there is nothing to
// check and nowhere to create a policy. The present-area case is the control, so "renders
// nothing" cannot pass merely because the panel never renders in this harness.

import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const listFunctionalAreas = vi.fn();
vi.mock('@/lib/api/user-management', () => ({ listFunctionalAreas: (...a: unknown[]) => listFunctionalAreas(...a) }));
vi.mock('@/lib/api/admin', () => ({ listAdminFunctionalAreas: vi.fn(async () => []) }));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    isAuthenticated: true,
    isIdentityAuthenticated: false,
    isLoading: false,
    claims: { authorities: ['notification:write'] },
  }),
}));
const listNotificationPolicies = vi.fn(async () => []);
vi.mock('@/lib/api/notifications', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  listNotificationPolicies: () => listNotificationPolicies(),
  listNotificationChannels: vi.fn(async () => []),
}));

import { AreaCapabilityProvider } from '@/lib/capabilities';
import { NotificationCoverage } from './NotificationCoverage';

afterEach(cleanup);
beforeEach(() => {
  listFunctionalAreas.mockReset();
  listNotificationPolicies.mockClear();
});

const mount = () =>
  render(
    <MemoryRouter>
      <AreaCapabilityProvider>
        <NotificationCoverage severity="major" />
      </AreaCapabilityProvider>
    </MemoryRouter>,
  );

describe('NotificationCoverage and the area catalogue', () => {
  it('renders the panel when notification-management is deployed', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management', 'notification-management']);
    const { container } = mount();
    await waitFor(() => expect(container.querySelector('[data-shot="rule-coverage"]')).not.toBeNull());
    await waitFor(() => expect(screen.queryByTestId('coverage-uncovered')).not.toBeNull());
  });

  it('renders nothing once the catalogue says the area is absent', async () => {
    listFunctionalAreas.mockResolvedValue(['user-management', 'device-management']);
    const { container } = mount();
    // Until the catalogue answers the status is unknown, which fails open; it must then retract.
    await waitFor(() => expect(listFunctionalAreas).toHaveBeenCalled());
    await waitFor(() => expect(container.querySelector('[data-shot="rule-coverage"]')).toBeNull());
    expect(container.textContent).toBe('');
  });
});

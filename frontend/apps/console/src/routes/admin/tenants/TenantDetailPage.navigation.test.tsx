// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Moving straight from one tenant to another on the same detail route. The tenant is found in
// a list that does not reload when only the token changes, and TenantForm seeds every field
// from the tenant it is first handed — so without a remount the second tenant's page showed
// the FIRST tenant's name, tier and governance overrides, and Save wrote them all to the
// second tenant.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AdminTenant } from '@/lib/api/admin';

const { updateTenant } = vi.hoisted(() => ({ updateTenant: vi.fn() }));

const TIER = { token: 'gold', name: 'Gold', color: '#d4af37' };
function tenant(token: string, name: string): AdminTenant {
  return {
    id: `id-${token}`,
    token,
    name,
    enabled: true,
    purgeState: 'NONE',
    purgeEpoch: null,
    ssoEnforced: false,
    tier: TIER,
    config: '',
    ingestReadingsPerSecond: null,
    ingestBurst: null,
    outboundCallsPerSecond: null,
    outboundBurst: null,
    aiExternalEnabled: false,
    aiInferenceRequestsPerMinute: null,
    aiInferenceBurst: null,
    provisioningRegistrationsPerSecond: null,
    provisioningBurst: null,
    heldCommandCeiling: null,
    shedPriority: null,
    geoFencePositionCeiling: null,
    geoFenceCeiling: null,
    geoFencePositionBudget: null,
    effectiveSettings: [],
    effectiveScalarSettings: [],
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
  };
}

vi.mock('@/lib/api/admin', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/admin')>();
  return {
    ...actual,
    listTenants: () => Promise.resolve([tenant('acme', 'Acme Corp'), tenant('globex', 'Globex Inc')]),
    listTenantTiers: () => Promise.resolve([TIER]),
    updateTenant: (...args: unknown[]) => {
      updateTenant(...args);
      return Promise.resolve({});
    },
  };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, isIdentityAuthenticated: true }),
}));
// The side panels read their own areas; none of them is what this test is about.
vi.mock('@/routes/admin/tenants/TenantSettingsPanel', () => ({ TenantSettingsPanel: () => null }));
vi.mock('@/routes/admin/tenants/TenantSsoPanel', () => ({ TenantSsoPanel: () => null }));
vi.mock('@/routes/admin/tenants/TenantAiModelsPanel', () => ({ TenantAiModelsPanel: () => null }));
vi.mock('./TenantDeletionPanel', () => ({ TenantDeletionPanel: () => null }));
vi.mock('@/components/AreaUnavailable', () => ({ AreaGate: () => null }));

import TenantDetailPage from './TenantDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

function GoTo({ to }: { to: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(to)}>
      go-{to}
    </button>
  );
}

afterEach(cleanup);

describe('TenantDetailPage across a direct tenant-to-tenant navigation', () => {
  it('the form shows the NEW tenant, and Save writes the new tenant’s values', async () => {
    render(
      <MemoryRouter initialEntries={['/admin/tenants/acme']}>
        <ConfirmProvider>
          <GoTo to="/admin/tenants/globex" />
          <Routes>
            <Route path="/admin/tenants/:token" element={<TenantDetailPage />} />
          </Routes>
        </ConfirmProvider>
      </MemoryRouter>,
    );
    await waitFor(async () =>
      expect(((await screen.findByLabelText('Name')) as HTMLInputElement).value).toBe('Acme Corp'),
    );

    fireEvent.click(screen.getByRole('button', { name: 'go-/admin/tenants/globex' }));
    await waitFor(async () =>
      expect(((await screen.findByLabelText('Name')) as HTMLInputElement).value).toBe('Globex Inc'),
    );

    const save = await screen.findByRole('button', { name: 'Save changes' });
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(save);
    await waitFor(() => expect(updateTenant).toHaveBeenCalledTimes(1));
    expect(updateTenant.mock.calls[0][0]).toBe('globex');
    expect((updateTenant.mock.calls[0][1] as { name: string }).name).toBe('Globex Inc');
  });
});

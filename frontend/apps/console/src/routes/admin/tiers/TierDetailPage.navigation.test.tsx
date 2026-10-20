// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Moving straight from one tier to another on the same detail route. The tier is found in a
// catalogue that does not reload when only the token changes, and TierForm seeds its fields
// from the tier it is first handed — so without a remount the second tier's page showed the
// FIRST tier's name and description, and Save wrote them to the second tier.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const { updateTenantTier } = vi.hoisted(() => ({ updateTenantTier: vi.fn() }));

function tier(token: string, name: string) {
  return {
    id: `id-${token}`,
    token,
    name,
    description: `${name} description`,
    config: '',
    color: '',
    displayOrder: 1,
    tenantCount: 0,
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
  };
}

vi.mock('@/lib/api/admin', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/admin')>();
  return {
    ...actual,
    listTenantTierCatalog: () => Promise.resolve([tier('gold', 'Gold Plan'), tier('silver', 'Silver Plan')]),
    listGovernanceDimensions: () => Promise.resolve([]),
    listTierColorPalette: () => Promise.resolve([]),
    updateTenantTier: (...args: unknown[]) => {
      updateTenantTier(...args);
      return Promise.resolve({});
    },
  };
});
// The AI-models tab reads its own area; it is not what this test is about.
vi.mock('@/routes/admin/tiers/TierAiModelsPanel', () => ({ TierAiModelsPanel: () => null }));
vi.mock('@/components/AreaUnavailable', () => ({ AreaGate: () => null }));

import TierDetailPage from './TierDetailPage';
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

const nameValue = async () => ((await screen.findByLabelText('Name')) as HTMLInputElement).value;

describe('TierDetailPage across a direct tier-to-tier navigation', () => {
  it('the form shows the NEW tier, and Save writes the new tier’s values', async () => {
    render(
      <MemoryRouter initialEntries={['/admin/tiers/gold']}>
        <ConfirmProvider>
          <GoTo to="/admin/tiers/silver" />
          <Routes>
            <Route path="/admin/tiers/:token" element={<TierDetailPage />} />
          </Routes>
        </ConfirmProvider>
      </MemoryRouter>,
    );
    await waitFor(async () => expect(await nameValue()).toBe('Gold Plan'));

    fireEvent.click(screen.getByRole('button', { name: 'go-/admin/tiers/silver' }));
    await waitFor(async () => expect(await nameValue()).toBe('Silver Plan'));

    fireEvent.click(await screen.findByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateTenantTier).toHaveBeenCalledTimes(1));
    expect(updateTenantTier.mock.calls[0][0]).toBe('silver');
    const sent = updateTenantTier.mock.calls[0][1] as { name: string; description: string };
    expect(sent.name).toBe('Silver Plan');
    expect(sent.description).toBe('Silver Plan description');
  });
});

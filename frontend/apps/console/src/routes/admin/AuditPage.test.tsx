// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The audit page's tenant filter, wired end to end: a completed deletion is offered beside the
// live tenants, labelled with its token and date, and choosing it filters the journal by the
// key its retained entries are filed under, not by the bare token.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';

const RETAINED = 'acme~2026-10-01T10:00:00Z';
const COMPLETED = '2026-10-01T10:00:00Z';

const listAdminAuditEvents = vi.fn();
const listTenants = vi.fn();
const listTenantDeletions = vi.fn();
vi.mock('@/lib/api/admin', () => ({
  listAdminAuditEvents: (...a: unknown[]) => listAdminAuditEvents(...a),
  listTenants: (...a: unknown[]) => listTenants(...a),
  listTenantDeletions: (...a: unknown[]) => listTenantDeletions(...a),
}));

import AdminAuditPage from './AuditPage';

beforeEach(async () => {
  listAdminAuditEvents.mockReset().mockResolvedValue({
    results: [],
    pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 },
  });
  listTenants.mockReset().mockResolvedValue([{ token: 'globex' }]);
  listTenantDeletions.mockReset().mockResolvedValue({
    results: [
      { token: 'acme', retainedAuditTenant: RETAINED, completedAt: COMPLETED },
      { token: 'initech', retainedAuditTenant: 'initech~2026-10-02T00:00:00Z', completedAt: null },
    ],
    pagination: { pageStart: 0, pageEnd: 0, totalRecords: 2 },
  });
  await i18n.changeLanguage('en');
});
afterEach(async () => {
  cleanup();
  await i18n.changeLanguage('en');
});

const deletedLabel = () =>
  i18n.t('adminAudit:deletedTenantOption', {
    token: 'acme',
    date: new Date(COMPLETED).toLocaleDateString(),
  });

async function openTenantFilter() {
  fireEvent.click(await screen.findByText(i18n.t('adminAudit:allTenantsPlaceholder')));
  await screen.findByText('globex');
}

describe('AdminAuditPage tenant filter', () => {
  it('filters by a completed deletion under its retained key', async () => {
    render(<AdminAuditPage />);
    await waitFor(() => expect(listTenantDeletions).toHaveBeenCalled());
    await openTenantFilter();

    expect(screen.queryByText(/initech/)).toBeNull();
    fireEvent.click(await screen.findByText(deletedLabel()));

    await waitFor(() =>
      expect(listAdminAuditEvents).toHaveBeenLastCalledWith(expect.objectContaining({ tenant: RETAINED })),
    );
  });

  it('still filters a live tenant by its token', async () => {
    render(<AdminAuditPage />);
    await openTenantFilter();
    fireEvent.click(screen.getByText('globex'));
    await waitFor(() =>
      expect(listAdminAuditEvents).toHaveBeenLastCalledWith(expect.objectContaining({ tenant: 'globex' })),
    );
  });

  it('labels the deletion in the selected language', async () => {
    await i18n.changeLanguage('es');
    render(<AdminAuditPage />);
    await openTenantFilter();
    const label = await screen.findByText(/^acme \(eliminado el /);
    expect(label.textContent).toBe(deletedLabel());
  });
});

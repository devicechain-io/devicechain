// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant-deletions page: loading, empty and error states, and a populated record.
// A deletion has no detail route (a completed one has no tenant to open), so the row is
// asserted by what it says. A failed read must show the failure, never an empty table.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, pageOf, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock('@/lib/api/admin', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/admin')>()),
  listTenantDeletions: (...a: unknown[]) => h.list(...a),
}));

import DeletionsPage from './DeletionsPage';

const t = (key: string) => i18n.t(key);

function record() {
  return {
    token: 'acme',
    epoch: '2026-10-29T12:00:00Z',
    erased: true,
    erasedAt: '2026-10-29T12:05:00Z',
    lastVerifiedAt: '2026-10-29T12:06:00Z',
    completedAt: '2026-10-30T00:00:00Z',
    rowsErased: 7,
    awaiting: '',
    elapsesAt: null,
    blockedBy: [],
    stores: [{ store: 'tsdb', complete: true, retaining: null, lastError: null, note: null }],
  };
}

describe('DeletionsPage', () => {
  beforeEach(() => {
    h.list.mockReset();
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<DeletionsPage />);
    expect(screen.getByRole('status').textContent).toBe(t('tenants:deletionsLoading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when nothing was deleted', async () => {
    h.list.mockResolvedValue(pageOf([]));
    renderListPage(<DeletionsPage />);
    expect(await screen.findByText(t('tenants:deletionsEmpty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<DeletionsPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('tenants:deletionsEmpty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a record with its token, completion and erased row count', async () => {
    h.list.mockResolvedValue(pageOf([record()]));
    renderListPage(<DeletionsPage />);
    const cell = await screen.findByText('acme');
    const row = cell.closest('tr') as HTMLElement;
    expect(row.textContent).toContain(t('tenants:deletionsStatusComplete'));
    expect(row.textContent).toContain('7');
  });
});

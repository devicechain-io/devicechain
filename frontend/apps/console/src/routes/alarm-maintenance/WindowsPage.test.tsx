// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm-maintenance windows list: loading, empty and error states, a populated row
// linking to the window, and the write gate: the create link needs alarm:write.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, pageOf, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({
  list: vi.fn(),
  claims: { authorities: [] as string[], scopeAnchors: [] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: h.claims }),
}));
vi.mock('@/lib/api/alarm-maintenance', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/alarm-maintenance')>()),
  listWindows: (...a: unknown[]) => h.list(...a),
}));

import WindowsPage from './WindowsPage';

const t = (key: string) => i18n.t(key);

const window1 = { token: 'dock-works', name: 'Dock works', reason: 'crane replacement' };

describe('WindowsPage', () => {
  beforeEach(() => {
    h.list.mockReset();
    h.claims = { authorities: ['alarm:read', 'alarm:write'], scopeAnchors: [] };
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<WindowsPage />);
    expect(screen.getByRole('status')).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when there are no windows', async () => {
    h.list.mockResolvedValue(pageOf([]));
    renderListPage(<WindowsPage />);
    expect(await screen.findByText(t('alarmMaintenance:empty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<WindowsPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('alarmMaintenance:empty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a window whose name links to its detail route', async () => {
    h.list.mockResolvedValue(pageOf([window1]));
    renderListPage(<WindowsPage />);
    const link = await screen.findByRole('link', { name: 'Dock works' });
    expect(screen.getByText('crane replacement')).toBeTruthy();
    fireEvent.click(link);
    expect(screen.getByTestId('where').textContent).toBe('/alarm-maintenance/dock-works');
  });

  it('offers the create link to a writer', async () => {
    h.list.mockResolvedValue(pageOf([window1]));
    renderListPage(<WindowsPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('link', { name: t('alarmMaintenance:create') }).getAttribute('href')).toBe(
      '/alarm-maintenance/new',
    );
  });

  it('offers no create link to a read-only member, who still sees the windows', async () => {
    h.claims = { authorities: ['alarm:read'], scopeAnchors: [] };
    h.list.mockResolvedValue(pageOf([window1]));
    renderListPage(<WindowsPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('link', { name: 'Dock works' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: t('alarmMaintenance:create') })).toBeNull();
  });
});

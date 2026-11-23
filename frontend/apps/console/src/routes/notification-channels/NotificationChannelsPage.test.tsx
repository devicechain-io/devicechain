// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The notification-channels list: loading, empty and error states, a populated row (which
// opens the edit drawer, there being no detail route), and the write gate: without
// notification:write there is no create button, no per-row delete, and the drawer says the
// channel is read-only.

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
vi.mock('@/lib/api/notifications', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/notifications')>()),
  listNotificationChannelRecords: (...a: unknown[]) => h.list(...a),
  listNotificationChannelTypes: () => Promise.resolve([]),
}));

import NotificationChannelsPage from './NotificationChannelsPage';

const t = (key: string, opts: Record<string, unknown> = {}) => i18n.t(key, opts);

function row() {
  return {
    token: 'ops-email',
    name: 'Ops email',
    channelType: 'email',
    enabled: true,
    hasSecret: false,
    config: '{}',
  };
}

describe('NotificationChannelsPage', () => {
  beforeEach(() => {
    h.list.mockReset();
    h.claims = { authorities: ['notification:read', 'notification:write'], scopeAnchors: [] };
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<NotificationChannelsPage />);
    expect(screen.getByRole('status').textContent).toBe(t('common:loading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when there are none', async () => {
    h.list.mockResolvedValue(pageOf([]));
    renderListPage(<NotificationChannelsPage />);
    expect(await screen.findByText(t('entities:notificationChannelListEmpty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<NotificationChannelsPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('entities:notificationChannelListEmpty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists a row that opens its edit drawer', async () => {
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<NotificationChannelsPage />);
    const rowEl = await screen.findByRole('button', { name: /Ops email/ });
    fireEvent.click(rowEl);
    expect(await screen.findByText(t('entities:notificationChannelEdit', { token: 'ops-email' }))).toBeTruthy();
  });

  it('offers create and delete to a writer', async () => {
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<NotificationChannelsPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('button', { name: new RegExp(t('entities:notificationChannelNew')) })).toBeTruthy();
    expect(screen.getByRole('button', { name: t('common:delete') })).toBeTruthy();
  });

  it('offers neither create nor delete to a read-only member, whose drawer is read-only', async () => {
    h.claims = { authorities: ['notification:read'], scopeAnchors: [] };
    h.list.mockResolvedValue(pageOf([row()]));
    renderListPage(<NotificationChannelsPage />);
    const rowEl = await screen.findByRole('button', { name: /Ops email/ });
    expect(screen.queryByRole('button', { name: new RegExp(t('entities:notificationChannelNew')) })).toBeNull();
    expect(screen.queryByRole('button', { name: t('common:delete') })).toBeNull();
    fireEvent.click(rowEl);
    expect(await screen.findByText(t('entities:notificationChannelReadOnly'))).toBeTruthy();
  });
});

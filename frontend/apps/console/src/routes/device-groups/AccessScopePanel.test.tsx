// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A device group's "Use as access scope" switch: offered only for a published dynamic group
// of devices, the server's limit said plainly, and every other group told what would make it
// eligible instead of offered a switch the server would refuse.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  authorities: ['device:read', 'device:write'] as string[],
  scope: [] as string[],
  group: null as unknown,
  setAccess: vi.fn(),
  publish: vi.fn(),
  toast: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities, scopeAnchors: h.scope } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => true) }));
vi.mock('@/lib/api/device-management', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/device-management')>()),
  getGroupAccess: vi.fn(async () => h.group),
  setGroupAccessScope: h.setAccess,
  publishEntityGroup: h.publish,
}));

import { AccessScopePanel } from './AccessScopePanel';

const group = (over: Record<string, unknown>) => ({
  token: 'north',
  memberType: 'device',
  membershipMode: 'dynamic',
  activeVersion: 2,
  accessScope: false,
  ...over,
});

beforeEach(() => {
  h.authorities = ['device:read', 'device:write'];
  h.scope = [];
  h.setAccess.mockReset();
  h.publish.mockReset();
  h.toast.mockReset();
});
afterEach(cleanup);

describe('the access-scope switch', () => {
  it('makes a published dynamic device group an access scope', async () => {
    h.group = group({});
    h.setAccess.mockResolvedValue(group({ accessScope: true }));
    render(<AccessScopePanel token="north" />);
    const box = await screen.findByRole('checkbox', { name: 'Use as access scope' });
    expect(screen.getByText(/Members follow published version 2/)).toBeTruthy();
    fireEvent.click(box);
    await waitFor(() => expect(h.setAccess).toHaveBeenCalledWith('north', true));
    expect(h.toast).toHaveBeenCalledWith('north is now an access scope.');
  });

  it('stops a group being one, after confirming', async () => {
    h.group = group({ accessScope: true });
    h.setAccess.mockResolvedValue(group({ accessScope: false }));
    render(<AccessScopePanel token="north" />);
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Use as access scope' }));
    await waitFor(() => expect(h.setAccess).toHaveBeenCalledWith('north', false));
  });

  it('says the tenant limit plainly rather than the raw refusal', async () => {
    h.group = group({});
    h.setAccess.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [
        { message: 'access-scope groups: 33 exceeds the limit of 32', extensions: { code: 'LIMIT_EXCEEDED' } },
      ]),
    );
    render(<AccessScopePanel token="north" />);
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Use as access scope' }));
    expect(await screen.findByText(/at most 32 access-scope groups/)).toBeTruthy();
    expect(screen.queryByText(/exceeds the limit/)).toBeNull();
  });

  it('offers no switch for a static group, and says why', async () => {
    h.group = group({ membershipMode: 'static', activeVersion: null });
    render(<AccessScopePanel token="north" />);
    expect(await screen.findByText(/Only a dynamic group/)).toBeTruthy();
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  it('offers a first publish for a dynamic group never published, and no switch until then', async () => {
    h.group = group({ activeVersion: null });
    h.publish.mockResolvedValue(1);
    render(<AccessScopePanel token="north" />);
    expect(await screen.findByText(/has not been published yet/)).toBeTruthy();
    expect(screen.queryByRole('checkbox')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Publish filter' }));
    await waitFor(() => expect(h.publish).toHaveBeenCalledWith('north', 0));
  });

  // Someone else published between this view's render and the click: the server refuses the
  // stated version, and the panel says so and reloads rather than reporting a publish.
  it('says so, and reloads, when the group moved on before the first publish', async () => {
    h.group = group({ activeVersion: null });
    h.publish.mockRejectedValue(
      new GraphQLRequestError('entity group was modified by another writer; reload and try again', 200, [
        { message: 'entity group was modified by another writer; reload and try again' },
      ]),
    );
    render(<AccessScopePanel token="north" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Publish filter' }));
    expect(await screen.findByText(/Someone else changed this group/)).toBeTruthy();
    expect(h.toast).not.toHaveBeenCalled();
  });

  // Publishing is a device write like the switch: someone who cannot write devices is told the
  // group is unpublished and offered nothing the server would refuse.
  it('offers no first publish to someone who cannot write devices', async () => {
    h.authorities = ['device:read'];
    h.group = group({ activeVersion: null });
    render(<AccessScopePanel token="north" />);
    expect(await screen.findByText(/has not been published yet/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Publish filter' })).toBeNull();
    expect(h.publish).not.toHaveBeenCalled();
  });

  it('shows the state but no working switch to someone who cannot write devices', async () => {
    h.authorities = ['device:read'];
    h.group = group({ accessScope: true });
    render(<AccessScopePanel token="north" />);
    const box = await screen.findByRole('checkbox', { name: 'Use as access scope' });
    expect(box.hasAttribute('disabled')).toBe(true);
    expect(screen.getByText(/This group is an access scope/)).toBeTruthy();
  });
});

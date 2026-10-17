// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant chip names what a scoped member is limited to — customers and groups of devices
// alike, by name — so a person switching between a full membership and a limited one can see
// which they are in.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ scope: [] as string[] }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['device:read'], roles: ['viewer'], scopeAnchors: h.scope } }),
}));
vi.mock('@/auth/TenantProvider', () => ({ useCurrentTenant: () => ({ token: 'oem', name: 'OEM', branding: null }) }));
vi.mock('@/lib/useBrandingLogo', () => ({ useBrandingLogoSrc: () => null }));
vi.mock('@/lib/api/customers', () => ({
  getCustomerNames: vi.fn(async (tokens: string[]) =>
    Object.fromEntries(tokens.filter((t) => t === 'acme').map((t) => [t, 'Acme Corp'])),
  ),
}));
vi.mock('@/lib/api/device-management', () => ({
  getGroupNames: vi.fn(async (tokens: string[]) =>
    Object.fromEntries(tokens.filter((t) => t === 'north').map((t) => [t, 'North wing'])),
  ),
}));

import { TenantChip } from './TenantChip';

afterEach(cleanup);

describe('the tenant chip', () => {
  it('names the group a member is limited to, beside a customer, and the role', async () => {
    h.scope = ['customer:acme', 'group:north'];
    render(<TenantChip />);
    expect(await screen.findByText('Acme Corp, North wing · viewer')).toBeTruthy();
  });

  it('shows a group it cannot name by its token, never as the whole tenant', async () => {
    h.scope = ['group:yard'];
    render(<TenantChip />);
    expect(await screen.findByText('yard · viewer')).toBeTruthy();
  });

  // The control: an unscoped member's chip names no scope at all.
  it('names no scope for a member who sees the whole tenant', () => {
    h.scope = [];
    render(<TenantChip />);
    expect(screen.getByText('OEM')).toBeTruthy();
    expect(screen.queryByText(/viewer/)).toBeNull();
  });
});

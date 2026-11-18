// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Every detail page reads its route param exactly once. The router hands useParams the value
// already decoded, so a page that decoded it again turned a token holding a '%' into another
// token ("a%2541" became "a%41", then "aA"), or threw on one such as "a%zz", which took the
// whole page down. Each page is opened at a token that is only valid if it is NOT decoded a
// second time, and must name that exact token while it loads.
//
// The data the pages load never arrives (every API function is a promise that stays
// pending), so what is asserted is the token the page itself took from the route.

import '@/i18n/config';
import type { ComponentType } from 'react';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useParams } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: { tenant: 'oem', username: 'ann', roles: [], authorities: ['*'], typ: 'access' },
    refreshMemberships: () => Promise.resolve(),
  }),
}));

// Each API module the pages read through, with every function replaced by one that never
// settles: the page stays in its loading state, which names the token it is showing.
async function pending(importOriginal: () => Promise<unknown>) {
  const mod = (await importOriginal()) as Record<string, unknown>;
  return Object.fromEntries(
    Object.entries(mod).map(([k, v]) => [k, typeof v === 'function' ? () => new Promise(() => {}) : v]),
  );
}
vi.mock('@/lib/api/admin', (o) => pending(o));
vi.mock('@/lib/api/ai-inference-admin', (o) => pending(o));
vi.mock('@/lib/api/identity-providers', (o) => pending(o));
vi.mock('@/lib/api/device-management', (o) => pending(o));
vi.mock('@/lib/api/device-state', (o) => pending(o));
vi.mock('@/lib/api/event-management', (o) => pending(o));
vi.mock('@/lib/api/integrations', (o) => pending(o));
vi.mock('@/lib/api/provisioning', (o) => pending(o));
vi.mock('@/lib/api/trust-anchors', (o) => pending(o));

import AiProviderDetailPage from '@/routes/admin/ai-providers/AiProviderDetailPage';
import IdentityProviderDetailPage from '@/routes/admin/identity-providers/IdentityProviderDetailPage';
import RoleDetailPage from '@/routes/admin/roles/RoleDetailPage';
import TenantDetailPage from '@/routes/admin/tenants/TenantDetailPage';
import TierDetailPage from '@/routes/admin/tiers/TierDetailPage';
import DeviceDetailPage from '@/routes/devices/DeviceDetailPage';
import IntegrationDetailPage from '@/routes/integrations/IntegrationDetailPage';
import ProvisioningProfileDetailPage from '@/routes/provisioning/ProvisioningProfileDetailPage';
import TrustAnchorDetailPage from '@/routes/trust-anchors/TrustAnchorDetailPage';
import { ResourceDetailPage } from '@/components/registry/ResourceDetailPage';
import type { RegistryResource } from '@/components/registry/types';

afterEach(cleanup);

// Tokens a second decode changes or throws on. The first is the one a double decode turns
// into a DIFFERENT valid token, the quiet case; the second is the one it throws on.
const TOKENS = ['sensor%2541', 'a%zz'];

function renderAt(path: string, url: string, element: React.ReactElement) {
  render(
    <MemoryRouter initialEntries={[url]}>
      <ToastProvider>
        <ConfirmProvider>
          <Routes>
            <Route path={path} element={element} />
          </Routes>
        </ConfirmProvider>
      </ToastProvider>
    </MemoryRouter>,
  );
}

const pages: { name: string; path: string; prefix: string; Page: ComponentType }[] = [
  { name: 'AI provider', path: '/admin/ai-providers/:token', prefix: '/admin/ai-providers/', Page: AiProviderDetailPage },
  { name: 'identity provider', path: '/admin/identity-providers/:token', prefix: '/admin/identity-providers/', Page: IdentityProviderDetailPage },
  { name: 'role', path: '/admin/roles/:scope/:token', prefix: '/admin/roles/tenant/', Page: RoleDetailPage },
  { name: 'tenant', path: '/admin/tenants/:token', prefix: '/admin/tenants/', Page: TenantDetailPage },
  { name: 'tier', path: '/admin/tiers/:token', prefix: '/admin/tiers/', Page: TierDetailPage },
  { name: 'device', path: '/devices/:token', prefix: '/devices/', Page: DeviceDetailPage },
  { name: 'integration', path: '/integrations/:token', prefix: '/integrations/', Page: IntegrationDetailPage },
  { name: 'provisioning profile', path: '/provisioning-profiles/:token', prefix: '/provisioning-profiles/', Page: ProvisioningProfileDetailPage },
  { name: 'trust anchor', path: '/trust-anchors/:token', prefix: '/trust-anchors/', Page: TrustAnchorDetailPage },
];

describe('the router decodes a detail page param once', () => {
  // The premise the pages rely on, pinned on its own: if a router upgrade ever stopped
  // decoding, every page below would show the escaped form instead, and this names why.
  it('useParams returns the decoded value, and does not throw on a literal "%"', () => {
    function Show() {
      return <p>{useParams<{ token: string }>().token}</p>;
    }
    renderAt('/x/:token', '/x/' + encodeURIComponent('a%zz'), <Show />);
    expect(screen.getByText('a%zz')).toBeTruthy();
  });

  for (const { name, path, prefix, Page } of pages) {
    for (const token of TOKENS) {
      it(`the ${name} page shows ${token}, the token in its address`, async () => {
        renderAt(path, prefix + encodeURIComponent(token), <Page />);
        expect((await screen.findAllByText(token)).length).toBeGreaterThan(0);
      });
    }
  }

  for (const token of TOKENS) {
    it(`a registry detail page loads ${token}, the token in its address`, async () => {
      const load = vi.fn((_token: string) => new Promise<null>(() => {}));
      const resource = {
        basePath: '/things',
        i18nKey: 'thing',
        list: () => new Promise(() => {}),
        load,
        remove: () => Promise.resolve(null),
        idOf: (t: { token: string }) => t.token,
        tokenOf: (t: { token: string }) => t.token,
        columns: [],
        nameOf: (t: { token: string }) => t.token,
        renderForm: () => null,
      } as unknown as RegistryResource<{ token: string }>;
      renderAt('/things/:token', '/things/' + encodeURIComponent(token), <ResourceDetailPage resource={resource} />);
      expect((await screen.findAllByText(token)).length).toBeGreaterThan(0);
      expect(load).toHaveBeenCalledWith(token);
    });
  }
});

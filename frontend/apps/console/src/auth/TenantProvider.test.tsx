// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THIS FILE EXISTS BECAUSE A MUTATION SURVIVED.
//
// Deleting `<TenantBasemapProvider>` from TenantProvider left all 400 console tests
// green. That is the exact failure mode the provider's own comment describes: a host
// that forgets it does not fail loudly — every map simply renders as it did before
// the tenant setting existed. Nothing behavioural in the console can see it, because
// no console test renders a real map widget.
//
// So the wiring is asserted directly: a probe under TenantProvider must SEE the
// tenant's basemap through the widgets-package hook. That is the whole contract
// between the console and every map it renders.

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { tenantPayload, getCurrentTenantMock } = vi.hoisted(() => ({
  tenantPayload: { value: null as Record<string, unknown> | null },
  getCurrentTenantMock: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { tenant: 'acme' } }),
}));

vi.mock('@/lib/api/user-management', () => ({
  getCurrentTenant: () => getCurrentTenantMock(),
}));

// The cached-resource hook is replaced with a plain fetch-on-mount so this test is
// about the PROVIDER TREE and not about cache versioning.
vi.mock('@/lib/hooks/use-cached-resource', () => ({
  useCachedResource: (key: string | null, fetcher: () => Promise<unknown>) => {
    const [value, setValue] = reactUseState<unknown>(null);
    reactUseEffect(() => {
      if (!key) return;
      let cancelled = false;
      fetcher().then((v) => {
        if (!cancelled) setValue(v);
      });
      return () => {
        cancelled = true;
      };
    }, [key]);
    return [value, setValue];
  },
}));

vi.mock('@/lib/branding', () => ({ applyBranding: () => {} }));

import { useEffect as reactUseEffect, useState as reactUseState } from 'react';
import { useTenantBasemap } from '@devicechain/widgets';
import { TenantProvider } from './TenantProvider';

const OSM = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';

function Probe() {
  const basemap = useTenantBasemap();
  return <div data-testid="probe">{basemap?.tileUrl ?? 'none'}</div>;
}

afterEach(cleanup);
beforeEach(() => {
  getCurrentTenantMock.mockReset();
  tenantPayload.value = {
    token: 'acme',
    name: null,
    description: null,
    branding: null,
    brandingOverride: null,
    basemap: { tileUrl: OSM, attribution: '© OSM', centerLat: null, centerLon: null, zoom: null },
    basemapOverride: null,
  };
  getCurrentTenantMock.mockImplementation(() => Promise.resolve(tenantPayload.value));
});

describe('TenantProvider installs the basemap seam', () => {
  it('hands the tenant basemap to everything beneath it', async () => {
    render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );

    await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe(OSM));
  });

  // The counterweight: a tenant with no basemap must resolve to nothing, NOT to some
  // invented default. The plain panel is a real state and has to stay reachable.
  it('reports no basemap when the tenant has none', async () => {
    tenantPayload.value = {
      ...(tenantPayload.value as Record<string, unknown>),
      basemap: {
        tileUrl: null,
        attribution: null,
        centerLat: null,
        centerLon: null,
        zoom: null,
      },
    };
    render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );

    // Wait for the fetch to land before asserting, so this cannot pass merely by
    // reading the pre-fetch state.
    await waitFor(() => expect(getCurrentTenantMock).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('none'));
  });
});

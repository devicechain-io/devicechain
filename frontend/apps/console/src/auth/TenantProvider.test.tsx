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

import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
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
import i18n, { DEFAULT_LOCALE, LOCALE_STORAGE_KEY } from '@/i18n/config';
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
    locale: null,
    localeOverride: null,
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

// 🔴 THE SAME ARGUMENT AS THE BASEMAP SEAM ABOVE, FOR THE SAME REASON.
// `applyTenantDefaultLocale` shipped as a documented seam, with its precedence
// contract pinned by its own unit tests and ZERO call sites, and nothing in the
// console went red for it. So what is asserted here is the WIRING rather than the
// function: these drive the REAL i18next instance through the REAL seam, so deleting
// the effect from TenantProvider — or handing it the wrong field — reddens.
//
// This covers rungs 1 and 2. Rungs 3 (the browser) and 4 (English) belong to
// i18next's detector and are proved in src/i18n/config.test.ts, which is the only
// place they are reachable.
describe('TenantProvider applies the tenant default locale (precedence rung 2)', () => {
  beforeEach(async () => {
    localStorage.clear();
    await i18n.changeLanguage(DEFAULT_LOCALE);
  });

  it('switches the console to the tenant default for a user who has not chosen one', async () => {
    tenantPayload.value = { ...(tenantPayload.value as Record<string, unknown>), locale: 'es' };

    render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });

  // Rung 1 beats rung 2, asserted as a VALUE rather than as an absence: a mutant that
  // drops the explicit-choice guard flips this to 'en'.
  it('leaves an explicit user choice in place', async () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'es');
    await i18n.changeLanguage('es');
    tenantPayload.value = { ...(tenantPayload.value as Record<string, unknown>), locale: 'en' };

    render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );

    await waitFor(() => expect(getCurrentTenantMock).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByTestId('probe')).toBeTruthy());
    expect(i18n.resolvedLanguage).toBe('es');
  });

  // 🔴 The EFFECTIVE locale is what gets applied, never the raw override. Handing the
  // seam `localeOverride` instead would leave a tenant that INHERITS the operator's
  // default falling through to the browser — the cascade would resolve correctly on
  // the server and then be discarded here.
  it('applies the EFFECTIVE locale, not the raw override', async () => {
    tenantPayload.value = {
      ...(tenantPayload.value as Record<string, unknown>),
      locale: 'es',
      localeOverride: null,
    };

    render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });

  // The counterweight: a tenant with no default must not move the language. Without
  // it, a seam that simply switched to Spanish unconditionally would pass above.
  it('leaves the language alone when neither tier sets a default', async () => {
    await i18n.changeLanguage('es');
    tenantPayload.value = { ...(tenantPayload.value as Record<string, unknown>), locale: null };

    render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );

    await waitFor(() => expect(getCurrentTenantMock).toHaveBeenCalled());
    expect(i18n.resolvedLanguage).toBe('es');
  });
});

// 🔴 A tenant default must not outlive the tenant. Leaving the tenant shell — logging
// out, or crossing to the instance-scoped /admin console — unmounts TenantProvider, and
// the language has to go back to the rungs that do not need a tenant.
//
// This was argued as unnecessary while the browser rung was dead: the shipped
// `locale.default` was "en", so every tenant carried a default and i18next was never
// sitting on a browser-detected language to revert TO. With the shipped default now
// absent, it routinely is.
describe('TenantProvider hands the language back when it unmounts', () => {
  beforeEach(async () => {
    localStorage.clear();
    await i18n.changeLanguage(DEFAULT_LOCALE);
  });

  it('reverts to the browser language after the tenant shell goes away', async () => {
    vi.spyOn(window.navigator, 'languages', 'get').mockReturnValue(['en-US', 'en']);
    tenantPayload.value = { ...(tenantPayload.value as Record<string, unknown>), locale: 'es' };

    const view = render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));

    act(() => view.unmount());

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('en'));
  });

  // The counterweight: the revert re-runs DETECTION, so a user who chose a language
  // keeps it across the unmount. A cleanup that forced English instead would pass the
  // test above and take a chosen language away on every logout.
  it('leaves an explicit user choice in place across the unmount', async () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'es');
    vi.spyOn(window.navigator, 'languages', 'get').mockReturnValue(['en-US', 'en']);
    await i18n.changeLanguage('es');
    tenantPayload.value = { ...(tenantPayload.value as Record<string, unknown>), locale: null };

    const view = render(
      <TenantProvider>
        <Probe />
      </TenantProvider>,
    );
    await waitFor(() => expect(getCurrentTenantMock).toHaveBeenCalled());

    act(() => view.unmount());

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'));
  });
});

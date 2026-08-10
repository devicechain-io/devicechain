// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant basemap editor (ADR-079).
//
// What is faked here is the SEAM around the page — the tenant cache, the auth claims,
// and the mutation — and nothing else. The page's own validation runs for real, which
// is the point: every rule below is one the server also enforces, and the value of the
// client copy is that it says WHY before a round trip, not that it exists.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { authorities, tenantState, setTenantBasemapMock, applyTenantMock, toastMock } = vi.hoisted(
  () => ({
    authorities: { value: ['basemap:write'] as string[] },
    tenantState: {
      value: null as null | {
        token: string;
        basemap: Record<string, unknown> | null;
        basemapOverride: Record<string, unknown> | null;
      },
    },
    setTenantBasemapMock: vi.fn(),
    applyTenantMock: vi.fn(),
    toastMock: vi.fn(),
  }),
);

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));

vi.mock('@/auth/TenantProvider', () => ({
  useCurrentTenant: () => tenantState.value,
  useSetCurrentTenant: () => applyTenantMock,
}));

vi.mock('@/lib/api/user-management', () => ({
  setTenantBasemap: (...args: unknown[]) => setTenantBasemapMock(...args),
}));

vi.mock('@/components/ui/toast', () => ({
  useToast: () => ({ toast: toastMock }),
}));

import BasemapPage from './BasemapPage';

const OSM = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';
const CREDIT = '© OpenStreetMap contributors';

/** An inheriting tenant: nothing of its own, nothing from the operator either. */
function emptyTenant() {
  return {
    token: 'acme',
    basemap: { tileUrl: null, attribution: null, centerLat: null, centerLon: null, zoom: null },
    basemapOverride: {
      tileUrl: null,
      attribution: null,
      centerLat: null,
      centerLon: null,
      zoom: null,
    },
  };
}

afterEach(cleanup);
beforeEach(() => {
  authorities.value = ['basemap:write'];
  tenantState.value = emptyTenant();
  setTenantBasemapMock.mockReset();
  setTenantBasemapMock.mockResolvedValue({ token: 'acme' });
  applyTenantMock.mockReset();
  toastMock.mockReset();
});

function type(id: string, value: string) {
  fireEvent.change(document.getElementById(id) as HTMLInputElement, { target: { value } });
}

function saveButton(): HTMLButtonElement {
  return screen.getByRole('button', { name: /save basemap/i }) as HTMLButtonElement;
}

describe('authority', () => {
  // 🔴 branding:write must NOT open this page. The whole reason basemap:write exists
  // is that the tile URL carries the tenant's provider credential.
  it('refuses a holder of branding:write alone', () => {
    authorities.value = ['branding:write'];
    render(<BasemapPage />);

    expect(screen.queryByText('basemap:write')).toBeTruthy();
    expect(document.getElementById('bm-tile-url')).toBeNull();
  });

  it('admits a holder of basemap:write', () => {
    render(<BasemapPage />);
    expect(document.getElementById('bm-tile-url')).toBeTruthy();
  });
});

describe('seeding', () => {
  // 🔴 The save is a FULL REPLACE, so a form seeded blank before the tenant lands
  // would clear a configured basemap on the next save. Refusing to render the form
  // until the real override arrives is the only safe reading of "not loaded yet".
  it('shows a loading state rather than a blank form before the tenant arrives', () => {
    tenantState.value = null;
    render(<BasemapPage />);

    expect(document.getElementById('bm-tile-url')).toBeNull();
  });

  it('seeds each field from its own value in the raw override', () => {
    // Distinct values throughout: a fixture that reused one could not see crossed wires.
    tenantState.value = {
      token: 'acme',
      basemap: {},
      basemapOverride: {
        tileUrl: OSM,
        attribution: CREDIT,
        centerLat: 33.7468,
        centerLon: -84.3903,
        zoom: 11.5,
      },
    };
    render(<BasemapPage />);

    expect((document.getElementById('bm-tile-url') as HTMLInputElement).value).toBe(OSM);
    expect((document.getElementById('bm-attribution') as HTMLInputElement).value).toBe(CREDIT);
    expect((document.getElementById('bm-center-lat') as HTMLInputElement).value).toBe('33.7468');
    expect((document.getElementById('bm-center-lon') as HTMLInputElement).value).toBe('-84.3903');
    expect((document.getElementById('bm-zoom') as HTMLInputElement).value).toBe('11.5');
  });
});

describe('the tile source is one value', () => {
  it('blocks a tile URL with no credit line', () => {
    render(<BasemapPage />);
    type('bm-tile-url', OSM);

    expect(saveButton().disabled).toBe(true);
    expect(screen.getByText(/licence violation/i)).toBeTruthy();
  });

  it('blocks a credit line with no tile URL', () => {
    render(<BasemapPage />);
    type('bm-attribution', CREDIT);

    expect(saveButton().disabled).toBe(true);
  });

  it('accepts both halves together', async () => {
    render(<BasemapPage />);
    type('bm-tile-url', OSM);
    type('bm-attribution', CREDIT);

    expect(saveButton().disabled).toBe(false);
  });
});

describe('the tile URL', () => {
  it('blocks http, naming mixed content as the reason', () => {
    render(<BasemapPage />);
    type('bm-tile-url', 'http://tile.example.invalid/{z}/{x}/{y}.png');
    type('bm-attribution', CREDIT);

    expect(saveButton().disabled).toBe(true);
    // Matched on the REASON, not on "https" — the field's own help text mentions the
    // scheme too, so a looser matcher finds the help and passes without the error
    // ever rendering.
    expect(screen.getByText(/blocks tiles fetched over HTTP/i)).toBeTruthy();
  });

  it('blocks a URL carrying no tile placeholder', () => {
    render(<BasemapPage />);
    type('bm-tile-url', 'https://tiles.example.invalid/style.json');
    type('bm-attribution', CREDIT);

    expect(saveButton().disabled).toBe(true);
  });

  it('accepts the non-XYZ schemes', () => {
    render(<BasemapPage />);
    type('bm-tile-url', 'https://tiles.example.invalid/{quadkey}.png');
    type('bm-attribution', CREDIT);

    expect(saveButton().disabled).toBe(false);
  });
});

describe('the fallback view', () => {
  it('blocks half a coordinate', () => {
    render(<BasemapPage />);
    type('bm-center-lat', '33.7468');

    expect(saveButton().disabled).toBe(true);
  });

  it('accepts a whole one', () => {
    render(<BasemapPage />);
    type('bm-center-lat', '33.7468');
    type('bm-center-lon', '-84.3903');

    expect(saveButton().disabled).toBe(false);
  });

  // A camera with no tile source is legitimate — an operator can set where maps open
  // before choosing a provider.
  it('allows a view with no tile source at all', () => {
    render(<BasemapPage />);
    type('bm-center-lat', '0');
    type('bm-center-lon', '0');
    type('bm-zoom', '3');

    expect(saveButton().disabled).toBe(false);
  });
});

// 🔴 Both suites below come from review findings on this slice.
describe('a non-numeric camera field', () => {
  // Number('abc') is NaN, JSON.stringify writes NaN as null, and the mutation is a
  // full replace — so before this gate, a stray character in Zoom cleared the stored
  // zoom and reported success.
  it('blocks the save rather than silently clearing the value', () => {
    render(<BasemapPage />);
    type('bm-zoom', 'abc');

    expect(saveButton().disabled).toBe(true);
  });

  it('blocks a non-numeric coordinate even when both halves are filled in', () => {
    render(<BasemapPage />);
    type('bm-center-lat', '33.7468');
    type('bm-center-lon', 'east');

    expect(saveButton().disabled).toBe(true);
  });

  // The counterweight: ordinary numbers, including negative and fractional ones, must
  // still save. A gate that refused everything would pass the two tests above.
  it('still accepts ordinary numbers', () => {
    render(<BasemapPage />);
    type('bm-center-lat', '-33.75');
    type('bm-center-lon', '151.21');
    type('bm-zoom', '11.5');

    expect(saveButton().disabled).toBe(false);
  });
});

describe('saving', () => {
  it('sends every field, each from its own input', async () => {
    render(<BasemapPage />);
    type('bm-tile-url', OSM);
    type('bm-attribution', CREDIT);
    type('bm-center-lat', '33.7468');
    type('bm-center-lon', '-84.3903');
    type('bm-zoom', '11.5');
    fireEvent.click(saveButton());

    await waitFor(() => expect(setTenantBasemapMock).toHaveBeenCalledTimes(1));
    expect(setTenantBasemapMock.mock.calls[0][0]).toEqual({
      tileUrl: OSM,
      attribution: CREDIT,
      centerLat: 33.7468,
      centerLon: -84.3903,
      zoom: 11.5,
    });
  });

  // 🔴 Clearing must send explicit nulls, not omit the keys. The mutation is a full
  // replace, and an omitted key is how a "clear" silently becomes a no-op.
  it('clears an override with explicit nulls rather than by omission', async () => {
    tenantState.value = {
      token: 'acme',
      basemap: {},
      basemapOverride: { tileUrl: OSM, attribution: CREDIT, centerLat: 1, centerLon: 2, zoom: 3 },
    };
    render(<BasemapPage />);
    type('bm-tile-url', '');
    type('bm-attribution', '');
    type('bm-center-lat', '');
    type('bm-center-lon', '');
    type('bm-zoom', '');
    fireEvent.click(saveButton());

    await waitFor(() => expect(setTenantBasemapMock).toHaveBeenCalledTimes(1));
    const sent = setTenantBasemapMock.mock.calls[0][0] as Record<string, unknown>;
    expect(Object.keys(sent).sort()).toEqual([
      'attribution',
      'centerLat',
      'centerLon',
      'tileUrl',
      'zoom',
    ]);
    for (const key of Object.keys(sent)) expect(sent[key]).toBeNull();
  });

  it('writes the fresh tenant into the cache so every map picks it up', async () => {
    const fresh = { token: 'acme' };
    setTenantBasemapMock.mockResolvedValue(fresh);
    render(<BasemapPage />);
    type('bm-tile-url', OSM);
    type('bm-attribution', CREDIT);
    fireEvent.click(saveButton());

    await waitFor(() => expect(applyTenantMock).toHaveBeenCalledWith(fresh));
  });

  it('surfaces a server refusal instead of reporting success', async () => {
    setTenantBasemapMock.mockRejectedValue(new Error('basemap.tileUrl must be an https URL'));
    render(<BasemapPage />);
    type('bm-tile-url', OSM);
    type('bm-attribution', CREDIT);
    fireEvent.click(saveButton());

    await waitFor(() => expect(screen.getByText(/must be an https URL/)).toBeTruthy());
    expect(toastMock).not.toHaveBeenCalled();
  });
});

describe('inheritance', () => {
  it('says which instance default is in force when this tenant sets none', () => {
    tenantState.value = {
      token: 'acme',
      basemap: { tileUrl: OSM, attribution: CREDIT },
      basemapOverride: {
        tileUrl: null,
        attribution: null,
        centerLat: null,
        centerLon: null,
        zoom: null,
      },
    };
    render(<BasemapPage />);

    expect(screen.getByTestId('basemap-inheriting').textContent).toContain(OSM);
  });

  // 🔴 tenant.basemap is the EFFECTIVE value and already includes this tenant's own
  // override. Before this was gated on the STORED override, a tenant that cleared the
  // field was told it was "inheriting the instance default: <its own URL>" — wrong
  // twice: not the instance default, and not what saving would keep.
  it('does not present the tenant own URL as the instance default while clearing it', () => {
    tenantState.value = {
      token: 'acme',
      basemap: { tileUrl: OSM, attribution: CREDIT },
      basemapOverride: {
        tileUrl: OSM,
        attribution: CREDIT,
        centerLat: null,
        centerLon: null,
        zoom: null,
      },
    };
    render(<BasemapPage />);
    type('bm-tile-url', '');
    type('bm-attribution', '');

    expect(screen.queryByTestId('basemap-inheriting')).toBeNull();
  });

  it('says nothing about inheriting once this tenant has its own', () => {
    tenantState.value = {
      token: 'acme',
      basemap: { tileUrl: OSM, attribution: CREDIT },
      basemapOverride: {
        tileUrl: OSM,
        attribution: CREDIT,
        centerLat: null,
        centerLon: null,
        zoom: null,
      },
    };
    render(<BasemapPage />);

    expect(screen.queryByTestId('basemap-inheriting')).toBeNull();
  });
});

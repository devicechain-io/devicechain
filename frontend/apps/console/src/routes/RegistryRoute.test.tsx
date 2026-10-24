// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The registry families' routes, exercised through the REAL route table (App.tsx) and the real
// lazily loaded RegistryRoute: every family's list and detail path renders its generic page for
// THAT family, a detail page receives its token from the path (decoded, as React Router hands it
// over), a family whose area a profile can leave out sits behind its AreaGate, switching between
// two families mounts a fresh page rather than reusing the previous family's instance, and a path
// that is no route at all goes home. The generic pages are stood in for, because the question is
// which page renders and with what, not what it shows.
import '@/i18n/config';
import { act, cleanup, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { MemoryRouter, useNavigate, useParams } from 'react-router-dom';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { REGISTRY_RESOURCES } from '@/routes/resources';

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    isAuthenticated: true,
    isLoading: false,
    isIdentityAuthenticated: true,
    superuser: false,
    claims: { tenant: 'oem', username: 'ann', roles: [], authorities: ['*'], typ: 'access', scopeAnchors: [] },
    login: vi.fn(),
    selectTenant: vi.fn(),
  }),
  consumeSessionExpired: () => false,
}));

// The test drives navigation through the layout, which also names where the browser is.
const nav = vi.hoisted(() => ({ go: (_: string) => {} }));
vi.mock('@/routes/AppLayout', async () => {
  const { Outlet, useLocation } = await import('react-router-dom');
  return {
    default: () => {
      const { pathname } = useLocation();
      const navigate = useNavigate();
      nav.go = (to: string) => navigate(to);
      return (
        <>
          <span data-testid="where">{pathname}</span>
          <Outlet />
        </>
      );
    },
  };
});
vi.mock('@/routes/Home', () => ({ default: () => <div>home page</div> }));

// The gate names its area around what it guards.
vi.mock('@/components/AreaUnavailable', () => ({
  AreaGate: ({ area, children }: { area: string; children: React.ReactNode }) => (
    <div data-testid={`gate-${area}`}>{children}</div>
  ),
}));

// The generic pages report which family they render, which family they were MOUNTED for (state
// captured on mount survives a re-render with new props, so a reused instance shows the old
// family), and, for a detail page, the token React Router passed it.
vi.mock('@/components/registry', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/registry')>();
  function Page({ kind, resource }: { kind: string; resource: { basePath: string } }) {
    const [mountedFor] = useState(resource.basePath);
    const { token } = useParams<{ token: string }>();
    return (
      <div>
        <span data-testid="page">{`${kind} ${resource.basePath}`}</span>
        <span data-testid="mounted-for">{mountedFor}</span>
        <span data-testid="token">{token ?? '(none)'}</span>
      </div>
    );
  }
  return {
    ...actual,
    ResourceListPage: (p: { resource: { basePath: string } }) => <Page kind="list" {...p} />,
    ResourceDetailPage: (p: { resource: { basePath: string } }) => <Page kind="detail" {...p} />,
  };
});

import App from '@/App';

// The registry families load lazily, and the first import of their module graph can take longer
// than a findBy waits when the whole suite runs at once. Which route renders is the question
// here, not how long the chunk takes, so the module is loaded before any test asks.
beforeAll(async () => {
  await import('@/routes/RegistryRoute');
}, 60_000);

afterEach(cleanup);

function visit(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

describe('the registry routes', () => {
  it('has families to test', () => {
    expect(REGISTRY_RESOURCES.length).toBeGreaterThan(10);
  });

  it.each(REGISTRY_RESOURCES.map((r) => [r.basePath] as const))('%s opens its own list page', async (base) => {
    visit(base);
    expect((await screen.findByTestId('page')).textContent).toBe(`list ${base}`);
    expect(screen.getByTestId('where').textContent).toBe(base);
  });

  it.each(REGISTRY_RESOURCES.map((r) => [r.basePath] as const))(
    '%s/:token opens its own detail page with the token from the path',
    async (base) => {
      visit(`${base}/tok-1`);
      expect((await screen.findByTestId('page')).textContent).toBe(`detail ${base}`);
      expect(screen.getByTestId('token').textContent).toBe('tok-1');
    },
  );

  it('hands a detail page an encoded token decoded, as the route table always has', async () => {
    visit('/device-types/a%20b%2Fc');
    expect((await screen.findByTestId('page')).textContent).toBe('detail /device-types');
    expect(screen.getByTestId('token').textContent).toBe('a b/c');
  });

  it('a trailing slash still opens the page', async () => {
    visit('/device-types/');
    expect((await screen.findByTestId('page')).textContent).toBe('list /device-types');
  });

  it('puts a family whose area can be left out behind its gate, and only that family', async () => {
    const gated = REGISTRY_RESOURCES.filter((r) => r.area);
    expect(gated.length).toBeGreaterThan(0);
    for (const r of gated) {
      visit(`${r.basePath}/tok-1`);
      const gate = await screen.findByTestId(`gate-${r.area}`);
      expect(gate.textContent).toContain(`detail ${r.basePath}`);
      cleanup();
      visit(r.basePath);
      expect((await screen.findByTestId(`gate-${r.area}`)).textContent).toContain(`list ${r.basePath}`);
      cleanup();
    }
    // The control: an ungated family renders with no gate around it.
    const open = REGISTRY_RESOURCES.find((r) => !r.area)!;
    visit(open.basePath);
    await screen.findByTestId('page');
    expect(document.querySelector('[data-testid^="gate-"]')).toBeNull();
  });

  it('mounts a fresh page when moving from one family to another', async () => {
    visit('/device-types');
    expect((await screen.findByTestId('mounted-for')).textContent).toBe('/device-types');
    await act(async () => nav.go('/device-groups'));
    expect((await screen.findByText('list /device-groups')).textContent).toBe('list /device-groups');
    expect(screen.getByTestId('mounted-for').textContent).toBe('/device-groups');
    await act(async () => nav.go('/assets/a-1'));
    expect((await screen.findByText('detail /assets')).textContent).toBe('detail /assets');
    expect(screen.getByTestId('mounted-for').textContent).toBe('/assets');
    await act(async () => nav.go('/customers/c-1'));
    expect((await screen.findByText('detail /customers')).textContent).toBe('detail /customers');
    expect(screen.getByTestId('mounted-for').textContent).toBe('/customers');
  });

  it.each(['/no-such-page', '/device-types/a/b', '/devices/x/y', '/login/elsewhere'])('sends %s home', async (path) => {
    visit(path);
    expect(await screen.findByText('home page')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/');
  });
});

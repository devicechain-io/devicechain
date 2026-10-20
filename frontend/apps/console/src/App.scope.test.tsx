// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The route guard for a member limited to part of the tenant (a customer user), exercised
// through the REAL route table: lib/scope.ts decides which paths such a member may open, and
// this proves App.tsx asks it. A bookmark to a page the server would refuse them lands on the
// home page; a page the server serves them opens. The layout and the pages are stood in for,
// because the question is which route renders, not what it shows.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

const claims = vi.hoisted(() => ({
  value: { tenant: 'oem', username: 'ann', roles: [], authorities: ['*'], typ: 'access', scopeAnchors: [] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    isAuthenticated: true,
    isLoading: false,
    isIdentityAuthenticated: true,
    superuser: false,
    claims: claims.value,
    login: vi.fn(),
    selectTenant: vi.fn(),
  }),
  consumeSessionExpired: () => false,
}));
// The layout names the path it was rendered at, so the test reads where the guard sent the
// browser; its real providers, sidebar and header are not what is under test.
vi.mock('@/routes/AppLayout', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/routes/AppLayout')>();
  const { Outlet, useLocation } = await import('react-router-dom');
  const { ConfirmProvider } = await import('@/components/ui/confirm-dialog');
  return {
    ...actual,
    default: () => {
      const { pathname } = useLocation();
      return (
        <ConfirmProvider>
          <span data-testid="where">{pathname}</span>
          <Outlet />
        </ConfirmProvider>
      );
    },
  };
});
vi.mock('@/routes/Dashboard', () => ({ default: () => <div>home page</div> }));
// No page under the layout gets an answer: a route that renders is caught by its loading
// state, and a route the guard refuses never asks.
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: () => new Promise(() => {}) };
});

import App from '@/App';

// The registry families load lazily, and the first import of their module graph can take longer
// than a findBy waits when the whole suite runs at once. Which route renders is the question
// here, not how long the chunk takes, so the module is loaded before any test asks.
beforeAll(async () => {
  await import('@/routes/RegistryRoute');
}, 60_000);

afterEach(cleanup);

function visit(path: string, scope: string[]) {
  claims.value = { ...claims.value, scopeAnchors: scope };
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

// A member limited to a group of devices is as scoped as one limited to a customer: the guard
// keys on there being a scope, not on the kind of anchor it names.
const SCOPES = [['customer:acme'], ['group:north'], ['customer:acme', 'group:north']];
const cases = (paths: string[]) => SCOPES.flatMap((scope) => paths.map((path) => [path, scope] as [string, string[]]));

describe('the route guard for a scoped member', () => {
  it.each(cases(['/customers', '/members', '/roles/new', '/device-types', '/device-groups', '/audit']))(
    'sends a scoped member home from %s (scope %j)',
    async (path, scope) => {
      visit(path, scope);
      expect(await screen.findByText('home page')).toBeTruthy();
      expect(screen.getByTestId('where').textContent).toBe('/');
    },
  );

  it.each(cases(['/devices', '/alarms', '/dashboards']))('lets a scoped member open %s (scope %j)', async (path, scope) => {
    visit(path, scope);
    expect((await screen.findByTestId('where')).textContent).toBe(path);
    expect(screen.queryByText('home page')).toBeNull();
  });

  // The negative control: the same authorities without a scope open every page, so the
  // redirect above is the scope's doing and not the fixture's.
  it('is the scope, not the authorities: the same token unscoped opens /customers', async () => {
    visit('/customers', []);
    expect((await screen.findByTestId('where')).textContent).toBe('/customers');
    expect(screen.queryByText('home page')).toBeNull();
  });
});

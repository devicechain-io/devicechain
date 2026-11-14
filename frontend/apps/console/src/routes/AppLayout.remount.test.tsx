// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type ReactNode } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ claims: { tenant: 'acme', username: 'alice', exp: 1 } }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

function Counter({ name }: { name: string }) {
  const [count, setCount] = useState(0);
  return <button onClick={() => setCount(count + 1)}>{name}: {count}</button>;
}
function Pass({ children }: { children?: ReactNode }) {
  return <>{children}</>;
}
vi.mock('@/auth/TenantProvider', () => ({
  TenantProvider: ({ children }: { children: ReactNode }) => <><Counter name="tenant" />{children}</>,
}));
vi.mock('@/auth/CurrentUserProvider', () => ({
  CurrentUserProvider: ({ children }: { children: ReactNode }) => <><Counter name="user" />{children}</>,
}));
vi.mock('@/components/ui/sidebar', () => ({
  SidebarProvider: Pass,
  SidebarInset: Pass,
  SidebarTrigger: () => null,
}));
vi.mock('@/routes/AppSidebar', () => ({ AppSidebar: () => null }));
vi.mock('@/routes/TenantChip', () => ({ TenantChip: () => null }));
vi.mock('@/routes/common', () => ({ BackLink: Pass }));
vi.mock('@/components/ui/error-boundary', () => ({ ErrorBoundary: Pass }));
vi.mock('@/components/RouteSuspense', () => ({ RouteSuspense: Pass }));

import AppLayout from './AppLayout';

function LayoutRoute() {
  return (
    <MemoryRouter initialEntries={['/devices/pump-1']}>
      <Routes>
        <Route element={<AppLayout />}>
          <Route path="/devices/:id" element={<Counter name="page" />} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

beforeEach(() => {
  h.claims = { tenant: 'acme', username: 'alice', exp: 1 };
});
afterEach(cleanup);

function editState() {
  for (const name of ['tenant', 'user', 'page']) {
    fireEvent.click(screen.getByRole('button', { name: `${name}: 0` }));
    expect(screen.getByRole('button', { name: `${name}: 1` })).toBeTruthy();
  }
}

describe('AppLayout session identity', () => {
  it.each([
    { tenant: 'other' },
    { username: 'bob' },
  ])('remounts providers and the open page when identity changes: %o', (change) => {
    const view = render(<LayoutRoute />);
    editState();
    h.claims = { ...h.claims, ...change };
    view.rerender(<LayoutRoute />);
    for (const name of ['tenant', 'user', 'page']) {
      expect(screen.getByRole('button', { name: `${name}: 0` })).toBeTruthy();
    }
  });

  it('preserves provider and page state when tokens refresh for the same identity', () => {
    const view = render(<LayoutRoute />);
    editState();
    h.claims = { ...h.claims, exp: 2 };
    view.rerender(<LayoutRoute />);
    for (const name of ['tenant', 'user', 'page']) {
      expect(screen.getByRole('button', { name: `${name}: 1` })).toBeTruthy();
    }
  });
});

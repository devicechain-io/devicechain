// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Link single sign-on account" action: it starts the server's link flow and navigates to
// the address the server returns, exactly as given; a refusal is shown, not swallowed; and
// only a password session is offered it.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { resetSsoStateForTests } from '@/lib/sso';
import { LinkSsoAccount } from './LinkSsoAccount';
import { NavUser } from './NavUser';

const mocks = vi.hoisted(() => ({
  beginSsoLink: vi.fn(),
  auth: {
    identityToken: 'id-token' as string | null,
    signInMethod: 'password' as 'password' | 'sso' | null,
  },
}));

vi.mock('@/lib/api/user-management', () => ({
  beginSso: vi.fn(),
  beginSsoLink: (...a: unknown[]) => mocks.beginSsoLink(...a),
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: { username: 'pat@example.com' },
    logout: vi.fn(),
    superuser: false,
    isIdentityAuthenticated: mocks.auth.identityToken !== null,
    identityToken: mocks.auth.identityToken,
    signInMethod: mocks.auth.signInMethod,
  }),
}));
vi.mock('@/auth/CurrentUserProvider', () => ({
  useCurrentUser: () => ({ email: 'pat@example.com', displayName: 'Pat', firstName: 'Pat', lastName: null }),
  useApplyCurrentUser: () => vi.fn(),
}));
vi.mock('@/lib/hooks/use-metrics-available', () => ({ useMetricsAvailable: () => false }));
vi.mock('@/components/ThemeToggle', () => ({ ThemeToggle: () => null }));
vi.mock('@/components/LocaleSwitcher', () => ({ LocaleSwitcher: () => null }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/routes/ProfileForm', () => ({ ProfileForm: () => null }));
// The menu and drawer are Radix portals; what is under test is what they are given.
vi.mock('@/components/registry', () => ({
  FormDrawer: ({ open, title, children }: { open: boolean; title: string; children: ReactNode }) =>
    open ? (
      <div role="dialog" aria-label={title}>
        {children}
      </div>
    ) : null,
}));
vi.mock('@/components/ui/dropdown-menu', () => ({
  DropdownMenu: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuSeparator: () => null,
  DropdownMenuItem: ({ children, onClick }: { children: ReactNode; onClick?: () => void }) => (
    <button onClick={onClick}>{children}</button>
  ),
}));
vi.mock('@/components/ui/sidebar', () => ({
  SidebarMenu: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SidebarMenuItem: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SidebarMenuButton: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  useSidebar: () => ({ isMobile: false }),
}));

const t = (key: string) => i18n.t(key);
const PROVIDER_URL = 'https://login.example.com/authorize?state=abc&x=1';

const realLocation = window.location;
const assign = vi.fn();

beforeEach(async () => {
  mocks.beginSsoLink.mockReset();
  assign.mockReset();
  mocks.auth.identityToken = 'id-token';
  mocks.auth.signInMethod = 'password';
  resetSsoStateForTests();
  window.sessionStorage.clear();
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: new Proxy({} as Location, {
      get: (_target, key) => {
        if (key === 'assign') return assign;
        const v: unknown = Reflect.get(realLocation, key, realLocation);
        return typeof v === 'function' ? v.bind(realLocation) : v;
      },
    }),
  });
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
});

function link(connection: string) {
  fireEvent.change(screen.getByLabelText(t('userMenu:linkSsoConnectionLabel')), {
    target: { value: connection },
  });
  fireEvent.click(screen.getByRole('button', { name: t('userMenu:linkSsoContinue') }));
}

describe('LinkSsoAccount', () => {
  it('starts the link with the identity token and the completion page, and navigates to the server URL as given', async () => {
    mocks.beginSsoLink.mockResolvedValue(PROVIDER_URL);
    render(<LinkSsoAccount />);
    link(' acme-sso ');
    await waitFor(() => expect(assign).toHaveBeenCalledWith(PROVIDER_URL));
    expect(mocks.beginSsoLink).toHaveBeenCalledWith({
      identityToken: 'id-token',
      connection: 'acme-sso',
      returnTo: window.location.origin + '/login/sso/complete',
    });
    // The page needs to know, on return, that an error= belongs to a link.
    expect(window.sessionStorage.getItem('dc-sso-link-pending')).toBe('1');
  });

  it('shows the server refusal, navigates nowhere, and leaves no link marker', async () => {
    mocks.beginSsoLink.mockRejectedValue(
      new GraphQLRequestError('this account is already linked to that identity provider', 200),
    );
    render(<LinkSsoAccount />);
    link('acme-sso');
    expect(await screen.findByText('this account is already linked to that identity provider')).toBeTruthy();
    expect(assign).not.toHaveBeenCalled();
    expect(window.sessionStorage.getItem('dc-sso-link-pending')).toBeNull();
  });

  it('asks for a connection before calling the server', () => {
    render(<LinkSsoAccount />);
    link('  ');
    expect(screen.getByText(t('userMenu:linkSsoConnectionRequired'))).toBeTruthy();
    expect(mocks.beginSsoLink).not.toHaveBeenCalled();
  });

  it.each(['en', 'es'])('says the sign-in ended when no identity session is held, in %s', async (lng) => {
    await i18n.changeLanguage(lng);
    mocks.auth.identityToken = null;
    render(<LinkSsoAccount />);
    link('acme-sso');
    expect(screen.getByText(i18n.t('userMenu:linkSsoSessionEnded'))).toBeTruthy();
    expect(i18n.t('userMenu:linkSsoSessionEnded')).not.toBe('linkSsoSessionEnded');
    expect(mocks.beginSsoLink).not.toHaveBeenCalled();
  });
});

describe('the user menu offers the link', () => {
  const renderMenu = () =>
    render(
      <MemoryRouter>
        <NavUser />
      </MemoryRouter>,
    );

  it('to a password session', () => {
    renderMenu();
    expect(screen.getByRole('button', { name: t('userMenu:linkSso') })).toBeTruthy();
  });

  it.each([
    ['a session that came through single sign-on', 'sso', 'id-token'],
    ['a session whose sign-in method is unknown', null, 'id-token'],
    ['a password session whose identity token has lapsed', 'password', null],
  ] as const)('but not to %s', (_name, method, token) => {
    mocks.auth.signInMethod = method;
    mocks.auth.identityToken = token;
    renderMenu();
    expect(screen.queryByRole('button', { name: t('userMenu:linkSso') })).toBeNull();
  });

  it('opening it shows the connection form', () => {
    renderMenu();
    fireEvent.click(screen.getByRole('button', { name: t('userMenu:linkSso') }));
    expect(screen.getByLabelText(t('userMenu:linkSsoConnectionLabel'))).toBeTruthy();
  });
});

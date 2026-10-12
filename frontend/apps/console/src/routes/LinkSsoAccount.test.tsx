// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Link single sign-on account" action: it starts the server's link flow (the server picks
// the connection from the person's own email domain) and navigates to the address the server
// returns, exactly as given; each typed refusal is shown in the person's language; a lapsed
// password sign-in is told to sign in again rather than the action disappearing; and a session
// that came through single sign-on is not offered it.

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

// An unsigned JWT-shaped string: the action only reads its expiry.
const jwt = (claims: Record<string, unknown>) =>
  `h.${btoa(JSON.stringify(claims)).replace(/=+$/, '')}.s`;
const LIVE = jwt({ exp: Math.floor(Date.now() / 1000) + 600 });
const LAPSED = jwt({ exp: Math.floor(Date.now() / 1000) - 60 });

const mocks = vi.hoisted(() => ({
  beginSsoLink: vi.fn(),
  logout: vi.fn(),
  auth: {
    identityToken: '' as string | null,
    signInMethod: 'password' as 'password' | 'sso' | null,
    superuser: false,
  },
}));

vi.mock('@/lib/api/user-management', () => ({
  beginSso: vi.fn(),
  beginSsoLink: (...a: unknown[]) => mocks.beginSsoLink(...a),
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    claims: { username: 'pat@example.com' },
    logout: mocks.logout,
    superuser: mocks.auth.superuser,
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
  mocks.logout.mockReset();
  assign.mockReset();
  mocks.auth.identityToken = LIVE;
  mocks.auth.signInMethod = 'password';
  mocks.auth.superuser = false;
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
afterEach(async () => {
  cleanup();
  Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
  await i18n.changeLanguage('en');
});

const continueButton = () => screen.getByRole('button', { name: t('userMenu:linkSsoContinue') });
const refusal = (code: string) =>
  new GraphQLRequestError('server prose', 200, [{ message: 'server prose', extensions: { code } }]);

describe('LinkSsoAccount', () => {
  it('starts the link with the identity token and the completion page alone, and navigates to the server URL as given', async () => {
    mocks.beginSsoLink.mockResolvedValue(PROVIDER_URL);
    render(<LinkSsoAccount />);
    // Nothing to type: the server picks the connection from the account's email domain.
    expect(screen.queryByRole('textbox')).toBeNull();
    fireEvent.click(continueButton());
    await waitFor(() => expect(assign).toHaveBeenCalledWith(PROVIDER_URL));
    expect(mocks.beginSsoLink).toHaveBeenCalledWith({
      identityToken: LIVE,
      returnTo: window.location.origin + '/login/sso/complete',
    });
    // The page needs to know, on return, that an error= belongs to a link.
    expect(window.sessionStorage.getItem('dc-sso-link-pending')).toBe('1');
  });

  const codes: [string, string][] = [
    ['SSO_LINK_NO_CONNECTION', 'userMenu:linkSsoErrorNoConnection'],
    ['SSO_LINK_ALREADY_LINKED', 'userMenu:linkSsoErrorAlreadyLinked'],
    ['SSO_LINK_NOT_ALLOWED', 'userMenu:linkSsoErrorNotAllowed'],
  ];
  describe.each(['en', 'es'])('in %s', (lng) => {
    it.each(codes)('maps %s to its text, navigates nowhere, and leaves no link marker', async (code, key) => {
      await i18n.changeLanguage(lng);
      expect(i18n.t(key)).not.toBe(key.split(':')[1]);
      mocks.beginSsoLink.mockRejectedValue(refusal(code));
      render(<LinkSsoAccount />);
      fireEvent.click(continueButton());
      expect(await screen.findByText(i18n.t(key))).toBeTruthy();
      expect(screen.queryByText('server prose')).toBeNull();
      expect(assign).not.toHaveBeenCalled();
      expect(window.sessionStorage.getItem('dc-sso-link-pending')).toBeNull();
    });

    it.each([
      ['SSO_LINK_SESSION_REQUIRED', 'userMenu:linkSsoSessionEnded'],
      ['SSO_LINK_PASSWORD_REQUIRED', 'userMenu:linkSsoErrorPasswordRequired'],
    ])('answers %s by offering to sign in again', async (code, key) => {
      await i18n.changeLanguage(lng);
      mocks.beginSsoLink.mockRejectedValue(refusal(code));
      render(<LinkSsoAccount />);
      fireEvent.click(continueButton());
      expect(await screen.findByText(i18n.t(key))).toBeTruthy();
      fireEvent.click(screen.getByRole('button', { name: i18n.t('userMenu:linkSsoSignInAgain') }));
      expect(mocks.logout).toHaveBeenCalled();
    });
  });

  it('shows an uncoded refusal as the server gave it', async () => {
    mocks.beginSsoLink.mockRejectedValue(new GraphQLRequestError('single sign-on is not available', 200));
    render(<LinkSsoAccount />);
    fireEvent.click(continueButton());
    expect(await screen.findByText('single sign-on is not available')).toBeTruthy();
  });

  it.each([
    ['the identity token has lapsed', 'password', LAPSED],
    ['no identity session is held', 'password', null],
    ['the sign-in method is unknown', null, LIVE],
  ] as const)('says to sign in again, without calling the server, when %s', (_name, method, token) => {
    mocks.auth.signInMethod = method;
    mocks.auth.identityToken = token;
    render(<LinkSsoAccount />);
    expect(screen.getByText(t('userMenu:linkSsoSessionEnded'))).toBeTruthy();
    expect(screen.queryByRole('button', { name: t('userMenu:linkSsoContinue') })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: t('userMenu:linkSsoSignInAgain') }));
    expect(mocks.logout).toHaveBeenCalled();
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

  it.each([
    ['a live password session', 'password', LIVE],
    // The action stays, and explains, rather than disappearing after 15 minutes.
    ['a password session whose identity token has lapsed', 'password', null],
    ['a session whose sign-in method is unknown', null, null],
  ] as const)('to %s', (_name, method, token) => {
    mocks.auth.signInMethod = method;
    mocks.auth.identityToken = token;
    renderMenu();
    expect(screen.getByRole('button', { name: t('userMenu:linkSso') })).toBeTruthy();
  });

  it('opening it after the password sign-in lapsed says to sign in again', () => {
    mocks.auth.identityToken = null;
    renderMenu();
    fireEvent.click(screen.getByRole('button', { name: t('userMenu:linkSso') }));
    expect(screen.getByText(t('userMenu:linkSsoSessionEnded'))).toBeTruthy();
  });

  it('but not to a session that came through single sign-on', () => {
    mocks.auth.signInMethod = 'sso';
    renderMenu();
    expect(screen.queryByRole('button', { name: t('userMenu:linkSso') })).toBeNull();
  });

  it('nor to a superuser', () => {
    mocks.auth.superuser = true;
    renderMenu();
    expect(screen.queryByRole('button', { name: t('userMenu:linkSso') })).toBeNull();
  });

  it('opening it shows where it goes, with nothing to type', () => {
    renderMenu();
    fireEvent.click(screen.getByRole('button', { name: t('userMenu:linkSso') }));
    expect(screen.getByText(t('userMenu:linkSsoExplain'))).toBeTruthy();
    expect(continueButton()).toBeTruthy();
  });
});

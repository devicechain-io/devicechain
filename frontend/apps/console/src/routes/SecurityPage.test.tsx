// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The account security page: enrol an authenticator, see where it stands, regenerate the
// recovery codes, turn it off. The promises are about secrets and about what each step needs:
//
//   - the QR code is drawn here (an SVG), never fetched from an image service;
//   - the recovery codes are shown once and the screen does not let go of them until the person
//     says they kept them; after that neither the codes NOR the enrolment secret is in the DOM;
//   - confirming swaps the replacement sign-in in (the one that enrolled has just ended);
//   - regenerate and disable need the password AND a code, and disable ends every session;
//   - a member sent here by a tenant's requirement lands in enrolment and goes back in after.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { ToastProvider } from '@/components/ui/toast';

const h = vi.hoisted(() => ({
  begin: vi.fn(),
  confirm: vi.fn(),
  regenerate: vi.fn(),
  disable: vi.fn(),
  status: vi.fn(),
  adoptIdentity: vi.fn(),
  forgetSessions: vi.fn(),
  selectTenant: vi.fn(),
  logout: vi.fn(),
  auth: { isIdentityAuthenticated: true, isAuthenticated: true, identityToken: 'ID1' as string | null },
}));

vi.mock('@/lib/api/mfa', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/mfa')>()),
  beginTotpEnrollment: (...a: unknown[]) => h.begin(...a),
  confirmTotpEnrollment: (...a: unknown[]) => h.confirm(...a),
  regenerateRecoveryCodes: (...a: unknown[]) => h.regenerate(...a),
  disableTotp: (...a: unknown[]) => h.disable(...a),
  getMfaStatus: (...a: unknown[]) => h.status(...a),
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    ...h.auth,
    adoptIdentity: h.adoptIdentity,
    forgetSessions: h.forgetSessions,
    selectTenant: h.selectTenant,
    logout: h.logout,
  }),
}));
// isExpired reads the JWT; the fake identity token is not one, and expiry is not under test.
vi.mock('@devicechain/client', async (orig) => ({
  ...(await orig<typeof import('@devicechain/client')>()),
  isExpired: () => false,
}));

import SecurityPage from './SecurityPage';

const t = (key: string, o?: Record<string, unknown>) => i18n.t(key, o);

const SECRET = 'JBSWY3DPEHPK3PXP';
const URI = `otpauth://totp/DeviceChain:me@example.com?secret=${SECRET}&issuer=DeviceChain`;
const CODES = ['AAAA-BBBB-CCCC-DDDD', 'EEEE-FFFF-GGGG-HHHH'];
const NEW_AUTH = { identityToken: 'ID2', expiresAt: '2099-01-01T00:00:00Z', superuser: false, memberships: [] };

function Probe() {
  const loc = useLocation();
  return <div data-testid="at">{loc.pathname}</div>;
}

function renderPage(state?: unknown) {
  return render(
    <ToastProvider>
      <MemoryRouter initialEntries={[{ pathname: '/security', state }]}>
        <Routes>
          <Route path="/security" element={<SecurityPage />} />
          <Route path="*" element={<Probe />} />
        </Routes>
      </MemoryRouter>
    </ToastProvider>,
  );
}

const OFF = { totpEnabled: false, enrolledAt: null, recoveryCodesRemaining: 0 };
const ON = { totpEnabled: true, enrolledAt: '2026-10-01T00:00:00Z', recoveryCodesRemaining: 9 };

const type = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });

// Gets from the password step to the scan step with the mocks above.
async function reachScan() {
  fireEvent.click(await screen.findByRole('button', { name: t('security:enrollStart') }));
  type(t('security:passwordLabel'), 'hunter2');
  fireEvent.click(screen.getByRole('button', { name: t('security:enrollContinue') }));
  await screen.findByText(SECRET);
}

beforeEach(async () => {
  Object.values(h).forEach((v) => typeof v === 'function' && 'mockReset' in v && (v as any).mockReset());
  h.auth = { isIdentityAuthenticated: true, isAuthenticated: true, identityToken: 'ID1' };
  h.status.mockResolvedValue(OFF);
  h.begin.mockResolvedValue({ otpauthUri: URI, secret: SECRET, expiresAt: '2099-01-01T00:00:00Z' });
  h.confirm.mockResolvedValue({ codes: CODES, auth: NEW_AUTH });
  h.adoptIdentity.mockResolvedValue(undefined);
  window.localStorage.clear();
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('setting up two-factor authentication', () => {
  it('re-checks the password, then shows the QR code (drawn here) and the secret for manual entry', async () => {
    const { container } = renderPage();
    await reachScan();
    expect(h.begin).toHaveBeenCalledWith('ID1', 'hunter2');
    // Drawn client-side: an SVG, and no image request anywhere.
    const qr = screen.getByRole('img', { name: t('security:qrLabel') });
    expect(qr.tagName.toLowerCase()).toBe('svg');
    expect(container.querySelector('img')).toBeNull();
    // Nothing on the page points anywhere: no src, no href, no request for the secret to ride.
    expect(container.querySelectorAll('[src], [href]').length).toBe(0);
    expect(screen.getByText(SECRET)).toBeTruthy();
  });

  it('keeps the secret out of storage and the address', async () => {
    renderPage();
    await reachScan();
    expect(JSON.stringify({ ...window.localStorage })).not.toContain(SECRET);
    expect(JSON.stringify({ ...window.sessionStorage })).not.toContain(SECRET);
    expect(window.location.href).not.toContain(SECRET);
  });

  it('confirms with the code, swaps the replacement sign-in in, and shows the codes once', async () => {
    renderPage();
    await reachScan();
    type(t('security:enrollCodeLabel'), '123456');
    fireEvent.click(screen.getByRole('button', { name: t('security:enrollConfirm') }));

    await screen.findByText(CODES[0]);
    expect(h.confirm).toHaveBeenCalledWith('ID1', 'hunter2', '123456');
    // The sign-in that enrolled has ended: its replacement is adopted.
    expect(h.adoptIdentity).toHaveBeenCalledWith(NEW_AUTH);
    expect(screen.getByText(CODES[1])).toBeTruthy();
    // The secret is gone the moment the codes are shown.
    expect(screen.queryByText(SECRET)).toBeNull();
  });

  it('does not let go of the codes until the person says they kept them', async () => {
    renderPage();
    await reachScan();
    type(t('security:enrollCodeLabel'), '123456');
    fireEvent.click(screen.getByRole('button', { name: t('security:enrollConfirm') }));
    await screen.findByText(CODES[0]);

    const proceed = screen.getByRole('button', { name: t('security:codesContinue') }) as HTMLButtonElement;
    expect(proceed.disabled).toBe(true);
    fireEvent.click(screen.getByRole('checkbox', { name: t('security:codesAcknowledge') }));
    expect(proceed.disabled).toBe(false);
  });

  it('leaves neither the codes nor the secret anywhere in the DOM once dismissed', async () => {
    h.status.mockResolvedValueOnce(OFF).mockResolvedValue(ON);
    const { container } = renderPage();
    await reachScan();
    type(t('security:enrollCodeLabel'), '123456');
    fireEvent.click(screen.getByRole('button', { name: t('security:enrollConfirm') }));
    await screen.findByText(CODES[0]);
    fireEvent.click(screen.getByRole('checkbox', { name: t('security:codesAcknowledge') }));
    fireEvent.click(screen.getByRole('button', { name: t('security:codesContinue') }));

    await screen.findByText(t('security:statusOn'));
    const html = container.innerHTML + document.body.innerHTML;
    for (const secret of [...CODES, SECRET, URI, 'hunter2']) expect(html).not.toContain(secret);
    expect(JSON.stringify({ ...window.localStorage })).not.toContain(CODES[0]);
  });

  it('tells the person the code or password was refused, and clears the code field', async () => {
    h.confirm.mockRejectedValue(Object.assign(new Error('invalid code'), {}));
    renderPage();
    await reachScan();
    type(t('security:enrollCodeLabel'), '123456');
    fireEvent.click(screen.getByRole('button', { name: t('security:enrollConfirm') }));
    // A non-GraphQL failure never got an answer: unreachable, not "wrong code".
    await screen.findByText(t('login:serverUnreachable'));
    expect(h.adoptIdentity).not.toHaveBeenCalled();
    expect((screen.getByLabelText(t('security:enrollCodeLabel')) as HTMLInputElement).value).toBe('');
  });

  it('start over drops the secret and asks for a new one', async () => {
    renderPage();
    await reachScan();
    fireEvent.click(screen.getByRole('button', { name: t('security:startOver') }));
    expect(screen.queryByText(SECRET)).toBeNull();
    expect(screen.getByLabelText(t('security:passwordLabel'))).toBeTruthy();
  });
});

describe('the enrolment redirect', () => {
  it('starts at enrolment, then goes back into the tenant after the codes are kept', async () => {
    h.selectTenant.mockResolvedValue(undefined);
    renderPage({ enrollForTenant: 'acme' });
    fireEvent.change(await screen.findByLabelText(t('security:passwordLabel')), { target: { value: 'hunter2' } });
    fireEvent.click(screen.getByRole('button', { name: t('security:enrollContinue') }));
    await screen.findByText(SECRET);
    type(t('security:enrollCodeLabel'), '123456');
    fireEvent.click(screen.getByRole('button', { name: t('security:enrollConfirm') }));
    await screen.findByText(CODES[0]);
    fireEvent.click(screen.getByRole('checkbox', { name: t('security:codesAcknowledge') }));
    fireEvent.click(screen.getByRole('button', { name: t('security:codesContinue') }));
    await waitFor(() => expect(h.selectTenant).toHaveBeenCalledWith('ID1', 'acme'));
    expect((await screen.findByTestId('at')).textContent).toBe('/');
  });
});

describe('when two-factor authentication is on', () => {
  beforeEach(() => h.status.mockResolvedValue(ON));

  it('shows the status', async () => {
    renderPage();
    await screen.findByText(t('security:statusOn'));
    expect(screen.getByText('9')).toBeTruthy();
    expect(screen.queryByRole('button', { name: t('security:enrollStart') })).toBeNull();
  });

  it('regenerate needs the password AND a code, then shows the new codes once', async () => {
    h.regenerate.mockResolvedValue(CODES);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: t('security:regenerate') }));
    const submit = screen.getByRole('button', { name: t('security:regenerateSubmit') }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    type(t('security:passwordLabel'), 'hunter2');
    expect(submit.disabled).toBe(true); // a password alone is not enough
    type(t('security:anyCodeLabel'), '654321');
    expect(submit.disabled).toBe(false);
    fireEvent.click(submit);
    await screen.findByText(CODES[0]);
    expect(h.regenerate).toHaveBeenCalledWith('ID1', 'hunter2', '654321');
  });

  it('disable needs the password AND a code, warns that every session ends, then signs out', async () => {
    h.disable.mockResolvedValue(true);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: t('security:disable') }));
    screen.getByText(t('security:disableWarning'));
    const submit = screen.getByRole('button', { name: t('security:disableSubmit') }) as HTMLButtonElement;
    type(t('security:anyCodeLabel'), 'abcd-efgh-jkmn-pqrs');
    expect(submit.disabled).toBe(true); // a code alone is not enough
    type(t('security:passwordLabel'), 'hunter2');
    fireEvent.click(submit);
    await waitFor(() => expect(h.disable).toHaveBeenCalledWith('ID1', 'hunter2', 'ABCD-EFGH-JKMN-PQRS'));
    // The server ended every session, this one included: forget them here, no logout call.
    await waitFor(() => expect(h.forgetSessions).toHaveBeenCalled());
    expect(h.logout).not.toHaveBeenCalled();
    expect((await screen.findByTestId('at')).textContent).toBe('/login');
  });

  it('does not sign out when disabling is refused', async () => {
    h.disable.mockRejectedValue(new Error('x'));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: t('security:disable') }));
    type(t('security:anyCodeLabel'), '123456');
    type(t('security:passwordLabel'), 'wrong');
    fireEvent.click(screen.getByRole('button', { name: t('security:disableSubmit') }));
    await screen.findByText(t('login:serverUnreachable'));
    expect(h.forgetSessions).not.toHaveBeenCalled();
  });

  it('warns when recovery codes run low', async () => {
    h.status.mockResolvedValue({ ...ON, recoveryCodesRemaining: 1 });
    renderPage();
    await screen.findByText(t('security:codesLow'));
  });
});

describe('without a usable identity session', () => {
  it('goes to the sign-in screen when nobody is signed in', async () => {
    h.auth = { isIdentityAuthenticated: false, isAuthenticated: false, identityToken: null };
    renderPage();
    expect((await screen.findByTestId('at')).textContent).toBe('/login');
  });

  it('asks a signed-in tenant session for a fresh sign-in instead of bouncing it', async () => {
    h.auth = { isIdentityAuthenticated: false, isAuthenticated: true, identityToken: null };
    renderPage();
    const button = await screen.findByRole('button', { name: t('security:signInAgainButton') });
    fireEvent.click(button);
    expect(h.logout).toHaveBeenCalled();
    expect(within(document.body).queryByTestId('at')).toBeNull();
  });
});

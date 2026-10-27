// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { errorCode } from '@devicechain/client';
import { MfaCodeStep } from '@/components/mfa/MfaCodeStep';
import { MFA_CODE, mfaChallengeOf, type MfaChallenge } from '@/lib/mfaChallenge';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Logo } from '@/components/brand/Logo';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { serverErrorCode, signInErrorKey } from '@/i18n/signInError';
import { startSso } from '@/lib/sso';
import { codeFromHash, dropInvitation, heldInvitation, holdInvitation, INVITATION_PATH } from '@/lib/invitation';
import type { IdentityAuth } from '@/lib/api/user-management';

// Read the code once, on the first render: from the link's fragment (moved into session
// storage and wiped from the address bar), or from session storage when the page is
// reached again after signing in through an identity provider.
function takeCode(): string | null {
  const fromLink = codeFromHash(window.location.hash);
  if (fromLink) {
    holdInvitation(fromLink);
    window.history.replaceState(null, '', INVITATION_PATH);
    return fromLink;
  }
  return heldInvitation();
}

type Way = 'new' | 'existing';

// AcceptInvitationPage is where an invitation link lands. The person either sets the
// password of a new account, or proves they own the existing account the invitation was
// issued to (by signing in, with a password or through single sign-on), and is then taken
// into the tenant. The server decides everything: an unusable code — expired, revoked,
// used, or for another account — gets one answer, so this page cannot be used to learn
// which.
export default function AcceptInvitationPage() {
  const { t } = useTranslation('members');
  const navigate = useNavigate();
  const { acceptInvitation, login, selectTenant, logout, identityToken } = useAuth();
  const [code] = useState(takeCode);
  const [way, setWay] = useState<Way>('new');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [ssoRequired, setSsoRequired] = useState(false);
  const [busy, setBusy] = useState(false);
  const [accepted, setAccepted] = useState<IdentityAuth | null>(null);
  // The challenge the existing account's sign-in was held for: its password was right but it has
  // two-factor authentication on. Component state only.
  const [mfa, setMfa] = useState<MfaChallenge | null>(null);

  const enter = async (auth: IdentityAuth, tenant: string) => {
    try {
      await selectTenant(auth.identityToken, tenant);
    } catch (err) {
      // The tenant requires a second factor and the account has none: the next step is setting
      // one up with the identity session this page just started, not an error.
      if (serverErrorCode(err) === MFA_CODE.enrollmentRequired) {
        navigate('/security', { replace: true, state: { enrollForTenant: tenant } });
        return;
      }
      throw err;
    }
    navigate('/', { replace: true });
  };

  const finish = async (auth: IdentityAuth) => {
    dropInvitation();
    const enterable = auth.memberships.filter((m) => !m.ssoRequired);
    if (enterable.length === 1) {
      await enter(auth, enterable[0].tenant);
      return;
    }
    setAccepted(auth);
    setBusy(false);
  };

  const fail = (err: unknown) => {
    const c = errorCode(err);
    if (c === 'INVALID_INVITATION') {
      // The server gives ONE answer for an expired code and for one issued to another
      // account, so it cannot be told which this is. Either way the HELD copy goes: one
      // left in session storage sends every later single sign-on in this tab back here
      // (the completion returns to the invitation whenever a code is held), to a page that
      // can only refuse it again. The page keeps the code in memory, so someone signed in
      // as the wrong account can still choose "Not you?", which holds it again.
      dropInvitation();
      setError(t('errorInvalidInvitation'));
    } else if (c === 'SSO_REQUIRED') {
      setSsoRequired(true);
      setError(t('errorSsoRequired'));
    } else {
      setError(t(signInErrorKey(err, 'login:invalidCredentials')));
    }
    setBusy(false);
  };

  const run = async (op: () => Promise<IdentityAuth>) => {
    if (!code) return;
    setError(null);
    setBusy(true);
    try {
      await finish(await op());
    } catch (err) {
      // The existing account's password was right but it is held for its second factor: ask
      // for the code, then accept as that account.
      const challenge = mfaChallengeOf(err);
      if (challenge) {
        setMfa(challenge);
        setBusy(false);
        return;
      }
      fail(err);
    }
  };

  const submitNew = (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirmPassword) {
      setError(t('acceptPasswordMismatch'));
      return;
    }
    void run(() => acceptInvitation(code!, { password }));
  };

  const submitExisting = (e: FormEvent) => {
    e.preventDefault();
    void run(async () => {
      const auth = await login(email.trim(), password);
      return acceptInvitation(code!, { identityToken: auth.identityToken });
    });
  };

  // Single sign-on leaves the page; the code waits in session storage, and the sign-in's
  // completion brings the browser back here holding an identity session. It is held
  // again here rather than trusted to still be there: a refusal drops the held copy.
  const sso = async () => {
    setError(null);
    if (code) holdInvitation(code);
    if (!email.trim()) {
      setError(t('login:ssoEmailRequired'));
      return;
    }
    setBusy(true);
    try {
      const url = await startSso({ email: email.trim(), returnTo: INVITATION_PATH });
      if (url === null) {
        setError(t('login:ssoNotConfigured'));
        setBusy(false);
        return;
      }
      window.location.assign(url);
    } catch (err) {
      fail(err);
    }
  };

  // Sign the current identity out and hold the code again: the page then offers a new
  // account or signing in with the right one. Held, because a sign-out that ends the
  // provider's session too leaves for the provider and comes back to the sign-in page,
  // and the next single sign-on from there returns here only while a code is held.
  // logout clears the tokens before it asks the server, so the identity is gone at once.
  const switchAccount = () => {
    setError(null);
    if (code) holdInvitation(code);
    void logout();
  };

  // The code was accepted, which signed the account in: accept the invitation as it.
  const handleVerified = async (auth: IdentityAuth) => {
    setMfa(null);
    await run(() => acceptInvitation(code!, { identityToken: auth.identityToken }));
  };

  const restartFromPassword = (message: string | null) => {
    setMfa(null);
    setPassword('');
    setError(message);
    setBusy(false);
  };

  const wayOptions = [
    { value: 'new' as const, label: t('acceptNewAccount') },
    { value: 'existing' as const, label: t('acceptExistingAccount') },
  ];

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo deviceColor="currentColor" className="mb-4 h-16 w-auto" />
          <h1 className="text-lg font-semibold text-foreground">{t('acceptTitle')}</h1>
        </div>
        <div className="space-y-4 rounded-lg border border-border bg-card p-6" data-shot="accept-invitation">
          {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
          {!code ? (
            <p className="text-sm text-muted-foreground">{t('acceptNoCode')}</p>
          ) : mfa ? (
            <MfaCodeStep challenge={mfa} onVerified={handleVerified} onRestart={restartFromPassword} />
          ) : accepted ? (
            <>
              <p className="text-sm text-muted-foreground">{t('acceptChooseTenant')}</p>
              {accepted.memberships
                .filter((m) => !m.ssoRequired)
                .map((m) => (
                  <Button
                    key={m.tenant}
                    variant="outline"
                    className="w-full justify-start"
                    disabled={busy}
                    onClick={() => {
                      setBusy(true);
                      enter(accepted, m.tenant).catch((err) => {
                        setError(t(signInErrorKey(err, 'login:enterTenantFailed')));
                        setBusy(false);
                      });
                    }}
                  >
                    {m.tenant}
                  </Button>
                ))}
            </>
          ) : identityToken ? (
            // Already signed in (for instance, back from single sign-on): accept as that
            // account. If it is not the account the invitation was issued to, the server
            // refuses it with the one invalid-invitation answer.
            <>
              <p className="text-sm text-muted-foreground">{t('acceptAsSignedIn')}</p>
              <Button
                className="w-full"
                loading={busy}
                disabled={busy}
                onClick={() => void run(() => acceptInvitation(code, { identityToken }))}
              >
                {t('acceptButton')}
              </Button>
              <Button variant="ghost" className="w-full" disabled={busy} onClick={switchAccount}>
                {t('acceptNotYou')}
              </Button>
            </>
          ) : (
            <>
              <p className="text-sm text-muted-foreground">{t('acceptIntro')}</p>
              {!ssoRequired && (
                <SegmentedControl
                  options={wayOptions}
                  value={way}
                  onValueChange={(w) => {
                    setWay(w);
                    setError(null);
                  }}
                  ariaLabel={t('acceptTitle')}
                  tone="joined"
                  size="sm"
                  fill
                />
              )}
              {ssoRequired ? null : way === 'new' ? (
                <form onSubmit={submitNew} className="space-y-4">
                  <FormField label={t('acceptPasswordLabel')} htmlFor="ai-password">
                    <Input
                      id="ai-password"
                      type="password"
                      autoComplete="new-password"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </FormField>
                  <FormField label={t('acceptConfirmPasswordLabel')} htmlFor="ai-confirm">
                    <Input
                      id="ai-confirm"
                      type="password"
                      autoComplete="new-password"
                      value={confirmPassword}
                      onChange={(e) => setConfirmPassword(e.target.value)}
                    />
                  </FormField>
                  <Button type="submit" className="w-full" loading={busy} disabled={busy || !password}>
                    {t('acceptButton')}
                  </Button>
                </form>
              ) : (
                <form onSubmit={submitExisting} className="space-y-4">
                  <FormField label={t('emailLabel')} htmlFor="ai-email">
                    <Input
                      id="ai-email"
                      type="email"
                      autoComplete="username"
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                    />
                  </FormField>
                  <FormField label={t('login:passwordLabel')} htmlFor="ai-existing-password">
                    <Input
                      id="ai-existing-password"
                      type="password"
                      autoComplete="current-password"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </FormField>
                  <Button type="submit" className="w-full" loading={busy} disabled={busy || !email.trim() || !password}>
                    {t('acceptSignInButton')}
                  </Button>
                </form>
              )}
              {(ssoRequired || way === 'existing') && (
                <div className="space-y-2 border-t border-border pt-4">
                  {ssoRequired && (
                    <FormField label={t('emailLabel')} htmlFor="ai-sso-email">
                      <Input
                        id="ai-sso-email"
                        type="email"
                        autoComplete="username"
                        value={email}
                        onChange={(e) => setEmail(e.target.value)}
                      />
                    </FormField>
                  )}
                  <Button variant="outline" className="w-full" disabled={busy} onClick={() => void sso()}>
                    {t('acceptSsoButton')}
                  </Button>
                </div>
              )}
            </>
          )}
        </div>
        <div className="mt-6 flex justify-center">
          <LocaleSwitcher />
        </div>
      </div>
    </div>
  );
}

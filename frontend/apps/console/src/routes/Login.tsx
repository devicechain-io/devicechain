// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useRef, useState, type FormEvent } from 'react';
import { Navigate, useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth, consumeSessionExpired } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Logo } from '@/components/brand/Logo';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { ErrorBanner } from '@/components/ui/error-banner';
import { serverErrorCode, signInErrorKey } from '@/i18n/signInError';
import { MfaCodeStep } from '@/components/mfa/MfaCodeStep';
import { MFA_CODE, mfaChallengeOf, type MfaChallenge } from '@/lib/mfaChallenge';
import type { IdentityAuth } from '@/lib/api/user-management';
import { discardPendingSso, startSso } from '@/lib/sso';

// Login is two-step (ADR-033): authenticate the email/password to get an identity
// token + the tenants the identity may act in, then select a tenant to start the
// session. A single membership is auto-selected so the common case stays one step.
//
// The first screen asks for the email ALONE (identifier-first). The server is asked which
// connection, if any, speaks for the email's domain (beginSso answers with the provider's
// address or null); a domain with one offers single sign-on, anything else gets the
// password field. The lookup is about the DOMAIN and never about the person: it is the
// same answer whether or not an account exists, so it cannot be used to find accounts, and
// an address at an unknown domain and one at a password-only domain both get null and so
// the same screen. Nothing here hides the password field on the strength of a tenant's
// policy: that policy is enforced by the server (selectTenant refuses a password session
// with SSO_REQUIRED), and exposing it before sign-in would tell anyone which domains belong
// to a tenant that requires single sign-on. A domain with a connection puts the password
// one step behind "Use a password instead" rather than removing it, so an account that has
// only a password (an operator, say) can still sign in.
//
// A password that is right does not always finish the sign-in: an account with two-factor
// authentication on is HELD, and login fails with the code MFA_CHALLENGE and a challenge handle.
// The screen then asks for the code (an authenticator's six digits, or a recovery code) and
// verifyMfa completes the sign-in. The handle lives in this component's state only: never in the
// address, never in storage. Every failure of the code is the same "invalid code" on purpose --
// the server does not say whether a code was wrong, replayed or locked, and neither does this.
//
// A member whose tenant requires a second factor but who has none yet is refused entry with
// MFA_ENROLLMENT_REQUIRED; that is not an error to show but a next step, so they are taken to
// set one up (/security) with the identity session they already hold.
//
// This is the reference i18n-converted screen (ADR-066): every user-facing string
// flows through the `login` catalog (frontend/apps/console/src/i18n/locales/en),
// each sentence is one key with no fragment concatenation, and cross-screen copy
// (Back) is drawn from the `common` namespace. Copy the pattern for the next screen.
// Why the session ended, as the notice above the form.
const SESSION_END_KEYS = {
  expired: 'sessionExpired',
  ssoRequired: 'sessionSsoRequired',
  mfaRequired: 'sessionMfaRequired',
  mfaEnrollmentRequired: 'sessionMfaEnrollmentRequired',
} as const;

export default function LoginPage() {
  const { isAuthenticated, login, selectTenant } = useAuth();
  const { t } = useTranslation('login');
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  // Which question the first screen is asking. 'identify' is the email alone; 'sso' is a
  // domain with a connection (the provider's address is held in ssoUrl, not yet visited);
  // 'password' is everything else, and the screen the explicit SSO button falls back to.
  const [step, setStep] = useState<'identify' | 'sso' | 'password'>('identify');
  const [ssoUrl, setSsoUrl] = useState<string | null>(null);
  const emailRef = useRef<HTMLInputElement>(null);
  const [identity, setIdentity] = useState<IdentityAuth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // The second-factor step: the challenge login held the sign-in for. Component state only.
  const [mfa, setMfa] = useState<MfaChallenge | null>(null);
  // Read once: true when we landed here because a session expired (not a fresh
  // visit or an explicit sign-out).
  const [expired] = useState(() => consumeSessionExpired());

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  // Which of the two things went wrong is NOT decided here — see i18n/signInError.ts.
  // The question "did the server evaluate this and say no, or did we never get an
  // answer?" has exactly one right predicate over the SDK's throw shapes, and the
  // obvious ones (the error class; the class plus a non-zero status) both report an
  // outage as a bad password.
  const failure = (err: unknown, rejectedKey: string) => setError(t(signInErrorKey(err, rejectedKey)));

  const enterTenant = async (auth: IdentityAuth, tenant: string) => {
    try {
      await selectTenant(auth.identityToken, tenant);
    } catch (err) {
      // The tenant requires a second factor and the account has none: the next step is
      // setting one up, with the identity session just started, then coming back in.
      if (serverErrorCode(err) === MFA_CODE.enrollmentRequired) {
        navigate('/security', { replace: true, state: { enrollForTenant: tenant } });
        return;
      }
      throw err;
    }
    navigate('/', { replace: true });
  };

  // Everything after the identity is established, whether by password or by password plus code.
  const proceed = async (auth: IdentityAuth) => {
    if (auth.superuser) {
      // A superuser's home is the admin console (ADR-033); from there they can
      // create tenants and switch into one. This is also the only landing for a
      // superuser on a tenant-less instance.
      navigate('/admin', { replace: true });
      return;
    }
    if (auth.memberships.length === 0) {
      // No tenant to enter yet. The admin console is where tenants get created
      // and memberships assigned (ADR-033 phase 4).
      setError(t('noTenantAccess'));
      setSubmitting(false);
      return;
    }
    // A tenant that requires single sign-on is listed (ssoRequired) so it does not just
    // vanish, but this password sign-in cannot enter it. When none can be entered, say
    // so here, next to the SSO button, instead of offering choices that would fail.
    if (auth.memberships.every((m) => m.ssoRequired)) {
      setError(t('ssoRequired'));
      setSubmitting(false);
      return;
    }
    if (auth.memberships.length === 1) {
      await enterTenant(auth, auth.memberships[0].tenant);
      return;
    }
    // Multiple tenants — let the user choose.
    setIdentity(auth);
    setSubmitting(false);
  };

  const handleCredentials = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const auth = await login(email.trim(), password);
      await proceed(auth);
    } catch (err) {
      // The password was right but the account is held for its second factor.
      const challenge = mfaChallengeOf(err);
      if (challenge) {
        setMfa(challenge);
        setSubmitting(false);
        return;
      }
      // The backend deliberately doesn't distinguish bad-email vs bad-password.
      failure(err, 'login:invalidCredentials');
      setSubmitting(false);
    }
  };

  // Back to the password step from the code step: the held sign-in is dropped, and the
  // password is cleared with it (a new attempt asks for it again).
  const restartFromPassword = (message: string | null) => {
    setMfa(null);
    setPassword('');
    setError(message);
    setSubmitting(false);
  };

  // The code was accepted: the held sign-in is spent. Whatever follows is entering a tenant.
  const handleVerified = async (auth: IdentityAuth) => {
    setMfa(null);
    try {
      await proceed(auth);
    } catch (err) {
      failure(err, 'login:enterTenantFailed');
      setSubmitting(false);
    }
  };

  // Single sign-on: the server picks the connection that speaks for the email's domain
  // and answers with the provider's address, which the browser is sent to as given. A
  // null answer means no connection covers the domain. `returnTo` is only a hint for
  // where to land afterwards; lib/sso sanitizes it to a same-origin path.
  const handleSso = async () => {
    setError(null);
    const address = email.trim();
    if (!address) {
      setError(t('ssoEmailRequired'));
      return;
    }
    setSubmitting(true);
    try {
      const url = await startSso({ email: address, returnTo: searchParams.get('returnTo') });
      if (url === null) {
        // Said, and the password field is put in front of them: the person asked for
        // single sign-on by name, so they are owed the answer.
        setStep('password');
        setError(t('ssoNotConfigured'));
        setSubmitting(false);
        return;
      }
      window.location.assign(url);
    } catch (err) {
      failure(err, 'login:ssoUnavailable');
      setSubmitting(false);
    }
  };

  // The first screen's question: which way does this email sign in? One begin request is
  // the whole lookup, and the browser is NOT sent anywhere yet: a domain with a connection
  // gets a choice, so a password-only account at that domain is never stranded.
  //
  // Every failure of the lookup falls through to the password field, without a banner. An
  // instance with no single sign-on, a throttled address and an unreachable server all end
  // up in the same place, and none of them says anything about the email: what a person
  // can still do is sign in with a password, which the server then judges on its own, and
  // the SSO button on that screen asks again by name.
  const handleIdentify = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    const address = email.trim();
    if (!address) return;
    setSubmitting(true);
    try {
      const url = await startSso({ email: address, returnTo: searchParams.get('returnTo') });
      if (url === null) {
        setStep('password');
      } else {
        setSsoUrl(url);
        setStep('sso');
      }
    } catch {
      setStep('password');
    }
    setSubmitting(false);
  };

  // Back to the email. Whatever the lookup left behind (the held address, the secret only
  // this tab keeps for finishing a sign-in) is dropped with it.
  const handleChangeEmail = () => {
    discardPendingSso();
    setSsoUrl(null);
    setStep('identify');
    setPassword('');
    setError(null);
    // The email field stays mounted, so its autoFocus does not fire again.
    emailRef.current?.focus();
  };

  // The single sign-on step's form submission (its button, or Enter, which the email field
  // still has after Enter on the first screen). This step has no password field, so the
  // form must never go to handleCredentials from here: that would send an empty password.
  const handleContinueSso = (e: FormEvent) => {
    e.preventDefault();
    if (ssoUrl !== null) window.location.assign(ssoUrl);
  };

  const handleUsePassword = () => {
    discardPendingSso();
    setSsoUrl(null);
    setStep('password');
    setError(null);
  };

  const handleSelectTenant = async (tenant: string) => {
    if (!identity) return;
    setError(null);
    setSubmitting(true);
    try {
      await enterTenant(identity, tenant);
    } catch (err) {
      failure(err, 'login:enterTenantFailed');
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          {/* deviceColor follows the theme's text color so the wordmark reads on
              both light and dark backgrounds; "Chain" and the cube stay brand. */}
          <Logo deviceColor="currentColor" className="mb-4 h-16 w-auto" />
          <p className="text-sm text-muted-foreground">
            {identity ? t('chooseTenantSubtitle') : mfa ? t('mfaSubtitle') : t('signInSubtitle')}
          </p>
        </div>

        {expired && !identity && !mfa && (
          <p className="mb-4 rounded-md border border-border bg-muted/50 px-3 py-2 text-center text-sm text-muted-foreground" data-shot="login-session-expired">
            {t(SESSION_END_KEYS[expired])}
          </p>
        )}

        {identity ? (
          <div className="space-y-3 rounded-lg border border-border bg-card p-6">
            {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
            {identity.memberships.map((m) => (
              <Button
                key={m.tenant}
                variant="outline"
                className="w-full justify-between"
                // Shown so the tenant does not silently vanish, but a password sign-in
                // cannot enter it: the server would refuse it (SSO_REQUIRED).
                disabled={submitting || m.ssoRequired}
                onClick={() => handleSelectTenant(m.tenant)}
              >
                <span>{m.tenant}</span>
                {m.ssoRequired && (
                  <span className="text-xs text-muted-foreground">{t('ssoRequiredBadge')}</span>
                )}
              </Button>
            ))}
            <Button
              variant="ghost"
              className="w-full"
              disabled={submitting}
              onClick={() => {
                setIdentity(null);
                setError(null);
              }}
            >
              {t('common:back')}
            </Button>
          </div>
        ) : mfa ? (
          <div data-shot="login-step-mfa" className="rounded-lg border border-border bg-card p-6">
            <MfaCodeStep challenge={mfa} onVerified={handleVerified} onRestart={restartFromPassword} />
          </div>
        ) : (
          <form
            data-shot={step === 'identify' ? 'login-step-identify' : step === 'password' ? 'login-step-password' : 'login-step-sso'}
            onSubmit={
              step === 'identify' ? handleIdentify : step === 'sso' ? handleContinueSso : handleCredentials
            }
            className="space-y-4 rounded-lg border border-border bg-card p-6"
          >
            {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

            <FormField label={t('emailLabel')} htmlFor="email">
              <Input
                ref={emailRef}
                id="email"
                data-shot="login-email"
                type="email"
                autoComplete="username"
                autoFocus={step === 'identify'}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                // Past the first screen the address is the thing being signed in, so it
                // stays visible (and in the form, for a password manager) but not editable.
                readOnly={step !== 'identify'}
                required
              />
            </FormField>

            {step === 'identify' && (
              <>
                <Button type="submit" className="w-full" loading={submitting} disabled={submitting} data-shot="login-continue">
                  {t('continue')}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  disabled={submitting}
                  onClick={handleSso}
                >
                  {t('ssoButton')}
                </Button>
              </>
            )}

            {step === 'sso' && (
              <>
                <p className="text-sm text-muted-foreground">{t('ssoDomainNotice')}</p>
                <Button type="submit" className="w-full" autoFocus>
                  {t('ssoContinue')}
                </Button>
                <Button type="button" variant="outline" className="w-full" onClick={handleUsePassword}>
                  {t('usePassword')}
                </Button>
                <Button type="button" variant="ghost" className="w-full" onClick={handleChangeEmail}>
                  {t('changeEmail')}
                </Button>
              </>
            )}

            {step === 'password' && (
              <>
                <FormField label={t('passwordLabel')} htmlFor="password">
                  <Input
                    id="password"
                    data-shot="login-password"
                    type="password"
                    autoComplete="current-password"
                    autoFocus
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                  />
                </FormField>

                <Button type="submit" className="w-full" loading={submitting} disabled={submitting}>
                  {t('signIn')}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  disabled={submitting}
                  onClick={handleSso}
                >
                  {t('ssoButton')}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  className="w-full"
                  disabled={submitting}
                  onClick={handleChangeEmail}
                >
                  {t('changeEmail')}
                </Button>
              </>
            )}
          </form>
        )}

        <div className="mt-6 flex justify-center">
          <LocaleSwitcher />
        </div>
      </div>
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth, consumeSessionExpired } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Logo } from '@/components/brand/Logo';
import { LocaleSwitcher } from '@/components/LocaleSwitcher';
import { ErrorBanner } from '@/components/ui/error-banner';
import { GraphQLRequestError } from '@devicechain/client';
import type { IdentityAuth } from '@/lib/api/user-management';

// Login is two-step (ADR-033): authenticate the email/password to get an identity
// token + the tenants the identity may act in, then select a tenant to start the
// session. A single membership is auto-selected so the common case stays one step.
//
// This is the reference i18n-converted screen (ADR-066): every user-facing string
// flows through the `login` catalog (frontend/apps/console/src/i18n/locales/en),
// each sentence is one key with no fragment concatenation, and cross-screen copy
// (Back) is drawn from the `common` namespace. Copy the pattern for the next screen.
export default function LoginPage() {
  const { isAuthenticated, login, selectTenant } = useAuth();
  const { t } = useTranslation('login');
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [identity, setIdentity] = useState<IdentityAuth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // Read once: true when we landed here because a session expired (not a fresh
  // visit or an explicit sign-out).
  const [expired] = useState(() => consumeSessionExpired());

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  const failure = (err: unknown, badCreds: string) =>
    setError(err instanceof GraphQLRequestError ? badCreds : t('serverUnreachable'));

  const enterTenant = async (auth: IdentityAuth, tenant: string) => {
    await selectTenant(auth.identityToken, tenant);
    navigate('/', { replace: true });
  };

  const handleCredentials = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const auth = await login(email.trim(), password);
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
      if (auth.memberships.length === 1) {
        await enterTenant(auth, auth.memberships[0].tenant);
        return;
      }
      // Multiple tenants — let the user choose.
      setIdentity(auth);
      setSubmitting(false);
    } catch (err) {
      // The backend deliberately doesn't distinguish bad-email vs bad-password.
      failure(err, t('invalidCredentials'));
      setSubmitting(false);
    }
  };

  const handleSelectTenant = async (tenant: string) => {
    if (!identity) return;
    setError(null);
    setSubmitting(true);
    try {
      await enterTenant(identity, tenant);
    } catch (err) {
      failure(err, t('enterTenantFailed'));
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
            {identity ? t('chooseTenantSubtitle') : t('signInSubtitle')}
          </p>
        </div>

        {expired && !identity && (
          <p className="mb-4 rounded-md border border-border bg-muted/50 px-3 py-2 text-center text-sm text-muted-foreground">
            {t('sessionExpired')}
          </p>
        )}

        {identity ? (
          <div className="space-y-3 rounded-lg border border-border bg-card p-6">
            {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
            {identity.memberships.map((m) => (
              <Button
                key={m.tenant}
                variant="outline"
                className="w-full justify-start"
                disabled={submitting}
                onClick={() => handleSelectTenant(m.tenant)}
              >
                {m.tenant}
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
        ) : (
          <form
            onSubmit={handleCredentials}
            className="space-y-4 rounded-lg border border-border bg-card p-6"
          >
            {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

            <FormField label={t('emailLabel')} htmlFor="email">
              <Input
                id="email"
                type="email"
                autoComplete="username"
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </FormField>

            <FormField label={t('passwordLabel')} htmlFor="password">
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </FormField>

            <Button type="submit" className="w-full" loading={submitting} disabled={submitting}>
              {t('signIn')}
            </Button>
          </form>
        )}

        <div className="mt-6 flex justify-center">
          <LocaleSwitcher />
        </div>
      </div>
    </div>
  );
}

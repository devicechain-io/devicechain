// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { GraphQLRequestError } from '@/lib/graphql/client';
import type { IdentityAuth } from '@/lib/api/user-management';

// Login is two-step (ADR-033): authenticate the email/password to get an identity
// token + the tenants the identity may act in, then select a tenant to start the
// session. A single membership is auto-selected so the common case stays one step.
export default function LoginPage() {
  const { isAuthenticated, login, selectTenant } = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [identity, setIdentity] = useState<IdentityAuth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  const failure = (err: unknown, badCreds: string) =>
    setError(
      err instanceof GraphQLRequestError
        ? badCreds
        : 'Could not reach the server. Check that the API is running.',
    );

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
      if (auth.memberships.length === 0) {
        // No tenant to enter yet (e.g. a superuser before any tenant exists). The
        // admin console is where tenants get created (ADR-033 phase 4).
        setError('This account has no tenant access yet. Ask an administrator to add you to a tenant.');
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
      failure(err, 'Invalid email or password.');
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
      failure(err, 'Could not enter that tenant.');
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          {/* Full-color lockup on a light panel so the navy wordmark reads in
              either theme. Swap the src for the Illustrator export when ready. */}
          <div className="mb-4 rounded-xl bg-white px-6 py-4 shadow-sm ring-1 ring-black/5">
            <img
              src="/branding/devicechain-lockup.svg"
              alt="DeviceChain"
              className="h-16 w-auto"
            />
          </div>
          <p className="text-sm text-muted-foreground">
            {identity ? 'Choose a tenant to continue' : 'Sign in to the management console'}
          </p>
        </div>

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
              Back
            </Button>
          </div>
        ) : (
          <form
            onSubmit={handleCredentials}
            className="space-y-4 rounded-lg border border-border bg-card p-6"
          >
            {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

            <FormField label="Email" htmlFor="email">
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

            <FormField label="Password" htmlFor="password">
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
              Sign in
            </Button>
          </form>
        )}
      </div>
    </div>
  );
}

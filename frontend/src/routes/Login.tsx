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

export default function LoginPage() {
  const { isAuthenticated, login } = useAuth();
  const navigate = useNavigate();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await login(username.trim(), password);
      navigate('/', { replace: true });
    } catch (err) {
      // Collapse the server's generic auth failure to a single friendly line;
      // the backend deliberately doesn't distinguish bad-user vs bad-password.
      const message =
        err instanceof GraphQLRequestError
          ? 'Invalid username or password.'
          : 'Could not reach the server. Check that the API is running.';
      setError(message);
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
          <p className="text-sm text-muted-foreground">Sign in to the management console</p>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4 rounded-lg border border-border bg-card p-6">
          {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

          <FormField label="Username" htmlFor="username">
            <Input
              id="username"
              autoComplete="username"
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
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
      </div>
    </div>
  );
}
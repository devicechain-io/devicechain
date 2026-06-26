// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { Cpu } from 'lucide-react';
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
          <div className="mb-3 flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
            <Cpu className="size-6" />
          </div>
          <h1 className="text-xl font-semibold tracking-tight text-foreground">DeviceChain</h1>
          <p className="mt-1 text-sm text-muted-foreground">Sign in to the management console</p>
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
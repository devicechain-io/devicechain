// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { Link, Navigate, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Logo } from '@/components/brand/Logo';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { signInErrorKey } from '@/i18n/signInError';
import { captureSsoReturn, runSsoCompletion } from '@/lib/sso';
import type { IdentityAuth } from '@/lib/api/user-management';

// Where the identity provider sends the browser back to. The address carries a one-time
// code, so this page is careful before it is anything else:
//
//   - captureSsoReturn() runs in the first render, ahead of every effect, and removes the
//     code from the address bar (main.tsx has already done it for a real page load; the
//     call is idempotent). It also tells the page to send no Referer.
//   - the code is redeemed once, by runSsoCompletion, whose result is shared across
//     React StrictMode's double mount;
//   - a failure shows its reason and a link back to sign-in. Nothing here retries or
//     navigates on its own, so a bad return cannot loop.
// The code is never logged.

const PROVIDER_ERROR_KEYS: Record<string, string> = {
  refused: 'login:ssoErrorRefused',
  link_required: 'login:ssoErrorLinkRequired',
  idp_error: 'login:ssoErrorIdp',
  groups_overage: 'login:ssoErrorGroupsOverage',
};

export default function LoginSsoCompletePage() {
  const { isAuthenticated, completeSso, selectTenant } = useAuth();
  const { t } = useTranslation('login');
  const navigate = useNavigate();
  // Strips the query before anything else in this tree can read or send it.
  captureSsoReturn();

  const [error, setError] = useState<string | null>(null);
  const [identity, setIdentity] = useState<IdentityAuth | null>(null);
  const [returnTo, setReturnTo] = useState('/');
  const [entering, setEntering] = useState(false);

  const enterTenant = async (auth: IdentityAuth, tenant: string, dest: string) => {
    await selectTenant(auth.identityToken, tenant);
    navigate(dest, { replace: true });
  };

  useEffect(() => {
    let live = true;
    void runSsoCompletion(completeSso).then(async (out) => {
      if (!live) return;
      if (!out.ok) {
        switch (out.reason) {
          case 'provider':
            setError(t(PROVIDER_ERROR_KEYS[out.providerError ?? ''] ?? 'ssoErrorRefused'));
            return;
          case 'missingCode':
            setError(t('ssoErrorMissingCode'));
            return;
          case 'missingVerifier':
            setError(t('ssoErrorMissingVerifier'));
            return;
          case 'redeemFailed':
            // A server that answered "no" means the code is spent or wrong; anything else
            // is an outage the person should hear about as one.
            setError(t(signInErrorKey(out.cause, 'login:ssoErrorRedeemFailed')));
            return;
        }
      }
      const { auth } = out;
      setReturnTo(out.returnTo);
      try {
        if (auth.superuser) {
          navigate('/admin', { replace: true });
        } else if (auth.memberships.length === 0) {
          setError(t('noTenantAccess'));
        } else if (auth.memberships.length === 1) {
          await enterTenant(auth, auth.memberships[0].tenant, out.returnTo);
        } else {
          setIdentity(auth);
        }
      } catch (err) {
        if (live) setError(t(signInErrorKey(err, 'login:enterTenantFailed')));
      }
    });
    return () => {
      live = false;
    };
    // The completion is page-lifetime state; only the mount matters. StrictMode's second
    // mount reuses the first one's redemption.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (isAuthenticated) return <Navigate to="/" replace />;

  const choose = async (tenant: string) => {
    if (!identity) return;
    setError(null);
    setEntering(true);
    try {
      await enterTenant(identity, tenant, returnTo);
    } catch (err) {
      setError(t(signInErrorKey(err, 'login:enterTenantFailed')));
      setEntering(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo deviceColor="currentColor" className="mb-4 h-16 w-auto" />
        </div>
        <div className="space-y-3 rounded-lg border border-border bg-card p-6">
          {error && <ErrorBanner message={error} />}
          {identity && !error ? (
            <>
              <p className="text-sm text-muted-foreground">{t('chooseTenantSubtitle')}</p>
              {identity.memberships.map((m) => (
                <Button
                  key={m.tenant}
                  variant="outline"
                  className="w-full justify-start"
                  disabled={entering}
                  onClick={() => choose(m.tenant)}
                >
                  {m.tenant}
                </Button>
              ))}
            </>
          ) : error ? (
            <Button asChild variant="outline" className="w-full">
              <Link to="/login" replace>
                {t('ssoBackToSignIn')}
              </Link>
            </Button>
          ) : (
            <LoadingState description={t('ssoCompleting')} />
          )}
        </div>
      </div>
    </div>
  );
}

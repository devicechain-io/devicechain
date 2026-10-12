// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Logo } from '@/components/brand/Logo';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { signInErrorKey } from '@/i18n/signInError';
import {
  captureSsoReturn,
  leaveSsoCompletion,
  resolveSsoLinkReturn,
  runSsoCompletion,
} from '@/lib/sso';
import type { IdentityAuth } from '@/lib/api/user-management';

// Where the identity provider sends the browser back to. The address carries a one-time
// code, so this page is careful before it is anything else:
//
//   - captureSsoReturn() runs in the first render, ahead of every effect, and removes the
//     code from the address bar (main.tsx has already done it for a real page load; the
//     call is idempotent). It also tells the page to send no Referer.
//   - the code is redeemed once, by runSsoCompletion, whose result is shared across
//     React StrictMode's double mount;
//   - a failure shows its reason and a way back to sign-in. Nothing here retries or
//     navigates on its own, so a bad return cannot loop;
//   - every way out — into a tenant, the admin console, or back to sign-in — is a FULL
//     navigation (leaveSsoCompletion), never the router. This document forbids the
//     Referer, and a client-side hop would carry that into the console, maps included.
// The code is never logged.

const PROVIDER_ERROR_KEYS: Record<string, string> = {
  refused: 'login:ssoErrorRefused',
  link_required: 'login:ssoErrorLinkRequired',
  idp_error: 'login:ssoErrorIdp',
  groups_overage: 'login:ssoErrorGroupsOverage',
};

// A link's refusal names the check that failed (lib/sso SSO_LINK_ERRORS); the provider failing
// keeps the sign-in's class. Anything else (a token that did not verify, an unknown reason)
// gets the general text, which lists what to check.
const LINK_ERROR_KEYS: Record<string, string> = {
  link_email_mismatch: 'ssoLinkErrorEmailMismatch',
  link_unverified_email: 'ssoLinkErrorUnverifiedEmail',
  link_session_ended: 'ssoLinkErrorSessionEnded',
  link_already_linked: 'ssoLinkErrorAlreadyLinked',
  link_not_allowed: 'ssoLinkErrorNotAllowed',
  idp_error: 'ssoErrorIdp',
};

export default function LoginSsoCompletePage() {
  const { isAuthenticated, completeSso, selectTenant } = useAuth();
  const { t } = useTranslation('login');
  // Strips the query before anything else in this tree can read or send it.
  captureSsoReturn();
  // A return from linking one's OWN account (beginSsoLink) lands here too, with `linked=1`
  // or `error=` and no code: the person is already signed in, nothing is redeemed, and the
  // page only says how the link went.
  const linkReturn = resolveSsoLinkReturn();

  const [error, setError] = useState<string | null>(null);
  const [identity, setIdentity] = useState<IdentityAuth | null>(null);
  const [returnTo, setReturnTo] = useState('/');
  const [entering, setEntering] = useState(false);
  // Whether a session already existed when this page opened: such a person is sent on
  // without the code being spent.
  const [arrivedSignedIn] = useState(isAuthenticated);
  // Once leaving, the page shows only its loading state: a session that becomes live
  // during the hop must not render anything in this document.
  const [leaving, setLeaving] = useState(false);
  const left = useRef(false);
  const leave = (dest: string) => {
    if (left.current) return;
    left.current = true;
    setLeaving(true);
    leaveSsoCompletion(dest);
  };

  const enterTenant = async (auth: IdentityAuth, tenant: string, dest: string) => {
    await selectTenant(auth.identityToken, tenant);
    leave(dest);
  };

  useEffect(() => {
    if (linkReturn) return;
    if (arrivedSignedIn) {
      leave('/');
      return;
    }
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
          leave('/admin');
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

  const backToSignIn = () => leave('/login');

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

  if (linkReturn) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background px-4">
        <div className="w-full max-w-sm">
          <div className="mb-8 flex flex-col items-center text-center">
            <Logo deviceColor="currentColor" className="mb-4 h-16 w-auto" />
          </div>
          <div className="space-y-3 rounded-lg border border-border bg-card p-6">
            {linkReturn.linked ? (
              <p className="text-sm text-foreground">{t('ssoLinkDone')}</p>
            ) : (
              <ErrorBanner message={t(LINK_ERROR_KEYS[linkReturn.providerError] ?? 'ssoLinkRefused')} />
            )}
            <Button
              variant="outline"
              className="w-full"
              disabled={leaving}
              onClick={() => leave('/')}
            >
              {t('ssoLinkContinue')}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo deviceColor="currentColor" className="mb-4 h-16 w-auto" />
        </div>
        <div className="space-y-3 rounded-lg border border-border bg-card p-6">
          {error && !leaving && <ErrorBanner message={error} />}
          {leaving || isAuthenticated ? (
            <LoadingState description={t('ssoCompleting')} />
          ) : identity && !error ? (
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
            <Button variant="outline" className="w-full" onClick={backToSignIn}>
              {t('ssoBackToSignIn')}
            </Button>
          ) : (
            <LoadingState description={t('ssoCompleting')} />
          )}
        </div>
      </div>
    </div>
  );
}

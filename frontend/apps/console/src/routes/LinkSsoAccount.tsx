// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { isExpired } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { serverErrorCode } from '@/i18n/signInError';
import { startSsoLink } from '@/lib/sso';
import { errMessage } from '@/routes/common';

// Links the signed-in person's own account at an identity provider to their identity, so
// they can sign in through it afterwards. The server decides everything that matters: it
// picks the connection from the identity's own email domain, accepts only a password sign-in,
// and only a provider that reports this identity's own email address. This starts the flow
// and navigates to the address the server returns, exactly as given; the provider then
// returns to the sign-in completion page, which reports how the link went.
//
// The server takes only the identity token of a password sign-in, which lasts 15 minutes. A
// lapsed one cannot be renewed from the tenant session (that token names no sign-in), so
// this says to sign in again and offers the way, rather than the action disappearing.

// The begin's refusals, by extensions.code. SSO_LINK_SESSION_REQUIRED and
// SSO_LINK_PASSWORD_REQUIRED are both answered by signing in again with a password.
const LINK_ERROR_KEYS: Record<string, string> = {
  SSO_LINK_NO_CONNECTION: 'linkSsoErrorNoConnection',
  SSO_LINK_ALREADY_LINKED: 'linkSsoErrorAlreadyLinked',
  SSO_LINK_NOT_ALLOWED: 'linkSsoErrorNotAllowed',
  SSO_LINK_PASSWORD_REQUIRED: 'linkSsoErrorPasswordRequired',
  SSO_LINK_SESSION_REQUIRED: 'linkSsoSessionEnded',
};
const SIGN_IN_AGAIN = new Set(['SSO_LINK_SESSION_REQUIRED', 'SSO_LINK_PASSWORD_REQUIRED']);

export function LinkSsoAccount() {
  const { t } = useTranslation('userMenu');
  const { identityToken, signInMethod, logout } = useAuth();
  const [error, setError] = useState<string | null>(null);
  const [signInAgain, setSignInAgain] = useState(false);
  const [busy, setBusy] = useState(false);

  // The context recomputes on state changes, not on the clock: a token it still reports can
  // have lapsed since, so the expiry is read again here.
  const usable =
    signInMethod === 'password' && identityToken !== null && !isExpired(identityToken, 0);

  const submit = async () => {
    if (!usable || identityToken === null) {
      setSignInAgain(true);
      return;
    }
    setError(null);
    setBusy(true);
    try {
      const url = await startSsoLink(identityToken);
      // Left busy: the page is about to be replaced by the provider's.
      window.location.assign(url);
    } catch (err) {
      const code = serverErrorCode(err);
      if (code && SIGN_IN_AGAIN.has(code)) setSignInAgain(true);
      const key = code ? LINK_ERROR_KEYS[code] : undefined;
      setError(key ? t(key) : errMessage(err));
      setBusy(false);
    }
  };

  if (!usable || signInAgain) {
    const fallback = signInMethod === 'sso' ? t('linkSsoErrorPasswordRequired') : t('linkSsoSessionEnded');
    return (
      <div className="space-y-4">
        <p className="text-sm text-foreground">{error ?? fallback}</p>
        <div className="flex gap-2">
          <Button onClick={() => void logout()}>{t('linkSsoSignInAgain')}</Button>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <p className="text-sm text-muted-foreground">{t('linkSsoExplain')}</p>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy}>
          {t('linkSsoContinue')}
        </Button>
      </div>
    </div>
  );
}

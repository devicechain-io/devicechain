// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { startSsoLink } from '@/lib/sso';
import { errMessage } from '@/routes/common';

// Links the signed-in person's own account at an identity provider to their identity, so
// they can sign in through it afterwards. The server decides everything that matters: it
// accepts only a password sign-in, only a tenant connection, and only a provider that
// reports this identity's own email address. This form starts the flow and navigates to the
// address the server returns, exactly as given; the provider then returns to the sign-in
// completion page, which reports how the link went.
//
// A refusal at this step arrives as the server's own message (it carries no code), shown
// as given. The refusals that come later, at the provider's return, are reported by the
// completion page.
export function LinkSsoAccount() {
  const { t } = useTranslation('userMenu');
  const { identityToken } = useAuth();
  const [connection, setConnection] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    const token = connection.trim();
    if (token === '') return setError(t('linkSsoConnectionRequired'));
    if (!identityToken) return setError(t('linkSsoSessionEnded'));
    setError(null);
    setBusy(true);
    try {
      const url = await startSsoLink(identityToken, token);
      // Left busy: the page is about to be replaced by the provider's.
      window.location.assign(url);
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <FormField
        label={t('linkSsoConnectionLabel')}
        htmlFor="link-sso-connection"
        description={t('linkSsoConnectionDescription')}
      >
        <Input
          id="link-sso-connection"
          value={connection}
          onChange={(e) => setConnection(e.target.value)}
          autoComplete="off"
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy}>
          {t('linkSsoContinue')}
        </Button>
      </div>
    </div>
  );
}

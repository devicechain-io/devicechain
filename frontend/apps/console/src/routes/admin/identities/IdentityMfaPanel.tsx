// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { serverErrorCode } from '@/i18n/signInError';
import { resetIdentityMfa } from '@/lib/api/admin';
import { MFA_CODE } from '@/lib/api/mfa';
import { errMessage } from '@/routes/common';

// An identity's two-factor state on the operator's identity page, and the reset for a lost
// authenticator. Resetting removes the factor and ends every session the identity has.
//
// The server asks for a NEW PASSWORD with the reset when the factor has failed attempts on
// record, because then the password may be in someone else's hands: it answers the code
// MFA_RESET_NEEDS_PASSWORD and changes nothing. The panel does not guess that up front. It
// tries the plain reset, and on that code asks for the password and tries again, so an
// operator is never asked for a password the server does not need, and never has a reset
// refused for want of one without being told what to do next.
export function IdentityMfaPanel({
  email,
  enabled,
  onChanged,
}: {
  email: string;
  enabled: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('identities');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needsPassword, setNeedsPassword] = useState(false);
  const [newPassword, setNewPassword] = useState('');

  const run = async (password?: string) => {
    setError(null);
    setBusy(true);
    try {
      await resetIdentityMfa(email, password);
      toast(t('mfaResetToast', { email }));
      setNeedsPassword(false);
      setNewPassword('');
      onChanged();
    } catch (err) {
      if (serverErrorCode(err) === MFA_CODE.resetNeedsPassword) {
        setNeedsPassword(true);
      } else {
        setError(errMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const start = async () => {
    if (
      !(await confirm({
        title: t('mfaResetConfirmTitle'),
        description: t('mfaResetConfirm', { email }),
        confirmLabel: t('mfaReset'),
      }))
    ) {
      return;
    }
    await run();
  };

  const submitPassword = (e: FormEvent) => {
    e.preventDefault();
    void run(newPassword);
  };

  return (
    <SectionPanel
      title={t('mfaSectionTitle')}
      description={t('mfaSectionDescription')}
      action={
        enabled && !needsPassword ? (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => void start()} data-shot="identity-mfa-reset">
            {t('mfaReset')}
          </Button>
        ) : undefined
      }
    >
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <Badge variant={enabled ? 'success' : 'outline'}>{enabled ? t('mfaOn') : t('mfaOff')}</Badge>
      {needsPassword && (
        <form onSubmit={submitPassword} className="mt-4 max-w-md space-y-3" data-shot="identity-mfa-reset-password">
          <p className="text-sm text-foreground">{t('mfaResetNeedsPassword')}</p>
          <FormField label={t('mfaNewPasswordLabel')} htmlFor="mfa-reset-password">
            <Input
              id="mfa-reset-password"
              type="password"
              autoComplete="new-password"
              autoFocus
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              required
            />
          </FormField>
          <div className="flex gap-2">
            <Button type="submit" loading={busy} disabled={busy || newPassword === ''}>
              {t('mfaResetWithPassword')}
            </Button>
            <Button
              type="button"
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setNeedsPassword(false);
                setNewPassword('');
              }}
            >
              {t('common:cancel')}
            </Button>
          </div>
        </form>
      )}
    </SectionPanel>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useRef, useState } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { useTranslation } from 'react-i18next';
import { GraphQLRequestError } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { serverErrorCode } from '@/i18n/signInError';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

function unavailable(err: unknown): boolean {
  const code = serverErrorCode(err);
  if (['STEP_UP_SSO', 'MFA_NOT_ENABLED', 'MFA_NOT_ENROLLED', 'MFA_ENROLLMENT_REQUIRED', 'UNAVAILABLE', 'MFA_UNAVAILABLE'].includes(code ?? '')) return true;
  if (err instanceof GraphQLRequestError && (err.status === 0 || err.status >= 500)) return true;
  return err instanceof Error && /unavailable|could not be checked right now|could not be refreshed right now|cannot query field ["']?stepUp|unknown field ["']?stepUp/i.test(err.message);
}

export function StepUpDialog({ open, onOpenChange, onSuccess, onUnavailable }: {
  open: boolean; onOpenChange: (open: boolean) => void; onSuccess: () => void; onUnavailable: () => void;
}) {
  const { t } = useTranslation('approvals');
  const { stepUp } = useAuth();
  const [code, setCode] = useState('');
  const [error, setError] = useState(false);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const submit = async () => {
    if (inFlight.current) return;
    inFlight.current = true; setBusy(true); setError(false);
    try {
      await stepUp(code);
      setCode(''); onOpenChange(false); onSuccess();
    } catch (err) {
      setCode('');
      if (unavailable(err)) { onOpenChange(false); onUnavailable(); }
      else setError(true);
    } finally { inFlight.current = false; setBusy(false); }
  };
  return <Dialog.Root open={open} onOpenChange={value => { if (!busy) { setCode(''); setError(false); onOpenChange(value); } }}>
    <Dialog.Portal><Dialog.Overlay className="fixed inset-0 z-50 bg-black/50" />
      <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-md -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-lg border bg-background p-6 shadow-lg">
        <Dialog.Title>{t('stepUp')}</Dialog.Title>
        <Dialog.Description>{t('stepUpHelp')}</Dialog.Description>
        <form className="space-y-3" onSubmit={e => { e.preventDefault(); void submit(); }}>
          <label htmlFor="step-up-code">{t('authenticatorCode')}</label>
          <Input id="step-up-code" inputMode="numeric" autoComplete="one-time-code" maxLength={7} value={code}
            onChange={e => setCode(e.target.value)} disabled={busy} autoFocus />
          {error && <p role="alert">{t('stepUpInvalid')}</p>}
          <Button type="submit" loading={busy} disabled={busy || !/^\d{6}$/.test(code.replace(/ /g, ''))}>{t('verifyCode')}</Button>
          <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>{t('common:cancel')}</Button>
        </form>
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>;
}

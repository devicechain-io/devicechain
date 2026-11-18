// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/lazy/automationReview';
import { useRef, useState } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { useTranslation } from 'react-i18next';
import { serverErrorCode } from '@/i18n/signInError';
import { StepUpDialog } from '@/components/StepUpDialog';
import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import type { BreakGlassRequest } from './use-break-glass';

export default function BreakGlass({ request }: { request: BreakGlassRequest }) {
  const { t } = useTranslation('automationReview');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [stepUp, setStepUp] = useState(false);
  const verified = useRef(false);
  const inFlight = useRef(false);
  const submit = async () => {
    if (inFlight.current || !reason.trim()) return;
    inFlight.current = true; setBusy(true);
    try { request.resolve(await request.retry(reason.trim())); }
    catch (err) {
      if (!verified.current && ['STEP_UP_REQUIRED', 'SECOND_FACTOR_REQUIRED'].includes(serverErrorCode(err) ?? '')) setStepUp(true);
      else request.reject(err);
    } finally { inFlight.current = false; setBusy(false); }
  };
  return <>
    <Dialog.Root open={!stepUp} onOpenChange={open => { if (!open && !busy) request.resolve(undefined); }}>
      <Dialog.Portal><Dialog.Overlay className="fixed inset-0 z-50 bg-black/50" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-md -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-lg border bg-background p-6">
          <Dialog.Title>{t('breakGlassTitle')}</Dialog.Title>
          <Dialog.Description>{t('breakGlassExplain')}</Dialog.Description>
          <form className="space-y-3" onSubmit={e => { e.preventDefault(); void submit(); }}>
            <label htmlFor="break-glass-reason">{t('breakGlassReason')}</label>
            <Textarea id="break-glass-reason" required maxLength={512} value={reason} onChange={e => setReason(e.target.value)} disabled={busy} />
            <Button type="submit" disabled={busy || !reason.trim()}>{t('breakGlassContinue')}</Button>
            <Button type="button" variant="outline" disabled={busy} onClick={() => request.resolve(undefined)}>{t('cancel')}</Button>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
    <StepUpDialog open={stepUp} onOpenChange={open => { if (!open) queueMicrotask(() => { if (!verified.current) request.resolve(undefined); }); }}
      onSuccess={() => { verified.current = true; void submit(); }}
      onUnavailable={() => request.reject(new Error(t('SECOND_FACTOR_REQUIRED')))} />
  </>;
}

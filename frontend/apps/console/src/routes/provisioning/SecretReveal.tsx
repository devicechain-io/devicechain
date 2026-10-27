// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reveal-once dialog for a provisioning profile's secret.
//
// 🔴 THE SECRET LIVES IN EXACTLY ONE PLACE: the `secret` prop of this dialog, which its owner
// holds in component state for as long as the dialog is open. The server keeps only a digest,
// so there is nothing to refetch; this component never asks for one, never writes the value to
// storage, a URL, a toast or a log, and keeps no copy of its own (the copy button reads the prop).
//
// The owner clears the state in `onClose`, and because that state is component state it is
// also gone when the owner unmounts, so leaving the page drops it. Closing is deliberately an
// explicit act: a click outside the dialog or an Esc does NOT dismiss it, because a secret
// that cannot be read again should not be lost to a stray click. The Close button always works.

import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import { AlertTriangle, Check, Copy } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { IconButton } from '@/components/ui/icon-button';
import { useToast } from '@/components/ui/toast';

const KEY_INPUT_ID = 'pp-reveal-key';
const SECRET_INPUT_ID = 'pp-reveal-secret';

/** What the dialog shows. `secret` is the value that can never be read again. */
export interface RevealedSecret {
  profileToken: string;
  provisionKey: string;
  secret: string;
  /** True when the secret replaces an earlier one (rotation), which changes the wording. */
  rotated: boolean;
}

export function SecretReveal({
  revealed,
  onClose,
}: {
  /** Null while nothing is being shown. */
  revealed: RevealedSecret | null;
  onClose: () => void;
}) {
  const { t } = useTranslation('provisioning');
  return (
    <DialogPrimitive.Root open={revealed != null} onOpenChange={(open) => !open && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/80" />
        <DialogPrimitive.Content
          data-shot="provisioning-secret"
          // Not dismissed by a stray click or Esc: see the header.
          onInteractOutside={(e) => e.preventDefault()}
          onEscapeKeyDown={(e) => e.preventDefault()}
          className="fixed left-1/2 top-1/2 z-50 grid w-full max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4 rounded-lg border bg-background p-6 shadow-lg"
        >
          {revealed && (
            <>
              <div className="flex flex-col gap-2">
                <DialogPrimitive.Title className="text-lg font-semibold text-foreground">
                  {revealed.rotated ? t('revealRotatedTitle') : t('revealCreatedTitle')}
                </DialogPrimitive.Title>
                <DialogPrimitive.Description className="text-sm text-muted-foreground">
                  {revealed.rotated
                    ? t('revealRotatedBody', { token: revealed.profileToken })
                    : t('revealCreatedBody', { token: revealed.profileToken })}
                </DialogPrimitive.Description>
              </div>
              <FormField label={t('revealKeyLabel')} htmlFor={KEY_INPUT_ID}>
                <div className="flex items-center gap-2">
                  <Input id={KEY_INPUT_ID} readOnly value={revealed.provisionKey} className="font-mono text-xs" />
                  <CopyField value={revealed.provisionKey} inputId={KEY_INPUT_ID} label={t('revealCopyKey')} />
                </div>
              </FormField>
              <FormField label={t('revealSecretLabel')} htmlFor={SECRET_INPUT_ID}>
                <div className="flex items-center gap-2">
                  <Input
                    id={SECRET_INPUT_ID}
                    readOnly
                    value={revealed.secret}
                    className="font-mono text-xs"
                    onFocus={(e) => e.currentTarget.select()}
                    data-shot-mask
                  />
                  <CopyField value={revealed.secret} inputId={SECRET_INPUT_ID} label={t('revealCopySecret')} />
                </div>
              </FormField>
              <p role="alert" className="flex items-start gap-2 text-sm text-foreground">
                <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warning" />
                <span>{t('revealShownOnce')}</span>
              </p>
              <div className="flex justify-end">
                <Button onClick={onClose}>{t('revealClose')}</Button>
              </div>
            </>
          )}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

/**
 * Copies a field. It never puts the value in a title, an aria-label or a toast; where the async
 * clipboard is missing (an http:// origin) it falls back to copying the selected field, and
 * failing that leaves the field selected and says to copy it by hand.
 */
export function CopyField({
  value,
  inputId,
  label,
  manualHint,
}: {
  value: string;
  inputId: string;
  label: string;
  /** What to say when copying failed; defaults to this dialog's own wording. */
  manualHint?: string;
}) {
  const { t } = useTranslation('provisioning');
  const { toast } = useToast();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = async () => {
    let ok = false;
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(value);
        ok = true;
      }
    } catch {
      ok = false;
    }
    if (!ok) {
      const input = document.getElementById(inputId) as HTMLInputElement | null;
      input?.focus();
      input?.select();
      try {
        ok = typeof document.execCommand === 'function' && document.execCommand('copy');
      } catch {
        ok = false;
      }
    }
    if (ok) {
      setCopied(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } else {
      toast(manualHint ?? t('revealCopyManually'), 'error');
    }
  };

  return (
    <IconButton label={label} size="sm" onClick={() => void copy()}>
      {copied ? <Check size={14} className="text-emerald-500" /> : <Copy size={14} />}
    </IconButton>
  );
}

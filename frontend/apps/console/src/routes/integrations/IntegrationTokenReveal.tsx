// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reveal-once dialog for an integration's bearer token, with the connect instructions
// filled in around it.
//
// 🔴 THE TOKEN LIVES IN EXACTLY ONE PLACE: the `revealed` prop of this dialog, which its owner
// holds in component state for as long as the dialog is open. The server keeps only a digest, so
// there is nothing to refetch; this component never asks for one, never writes the value to
// storage, a URL, a toast or a log, and keeps no copy of its own (the copy buttons read the
// prop). The owner clears the state in `onClose`, and because that state is component state it is
// gone when the owner unmounts too. Closing is an explicit act: a click outside or Esc does NOT
// dismiss it, because a token that cannot be read again should not be lost to a stray click.

import { useTranslation } from 'react-i18next';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import { AlertTriangle } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { useAuth } from '@/auth/AuthProvider';
import { CopyField } from '@/routes/provisioning/SecretReveal';
import type { Integration } from '@/lib/api/integrations';
import { ConnectSnippet } from './ConnectSnippet';

const ID_PREFIX = 'ig-reveal';
const TOKEN_INPUT_ID = 'ig-reveal-token';

/** What the dialog shows. `token` is the value that can never be read again. */
export interface RevealedToken {
  integration: Pick<Integration, 'token' | 'type' | 'generic'>;
  token: string;
  /** True when the token replaces an earlier one (rotation), which changes the wording. */
  rotated: boolean;
}

export function IntegrationTokenReveal({
  revealed,
  onClose,
}: {
  /** Null while nothing is being shown. */
  revealed: RevealedToken | null;
  onClose: () => void;
}) {
  const { t } = useTranslation('integrations');
  const { claims } = useAuth();
  return (
    <DialogPrimitive.Root open={revealed != null} onOpenChange={(open) => !open && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/80" />
        <DialogPrimitive.Content
          data-shot="integration-token"
          onInteractOutside={(e) => e.preventDefault()}
          onEscapeKeyDown={(e) => e.preventDefault()}
          className="fixed left-1/2 top-1/2 z-50 grid max-h-[90vh] w-full max-w-2xl -translate-x-1/2 -translate-y-1/2 gap-4 overflow-y-auto rounded-lg border bg-background p-6 shadow-lg"
        >
          {revealed && (
            <>
              <div className="flex flex-col gap-2">
                <DialogPrimitive.Title className="text-lg font-semibold text-foreground">
                  {revealed.rotated ? t('revealRotatedTitle') : t('revealCreatedTitle')}
                </DialogPrimitive.Title>
                <DialogPrimitive.Description className="text-sm text-muted-foreground">
                  {revealed.rotated
                    ? t('revealRotatedBody', { token: revealed.integration.token })
                    : t('revealCreatedBody', { token: revealed.integration.token })}
                </DialogPrimitive.Description>
              </div>
              <FormField label={t('revealTokenLabel')} htmlFor={TOKEN_INPUT_ID}>
                <div className="flex items-center gap-2">
                  <Input
                    id={TOKEN_INPUT_ID}
                    readOnly
                    value={revealed.token}
                    className="font-mono text-xs"
                    onFocus={(e) => e.currentTarget.select()}
                    data-shot-mask
                  />
                  <CopyField
                    value={revealed.token}
                    inputId={TOKEN_INPUT_ID}
                    label={t('revealCopyToken')}
                    manualHint={t('copyManually')}
                  />
                </div>
              </FormField>
              <p role="alert" className="flex items-start gap-2 text-sm text-foreground">
                <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warning" />
                <span>{t('revealShownOnce')}</span>
              </p>
              <ConnectSnippet
                idPrefix={ID_PREFIX}
                integration={revealed.integration}
                input={{
                  tenant: claims?.tenant ?? '',
                  integrationToken: revealed.integration.token,
                  bearerToken: revealed.token,
                }}
              />
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

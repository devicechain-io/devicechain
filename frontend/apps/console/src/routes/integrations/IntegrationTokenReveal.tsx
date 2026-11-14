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
import { RevealOnceDialog } from '@/components/RevealOnceDialog';
import { AlertTriangle } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { useAuth } from '@/auth/AuthProvider';
import { CopyField } from '@/routes/provisioning/SecretReveal';
import type { Integration } from '@/lib/api/integrations';
import { ConnectSnippet } from './ConnectSnippet';
import '@/i18n/lazy/integrations';

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
  if (!revealed) return null;
  return (
    <RevealOnceDialog
      shot="integration-token"
      title={revealed.rotated ? t('revealRotatedTitle') : t('revealCreatedTitle')}
      description={revealed.rotated ? t('revealRotatedBody', { token: revealed.integration.token }) : t('revealCreatedBody', { token: revealed.integration.token })}
      onDone={onClose}
      doneLabel={t('revealClose')}
      contentClassName="max-w-2xl"
    >
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
    </RevealOnceDialog>
  );
}

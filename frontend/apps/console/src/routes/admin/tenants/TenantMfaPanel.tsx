// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { setTenantMfaRequired } from '@/lib/api/admin';
import { errMessage } from '@/routes/common';

// The tenant-level "Require two-factor authentication" control on the operator's tenant page.
// A tenant's requirement is its own setting OR its tier's floor; no tenant setting can go below
// the floor, so when the tier requires it the control says so and cannot be turned off here
// (the server refuses anyway; showing a button that fails would be worse than saying why).
// Turning it on is confirmed first: members who sign in with a password and no second factor
// are asked to set one up, and password sessions already inside are refused at their next
// refresh.
export function TenantMfaPanel({
  tenant,
  required,
  byTier,
  onChanged,
}: {
  tenant: string;
  required: boolean;
  byTier: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('tenants');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggle = async () => {
    setError(null);
    if (
      !required &&
      !(await confirm({
        title: t('mfaEnableConfirmTitle', { token: tenant }),
        description: t('mfaEnableConfirm', { token: tenant }),
        confirmLabel: t('mfaRequire'),
        destructive: false,
      }))
    ) {
      return;
    }
    setBusy(true);
    try {
      await setTenantMfaRequired(tenant, !required);
      toast(t(required ? 'mfaDisabledToast' : 'mfaEnabledToast', { token: tenant }));
      onChanged();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel
      title={t('mfaPanelTitle')}
      description={t('mfaPanelDescription')}
      action={
        <Button variant="outline" size="sm" disabled={busy || byTier} onClick={toggle}>
          {required ? t('mfaStopRequiring') : t('mfaRequire')}
        </Button>
      }
    >
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={required || byTier ? 'secondary' : 'outline'}>
          {required || byTier ? t('mfaRequiredBadge') : t('mfaNotRequiredBadge')}
        </Badge>
        {byTier && <span className="text-sm text-muted-foreground">{t('mfaByTierNote')}</span>}
      </div>
    </SectionPanel>
  );
}

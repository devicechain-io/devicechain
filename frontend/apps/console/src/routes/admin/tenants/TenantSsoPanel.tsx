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
import { serverErrorCode } from '@/i18n/signInError';
import { setTenantSsoEnforced } from '@/lib/api/admin';
import { errMessage } from '@/routes/common';

// The server's lock-out refusals, by extensions.code. Matched on the code, never on the
// prose, which is English and may be reworded.
const REFUSAL_KEYS: Record<string, string> = {
  SSO_ENFORCE_NO_CONNECTION: 'ssoRefusedNoConnection',
};

// The tenant-level "Require single sign-on" control. Turning it on ends every member's
// password session at its next refresh, so it is confirmed first; turning it off is not
// (it only lets password sign-ins back in). Turning it on needs at least one enabled
// connection serving the tenant, and the server refuses otherwise: that refusal is shown in
// the panel, translated by its code, because it names what to do next (connect a provider
// first) and a toast would be gone before it was read.
export function TenantSsoPanel({
  tenant,
  enforced,
  onChanged,
}: {
  tenant: string;
  enforced: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('tenants');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);

  const toggle = async () => {
    setRefusal(null);
    if (
      !enforced &&
      !(await confirm({
        title: t('ssoEnableConfirmTitle', { token: tenant }),
        description: t('ssoEnableConfirm', { token: tenant }),
        confirmLabel: t('ssoRequire'),
      }))
    ) {
      return;
    }
    setBusy(true);
    try {
      await setTenantSsoEnforced(tenant, !enforced);
      toast(t(enforced ? 'ssoDisabledToast' : 'ssoEnabledToast', { token: tenant }));
      onChanged();
    } catch (err) {
      const key = REFUSAL_KEYS[serverErrorCode(err) ?? ''];
      setRefusal(key ? t(key) : errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel
      title={t('ssoPanelTitle')}
      description={t('ssoPanelDescription')}
      action={
        <Button variant="outline" size="sm" disabled={busy} onClick={toggle}>
          {enforced ? t('ssoStopRequiring') : t('ssoRequire')}
        </Button>
      }
    >
      {refusal && <ErrorBanner message={refusal} onDismiss={() => setRefusal(null)} />}
      <Badge variant={enforced ? 'secondary' : 'outline'}>
        {enforced ? t('ssoRequiredBadge') : t('ssoNotRequiredBadge')}
      </Badge>
    </SectionPanel>
  );
}

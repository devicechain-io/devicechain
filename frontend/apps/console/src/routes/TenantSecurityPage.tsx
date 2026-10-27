// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority, isScoped } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Checkbox } from '@/components/ui/checkbox';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { getTenantMfa, setTenantMfaRequired } from '@/lib/api/mfa';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';

// Security settings of the tenant itself: whether its people's password sign-ins must use a
// second factor. A member of the tenant (member:write, not limited to part of it) sets it.
//
// The requirement is the OR of this setting and the tenant's TIER floor. A tenant admin can
// turn their own setting on or off but cannot go below the floor, so when the tier requires it
// the control is on, locked, and says why: a checkbox that silently refuses a click, or one
// that shows "off" while the requirement is in force, would both misreport the state.
//
// Turning it on strands nobody: a member with no second factor can still sign in and is taken
// to set one up. Single sign-on is governed by its connection's requirements, not by this.
export default function TenantSecurityPage() {
  const { t } = useTranslation('security');
  const { claims } = useAuth();
  const confirm = useConfirm();
  const { toast } = useToast();
  const [version, reload] = useReload();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const allowed = hasAuthority(claims, 'member:write') && !isScoped(claims);
  // `current`: the checkbox acts on this value, so it must be the answer to THIS read.
  const { current: tenant, loading, error: loadError } = useQuery(getTenantMfa, [version]);

  if (!allowed) {
    return (
      <PageShell title={t('tenantTitle')} description={t('tenantDescription')}>
        <p className="text-sm text-muted-foreground">{t('tenantNoPermission')}</p>
      </PageShell>
    );
  }
  if (!tenant) {
    return (
      <PageShell title={t('tenantTitle')} description={t('tenantDescription')}>
        {loading ? <LoadingState description={t('loading')} /> : <ErrorState description={loadError ?? t('loadFailed')} />}
      </PageShell>
    );
  }

  const byTier = tenant.mfaRequiredByTier;
  // What is in force: the tenant's own setting OR the tier floor.
  const checked = tenant.mfaRequired || byTier;

  const change = async (next: boolean) => {
    setError(null);
    if (
      next &&
      !(await confirm({
        title: t('tenantEnableConfirmTitle'),
        description: t('tenantEnableConfirm'),
        confirmLabel: t('tenantRequire'),
        destructive: false,
      }))
    ) {
      return;
    }
    setBusy(true);
    try {
      await setTenantMfaRequired(next);
      toast(t(next ? 'tenantEnabledToast' : 'tenantDisabledToast'));
      reload();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <PageShell title={t('tenantTitle')} description={t('tenantDescription')}>
      <div className="max-w-2xl space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <SectionPanel title={t('tenantPanelTitle')}>
          <label className="flex items-start gap-3 text-sm text-foreground" data-shot="tenant-mfa-toggle">
            <Checkbox
              checked={checked}
              disabled={busy || byTier}
              onCheckedChange={(v) => void change(v === true)}
              aria-label={t('tenantRequireLabel')}
              className="mt-0.5"
            />
            <span className="space-y-1">
              <span className="block font-medium">{t('tenantRequireLabel')}</span>
              <span className="block text-muted-foreground">
                {byTier ? t('tenantLockedByTier') : t('tenantRequireHelp')}
              </span>
            </span>
          </label>
          <p className="mt-4 text-sm text-muted-foreground">{t('tenantSsoNote')}</p>
        </SectionPanel>
      </div>
    </PageShell>
  );
}

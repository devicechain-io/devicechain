// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Ban, Power, Trash2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { CopyToken } from '@/components/ui/copy-token';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants, setTenantEnabled, deleteTenant } from '@/lib/api/admin';
import { PurgeBadge, StatusBadge, errMessage, useReload } from '@/routes/common';
import { TenantForm } from '@/routes/admin/tenants/TenantForm';
import { TenantSettingsPanel } from '@/routes/admin/tenants/TenantSettingsPanel';
import { TenantDeletionPanel } from './TenantDeletionPanel';
import { TenantAiModelsPanel } from '@/routes/admin/tenants/TenantAiModelsPanel';
import { TierPill } from '@/components/tiers/TierPill';

export default function TenantDetailPage() {
  const { t } = useTranslation('tenants');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: tenants, loading, error } = useQuery(listTenants, [version]);

  const tenant = tenants?.find((t) => t.token === token) ?? null;

  // Gate the spinner and error on the FIRST load only (no data yet). Saving reloads this
  // query, and useQuery re-enters `loading` on every refetch while keeping the prior
  // `tenants` — so a bare `if (loading)` unmounted the whole page (and TenantForm) to a
  // spinner after every save, remounting the form on its default Basic tab and dumping an
  // operator who saved from Settings back to Basic. With `!tenants` the form stays mounted
  // and the active tab is preserved; the refetch repaints in place. (Same first-load
  // gating the tier detail page uses.)
  if (loading && !tenants) {
    return (
      <PageShell title={token}>
        <LoadingState description={t('loadingTenant')} />
      </PageShell>
    );
  }
  if (error && !tenants) {
    return (
      <PageShell title={token}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!tenant) {
    return (
      <PageShell title={token}>
        <ErrorState description={t('tenantNotFound', { token })} />
      </PageShell>
    );
  }

  const toggleEnabled = async () => {
    try {
      await setTenantEnabled(tenant.token, !tenant.enabled);
      toast(
        tenant.enabled
          ? t('tenantDisabledToast', { token: tenant.token })
          : t('tenantEnabledToast', { token: tenant.token }),
      );
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const remove = async () => {
    if (
      !(await confirm({
        title: t('deleteTenantTitle'),
        description: t('deleteTenantConfirm', { token: tenant.token }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      const ok = await deleteTenant(tenant.token);
      // `false` here means "nothing changed", which for a tenant we are looking at
      // means it was already deleted — not that it is missing.
      toast(
        ok
          ? t('tenantDeletedToast', { token: tenant.token })
          : t('tenantAlreadyDeletedToast', { token: tenant.token }),
      );
      navigate('/admin/tenants');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  // A tenant that has been through the delete door (ADR-077). Its row survives to
  // reserve the token, but it admits nobody and must not offer live-tenant actions.
  const purged = tenant.purgeState !== 'active';

  return (
    <PageShell
      // The display name is the title; its token rides on the same line as a copyable
      // chip, and the tier + enabled state sit below as badges (a tenant can be unnamed,
      // so fall back to the token as the title too).
      title={tenant.name || tenant.token}
      titleAdornment={tenant.name ? <CopyToken value={tenant.token} /> : undefined}
      description={
        <div className="flex items-center gap-2">
          <TierPill label={tenant.tier.token} color={tenant.tier.color} />
          <StatusBadge enabled={tenant.enabled} />
          {purged && <PurgeBadge state={tenant.purgeState} epoch={tenant.purgeEpoch} />}
        </div>
      }
      action={
        // Neither action is offered once the tenant is deleted: re-enabling it would
        // be a lie (the grant path refuses entry on the lifecycle, not on this flag),
        // and deleting it again does nothing.
        purged ? null : (
          <>
            <Button variant="outline" size="sm" onClick={toggleEnabled}>
              {tenant.enabled ? <Ban size={14} /> : <Power size={14} />}
              {tenant.enabled ? t('disable') : t('enable')}
            </Button>
            <Button variant="destructive" size="sm" onClick={remove}>
              <Trash2 size={14} /> {t('delete')}
            </Button>
          </>
        )
      }
    >
      {/* Tabbed: Basic + Settings edit the tenant (one atomic save), and the Effective
          tab is the read-only RESULT of what Settings sets, folded onto the tenant's
          tier — re-read after every save, not another way to edit it. TenantForm renders
          its own per-tab SectionPanels, so it is not wrapped here. */}
      <TenantForm
        tenant={tenant}
        onDone={(m) => {
          toast(m);
          reload();
        }}
        effectiveSettingsPanel={<TenantSettingsPanel tenant={tenant} />}
        aiModelsPanel={<TenantAiModelsPanel tenant={tenant} />}
        // Only while a deletion is running. On an active tenant the tab would be an empty
        // room, and on a FINISHED deletion there is no tenant page left to show it on —
        // completion removes the row, which is why the history lives at /admin/deletions.
        deletionPanel={purged ? <TenantDeletionPanel token={tenant.token} /> : undefined}
      />
    </PageShell>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Ban, Power, Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants, setTenantEnabled, deleteTenant } from '@/lib/api/admin';
import { BackLink, StatusBadge, errMessage, useReload } from '@/routes/common';
import { TenantForm } from '@/routes/admin/tenants/TenantForm';
import { TenantSettingsPanel } from '@/routes/admin/tenants/TenantSettingsPanel';
import { TierPill } from '@/components/tiers/TierPill';

export default function TenantDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: tenants, loading, error } = useQuery(listTenants, [version]);

  const tenant = tenants?.find((t) => t.token === token) ?? null;

  const back = <BackLink to="/admin/tenants">Tenants</BackLink>;

  // Gate the spinner and error on the FIRST load only (no data yet). Saving reloads this
  // query, and useQuery re-enters `loading` on every refetch while keeping the prior
  // `tenants` — so a bare `if (loading)` unmounted the whole page (and TenantForm) to a
  // spinner after every save, remounting the form on its default Basic tab and dumping an
  // operator who saved from Settings back to Basic. With `!tenants` the form stays mounted
  // and the active tab is preserved; the refetch repaints in place. (Same first-load
  // gating the tier detail page uses.)
  if (loading && !tenants) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description="Loading tenant…" />
      </PageShell>
    );
  }
  if (error && !tenants) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!tenant) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={`Tenant “${token}” not found.`} />
      </PageShell>
    );
  }

  const toggleEnabled = async () => {
    try {
      await setTenantEnabled(tenant.token, !tenant.enabled);
      toast(`Tenant “${tenant.token}” ${tenant.enabled ? 'disabled' : 'enabled'}`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const remove = async () => {
    if (
      !(await confirm({
        title: 'Delete tenant',
        description: `Delete “${tenant.token}”? This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      const ok = await deleteTenant(tenant.token);
      toast(ok ? `Tenant “${tenant.token}” deleted` : `Tenant “${tenant.token}” not found`);
      navigate('/admin/tenants');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      // The display name is the title; the token, tier, and enabled state ride beside it
      // as badges — one row, matching the tier detail page (a tenant can be unnamed, so
      // fall back to the token as the title too).
      title={tenant.name || tenant.token}
      description={
        <div className="mt-1 flex items-center gap-2">
          <Badge variant="outline" className="font-mono">
            {tenant.token}
          </Badge>
          <TierPill label={tenant.tier.token} color={tenant.tier.color} />
          <StatusBadge enabled={tenant.enabled} />
        </div>
      }
      action={
        <div className="flex items-center gap-2">
          {back}
          <Button variant="outline" size="sm" onClick={toggleEnabled}>
            {tenant.enabled ? <Ban size={14} /> : <Power size={14} />}
            {tenant.enabled ? 'Disable' : 'Enable'}
          </Button>
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
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
      />
    </PageShell>
  );
}

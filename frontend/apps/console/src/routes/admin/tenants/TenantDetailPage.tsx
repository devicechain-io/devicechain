// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Ban, Power, Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants, setTenantEnabled, deleteTenant } from '@/lib/api/admin';
import { BackLink, StatusBadge, errMessage, useReload } from '@/routes/common';
import { TenantForm } from '@/routes/admin/tenants/TenantForm';

export default function TenantDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: tenants, loading, error } = useQuery(listTenants, [version]);

  const tenant = tenants?.find((t) => t.token === token) ?? null;

  const back = <BackLink to="/admin/tenants">Tenants</BackLink>;

  if (loading) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description="Loading tenant…" />
      </PageShell>
    );
  }
  if (error) {
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
    if (!window.confirm(`Delete tenant “${tenant.token}”? This cannot be undone.`)) return;
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
      title={token}
      description={
        <div className="mt-1 flex items-center gap-2">
          <StatusBadge enabled={tenant.enabled} />
          {tenant.name && <span className="text-sm text-muted-foreground">{tenant.name}</span>}
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
      <SectionPanel>
        <TenantForm
          tenant={tenant}
          onDone={(m) => {
            toast(m);
            reload();
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}

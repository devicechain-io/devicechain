// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants, setTenantEnabled, deleteTenant } from '@/lib/api/admin';
import { AdminCard, BackLink, errMessage, useReload } from '@/routes/admin/common';
import { TenantForm } from '@/routes/admin/tenants/TenantForm';

export default function TenantDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: tenants, loading, error } = useQuery(listTenants, [version]);

  const tenant = tenants?.find((t) => t.token === token) ?? null;

  if (loading) {
    return (
      <PageShell title={token} action={<BackLink to="/admin/tenants">Tenants</BackLink>}>
        <LoadingState description="Loading tenant…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} action={<BackLink to="/admin/tenants">Tenants</BackLink>}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!tenant) {
    return (
      <PageShell title={token} action={<BackLink to="/admin/tenants">Tenants</BackLink>}>
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
      description={tenant.name ?? '—'}
      action={<BackLink to="/admin/tenants">Tenants</BackLink>}
    >
      <AdminCard title={`Edit tenant “${token}”`}>
        <div className="space-y-6">
          <TenantForm
            tenant={tenant}
            onDone={(m) => {
              toast(m);
              reload();
            }}
          />
          <div className="flex gap-2 border-t border-border pt-4">
            <Button variant="outline" onClick={toggleEnabled}>
              {tenant.enabled ? 'Disable' : 'Enable'}
            </Button>
            <Button variant="destructive" onClick={remove}>
              Delete tenant
            </Button>
          </div>
        </div>
      </AdminCard>
    </PageShell>
  );
}

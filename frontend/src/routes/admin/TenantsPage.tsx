// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants, setTenantEnabled, deleteTenant, type AdminTenant } from '@/lib/api/admin';
import { StatusBadge, errMessage, useReload } from '@/routes/admin/common';

export default function TenantsPage() {
  const navigate = useNavigate();
  const [version, reload] = useReload();
  const { data: tenants, loading, error } = useQuery(listTenants, [version]);
  const { toast } = useToast();

  const toggleEnabled = async (t: AdminTenant) => {
    try {
      await setTenantEnabled(t.token, !t.enabled);
      toast(`Tenant “${t.token}” ${t.enabled ? 'disabled' : 'enabled'}`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const remove = async (t: AdminTenant) => {
    if (!window.confirm(`Delete tenant “${t.token}”? This cannot be undone.`)) return;
    try {
      const ok = await deleteTenant(t.token);
      toast(ok ? `Tenant “${t.token}” deleted` : `Tenant “${t.token}” not found`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Tenants"
      description="The instance's tenant registry. A tenant is a control-plane record, not a provisioned resource."
      action={
        <Button onClick={() => navigate('/admin/tenants/new')}>
          <Plus size={16} /> New tenant
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description="Loading tenants…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !tenants || tenants.length === 0 ? (
          <EmptyState description="No tenants yet. Create the first one to get started." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Status</DataTableHeaderCell>
              <DataTableHeaderCell>Config</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">Actions</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {tenants.map((t) => (
                <DataTableRow key={t.id}>
                  <DataTableCell className="font-medium">{t.token}</DataTableCell>
                  <DataTableCell>{t.name ?? '—'}</DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={t.enabled} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate font-mono text-xs text-muted-foreground">
                    {t.config ?? '—'}
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => navigate(`/admin/tenants/${encodeURIComponent(t.token)}`)}
                      >
                        Edit
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => toggleEnabled(t)}>
                        {t.enabled ? 'Disable' : 'Enable'}
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => remove(t)}>
                        Delete
                      </Button>
                    </div>
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>
    </PageShell>
  );
}

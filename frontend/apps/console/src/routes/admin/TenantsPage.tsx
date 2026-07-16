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
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants } from '@/lib/api/admin';
import { StatusBadge, rowLinkProps } from '@/routes/common';

export default function TenantsPage() {
  const navigate = useNavigate();
  const { data: tenants, loading, error } = useQuery(listTenants, []);

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
              <DataTableHeaderCell>Tier</DataTableHeaderCell>
              <DataTableHeaderCell>Status</DataTableHeaderCell>
              <DataTableHeaderCell>Config</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {tenants.map((t) => (
                <DataTableRow
                  key={t.id}
                  {...rowLinkProps(() => navigate(`/admin/tenants/${encodeURIComponent(t.token)}`))}
                >
                  <DataTableCell className="font-medium">{t.token}</DataTableCell>
                  <DataTableCell>{t.name ?? '—'}</DataTableCell>
                  {/* Never blank: the tier is a required FK (ADR-065). */}
                  <DataTableCell>{t.tier.name || t.tier.token}</DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={t.enabled} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate font-mono text-xs text-muted-foreground">
                    {t.config ?? '—'}
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

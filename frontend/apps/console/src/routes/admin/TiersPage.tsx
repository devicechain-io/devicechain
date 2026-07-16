// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
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
import { listTenantTierCatalog } from '@/lib/api/admin';
import { rowLinkProps } from '@/routes/common';

export default function TiersPage() {
  const navigate = useNavigate();
  const { data: tiers, loading, error } = useQuery(listTenantTierCatalog, []);

  return (
    <PageShell
      title="Tiers"
      description="The packaging a tenant is sold. Each tier sets the defaults its tenants are held to across the platform — ceilings today, AI model access as it lands. Editing one moves every tenant at it, within a minute and with no restart."
      action={
        <Button onClick={() => navigate('/admin/tiers/new')}>
          <Plus size={16} /> New tier
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description="Loading tiers…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !tiers || tiers.length === 0 ? (
          <EmptyState description="No tiers defined yet." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Description</DataTableHeaderCell>
              <DataTableHeaderCell>Tenants</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {tiers.map((t) => (
                <DataTableRow
                  key={t.id}
                  {...rowLinkProps(() => navigate(`/admin/tiers/${encodeURIComponent(t.token)}`))}
                >
                  <DataTableCell className="font-medium">{t.token}</DataTableCell>
                  <DataTableCell>{t.name ?? '—'}</DataTableCell>
                  <DataTableCell className="max-w-md text-muted-foreground">
                    {t.description ?? '—'}
                  </DataTableCell>
                  <DataTableCell>
                    <Badge variant="secondary">{t.tenantCount}</Badge>
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

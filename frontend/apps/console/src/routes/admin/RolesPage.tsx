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
import { listRoles } from '@/lib/api/admin';
import { rowLinkProps } from '@/routes/common';

export default function RolesPage() {
  const navigate = useNavigate();
  const { data: roles, loading, error } = useQuery(listRoles, []);

  return (
    <PageShell
      title="Roles"
      description="The global role catalog. System roles gate the admin API; tenant roles gate the data plane."
      action={
        <Button onClick={() => navigate('/admin/roles/new')}>
          <Plus size={16} /> New role
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description="Loading roles…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !roles || roles.length === 0 ? (
          <EmptyState description="No roles defined yet." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Scope</DataTableHeaderCell>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Authorities</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {roles.map((r) => (
                <DataTableRow
                  key={r.id}
                  {...rowLinkProps(() => navigate(`/admin/roles/${r.scope}/${encodeURIComponent(r.token)}`))}
                >
                  <DataTableCell>
                    <Badge variant="secondary">{r.scope}</Badge>
                  </DataTableCell>
                  <DataTableCell className="font-medium">{r.token}</DataTableCell>
                  <DataTableCell>{r.name ?? '—'}</DataTableCell>
                  <DataTableCell>
                    <div className="flex flex-wrap gap-1">
                      {r.authorities.map((a) => (
                        <Badge key={a} variant="outline" className="font-mono text-xs">
                          {a}
                        </Badge>
                      ))}
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

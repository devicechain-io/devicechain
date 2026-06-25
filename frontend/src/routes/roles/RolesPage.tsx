// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@/lib/hooks/use-query';
import { listRoles } from '@/lib/api/user-management';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Badge } from '@/components/ui/badge';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

export default function RolesPage() {
  const { data: roles, loading, error } = useQuery(listRoles);

  if (loading) return <LoadingState description="Loading roles…" />;
  if (error) return <ErrorState description={error} />;

  return (
    <PageShell
      title="Roles"
      description="Capability bundles granted to users (requires role:read). Enforcement is on authorities, not role names."
    >
      {!roles || roles.length === 0 ? (
        <EmptyState description="No roles defined in this tenant yet." />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Token</DataTableHeaderCell>
            <DataTableHeaderCell>Name</DataTableHeaderCell>
            <DataTableHeaderCell>Description</DataTableHeaderCell>
            <DataTableHeaderCell>Authorities</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {roles.map((role) => (
              <DataTableRow key={role.id}>
                <DataTableCell>
                  <span className="font-mono text-xs text-foreground">{role.token}</span>
                </DataTableCell>
                <DataTableCell className="font-medium text-foreground">{role.name || '—'}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{role.description || '—'}</DataTableCell>
                <DataTableCell>
                  <div className="flex flex-wrap gap-1">
                    {role.authorities.map((a) => (
                      <Badge
                        key={a}
                        variant={a === '*' ? 'default' : 'secondary'}
                        className="font-mono text-[11px]"
                      >
                        {a === '*' ? '★ all' : a}
                      </Badge>
                    ))}
                  </div>
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </PageShell>
  );
}
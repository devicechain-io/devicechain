// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@/lib/hooks/use-query';
import { listUsers } from '@/lib/api/user-management';
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

export default function UsersPage() {
  const { data: users, loading, error } = useQuery(listUsers);

  if (loading) return <LoadingState description="Loading users…" />;
  if (error) return <ErrorState description={error} />;

  return (
    <PageShell title="Users" description="People with access to this tenant (requires user:read)">
      {!users || users.length === 0 ? (
        <EmptyState description="No users in this tenant yet." />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Username</DataTableHeaderCell>
            <DataTableHeaderCell>Name</DataTableHeaderCell>
            <DataTableHeaderCell>Email</DataTableHeaderCell>
            <DataTableHeaderCell>Roles</DataTableHeaderCell>
            <DataTableHeaderCell className="text-center">Status</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {users.map((user) => {
              const fullName = [user.firstName, user.lastName].filter(Boolean).join(' ');
              return (
                <DataTableRow key={user.id}>
                  <DataTableCell>
                    <span className="font-medium text-foreground">{user.username}</span>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{fullName || '—'}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{user.email || '—'}</DataTableCell>
                  <DataTableCell>
                    <div className="flex flex-wrap gap-1">
                      {user.roles.length > 0 ? (
                        user.roles.map((r) => (
                          <Badge key={r.id} variant="secondary">
                            {r.name || r.token}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </div>
                  </DataTableCell>
                  <DataTableCell className="text-center">
                    {user.enabled ? (
                      <Badge variant="outline" className="border-success/40 text-success">
                        Enabled
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="border-destructive/40 text-destructive">
                        Disabled
                      </Badge>
                    )}
                  </DataTableCell>
                </DataTableRow>
              );
            })}
          </DataTableBody>
        </DataTable>
      )}
    </PageShell>
  );
}
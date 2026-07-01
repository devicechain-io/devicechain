// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listAdminAuditEvents, listTenants, type AdminAuditEvent } from '@/lib/api/admin';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

const pageSize = 25;

// Operation → badge colour, covering both auth events and admin mutations:
// created/succeeded green, failed/removed red, refreshed/changed neutral.
function operationVariant(op: string): 'success' | 'destructive' | 'secondary' | 'outline' {
  switch (op) {
    case 'login':
    case 'create':
      return 'success';
    case 'login_failed':
    case 'delete':
      return 'destructive';
    case 'refresh':
    case 'update':
      return 'secondary';
    default:
      return 'outline';
  }
}

// The journal stores schema-qualified table names (e.g. "user-management.roles");
// show just the table, plus the affected row's captured label (e.g. its token)
// when present, else its pk. Auth events carry no table.
function target(row: AdminAuditEvent): string {
  if (!row.tableName) return '—';
  const dot = row.tableName.lastIndexOf('.');
  const table = dot >= 0 ? row.tableName.slice(dot + 1) : row.tableName;
  const id = row.entityLabel || (row.entityPk ? `#${row.entityPk}` : '');
  return id ? `${table} ${id}` : table;
}

export default function AdminAuditPage() {
  const [pageNumber, setPageNumber] = useState(1);
  const [category, setCategory] = useState('');
  const [tenant, setTenant] = useState('');
  const [actorInput, setActorInput] = useState('');
  // Debounce the actor field so typing a name fires one query, not one per key.
  const actor = useDebouncedValue(actorInput.trim(), 300);

  // Tenants populate the tenant filter dropdown (this is the cross-tenant view).
  const { data: tenants } = useQuery(() => listTenants(), []);

  // Reset to the first page whenever a filter changes.
  useEffect(() => setPageNumber(1), [category, tenant, actor]);

  const { data, loading, error } = useQuery(
    () =>
      listAdminAuditEvents({
        pageNumber,
        pageSize,
        category: category || undefined,
        tenant: tenant || undefined,
        actor: actor || undefined,
      }),
    [pageNumber, category, tenant, actor],
  );

  const results: AdminAuditEvent[] = data?.results ?? [];

  return (
    <PageShell
      title="Audit"
      description="Authentication events and identity, role & tenant administration across the instance (ADR-019)."
      action={
        <div className="flex items-center gap-2">
          <select
            aria-label="Filter by tenant"
            className="h-9 rounded-md border border-input bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            value={tenant}
            onChange={(e) => setTenant(e.target.value)}
          >
            <option value="">All tenants</option>
            {(tenants ?? []).map((t) => (
              <option key={t.id} value={t.token}>
                {t.token}
              </option>
            ))}
          </select>
          <select
            aria-label="Filter by category"
            className="h-9 rounded-md border border-input bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
          >
            <option value="">All categories</option>
            <option value="auth">Auth</option>
            <option value="mutation">Mutation</option>
          </select>
          <Input
            className="h-9 w-48"
            placeholder="Filter by actor…"
            value={actorInput}
            onChange={(e) => setActorInput(e.target.value)}
          />
        </div>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description="Loading audit log…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : results.length === 0 ? (
          <EmptyState description="No audit records match." />
        ) : (
          <>
            <DataTable>
              <DataTableHead>
                <DataTableHeaderCell>Time</DataTableHeaderCell>
                <DataTableHeaderCell>Category</DataTableHeaderCell>
                <DataTableHeaderCell>Actor</DataTableHeaderCell>
                <DataTableHeaderCell>Operation</DataTableHeaderCell>
                <DataTableHeaderCell>Tenant</DataTableHeaderCell>
                <DataTableHeaderCell>Target</DataTableHeaderCell>
              </DataTableHead>
              <DataTableBody>
                {results.map((row) => (
                  <DataTableRow key={row.id}>
                    <DataTableCell className="whitespace-nowrap text-muted-foreground">
                      {formatTime(row.occurredTime)}
                    </DataTableCell>
                    <DataTableCell>
                      <Badge variant={row.category === 'auth' ? 'secondary' : 'outline'}>
                        {row.category}
                      </Badge>
                    </DataTableCell>
                    <DataTableCell className="font-medium text-foreground">
                      {row.actor || '—'}
                    </DataTableCell>
                    <DataTableCell>
                      <Badge variant={operationVariant(row.operation)}>{row.operation}</Badge>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{row.tenant || '—'}</DataTableCell>
                    <DataTableCell className="text-foreground">{target(row)}</DataTableCell>
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
            <Pagination
              pageNumber={pageNumber}
              pageSize={pageSize}
              pagination={data!.pagination}
              onPageChange={setPageNumber}
              className="mt-4"
            />
          </>
        )}
      </div>
    </PageShell>
  );
}

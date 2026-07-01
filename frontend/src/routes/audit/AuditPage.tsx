// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listAuditEvents, type AuditEvent } from '@/lib/api/audit';
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

// Operation → badge colour. create/update/delete are the mutation operations
// this service records; anything else (should not occur here) falls back neutral.
function operationVariant(op: string): 'success' | 'secondary' | 'destructive' | 'outline' {
  switch (op) {
    case 'create':
      return 'success';
    case 'update':
      return 'secondary';
    case 'delete':
      return 'destructive';
    default:
      return 'outline';
  }
}

// The audit journal stores schema-qualified table names (e.g.
// "device-management.devices"); show just the table for readability.
function shortTable(tableName: string | null | undefined): string {
  if (!tableName) return '—';
  const dot = tableName.lastIndexOf('.');
  return dot >= 0 ? tableName.slice(dot + 1) : tableName;
}

export default function AuditPage() {
  const [pageNumber, setPageNumber] = useState(1);
  const [operation, setOperation] = useState('');
  const [actorInput, setActorInput] = useState('');
  // Debounce the actor field so typing a name fires one query, not one per key.
  const actor = useDebouncedValue(actorInput.trim(), 300);

  // Reset to the first page whenever a filter changes so the view never lands on
  // an out-of-range page.
  useEffect(() => setPageNumber(1), [operation, actor]);

  const { data, loading, error } = useQuery(
    () =>
      listAuditEvents({
        pageNumber,
        pageSize,
        operation: operation || undefined,
        actor: actor || undefined,
      }),
    [pageNumber, operation, actor],
  );

  const results: AuditEvent[] = data?.results ?? [];

  return (
    <PageShell
      title="Audit"
      description="Every change to this tenant's registry, recorded by construction (ADR-019)."
      action={
        <div className="flex items-center gap-2">
          <select
            aria-label="Filter by operation"
            className="h-9 rounded-md border border-input bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            value={operation}
            onChange={(e) => setOperation(e.target.value)}
          >
            <option value="">All operations</option>
            <option value="create">Create</option>
            <option value="update">Update</option>
            <option value="delete">Delete</option>
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
              <DataTableHeaderCell>Actor</DataTableHeaderCell>
              <DataTableHeaderCell>Operation</DataTableHeaderCell>
              <DataTableHeaderCell>Table</DataTableHeaderCell>
              <DataTableHeaderCell>Target</DataTableHeaderCell>
              <DataTableHeaderCell>Rows</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((row) => (
                <DataTableRow key={row.id}>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(row.occurredTime)}
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    {row.actor || '—'}
                  </DataTableCell>
                  <DataTableCell>
                    <Badge variant={operationVariant(row.operation)}>{row.operation}</Badge>
                  </DataTableCell>
                  <DataTableCell className="text-foreground">{shortTable(row.tableName)}</DataTableCell>
                  <DataTableCell className="font-mono text-xs text-muted-foreground">
                    {row.entityLabel || (row.entityPk ? `#${row.entityPk}` : '—')}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{row.rowsAffected}</DataTableCell>
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
    </PageShell>
  );
}

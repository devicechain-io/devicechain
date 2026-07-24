// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listAuditEvents, type AuditEvent } from '@/lib/api/audit';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
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

// Options carry a `labelKey` into the `audit` catalog (ADR-066), resolved with t()
// inside the component; the `value` is the wire enum and never localized.
const OPERATION_OPTIONS = [
  { value: 'create', labelKey: 'opCreate' },
  { value: 'update', labelKey: 'opUpdate' },
  { value: 'delete', labelKey: 'opDelete' },
];

// Wire enum → catalog key for the row badge, so the badge is localized to match
// the filter above it. An unexpected operation falls back to its raw value rather
// than a blank cell.
const OPERATION_LABEL_KEY: Record<string, string> = {
  create: 'opCreate',
  update: 'opUpdate',
  delete: 'opDelete',
};

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
  const { t } = useTranslation('audit');
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
  const operationOptions = OPERATION_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }));

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        <div className="flex items-center gap-2">
          <Combobox
            className="h-9 w-44"
            placeholder={t('allOperationsPlaceholder')}
            value={operation}
            onChange={setOperation}
            options={operationOptions}
          />
          <Input
            className="h-9 w-48"
            placeholder={t('actorPlaceholder')}
            value={actorInput}
            onChange={(e) => setActorInput(e.target.value)}
          />
        </div>
      }
    >
      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colTime')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colActor')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colOperation')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colTable')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colTarget')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colRows')}</DataTableHeaderCell>
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
                    <Badge variant={operationVariant(row.operation)}>
                      {OPERATION_LABEL_KEY[row.operation] ? t(OPERATION_LABEL_KEY[row.operation]) : row.operation}
                    </Badge>
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

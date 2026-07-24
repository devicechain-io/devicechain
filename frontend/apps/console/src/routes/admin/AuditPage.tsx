// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listAdminAuditEvents, listTenants, type AdminAuditEvent } from '@/lib/api/admin';
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

// Wire enum → catalog key for the operation badge. An operation this map does not
// cover (should not occur) falls back to its raw value rather than a blank cell —
// see the render below.
const OPERATION_LABEL_KEY: Record<string, string> = {
  login: 'opLogin',
  login_failed: 'opLoginFailed',
  create: 'opCreate',
  delete: 'opDelete',
  refresh: 'opRefresh',
  update: 'opUpdate',
};

// Category filter options: `value` is the wire enum sent to the query and never
// localized; `labelKey` resolves through t() at render time.
const CATEGORY_OPTIONS = [
  { value: 'auth', labelKey: 'categoryAuth' },
  { value: 'mutation', labelKey: 'categoryMutation' },
];

// Wire enum → catalog key for the category badge, mirroring CATEGORY_OPTIONS.
const CATEGORY_LABEL_KEY: Record<string, string> = {
  auth: 'categoryAuth',
  mutation: 'categoryMutation',
};

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
  const { t } = useTranslation('adminAudit');
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
  const categoryOptions = CATEGORY_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }));

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        <div className="flex items-center gap-2">
          <Combobox
            className="h-9 w-44"
            placeholder={t('allTenantsPlaceholder')}
            value={tenant}
            onChange={setTenant}
            options={(tenants ?? []).map((tr) => ({ value: tr.token }))}
          />
          <Combobox
            className="h-9 w-44"
            placeholder={t('allCategoriesPlaceholder')}
            value={category}
            onChange={setCategory}
            options={categoryOptions}
          />
          <Input
            className="h-9 w-48"
            placeholder={t('filterByActorPlaceholder')}
            value={actorInput}
            onChange={(e) => setActorInput(e.target.value)}
          />
        </div>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description={t('loadingAuditLog')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : results.length === 0 ? (
          <EmptyState description={t('noAuditRecordsMatch')} />
        ) : (
          <>
            <DataTable>
              <DataTableHead>
                <DataTableHeaderCell>{t('colTime')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colCategory')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colActor')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colOperation')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colTenant')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colTarget')}</DataTableHeaderCell>
              </DataTableHead>
              <DataTableBody>
                {results.map((row) => (
                  <DataTableRow key={row.id}>
                    <DataTableCell className="whitespace-nowrap text-muted-foreground">
                      {formatTime(row.occurredTime)}
                    </DataTableCell>
                    <DataTableCell>
                      <Badge variant={row.category === 'auth' ? 'secondary' : 'outline'}>
                        {CATEGORY_LABEL_KEY[row.category] ? t(CATEGORY_LABEL_KEY[row.category]) : row.category}
                      </Badge>
                    </DataTableCell>
                    <DataTableCell className="font-medium text-foreground">
                      {row.actor || '—'}
                    </DataTableCell>
                    <DataTableCell>
                      <Badge variant={operationVariant(row.operation)}>
                        {OPERATION_LABEL_KEY[row.operation] ? t(OPERATION_LABEL_KEY[row.operation]) : row.operation}
                      </Badge>
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

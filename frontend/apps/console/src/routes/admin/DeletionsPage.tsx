import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { PageShell } from '@/components/ui/page-shell';
import { listTenantDeletions } from '@/lib/api/admin';
import type { AdminTenantDeletion } from '@/lib/api/admin';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';

import { isComplete, summarize } from './tenants/deletions';

/** How many records a page shows. */
const PAGE_SIZE = 50;

type Filter = 'all' | 'inFlight' | 'completed';

/**
 * The filters, in the order they are offered.
 *
 * Declared here rather than inline in the JSX because the no-literal-string lint gate reads
 * any string literal in markup as untranslated copy — and it is right to: these are IDs, and
 * the fact that they look like words is exactly the confusion the gate exists to catch. The
 * visible labels come from `deletionsFilter_*`.
 */
const FILTERS: Filter[] = ['all', 'inFlight', 'completed'];

const FILTER_ARG: Record<Filter, boolean | undefined> = {
  all: undefined,
  inFlight: false,
  completed: true,
};

/**
 * Every tenant deletion on this instance (ADR-077).
 *
 * 🔴 IT IS AN INSTANCE-LEVEL PAGE AND NOT A TAB ON A TENANT, and the reason is structural
 * rather than a layout preference: a COMPLETED deletion has no tenant. Completion removes
 * the tenant row — that removal IS the token release — so the record outlives the page it
 * would otherwise have lived on, and the moment a deletion finishes it would disappear from
 * the only place showing it.
 *
 * That also makes this the auditor's page. "Prove this customer's data was erased" is
 * answered by a completed record and its per-store lines, which is exactly the artifact
 * that survives.
 */
export default function DeletionsPage() {
  const { t } = useTranslation('tenants');
  const [filter, setFilter] = useState<Filter>('all');
  const {
    data: deletions,
    loading,
    error,
  } = useQuery(() => listTenantDeletions(FILTER_ARG[filter], PAGE_SIZE), [filter]);

  return (
    <PageShell
      title={t('deletionsTitle')}
      description={t('deletionsDescription')}
      action={
        <div className="flex gap-1">
          {FILTERS.map((f) => (
            <Button
              key={f}
              variant={filter === f ? 'default' : 'outline'}
              size="sm"
              onClick={() => setFilter(f)}
            >
              {t(`deletionsFilter_${f}`)}
            </Button>
          ))}
        </div>
      }
    >
      {loading && !deletions ? (
        <LoadingState description={t('deletionsLoading')} />
      ) : error && !deletions ? (
        <ErrorState description={error} />
      ) : !deletions || deletions.length === 0 ? (
        <EmptyState description={t('deletionsEmpty')} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deletionsColRequested')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deletionsColStatus')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deletionColErased')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deletionsColSystems')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {deletions.map((d) => (
              // Keyed on (token, epoch), never token alone: a token is released on
              // completion and reused, so one token carries several records and a
              // token-keyed list would collapse a predecessor into its successor.
              <DeletionRow key={`${d.token}|${d.epoch}`} deletion={d} />
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </PageShell>
  );
}

function DeletionRow({ deletion }: { deletion: AdminTenantDeletion }) {
  const { t } = useTranslation('tenants');
  const done = isComplete(deletion);
  const summary = summarize(deletion, Date.now());
  const notClean = deletion.stores.filter((s) => !s.complete).length;

  return (
    <DataTableRow>
      {/* The token, and deliberately never a NAME. The record does not store one, so that
          the erasure's own evidence is not the last place the customer's details live. */}
      <DataTableCell className="font-mono text-xs">{deletion.token}</DataTableCell>
      <DataTableCell className="whitespace-nowrap text-muted-foreground">
        {formatTime(deletion.epoch)}
      </DataTableCell>
      <DataTableCell>
        <div className="flex items-center gap-2">
          <Badge variant={done ? 'success' : 'secondary'}>
            {t(done ? 'deletionsStatusComplete' : 'deletionsStatusInFlight')}
          </Badge>
          <span className="text-xs text-muted-foreground">{t(summary.key, summary.values)}</span>
        </div>
      </DataTableCell>
      <DataTableCell className="tabular-nums">{deletion.rowsErased}</DataTableCell>
      <DataTableCell className="text-muted-foreground text-xs">
        {notClean === 0
          ? t('deletionsAllClean', { count: deletion.stores.length })
          : t('deletionsSomeHolding', { count: notClean })}
      </DataTableCell>
    </DataTableRow>
  );
}

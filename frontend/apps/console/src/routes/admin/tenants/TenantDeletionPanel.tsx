import { useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
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
import { getTenantDeletion } from '@/lib/api/admin';
import type { AdminTenantDeletionStore } from '@/lib/api/admin';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { useReload } from '@/routes/common';

import { hasNote, remedyKey, shouldPoll, storeProblem, storeState, summarize } from './deletions';

/** How often an in-flight deletion is re-read. Mirrors the alarms page's poll. */
const POLL_MS = 30000;

/**
 * The in-flight state of one tenant's deletion (ADR-077).
 *
 * It answers the four questions an operator actually has, in the order they have them: is
 * anything blocking, which system, can I fix it, and when does this end. The badge on the
 * page header says only THAT a deletion is running.
 */
export function TenantDeletionPanel({ token }: { token: string }) {
  const { t } = useTranslation('tenants');
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => getTenantDeletion(token), [token, version]);

  // Poll only while something can still change. A completed record never changes again, so
  // polling one is pure waste — and the coordinator writes a record only on its first pass,
  // so a just-deleted tenant polls until that record appears.
  const live = data === null || shouldPoll(data ?? null);
  useEffect(() => {
    if (!live) return undefined;
    const id = window.setInterval(reload, POLL_MS);
    return () => window.clearInterval(id);
  }, [live, reload]);

  // Recomputed per render rather than memoized on a clock: the countdown is derived from
  // now, and the poll is what moves it.
  const summary = useMemo(() => (data ? summarize(data, Date.now()) : null), [data]);

  if (loading && !data) return <LoadingState description={t('deletionLoading')} />;
  if (error && !data) return <ErrorState description={error} />;

  // No record yet. This is NOT an error and must not look like one: the purge coordinator
  // writes the record on its first pass, up to a minute after the delete, which is exactly
  // when an operator is watching.
  if (!data) return <EmptyState description={t('deletionNotStartedYet')} />;

  return (
    <div className="space-y-4">
      <p className="text-sm">{summary ? t(summary.key, summary.values) : null}</p>

      {data.blockedBy.length > 0 && (
        <ul className="text-sm text-muted-foreground list-disc pl-5 space-y-1">
          {data.blockedBy.map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      )}

      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('deletionColSystem')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('deletionColState')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('deletionColDetail')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('deletionColErased')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('deletionColAttempted')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {data.stores.map((line) => (
            <StoreRow key={line.store} line={line} />
          ))}
        </DataTableBody>
      </DataTable>
    </div>
  );
}

/** One storage system's row. */
function StoreRow({ line }: { line: AdminTenantDeletionStore }) {
  const { t } = useTranslation('tenants');
  const state = storeState(line);
  const problem = storeProblem(line);
  const remedy = remedyKey(line);

  return (
    <DataTableRow>
      <DataTableCell className="font-mono text-xs">{line.store}</DataTableCell>
      <DataTableCell>
        <Badge variant={STATE_VARIANT[state]}>{t(STATE_LABEL[state])}</Badge>
      </DataTableCell>
      <DataTableCell className="text-muted-foreground">
        {problem}
        {/* The note is a FOOTNOTE to clean, never a problem. A store carrying one is working
            as designed — the telemetry store on an instance with no telemetry, or the
            key-value store's exempted buckets — so it is rendered muted and apart from the
            column that carries things to act on. */}
        {hasNote(line) && <span className="block text-xs italic mt-1">{line.note}</span>}
        {remedy && <span className="block text-xs mt-1">{t(remedy)}</span>}
      </DataTableCell>
      {/* Rows accumulate across passes and are evidence the sweep ran — never a percentage.
          A deletion cannot complete on the pass that erased something, so the per-pass
          figure is zero on every pass that could finish one. */}
      <DataTableCell className="tabular-nums">{line.rowsErased}</DataTableCell>
      <DataTableCell className="whitespace-nowrap text-muted-foreground">
        {line.attemptedAt ? formatTime(line.attemptedAt) : '—'}
      </DataTableCell>
    </DataTableRow>
  );
}

const STATE_VARIANT = {
  clean: 'success',
  retaining: 'destructive',
  retrying: 'secondary',
} as const;

const STATE_LABEL = {
  clean: 'deletionStateClean',
  retaining: 'deletionStateRetaining',
  retrying: 'deletionStateRetrying',
} as const;

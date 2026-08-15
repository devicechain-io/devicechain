// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command-batch list — the record of every fleet write this tenant has fired.
//
// It exists because the create mutation's response is consumed once: a device that was
// REFUSED gets no command row (there is no status meaning "wanted but not created"), so
// without this record a refusal leaves no trace at all, and an operator who pushes a
// firmware command across a fleet and comes back in the morning has nothing to read.
//
// 🔴 READ-ONLY, deliberately: no create, no cancel. Both are separate slices.
//
// SERVER ORDER IS DISPLAY ORDER. The service returns batches newest-first and this table
// does not re-sort — which is what makes the page answer "show me what I just fired"
// rather than "show me an arbitrary slice sorted by something".

import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listCommandBatches } from '@/lib/api/command-delivery';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { formatTime } from '@/lib/utils';
import { rowLinkProps } from '@/routes/common';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { TargetKindBadge } from './TargetKindBadge';

const pageSize = 20;

// The wire values of CommandBatch.targetKind. An unrecognized value matches nothing
// server-side rather than erroring, so the filter is a closed set of the two the service
// writes; the labels are localized, the values never are.
const TARGET_KIND_OPTIONS = [
  { value: 'DEVICE_LIST', labelKey: 'targetDeviceList' },
  { value: 'GROUP', labelKey: 'targetGroup' },
];

export default function CommandBatchesPage() {
  const { t } = useTranslation('commandBatches');
  const navigate = useNavigate();
  const [pageNumber, setPageNumber] = useState(1);
  const [name, setName] = useState('');
  const [groupToken, setGroupToken] = useState('');
  const [targetKind, setTargetKind] = useState('');
  // The two text filters feed the query only once typing pauses, so a search is one
  // request rather than one per keystroke.
  const debouncedName = useDebouncedValue(name);
  const debouncedGroup = useDebouncedValue(groupToken);

  // Any filter change re-narrows the result set, so page 3 of the old set is meaningless
  // against the new one — go back to the head.
  useEffect(() => setPageNumber(1), [debouncedName, debouncedGroup, targetKind]);

  const { data, loading, error } = useQuery(
    () =>
      listCommandBatches({
        pageNumber,
        pageSize,
        name: debouncedName.trim() || undefined,
        groupToken: debouncedGroup.trim() || undefined,
        targetKind: targetKind || undefined,
      }),
    [pageNumber, debouncedName, debouncedGroup, targetKind],
  );

  const results = data?.results ?? [];

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      banner="devices"
      action={
        <>
          <Input
            className="h-9 w-44"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('filterCommandPlaceholder')}
            aria-label={t('filterCommandLabel')}
          />
          <Input
            className="h-9 w-44"
            value={groupToken}
            onChange={(e) => setGroupToken(e.target.value)}
            placeholder={t('filterGroupPlaceholder')}
            aria-label={t('filterGroupLabel')}
          />
          <Combobox
            className="h-9 w-40"
            options={TARGET_KIND_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }))}
            value={targetKind}
            onChange={setTargetKind}
            placeholder={t('filterAllTargets')}
            ariaLabel={t('filterTargetLabel')}
          />
        </>
      }
    >
      {loading && !data ? (
        <LoadingState description={t('loadingBatches')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colFired')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colCommand')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colTarget')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colAccepted')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colRefused')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colCancelled')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((b) => (
                <DataTableRow
                  key={b.id}
                  {...rowLinkProps(() => navigate(`/command-batches/${encodeURIComponent(b.token)}`))}
                >
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(b.createdAt)}
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">{b.name}</DataTableCell>
                  <DataTableCell>
                    <div className="flex items-center gap-2">
                      <TargetKindBadge targetKind={b.targetKind} />
                      {b.groupToken && (
                        <span className="text-muted-foreground">
                          {b.groupVersion == null
                            ? b.groupToken
                            : t('groupAtVersion', { group: b.groupToken, version: b.groupVersion })}
                        </span>
                      )}
                    </div>
                  </DataTableCell>
                  {/* Accepted OF resolved, as one fraction rather than two columns: the
                      pair only means anything read together — 4 accepted is good news
                      out of 4 and bad news out of 5,000. */}
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {t('acceptedOfResolved', { accepted: b.accepted, resolved: b.resolved })}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {b.resolved - b.accepted}
                  </DataTableCell>
                  <DataTableCell>
                    {b.cancelledAt && <Badge variant="outline">{t('cancelledBadge')}</Badge>}
                  </DataTableCell>
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

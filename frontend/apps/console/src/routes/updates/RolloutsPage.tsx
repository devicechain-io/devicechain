// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Group rollouts: an artifact moved across a device group a capped number of devices at a
// time. The list answers the question an operator opens it with: which rollouts are moving,
// which are stopped, and how far each has got.
//
// Routed page: the AreaGate in App.tsx renders the "not deployed" state on 'absent' only.

import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { listRollouts, type UpdateRolloutState } from '@/lib/api/update-management';
import { useUpdateAccess } from '@/lib/updateAuthority';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/ui/combobox';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { rowLinkProps } from '@/routes/common';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { FormDrawer } from '@/components/registry';
import { rolloutStateLabel } from '@/lib/enumLabel';
import { NewRolloutForm } from './NewRolloutForm';
import { DownloadsNotice } from './DownloadsNotice';
import { StoreNotice } from './StoreNotice';
import { RolloutProgressBar, RolloutStateBadge, pauseReasonText } from './rolloutBadges';

const pageSize = 50;
const STATES: UpdateRolloutState[] = ['DRAFT', 'RUNNING', 'PAUSED', 'COMPLETED', 'ABORTED'];

export default function RolloutsPage() {
  const { t } = useTranslation('updates');
  const navigate = useNavigate();
  const { canWrite } = useUpdateAccess();
  const [creating, setCreating] = useState(false);
  const [pageNumber, setPageNumber] = useState(1);
  const [state, setState] = useState('');

  useEffect(() => setPageNumber(1), [state]);

  const { data, loading, error } = useQuery(
    () => listRollouts({ pageNumber, pageSize, state: (state || undefined) as UpdateRolloutState | undefined }),
    [pageNumber, state],
  );
  const open = (token: string) => navigate(`/updates/rollouts/${encodeURIComponent(token)}`);

  return (
    <PageShell
      title={t('rolloutsTitle')}
      description={t('rolloutsDescription')}
      banner="devices"
      action={
        <>
          <Combobox
            className="h-9 w-40"
            options={STATES.map((s) => ({ value: s, label: rolloutStateLabel(t, s) }))}
            value={state}
            onChange={setState}
            placeholder={t('filterAllStates')}
            ariaLabel={t('filterStateLabel')}
          />
          {canWrite && (
            <Button onClick={() => setCreating(true)} data-shot="new-button">
              <Plus size={16} /> {t('newRollout')}
            </Button>
          )}
        </>
      }
    >
      <DownloadsNotice />
      <StoreNotice />
      {canWrite && (
        <FormDrawer
          shot="rollout-new"
          open={creating}
          onOpenChange={setCreating}
          title={t('newRollout')}
          description={t('newRolloutDescription')}
        >
          <NewRolloutForm
            onCreated={(token) => {
              setCreating(false);
              open(token);
            }}
          />
        </FormDrawer>
      )}
      {loading && !data ? (
        <LoadingState description={t('loadingRollouts')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : (data?.results.length ?? 0) === 0 ? (
        <EmptyState description={t('emptyRollouts')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colArtifact')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colGroup')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colState')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colProgress')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colMaxInFlight')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {data!.results.map((r) => {
                const paused = pauseReasonText(t, r);
                return (
                  <DataTableRow key={r.id} {...rowLinkProps(() => open(r.token))}>
                    <DataTableCell className="font-medium text-foreground">{r.name}</DataTableCell>
                    <DataTableCell className="whitespace-nowrap">
                      {r.artifact.name} {r.artifact.version}
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{r.groupToken}</DataTableCell>
                    <DataTableCell>
                      <RolloutStateBadge state={r.state} />
                      {paused && <p className="mt-1 text-xs text-muted-foreground">{paused}</p>}
                    </DataTableCell>
                    <DataTableCell className="min-w-48">
                      {r.state === 'DRAFT' ? (
                        <span className="text-xs text-muted-foreground">{t('rolloutNotStarted')}</span>
                      ) : (
                        <RolloutProgressBar progress={r.progress} />
                      )}
                    </DataTableCell>
                    <DataTableCell className="whitespace-nowrap text-muted-foreground">{r.maxInFlight}</DataTableCell>
                  </DataTableRow>
                );
              })}
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

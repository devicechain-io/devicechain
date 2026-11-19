// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { DataTable, DataTableHead, DataTableHeaderCell, DataTableBody, DataTableRow, DataTableCell } from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { listWindows } from '@/lib/api/alarm-maintenance';

export default function WindowsPage() {
  const { t } = useTranslation('alarmMaintenance');
  const { claims } = useAuth();
  const read = hasAuthority(claims, 'alarm:read');
  const write = hasAuthority(claims, 'alarm:write');
  const [status, setStatus] = useState('ACTIVE');
  const [page, setPage] = useState(1);
  const query = useQuery(() => read ? listWindows({ pageNumber: page, pageSize: 25, status }) : Promise.resolve(null), [read, status, page], [read, status, page]);
  return <PageShell title={t('title')} action={read && query.data && write ? <Button asChild><Link to="/alarm-maintenance/new">{t('create')}</Link></Button> : undefined}>
    {query.loading && !query.data ? <LoadingState /> : query.error && !query.data ? <ErrorState description={query.error} /> : query.data ? <div className="space-y-4">
      <SegmentedControl value={status} onValueChange={v => { setStatus(v); setPage(1); }} ariaLabel={t('status')}
        options={['SCHEDULED', 'ACTIVE', 'ENDED'].map(value => ({ value, label: t(`status${value}`) }))} />
      {query.data.results.length ? <DataTable><DataTableHead><DataTableHeaderCell>{t('name')}</DataTableHeaderCell><DataTableHeaderCell>{t('reason')}</DataTableHeaderCell></DataTableHead>
        <DataTableBody>{query.data.results.map(w => <DataTableRow key={w.token}><DataTableCell><Link className="text-primary underline" to={`/alarm-maintenance/${encodeURIComponent(w.token)}`}>{w.name || w.token}</Link></DataTableCell><DataTableCell>{w.reason}</DataTableCell></DataTableRow>)}</DataTableBody>
      </DataTable> : <EmptyState description={t('empty')} />}
      <Pagination pageNumber={page} pageSize={25} pagination={query.data.pagination} onPageChange={setPage} />
    </div> : null}
  </PageShell>;
}

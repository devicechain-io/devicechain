// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, Layers } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices } from '@/lib/api/device-management';
import { getDeviceStates } from '@/lib/api/device-state';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { rowLinkProps, useReload } from '@/routes/common';
import { FormDrawer } from '@/components/registry';
import { DeviceForm } from '@/routes/devices/DeviceForm';
import { DeviceBulkForm } from '@/routes/devices/DeviceBulkForm';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

const pageSize = 20;

// StatusDot shows a device's connectivity at a glance. `active` is undefined when
// no state is known (no state:read authority, or the device has never reported) —
// rendered as a neutral dash so the list stays useful without it.
function StatusDot({ active }: { active: boolean | undefined }) {
  const { t } = useTranslation('devices');
  if (active === undefined) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="inline-flex items-center gap-1.5 text-sm">
      <span className={cn('inline-block size-2 rounded-full', active ? 'bg-success' : 'bg-muted-foreground/40')} />
      <span className={active ? 'text-foreground' : 'text-muted-foreground'}>
        {active ? t('online') : t('offline')}
      </span>
    </span>
  );
}

export default function DevicesPage() {
  const { t } = useTranslation('devices');
  const navigate = useNavigate();
  const { toast } = useToast();
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  const [bulkCreating, setBulkCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listDevices({ pageNumber, pageSize }),
    [pageNumber, version],
  );

  const results = data?.results ?? [];
  const tokens = results.map((d) => d.token);
  // Status is best-effort and loaded separately: if state:read is missing or no
  // state exists yet, this query just yields nothing and the list is unaffected.
  const { data: states } = useQuery(() => getDeviceStates(tokens), [tokens.join(',')]);
  const activeByToken = new Map((states ?? []).map((s) => [s.deviceToken, s.active]));

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      banner="devices"
      action={
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setBulkCreating(true)}>
            <Layers size={16} /> {t('bulkCreate')}
          </Button>
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} /> {t('newDevice')}
          </Button>
        </div>
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title={t('newDevice')}>
        <DeviceForm
          onDone={(m) => {
            toast(m);
            setCreating(false);
            reload();
          }}
        />
      </FormDrawer>
      <FormDrawer open={bulkCreating} onOpenChange={setBulkCreating} title={t('bulkCreateTitle')}>
        <DeviceBulkForm
          onDone={(m) => {
            toast(m);
            setBulkCreating(false);
            reload();
          }}
        />
      </FormDrawer>
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
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colDescription')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colCreated')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((device) => (
                <DataTableRow
                  key={device.id}
                  {...rowLinkProps(() => navigate(`/devices/${encodeURIComponent(device.token)}`))}
                >
                  <DataTableCell>
                    <StatusDot active={activeByToken.get(device.token)} />
                  </DataTableCell>
                  <DataTableCell>
                    <span className="font-mono text-xs text-foreground">{device.token}</span>
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    {device.name || '—'}
                  </DataTableCell>
                  <DataTableCell>
                    <TypeCapsule appearance={appearanceOf(device.deviceType)} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {device.description || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {device.createdAt ? new Date(device.createdAt).toLocaleDateString() : '—'}
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

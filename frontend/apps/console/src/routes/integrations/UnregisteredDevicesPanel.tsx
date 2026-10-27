// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The identifiers an integration has seen that no device in its scope carries. Each row links to
// the new-device form with that alias already filled in (and the type, when the integration's
// scope is a single one), which is how an unknown sender becomes a device. The list is a set of
// observations: clearing it only forgets them, and a sender that is still sending is recorded
// again.

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Trash2 } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import { clearUnregisteredDevices, listUnregisteredDevices, type Integration } from '@/lib/api/integrations';
import { Button } from '@/components/ui/button';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { Pagination } from '@/components/ui/pagination';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import { formatTime } from '@/lib/utils';
import { newDeviceLink } from '@/routes/devices/deviceAliases';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

const pageSize = 50;

export function UnregisteredDevicesPanel({
  integration,
  canWrite,
}: {
  integration: Pick<Integration, 'token' | 'deviceTypes'>;
  canWrite: boolean;
}) {
  const { t } = useTranslation('integrations');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [pageNumber, setPageNumber] = useState(1);
  const [version, setVersion] = useState(0);
  const { data, loading, error } = useQuery(
    () => listUnregisteredDevices({ integration: integration.token, pageNumber, pageSize }),
    [integration.token, pageNumber, version],
  );
  // With one type in scope the new device can only be of that type, so it is chosen for the operator.
  const onlyType = integration.deviceTypes.length === 1 ? integration.deviceTypes[0].token : undefined;

  const clear = async () => {
    if (!(await confirm({ title: t('clearTitle'), description: t('clearConfirm'), confirmLabel: t('clearAction') }))) return;
    try {
      const n = await clearUnregisteredDevices(integration.token);
      toast(t('clearedToast', { count: n }));
      setPageNumber(1);
      setVersion((v) => v + 1);
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  // First load only: a re-read keeps the rows on screen.
  const body =
    loading && !data ? (
      <LoadingState description={t('unregisteredLoading')} />
    ) : error && !data ? (
      <ErrorState description={error} />
    ) : (data?.results.length ?? 0) === 0 ? (
      <p className="text-sm text-muted-foreground">{t('unregisteredEmpty')}</p>
    ) : (
      <>
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('colScheme')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colIdentifier')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colSeen')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colLastSeen')}</DataTableHeaderCell>
            <DataTableHeaderCell>{null}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {data!.results.map((u) => (
              <DataTableRow key={u.id}>
                <DataTableCell className="font-mono text-xs text-muted-foreground">{u.scheme}</DataTableCell>
                <DataTableCell className="font-mono text-xs text-foreground">{u.value}</DataTableCell>
                <DataTableCell className="tabular-nums text-muted-foreground">{u.count}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{formatTime(u.lastSeen)}</DataTableCell>
                <DataTableCell>
                  {canWrite && (
                    <Link
                      className="text-primary underline-offset-2 hover:underline"
                      to={newDeviceLink(u.scheme, u.value, onlyType)}
                    >
                      {t('createDeviceWithAlias')}
                    </Link>
                  )}
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
    );

  return (
    <SectionPanel
      title={t('unregisteredTitle')}
      description={t('unregisteredDescription')}
      action={
        canWrite && (data?.results.length ?? 0) > 0 ? (
          <Button variant="outline" size="sm" onClick={clear}>
            <Trash2 size={14} /> {t('clearAction')}
          </Button>
        ) : undefined
      }
    >
      {body}
    </SectionPanel>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarms raised on one device: the same rows the tenant alarm list shows, narrowed to this
// device, so an operator looking at a device can see what is wrong with it and acknowledge it
// without leaving the page. The tenant list is one link away for the rest.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { acknowledgeAlarm, listAlarms } from '@/lib/api/alarms';
import { useQuery } from '@/lib/hooks/use-query';
import { useMetricUnits } from '@/lib/hooks/use-metric-units';
import { errMessage, useReload } from '@/routes/common';
import { formatTime } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
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
import { AlarmSeverityBadge, AlarmStatusBadge } from '@/routes/alarms/badges';
import { formatAlarmValue } from '@/routes/alarms/alarmDisplay';

const pageSize = 10;
// The list refreshes on a timer: the live stream is a tenant-wide feed, and a device tab that
// opened its own subscription for ten rows would be more machinery than it is worth.
const REFRESH_MS = 30000;

export function DeviceAlarmsPanel({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation(['alarms', 'devices', 'common']);
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'alarm:write');
  const { toast } = useToast();
  const [pageNumber, setPageNumber] = useState(1);
  const [version, reload] = useReload();
  const [acting, setActing] = useState<string | null>(null);

  const { data, loading, error } = useQuery(
    () => listAlarms({ pageNumber, pageSize, originatorType: 'device', originatorToken: deviceToken }),
    [deviceToken, pageNumber, version],
  );
  useEffect(() => {
    const id = window.setInterval(reload, REFRESH_MS);
    return () => window.clearInterval(id);
  }, [reload]);
  useEffect(() => setPageNumber(1), [deviceToken]);

  const results = data?.results ?? [];
  const unitOf = useMetricUnits(results.length > 0 ? [deviceToken] : []);

  const acknowledge = async (token: string) => {
    if (acting) return;
    setActing(token);
    try {
      await acknowledgeAlarm(token);
      toast(t('alarms:alarmAcknowledged'));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setActing(null);
    }
  };

  if (!data && loading) return <LoadingState description={t('alarms:loading')} />;
  if (!data && error) return <ErrorState description={error} />;

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Link
          to="/alarms"
          className="text-sm text-primary underline-offset-2 hover:underline"
          data-shot="device-alarms-all"
        >
          {t('devices:alarmsViewAll')}
        </Link>
      </div>
      {results.length === 0 ? (
        <EmptyState description={t('devices:alarmsEmpty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('alarms:colSeverity')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('alarms:colAlarm')}</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">{t('common:colValue')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('alarms:colRaised')}</DataTableHeaderCell>
              {canWrite && <DataTableHeaderCell className="text-right">{t('common:colActions')}</DataTableHeaderCell>}
            </DataTableHead>
            <DataTableBody>
              {results.map((a) => (
                <DataTableRow key={a.id}>
                  <DataTableCell>
                    <AlarmSeverityBadge severity={a.severity} />
                  </DataTableCell>
                  <DataTableCell>
                    <AlarmStatusBadge state={a.state} acknowledged={a.acknowledged} />
                  </DataTableCell>
                  <DataTableCell>
                    <div className="font-medium text-foreground">{a.alarmKey}</div>
                    <div className="font-mono text-xs text-muted-foreground">{a.metricKey}</div>
                  </DataTableCell>
                  <DataTableCell className="text-right tabular-nums text-foreground">
                    {formatAlarmValue(a.lastValue, unitOf(deviceToken, a.metricKey))}
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(a.raisedTime)}
                  </DataTableCell>
                  {canWrite && (
                    <DataTableCell className="text-right">
                      {a.state === 'ACTIVE' && !a.acknowledged ? (
                        <Button
                          variant="outline"
                          size="sm"
                          loading={acting === a.token}
                          disabled={acting !== null}
                          onClick={() => acknowledge(a.token)}
                        >
                          {t('alarms:acknowledge')}
                        </Button>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </DataTableCell>
                  )}
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={pageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
          />
        </>
      )}
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { formatTime } from '@/lib/utils';
import { useAuth } from '@/auth/AuthProvider';
import { mayWrite } from '@/lib/scope';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import {
  getFailedEventSummary,
  listFailedEvents,
  type FailedEvent,
  type FailedEventReason,
} from '@/lib/api/ingest-failures';
import '@/i18n/lazy/ingestFailures';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { ToggleButton } from '@/components/ui/toggle-button';
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

// The reasons the API names, in the order the chips are offered. UNKNOWN is last: it stands for
// a reason this console has no wording for, and the row carries its number.
const REASONS: FailedEventReason[] = [
  'DEVICE_NOT_FOUND',
  'UNAUTHENTICATED',
  'NOT_GATEWAY_CHILD',
  'DEVICE_DISABLED',
  'INVALID',
  'API_CALL_FAILED',
  'INTEGRATION_ALIAS_MISMATCH',
  'DECODE_FAILED',
  'UNKNOWN',
];

// Time ranges, in hours. The index keeps records for a retention window an operator sets (7 days
// by default), so a range longer than that simply shows what is still held.
const RANGES = [
  { value: '1', hours: 1, labelKey: 'range1h' },
  { value: '24', hours: 24, labelKey: 'range24h' },
  { value: '168', hours: 168, labelKey: 'range7d' },
];

function sinceFor(hours: number): string {
  return new Date(Date.now() - hours * 3600_000).toISOString();
}

function IngestFailuresView() {
  const { t } = useTranslation('ingestFailures');
  const [pageNumber, setPageNumber] = useState(1);
  // The device page links here with the device it is about, and a profile's Decoder tab with the reason.
  const [params] = useSearchParams();
  const [reason, setReason] = useState<FailedEventReason | ''>(() => {
    const r = params.get('reason');
    return REASONS.find((x) => x === r) ?? '';
  });
  const [range, setRange] = useState('24');
  const [deviceInput, setDeviceInput] = useState(params.get('device') ?? '');
  const device = useDebouncedValue(deviceInput.trim(), 300);

  useEffect(() => setPageNumber(1), [reason, range, device]);

  const hours = RANGES.find((r) => r.value === range)?.hours ?? 24;

  const summary = useQuery(() => getFailedEventSummary(sinceFor(hours)), [hours]);
  const { data, loading, error } = useQuery(
    () =>
      listFailedEvents({
        pageNumber,
        pageSize,
        reason: reason || undefined,
        deviceToken: device || undefined,
        since: sinceFor(hours),
      }),
    [pageNumber, reason, device, hours],
  );

  const counts = useMemo(() => {
    const m = new Map<string, number>();
    for (const line of summary.data ?? []) m.set(line.reason, (m.get(line.reason) ?? 0) + line.records);
    return m;
  }, [summary.data]);
  const topDevices = useMemo(() => {
    const line = (summary.data ?? []).find((l) => l.reason === reason);
    return line ? line.devices.map((d) => `${d.deviceToken} (${d.records})`).join(', ') : '';
  }, [summary.data, reason]);

  const rangeOptions = RANGES.map((r) => ({ value: r.value, label: t(r.labelKey) }));
  const results: FailedEvent[] = data?.results ?? [];

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        <div className="flex items-center gap-2">
          <Combobox
            className="h-9 w-44"
            allowClear={false}
            value={range}
            onChange={setRange}
            options={rangeOptions}
          />
          <Input
            className="h-9 w-56"
            placeholder={t('deviceFilterPlaceholder')}
            data-shot="ingest-failures-device"
            value={deviceInput}
            onChange={(e) => setDeviceInput(e.target.value)}
          />
        </div>
      }
    >
      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label={t('colReason')}>
        <ToggleButton
          pressed={reason === ''}
          onClick={() => setReason('')}
          className="rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Badge variant={reason === '' ? 'default' : 'outline'}>{t('allReasons')}</Badge>
        </ToggleButton>
        {REASONS.map((r) => (
          <ToggleButton
            key={r}
            pressed={reason === r}
            title={t(`reasonHint_${r}`)}
            onClick={() => setReason(reason === r ? '' : r)}
            className="rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Badge variant={reason === r ? 'default' : 'outline'}>
              {t(`reason_${r}`)}
              {counts.has(r) ? ` · ${counts.get(r)}` : ''}
            </Badge>
          </ToggleButton>
        ))}
      </div>
      {reason && (
        <p className="mb-3 text-sm text-muted-foreground">
          {t(`reasonHint_${reason}`)}
          {topDevices ? ` ${t('topDevices', { devices: topDevices })}` : ''}
        </p>
      )}

      {loading && !data ? (
        <LoadingState description={t('loading')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colTime')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colReason')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colDevice')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colError')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colSuppressed')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((row) => (
                <DataTableRow key={row.id}>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(row.occurredTime)}
                  </DataTableCell>
                  <DataTableCell>
                    <Badge variant="secondary">{t(`reason_${row.reason}`)}</Badge>
                    {row.reason === 'UNKNOWN' && row.reasonCode !== 0 && (
                      <span className="ml-2 text-xs text-muted-foreground">
                        {t('reasonCode', { code: row.reasonCode })}
                      </span>
                    )}
                  </DataTableCell>
                  <DataTableCell className="font-mono text-xs text-foreground">
                    {row.overflow ? t('overflowRow') : (row.deviceToken ?? t('noDevice'))}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {row.error || '—'}
                    {row.payloadStatus === 'EXPIRED' && (
                      <span className="mt-1 block text-xs">{t('payloadExpired')}</span>
                    )}
                    {row.payloadStatus === 'NOT_RETAINED' && (
                      <span className="mt-1 block text-xs">{t('payloadNotRetained')}</span>
                    )}
                  </DataTableCell>
                  <DataTableCell>
                    {row.suppressed > 0 ? (
                      <Badge variant="outline" title={t('suppressedHint')}>
                        {t('suppressedBadge', { count: row.suppressed })}
                      </Badge>
                    ) : (
                      '—'
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
      )}
    </PageShell>
  );
}

/**
 * The page is for the members who manage devices (device:write), not the read-only baseline every
 * member holds, and never for a member limited to some customers. The server refuses the others
 * too; this keeps them from landing on an error, and the queries do not fire for them.
 */
export default function IngestFailuresPage() {
  const { t } = useTranslation('ingestFailures');
  const { claims } = useAuth();
  if (!mayWrite(claims, 'device:write')) {
    return (
      <PageShell title={t('title')}>
        <EmptyState description={t('notPermitted')} />
      </PageShell>
    );
  }
  return <IngestFailuresView />;
}

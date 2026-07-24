// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { useAlarmStream, type AlarmStreamStatus } from '@/lib/hooks/use-alarm-stream';
import {
  acknowledgeAlarm,
  clearAlarm,
  listAlarms,
  type Alarm,
  type AlarmEvent,
} from '@/lib/api/alarms';
import { errMessage, useReload } from '@/routes/common';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/ui/combobox';
import { useToast } from '@/components/ui/toast';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { cn } from '@/lib/utils';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { AlarmEventTypeBadge, AlarmSeverityBadge, AlarmStatusBadge } from './badges';

const pageSize = 25;

// Options carry a `labelKey` into the `alarms` catalog (ADR-066), resolved with t()
// inside the component; the `value` is the wire enum and never localized.
const STATE_OPTIONS = [
  { value: 'ACTIVE', labelKey: 'stateActive' },
  { value: 'CLEARED', labelKey: 'stateCleared' },
];

// Ordered by descending severity so the dropdown reads like the ramp (ADR-041).
const SEVERITY_OPTIONS = [
  { value: 'CRITICAL', labelKey: 'sevCritical' },
  { value: 'MAJOR', labelKey: 'sevMajor' },
  { value: 'MINOR', labelKey: 'sevMinor' },
  { value: 'WARNING', labelKey: 'sevWarning' },
  { value: 'INDETERMINATE', labelKey: 'sevIndeterminate' },
];

const ACK_OPTIONS = [
  { value: 'false', labelKey: 'ackUnacknowledged' },
  { value: 'true', labelKey: 'ackAcknowledged' },
];

// LiveIndicator shows the health of the alarm-events subscription so an operator
// trusts the list is current: a steady green dot when the feed is flowing, muted
// while connecting, amber when reconnecting (graphql-ws retries under the hood and
// the periodic reconcile keeps the list fresh regardless).
function LiveIndicator({ status }: { status: AlarmStreamStatus }) {
  const { t } = useTranslation('alarms');
  const map: Record<AlarmStreamStatus, { dot: string; labelKey: string }> = {
    live: { dot: 'bg-green-500', labelKey: 'live' },
    connecting: { dot: 'bg-muted-foreground', labelKey: 'connecting' },
    reconnecting: { dot: 'bg-amber-500', labelKey: 'reconnecting' },
  };
  const { dot, labelKey } = map[status];
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <span className={cn('inline-block size-2 rounded-full', dot, status === 'live' && 'animate-pulse')} />
      {t(labelKey)}
    </span>
  );
}

// The originator is addressed by (type, id); originatorToken resolves it to a
// friendly token for a device (ADR-013). Fall back to a "type #id" reference when
// unresolved (a non-device originator, or one since deleted).
function originatorLabel(a: Alarm | AlarmEvent): string {
  if (a.originatorToken) return a.originatorToken;
  return `${a.originatorType} #${a.originatorId}`;
}

export default function AlarmsPage() {
  const { t } = useTranslation('alarms');
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'alarm:write');
  const { toast } = useToast();
  // Localize the filter option labels here (the wire `value` stays as-is). Rebuilt
  // each render so they follow a live language switch.
  const stateOptions = STATE_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }));
  const severityOptions = SEVERITY_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }));
  const ackOptions = ACK_OPTIONS.map((o) => ({ value: o.value, label: t(o.labelKey) }));

  const [pageNumber, setPageNumber] = useState(1);
  const [state, setState] = useState('');
  const [severity, setSeverity] = useState('');
  const [ack, setAck] = useState('');
  const [version, reload] = useReload();
  // In-flight ack/clear actions, keyed `${token}:${action}` — a Set so one row's
  // action never disables another's, and a button spins only for its own action.
  const [acting, setActing] = useState<ReadonlySet<string>>(() => new Set());
  const inFlight = useRef<Set<string>>(new Set());
  const [lastEvent, setLastEvent] = useState<AlarmEvent | null>(null);

  const acknowledged = ack === '' ? undefined : ack === 'true';

  // Reset to the first page whenever a filter changes so the view never lands on an
  // out-of-range page.
  useEffect(() => setPageNumber(1), [state, severity, ack]);

  const { data, loading, error } = useQuery(
    () =>
      listAlarms({
        pageNumber,
        pageSize,
        state: state || undefined,
        severity: severity || undefined,
        acknowledged,
      }),
    [pageNumber, state, severity, ack, version],
  );

  // Debounced reconcile: a burst of live events coalesces into a single refetch. The
  // stream is best-effort (at-most-once), so a live event reconciles the query — the
  // source of truth — rather than being spliced into the paginated rows, which would
  // mean re-deriving the server's ordering, page boundaries, and filter membership on
  // the client.
  const reconcileTimer = useRef<number | null>(null);
  const scheduleReconcile = useCallback(() => {
    if (reconcileTimer.current != null) return;
    reconcileTimer.current = window.setTimeout(() => {
      reconcileTimer.current = null;
      reload();
    }, 1000);
  }, [reload]);
  useEffect(
    () => () => {
      if (reconcileTimer.current != null) window.clearTimeout(reconcileTimer.current);
    },
    [],
  );

  const onEvent = useCallback(
    (ev: AlarmEvent) => {
      setLastEvent(ev);
      scheduleReconcile();
    },
    [scheduleReconcile],
  );

  // Subscribe unfiltered (the stream carries only this tenant's transitions); on a
  // reconnect, reconcile the query to catch anything missed during the gap.
  const { status } = useAlarmStream(onEvent, reload);

  // Periodic reconcile backstop (the schema's own guidance for the at-most-once
  // stream): a dropped event with no later event to trigger a refetch would otherwise
  // leave a stale row — e.g. a missed CLEARED showing a ghost active alarm.
  useEffect(() => {
    const id = window.setInterval(reload, 30000);
    return () => window.clearInterval(id);
  }, [reload]);

  const rowBusy = (token: string) => acting.has(`${token}:ack`) || acting.has(`${token}:clear`);

  const act = async (token: string, action: 'ack' | 'clear') => {
    const key = `${token}:${action}`;
    // Synchronous single-flight guard: a second click landing before the disabled
    // state renders can't fire a duplicate mutation.
    if (inFlight.current.has(key)) return;
    inFlight.current.add(key);
    setActing((s) => new Set(s).add(key));
    try {
      if (action === 'ack') {
        await acknowledgeAlarm(token);
        toast(t('alarmAcknowledged'));
      } else {
        await clearAlarm(token);
        toast(t('alarmCleared'));
      }
      // Reconcile from the authoritative row immediately (the actor sees their action
      // land without waiting on the debounced stream reconcile).
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      inFlight.current.delete(key);
      setActing((s) => {
        const n = new Set(s);
        n.delete(key);
        return n;
      });
    }
  };

  const results: Alarm[] = data?.results ?? [];
  // Keep the table mounted through a background reconcile: useQuery preserves the
  // prior data while re-fetching, so gate the full-screen states on data absence, not
  // the loading flag — otherwise every live event or poll would blink the list to a
  // spinner.
  const showTable = data != null;

  return (
    <PageShell
      title={t('title')}
      banner="devices"
      description={
        <div className="mt-1 flex items-center gap-3">
          <span className="text-sm text-muted-foreground">{t('description')}</span>
          <LiveIndicator status={status} />
          {lastEvent && (
            <span className="hidden items-center gap-1.5 text-xs text-muted-foreground md:inline-flex">
              <AlarmEventTypeBadge eventType={lastEvent.eventType} />
              {originatorLabel(lastEvent)} · {lastEvent.alarmKey}
            </span>
          )}
        </div>
      }
      action={
        <div className="flex items-center gap-2">
          <Combobox
            className="h-9 w-36"
            placeholder={t('allStates')}
            value={state}
            onChange={setState}
            options={stateOptions}
          />
          <Combobox
            className="h-9 w-40"
            placeholder={t('allSeverities')}
            value={severity}
            onChange={setSeverity}
            options={severityOptions}
          />
          <Combobox
            className="h-9 w-44"
            placeholder={t('anyAck')}
            value={ack}
            onChange={setAck}
            options={ackOptions}
          />
        </div>
      }
    >
      {!showTable && loading ? (
        <LoadingState description={t('loading')} />
      ) : !showTable && error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colSeverity')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colOriginator')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colAlarm')}</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">{t('common:colValue')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colRaised')}</DataTableHeaderCell>
              {canWrite && (
                <DataTableHeaderCell className="text-right">
                  {t('common:colActions')}
                </DataTableHeaderCell>
              )}
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
                  <DataTableCell className="font-mono text-xs text-foreground">
                    {originatorLabel(a)}
                  </DataTableCell>
                  <DataTableCell>
                    <div className="font-medium text-foreground">{a.alarmKey}</div>
                    <div className="font-mono text-xs text-muted-foreground">{a.metricKey}</div>
                    {a.message && (
                      <div className="max-w-xs truncate text-xs text-muted-foreground" title={a.message}>
                        {a.message}
                      </div>
                    )}
                  </DataTableCell>
                  <DataTableCell className="text-right tabular-nums text-foreground">
                    {a.lastValue ?? '—'}
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(a.raisedTime)}
                  </DataTableCell>
                  {canWrite && (
                    <DataTableCell className="text-right">
                      {a.state === 'ACTIVE' ? (
                        <div className="flex justify-end gap-2">
                          {!a.acknowledged && (
                            <Button
                              variant="outline"
                              size="sm"
                              loading={acting.has(`${a.token}:ack`)}
                              disabled={rowBusy(a.token)}
                              onClick={() => act(a.token, 'ack')}
                            >
                              {t('acknowledge')}
                            </Button>
                          )}
                          <Button
                            variant="outline"
                            size="sm"
                            loading={acting.has(`${a.token}:clear`)}
                            disabled={rowBusy(a.token)}
                            onClick={() => act(a.token, 'clear')}
                          >
                            {t('clear')}
                          </Button>
                        </div>
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
            className="mt-4"
          />
        </>
      )}
    </PageShell>
  );
}

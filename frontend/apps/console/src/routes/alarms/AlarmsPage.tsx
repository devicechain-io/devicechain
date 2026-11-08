// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { formatTime } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { useAlarmStream, type AlarmStreamStatus } from '@/lib/hooks/use-alarm-stream';
import {
  acknowledgeAlarm,
  acknowledgeAlarms,
  clearAlarm,
  listAlarms,
  type Alarm,
  type AlarmEvent,
} from '@/lib/api/alarms';
import { errMessage, useReload } from '@/routes/common';
import { isoTime, type CsvColumn } from '@/lib/csv';
import { ExportCsvButton } from '@/components/ExportCsvButton';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/ui/combobox';
import { Checkbox } from '@/components/ui/checkbox';
import { useMetricUnits } from '@/lib/hooks/use-metric-units';
import { OriginatorLink, formatAlarmValue, originatorLabel } from './alarmDisplay';
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
// The query parameters the filters ride on.
const STATE_PARAM = 'state';
const SEVERITY_PARAM = 'severity';
const ACK_PARAM = 'ack';
const EXPORT_STEM = 'alarms';

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
  // The filters live in the address, so the home page can link here narrowed to a severity and
  // Back, Forward and a shared link all land on the same list. A value that is not one of the
  // choices reads as no filter.
  const [params, setParams] = useSearchParams();
  const fromAddress = (key: string, choices: string[]) => {
    const v = params.get(key) ?? '';
    return choices.includes(v) ? v : '';
  };
  const state = fromAddress(STATE_PARAM, STATE_OPTIONS.map((o) => o.value));
  const severity = fromAddress(SEVERITY_PARAM, SEVERITY_OPTIONS.map((o) => o.value));
  const ack = fromAddress(ACK_PARAM, ACK_OPTIONS.map((o) => o.value));
  const setFilter = (key: string) => (value: string) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (value) next.set(key, value);
        else next.delete(key);
        return next;
      },
      { replace: true },
    );
  const [version, reload] = useReload();
  // In-flight ack/clear actions, keyed `${token}:${action}` — a Set so one row's
  // action never disables another's, and a button spins only for its own action.
  const [acting, setActing] = useState<ReadonlySet<string>>(() => new Set());
  const inFlight = useRef<Set<string>>(new Set());
  const [lastEvent, setLastEvent] = useState<AlarmEvent | null>(null);
  // The alarms ticked for a bulk acknowledge, by token. Cleared whenever the list under them
  // changes (a filter, a page, an action), so a tick never outlives the row it was made on.
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [bulkBusy, setBulkBusy] = useState(false);

  const acknowledged = ack === '' ? undefined : ack === 'true';

  // Reset to the first page whenever a filter changes so the view never lands on an
  // out-of-range page.
  useEffect(() => setPageNumber(1), [state, severity, ack]);
  useEffect(() => setSelected(new Set()), [state, severity, ack, pageNumber]);

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

  // Only an active, unacknowledged alarm offers acknowledging; the rest are not selectable.
  const canAcknowledge = (a: Alarm) => a.state === 'ACTIVE' && !a.acknowledged;

  const acknowledgeSelected = async () => {
    if (bulkBusy || chosen.length === 0) return;
    setBulkBusy(true);
    try {
      const outcome = await acknowledgeAlarms(chosen);
      if (outcome.refusals.length > 0) {
        toast(t('bulkAcknowledgedPartly', { count: outcome.acknowledged, refused: outcome.refusals.length }), 'error');
      } else {
        toast(t('bulkAcknowledged', { count: outcome.acknowledged }));
      }
      setSelected(new Set());
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBulkBusy(false);
    }
  };

  // Wire values for the enums (stable for a spreadsheet filter), ISO-8601 for times.
  const exportColumns: CsvColumn<Alarm>[] = [
    { header: t('common:colToken'), value: (a) => a.token },
    { header: t('colSeverity'), value: (a) => a.severity },
    { header: t('common:colStatus'), value: (a) => a.state },
    { header: t('colAcknowledged'), value: (a) => a.acknowledged },
    { header: t('colOriginator'), value: (a) => originatorLabel(a) },
    { header: t('colAlarm'), value: (a) => a.alarmKey },
    { header: t('colMetric'), value: (a) => a.metricKey },
    { header: t('common:colValue'), value: (a) => a.lastValue },
    { header: t('colRaised'), value: (a) => isoTime(a.raisedTime) },
    { header: t('colCleared'), value: (a) => isoTime(a.clearedTime) },
  ];

  const results: Alarm[] = data?.results ?? [];
  const unitOf = useMetricUnits(results.map((a) => (a.originatorType.toLowerCase() === 'device' ? a.originatorToken : null)));
  const selectable = results.filter(canAcknowledge);
  // What the bar acts on is the ticks that still belong to an acknowledgeable row: a live
  // refresh may have acknowledged, cleared or dropped a ticked alarm since it was ticked.
  const chosen = selectable.filter((a) => selected.has(a.token)).map((a) => a.token);
  // A tick whose row has left the refreshed list is forgotten, not held in reserve: were the alarm
  // to come back on a later refresh it would rejoin a bulk acknowledge nobody asked it to join.
  useEffect(() => {
    setSelected((s) => {
      const keep = new Set(chosen);
      return keep.size === s.size ? s : keep;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);
  const allSelected = selectable.length > 0 && selectable.every((a) => selected.has(a.token));
  const toggleAll = (on: boolean) => setSelected(on ? new Set(selectable.map((a) => a.token)) : new Set());
  const toggleOne = (token: string, on: boolean) =>
    setSelected((s) => {
      const n = new Set(s);
      if (on) n.add(token);
      else n.delete(token);
      return n;
    });
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
            onChange={setFilter(STATE_PARAM)}
            options={stateOptions}
          />
          <Combobox
            className="h-9 w-40"
            placeholder={t('allSeverities')}
            value={severity}
            onChange={setFilter(SEVERITY_PARAM)}
            options={severityOptions}
          />
          <Combobox
            className="h-9 w-44"
            placeholder={t('anyAck')}
            value={ack}
            onChange={setFilter(ACK_PARAM)}
            options={ackOptions}
          />
          <ExportCsvButton
            fileStem={EXPORT_STEM}
            columns={exportColumns}
            scopeKey={`${state}|${severity}|${ack}`}
            fetchPage={(pageNumber, pageSize) =>
              listAlarms({
                pageNumber,
                pageSize,
                state: state || undefined,
                severity: severity || undefined,
                acknowledged,
              })
            }
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
          {canWrite && chosen.length > 0 && (
            <div className="mb-3 flex items-center gap-3 rounded-md border border-border bg-muted/40 px-3 py-2">
              <Button size="sm" loading={bulkBusy} onClick={acknowledgeSelected} data-shot="alarm-bulk-acknowledge">
                {t('acknowledgeSelected', { count: chosen.length })}
              </Button>
              <Button variant="ghost" size="sm" disabled={bulkBusy} onClick={() => setSelected(new Set())}>
                {t('clearSelection')}
              </Button>
            </div>
          )}
          <DataTable>
            <DataTableHead>
              {canWrite && (
                <DataTableHeaderCell className="w-8">
                  <Checkbox
                    checked={allSelected}
                    disabled={selectable.length === 0 || bulkBusy}
                    onCheckedChange={(v) => toggleAll(v === true)}
                    aria-label={t('selectAllAcknowledgeable')}
                  />
                </DataTableHeaderCell>
              )}
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
                  {canWrite && (
                    <DataTableCell>
                      <Checkbox
                        checked={selected.has(a.token)}
                        disabled={!canAcknowledge(a) || bulkBusy}
                        onCheckedChange={(v) => toggleOne(a.token, v === true)}
                        aria-label={t('selectAlarm', { alarm: a.alarmKey })}
                      />
                    </DataTableCell>
                  )}
                  <DataTableCell>
                    <AlarmSeverityBadge severity={a.severity} />
                  </DataTableCell>
                  <DataTableCell>
                    <AlarmStatusBadge state={a.state} acknowledged={a.acknowledged} />
                  </DataTableCell>
                  <DataTableCell className="font-mono text-xs text-foreground">
                    <OriginatorLink alarm={a} />
                  </DataTableCell>
                  <DataTableCell>
                    <div className="font-medium text-foreground">{a.alarmKey}</div>
                    <div className="font-mono text-xs text-muted-foreground">{a.metricKey}</div>
                  </DataTableCell>
                  <DataTableCell className="text-right tabular-nums text-foreground">
                    {/* The dashboard formatter, so an alarm reads like the metric does on a card:
                        a metric definition declares a unit but no precision, so the default
                        rounding applies (the CSV export above keeps the exact value). The unit is
                        the one the device's profile declares for the metric, when it can be read. */}
                    {formatAlarmValue(a.lastValue, unitOf(a.originatorToken, a.metricKey))}
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
                              data-shot="alarm-acknowledge"
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

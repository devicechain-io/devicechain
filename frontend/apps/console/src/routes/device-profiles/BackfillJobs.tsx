// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The backfill jobs of a profile: where each stands, how far it has got, what it wrote and what it
// skipped, and a cancel for one that is still active. While any job is active the list re-reads the
// server every few seconds; nothing is cached, so the counters are always the job's own.

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { errMessage } from '@/routes/common';
import { useQuery } from '@/lib/hooks/use-query';
import {
  cancelCalculatedMetricBackfill,
  listCalculatedMetricBackfills,
  type CalculatedMetricBackfillJob,
} from '@/lib/api/event-management';

/** How often an active job's counters are re-read. */
export const BACKFILL_POLL_MS = 5000;

const ACTIVE = ['QUEUED', 'RUNNING'];

// Skip reasons a formula reports, plus the two a backfill adds. Anything else is shown as sent.
const KNOWN_REASONS = [
  'missing_input',
  'invalid_input',
  'non_finite',
  'div_zero',
  'overflow',
  'range',
  'unstorable',
  'eval_error',
  'ambiguous',
  'unclassified',
];

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

const num = (n: number) => n.toLocaleString();

function statusVariant(status: string): 'success' | 'destructive' | 'warning' | 'secondary' {
  if (status === 'SUCCEEDED') return 'success';
  if (status === 'FAILED') return 'destructive';
  if (ACTIVE.includes(status)) return 'warning';
  return 'secondary';
}

export function BackfillJobs({
  profileToken,
  canCancel,
  reloadKey,
}: {
  profileToken: string;
  /** Whether the caller may cancel (device:write); the action is hidden without it. */
  canCancel: boolean;
  /** Bumped by the parent to re-read at once, e.g. right after a job is started. */
  reloadKey: number;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [tick, setTick] = useState(0);
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(() => new Set());
  const { data, loading, error } = useQuery(
    () => listCalculatedMetricBackfills(profileToken),
    [profileToken, reloadKey, tick],
  );

  const anyActive = (data ?? []).some((j) => ACTIVE.includes(j.status));
  useEffect(() => {
    if (!anyActive) return;
    const id = setInterval(() => setTick((n) => n + 1), BACKFILL_POLL_MS);
    return () => clearInterval(id);
  }, [anyActive]);

  const cancel = async (job: CalculatedMetricBackfillJob) => {
    if (
      !(await confirm({
        title: t('backfillCancel'),
        description: t('backfillCancelConfirm', { token: job.token }),
        confirmLabel: t('backfillCancel'),
        destructive: true,
      }))
    )
      return;
    setCancelling((s) => new Set(s).add(job.token));
    try {
      await cancelCalculatedMetricBackfill(job.token);
      toast(t('backfillCancelRequested'));
      setTick((n) => n + 1);
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setCancelling((s) => {
        const n = new Set(s);
        n.delete(job.token);
        return n;
      });
    }
  };

  if (loading && !data) return <LoadingState description={t('backfillJobsLoading')} />;
  if (error && !data) return <ErrorState description={error} />;
  const jobs = data ?? [];
  if (jobs.length === 0) {
    return (
      <p className="rounded-md border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
        {t('backfillJobsEmpty')}
      </p>
    );
  }

  return (
    <DataTable>
      <DataTableHead>
        <DataTableHeaderCell>{t('backfillColJob')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('backfillColStatus')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('backfillColProgress')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('backfillColCounts')}</DataTableHeaderCell>
        {canCancel && <DataTableHeaderCell className="text-right">{t('backfillColActions')}</DataTableHeaderCell>}
      </DataTableHead>
      <DataTableBody>
        {jobs.map((j) => {
          const active = ACTIVE.includes(j.status);
          const pct = j.progress == null ? null : Math.round(j.progress * 100);
          return (
            <DataTableRow key={j.token} data-testid={`bf-job-${j.token}`}>
              <DataTableCell>
                <div className="font-mono text-xs">{j.token}</div>
                <div className="text-xs text-muted-foreground">
                  {t('backfillJobVersion', { version: j.profileVersion })} · {j.metricKeys.join(', ')}
                </div>
                <div className="text-xs text-muted-foreground">
                  {fmtTime(j.from)} – {fmtTime(j.to)}
                </div>
                {j.warnings.map((w) => (
                  <div key={w} className="text-xs text-amber-600 dark:text-amber-500">
                    {w}
                  </div>
                ))}
              </DataTableCell>
              <DataTableCell>
                <Badge variant={statusVariant(j.status)}>{t(`backfillStatus.${j.status}`, j.status)}</Badge>
                {j.cancelRequestedAt && active && (
                  <div className="text-xs text-muted-foreground">{t('backfillCancelling')}</div>
                )}
                {j.failure && <div className="max-w-xs text-xs text-destructive">{j.failure}</div>}
              </DataTableCell>
              <DataTableCell className="tabular-nums">
                {pct == null ? (
                  <span className="text-muted-foreground">—</span>
                ) : (
                  <span>{t('backfillProgress', { pct })}</span>
                )}
                <div className="text-xs text-muted-foreground">
                  {j.devicesTotal == null
                    ? t('backfillDevicesDone', { done: num(j.devicesDone) })
                    : t('backfillDevicesOf', { done: num(j.devicesDone), total: num(j.devicesTotal) })}
                </div>
              </DataTableCell>
              <DataTableCell className="text-xs tabular-nums">
                <div>{t('backfillWritten', { count: num(j.written) })}</div>
                <div className="text-muted-foreground">
                  {t('backfillSamplesPresent', { samples: num(j.samples), present: num(j.alreadyPresent) })}
                </div>
                {j.skipped.map((s) => (
                  <div key={s.reason} className="text-muted-foreground">
                    {t('backfillSkipped', {
                      count: num(s.count),
                      reason: KNOWN_REASONS.includes(s.reason) ? t(`backfillSkip.${s.reason}`) : s.reason,
                    })}
                  </div>
                ))}
                {j.windowsCompressed > 0 && (
                  <div className="text-muted-foreground">{t('backfillCompressed', { count: num(j.windowsCompressed) })}</div>
                )}
                {j.windowsBeyondRetention > 0 && (
                  <div className="text-muted-foreground">
                    {t('backfillBeyondRetention', { count: num(j.windowsBeyondRetention) })}
                  </div>
                )}
              </DataTableCell>
              {canCancel && (
                <DataTableCell className="text-right">
                  {active && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => cancel(j)}
                      loading={cancelling.has(j.token)}
                      disabled={!!j.cancelRequestedAt}
                    >
                      {t('backfillCancel')}
                    </Button>
                  )}
                </DataTableCell>
              )}
            </DataTableRow>
          );
        })}
      </DataTableBody>
    </DataTable>
  );
}

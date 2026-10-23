// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// One group rollout: what it moves, to whom, how far it has got, and its controls.
//
// Every number after dispatch is the device's own update attempt, read through the assignment
// the rollout made; the page never derives an outcome of its own. The controls say what they do
// NOT do as plainly as what they do: pausing and aborting stop new devices being told, and
// leave the devices already updating to finish, because the platform cannot recall an
// installer. Each of those devices can still be cancelled on its own from its Updates tab.

import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { Pause, Play, Square, Rocket } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { CopyToken } from '@/components/ui/copy-token';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { HintText } from '@/components/ui/hint-text';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { useReload, errMessage } from '@/routes/common';
import { formatTime } from '@/lib/utils';
import { rolloutOutcomeLabel, updateStateLabel } from '@/lib/enumLabel';
import { useUpdateAccess } from '@/lib/updateAuthority';
import {
  abortRollout,
  getRollout,
  listRolloutTargets,
  pauseRollout,
  resumeRollout,
  startRollout,
  type UpdateRollout,
  type UpdateRolloutOutcome,
} from '@/lib/api/update-management';
import { Fact } from '@/routes/command-batches/Fact';
import { RolloutOutcomeBadge, RolloutProgressBar, RolloutStateBadge, pauseReasonText } from './rolloutBadges';

const targetsPageSize = 50;
// A running rollout moves on its own; the page re-reads it on this cadence while it does.
const RUNNING_REFRESH_MS = 10_000;
const OUTCOMES: UpdateRolloutOutcome[] = ['PENDING', 'IN_FLIGHT', 'SUCCEEDED', 'FAILED', 'UNKNOWN', 'CANCELLED', 'SKIPPED'];

export default function RolloutDetailPage() {
  const { t } = useTranslation('updates');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';
  const [reloadKey, reload] = useReload();
  const query = useQuery(() => getRollout(token), [token, reloadKey]);
  // The sticky display value may be another rollout's while this one loads: never show it here.
  const data = query.data && query.data.token === token ? query.data : null;
  // The controls act on the rollout's state, so they are live only while the answer on screen is
  // the settled answer for this rollout: not while a re-read after an action (or the refresh of a
  // running rollout) is in flight, when the sticky value may show a state that no longer holds.
  const actionable = query.current != null && query.current.token === token;

  const running = data?.state === 'RUNNING';
  useEffect(() => {
    if (!running) return;
    const id = setInterval(reload, RUNNING_REFRESH_MS);
    return () => clearInterval(id);
  }, [running, reload]);

  if (query.loading && !data) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description={t('loadingRollout')} />
      </PageShell>
    );
  }
  if (query.error && !data) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={query.error} />
      </PageShell>
    );
  }
  if (!data) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={t('rolloutNotFound', { token })} />
      </PageShell>
    );
  }
  return (
    <RolloutDetail key={data.token} rollout={data} actionable={actionable} reloadKey={reloadKey} onChanged={reload} />
  );
}

function RolloutDetail({
  rollout,
  actionable,
  reloadKey,
  onChanged,
}: {
  rollout: UpdateRollout;
  actionable: boolean;
  reloadKey: number;
  onChanged: () => void;
}) {
  const { t } = useTranslation('updates');
  const { canWrite } = useUpdateAccess();
  const paused = pauseReasonText(t, rollout);
  const [error, setError] = useState<string | null>(null);

  return (
    <PageShell
      title={rollout.name}
      titleAdornment={<CopyToken value={rollout.token} />}
      banner="devices"
      action={canWrite ? <RolloutControls rollout={rollout} actionable={actionable} onChanged={onChanged} onError={setError} /> : undefined}
      description={
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <RolloutStateBadge state={rollout.state} />
          {paused && <span className="text-sm text-muted-foreground">{paused}</span>}
        </div>
      }
    >
      <div className="space-y-6">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        {canWrite && rollout.state === 'PAUSED' && rollout.pauseReason === 'FAILURE_THRESHOLD' && (
          <RaiseThresholdPanel rollout={rollout} actionable={actionable} onChanged={onChanged} />
        )}
        <SummaryPanel rollout={rollout} />
        {rollout.state !== 'DRAFT' && <TargetsPanel rollout={rollout} reloadKey={reloadKey} />}
      </div>
    </PageShell>
  );
}

function RolloutControls({
  rollout,
  actionable,
  onChanged,
  onError,
}: {
  rollout: UpdateRollout;
  actionable: boolean;
  onChanged: () => void;
  onError: (message: string | null) => void;
}) {
  const { t } = useTranslation('updates');
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);

  const act = async (run: () => Promise<unknown>) => {
    setBusy(true);
    onError(null);
    try {
      await run();
    } catch (err) {
      onError(errMessage(err));
    } finally {
      setBusy(false);
      onChanged();
    }
  };

  const start = async () => {
    const ok = await confirm({
      title: t('rolloutStartConfirmTitle', { name: rollout.name }),
      description: t('rolloutStartConfirmBody', { group: rollout.groupToken, cap: rollout.maxInFlight }),
      confirmLabel: t('rolloutStart'),
      destructive: false,
    });
    if (ok) await act(() => startRollout(rollout.token));
  };
  const pause = () => act(() => pauseRollout(rollout.token));
  const resume = () => act(() => resumeRollout({ token: rollout.token }));
  const abort = async () => {
    const ok = await confirm({
      title: t('rolloutAbortConfirmTitle', { name: rollout.name }),
      // The in-flight count is the one on screen when the dialog opened, and is stated as such.
      description: t('rolloutAbortConfirmBody', { inFlight: rollout.progress.inFlight + rollout.progress.unknown }),
      confirmLabel: t('rolloutAbort'),
    });
    if (ok) await act(() => abortRollout(rollout.token));
  };

  const threshold = rollout.state === 'PAUSED' && rollout.pauseReason === 'FAILURE_THRESHOLD';
  return (
    <div className="flex items-center gap-2">
      {rollout.state === 'DRAFT' && (
        <Button onClick={start} loading={busy} disabled={busy || !actionable}>
          <Rocket size={14} /> {t('rolloutStart')}
        </Button>
      )}
      {rollout.state === 'RUNNING' && (
        <Button variant="outline" onClick={pause} loading={busy} disabled={busy || !actionable}>
          <Pause size={14} /> {t('rolloutPause')}
        </Button>
      )}
      {rollout.state === 'PAUSED' && !threshold && (
        <Button onClick={resume} loading={busy} disabled={busy || !actionable}>
          <Play size={14} /> {t('rolloutResume')}
        </Button>
      )}
      {(rollout.state === 'DRAFT' || rollout.state === 'RUNNING' || rollout.state === 'PAUSED') && (
        <Button variant="destructive" onClick={abort} loading={busy} disabled={busy || !actionable}>
          <Square size={14} /> {t('rolloutAbort')}
        </Button>
      )}
    </div>
  );
}

// A rollout that paused at its failure threshold resumes only with a HIGHER threshold: the
// service refuses to reset the count, and this panel asks for the new number rather than
// offering a Resume that would be refused.
function RaiseThresholdPanel({
  rollout,
  actionable,
  onChanged,
}: {
  rollout: UpdateRollout;
  actionable: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('updates');
  const failures = rollout.progress.failed + rollout.progress.unknown;
  const [value, setValue] = useState(String(failures + 1));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const n = /^\d+$/.test(value.trim()) ? Number(value.trim()) : NaN;
  const ok = Number.isSafeInteger(n) && n > failures;

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await resumeRollout({ token: rollout.token, failureThreshold: n });
      onChanged();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('rolloutThresholdPanelTitle')}>
      <HintText size="md">
        {t('rolloutThresholdPanelBody', { failures, threshold: rollout.failureThreshold ?? 0 })}
      </HintText>
      <div className="mt-3 flex flex-wrap items-end gap-2">
        <FormField label={t('rolloutFieldThreshold')} htmlFor="rollout-new-threshold">
          <Input
            id="rollout-new-threshold"
            className="w-32"
            inputMode="numeric"
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        </FormField>
        <Button onClick={submit} loading={busy} disabled={!ok || busy || !actionable}>
          <Play size={14} /> {t('rolloutResumeWithThreshold')}
        </Button>
      </div>
      {!ok && <p className="mt-1 text-sm text-destructive">{t('rolloutThresholdTooLow', { failures })}</p>}
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
    </SectionPanel>
  );
}

function SummaryPanel({ rollout }: { rollout: UpdateRollout }) {
  const { t } = useTranslation('updates');
  const p = rollout.progress;
  return (
    <SectionPanel title={t('rolloutSummaryTitle')}>
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Fact label={t('colArtifact')}>
          <Link className="underline" to={`/updates/artifacts/${encodeURIComponent(rollout.artifact.token)}`}>
            {rollout.artifact.name} {rollout.artifact.version}
          </Link>
        </Fact>
        <Fact label={t('colGroup')}>
          {rollout.groupVersion == null
            ? rollout.groupToken
            : t('rolloutGroupAtVersion', { group: rollout.groupToken, version: rollout.groupVersion })}
        </Fact>
        <Fact label={t('colMaxInFlight')}>{rollout.maxInFlight}</Fact>
        <Fact label={t('rolloutFieldThreshold')}>{rollout.failureThreshold ?? t('rolloutNoThreshold')}</Fact>
        <Fact label={t('rolloutCreated')}>
          {formatTime(rollout.createdAt)}
          {rollout.createdBy ? ` · ${rollout.createdBy}` : ''}
        </Fact>
        <Fact label={t('rolloutStarted')}>{rollout.startedAt ? formatTime(rollout.startedAt) : '—'}</Fact>
        <Fact label={t('rolloutFinished')}>{rollout.finishedAt ? formatTime(rollout.finishedAt) : '—'}</Fact>
        <Fact label={t('rolloutLastChange')}>
          {rollout.stateChangedAt ? formatTime(rollout.stateChangedAt) : '—'}
          {rollout.stateChangedAt ? ` · ${rollout.stateChangedBy ?? t('rolloutChangedBySelf')}` : ''}
        </Fact>
      </dl>
      {rollout.state === 'DRAFT' ? (
        <HintText size="md" className="mt-4">{t('rolloutDraftHint')}</HintText>
      ) : (
        <>
          <RolloutProgressBar progress={p} className="mt-4" />
          <dl className="mt-4 grid gap-4 sm:grid-cols-4 lg:grid-cols-8" data-testid="rollout-counts">
            <Fact label={t('rolloutTargets')}>{p.total}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'PENDING')}>{p.pending}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'IN_FLIGHT')}>{p.inFlight}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'SUCCEEDED')}>{p.succeeded}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'FAILED')}>{p.failed}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'UNKNOWN')}>{p.unknown}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'CANCELLED')}>{p.cancelled}</Fact>
            <Fact label={rolloutOutcomeLabel(t, 'SKIPPED')}>{p.skipped}</Fact>
          </dl>
          {(rollout.state === 'PAUSED' || rollout.state === 'ABORTED') && p.inFlight + p.unknown > 0 && (
            <HintText size="md" className="mt-3">
              {t('rolloutStillUpdating', { count: p.inFlight + p.unknown })}
            </HintText>
          )}
        </>
      )}
    </SectionPanel>
  );
}

function TargetsPanel({ rollout, reloadKey }: { rollout: UpdateRollout; reloadKey: number }) {
  const { t } = useTranslation('updates');
  const [pageNumber, setPageNumber] = useState(1);
  const [outcome, setOutcome] = useState('');
  useEffect(() => setPageNumber(1), [outcome]);
  const { data, loading, error } = useQuery(
    () =>
      listRolloutTargets(rollout.token, {
        pageNumber,
        pageSize: targetsPageSize,
        outcome: (outcome || undefined) as UpdateRolloutOutcome | undefined,
      }),
    [rollout.token, pageNumber, outcome, reloadKey],
  );

  return (
    <SectionPanel
      title={t('rolloutTargetsTitle')}
      action={
        <Combobox
          className="h-9 w-44"
          options={OUTCOMES.map((o) => ({ value: o, label: rolloutOutcomeLabel(t, o) }))}
          value={outcome}
          onChange={setOutcome}
          placeholder={t('rolloutAnyOutcome')}
          ariaLabel={t('rolloutOutcomeFilterLabel')}
        />
      }
    >
      {loading && !data ? (
        <LoadingState description={t('loadingRolloutTargets')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : (data?.results.length ?? 0) === 0 ? (
        <EmptyState description={t('emptyRolloutTargets')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colDevice')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colOutcome')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colUpdateState')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colDispatched')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colReason')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {data!.results.map((r) => (
                <DataTableRow key={r.deviceToken}>
                  <DataTableCell className="font-mono text-foreground">
                    <Link className="underline" to={`/devices/${encodeURIComponent(r.deviceToken)}`}>
                      {r.deviceToken}
                    </Link>
                  </DataTableCell>
                  <DataTableCell>
                    <RolloutOutcomeBadge outcome={r.outcome} />
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {r.attemptState ? updateStateLabel(t, r.attemptState) : '—'}
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {r.dispatchedAt ? formatTime(r.dispatchedAt) : '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {r.skipCode ? <span title={r.skipDetail ?? undefined}>{r.skipCode}</span> : '—'}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={targetsPageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
            className="mt-4"
          />
        </>
      )}
    </SectionPanel>
  );
}

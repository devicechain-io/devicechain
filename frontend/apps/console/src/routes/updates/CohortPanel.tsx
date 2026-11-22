// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A staged rollout's cohort plan, where it stands, and the operator's decisions at a cohort gate.
//
// 🔴 EVERY DECISION NAMES THE COHORT THE OPERATOR IS LOOKING AT. Promote sends the viewed
// `currentCohort` as `expectedCohort`, so a stale tab, or a second operator who already promoted,
// is refused by the service rather than advancing the rollout a second time.
//
// 🔴 ACCEPTING FAILURES IS NEVER ONE CLICK. Promoting out of a COHORT_FAILED pause sets
// `overrideFailure`, which waves failed or unknown devices through, so it goes through a confirm
// that says so. Failure-count and failure-rate halts are NOT overridden by it; a rate halt has its
// own explicit, numbered override (`haltSequence`) so a stale view cannot override a halt it never saw.

import { useState } from 'react';
import { ChevronsRight, Play } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { SectionPanel } from '@/components/ui/section-panel';
import { HintText } from '@/components/ui/hint-text';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { errMessage } from '@/routes/common';
import { rolloutPauseReasonLabel } from '@/lib/enumLabel';
import { promoteRollout, resumeHaltedRollout, type UpdateRollout } from '@/lib/api/update-management';
import '@/i18n/lazy/updates';

type CohortStatus = 'done' | 'current' | 'upcoming';

function statusOf(rollout: UpdateRollout, index: number): CohortStatus {
  if (rollout.state === 'DRAFT') return 'upcoming';
  if (rollout.state === 'COMPLETED' || index < rollout.currentCohort) return 'done';
  return index === rollout.currentCohort ? 'current' : 'upcoming';
}

export function CohortPanel({
  rollout,
  canWrite,
  actionable,
  onChanged,
}: {
  rollout: UpdateRollout;
  canWrite: boolean;
  actionable: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('updates');
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const cohorts = rollout.cohorts;
  const paused = rollout.state === 'PAUSED';
  const reason = paused ? rollout.pauseReason : null;
  const viewed = rollout.currentCohort;
  const current = cohorts[viewed];
  const isLast = viewed >= cohorts.length - 1;

  const act = async (run: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await run();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
      onChanged();
    }
  };

  const promote = () => act(() => promoteRollout({ token: rollout.token, expectedCohort: viewed }));
  const promoteOverride = async () => {
    const ok = await confirm({
      title: t('cohortOverrideConfirmTitle', { cohort: current?.name ?? viewed + 1 }),
      description: t('cohortOverrideConfirmBody', {
        failures: rollout.progress.failed + rollout.progress.unknown,
      }),
      confirmLabel: t('cohortPromoteAnyway'),
    });
    if (ok) await act(() => promoteRollout({ token: rollout.token, expectedCohort: viewed, overrideFailure: true }));
  };
  const overrideHalt = async () => {
    const ok = await confirm({
      title: t('cohortHaltConfirmTitle'),
      description: t('cohortHaltConfirmBody', {
        failures: rollout.haltFailures,
        samples: rollout.haltSamples,
        percentage: rollout.failureRate?.percentage ?? 0,
      }),
      confirmLabel: t('cohortHaltOverride'),
    });
    if (ok) await act(() => resumeHaltedRollout({ token: rollout.token, haltSequence: rollout.haltSequence }));
  };

  const disabled = busy || !actionable;
  return (
    <SectionPanel title={t('cohortPanelTitle')}>
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      {rollout.state !== 'DRAFT' && rollout.state !== 'COMPLETED' && current && (
        <p className="mb-3 text-sm" data-testid="cohort-current">
          {t('cohortCurrent', { position: viewed + 1, total: cohorts.length, name: current.name })}
          {reason && <span className="ml-2 text-muted-foreground">{rolloutPauseReasonLabel(t, reason)}</span>}
        </p>
      )}
      {cohorts.length > 0 && (
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('cohortColNumber')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('cohortColName')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('cohortColSize')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('cohortColPromotion')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('cohortColStatus')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {cohorts.map((c, i) => {
            const status = statusOf(rollout, i);
            return (
              <DataTableRow key={c.name}>
                <DataTableCell className="text-muted-foreground">{i + 1}</DataTableCell>
                <DataTableCell className="text-foreground">{c.name}</DataTableCell>
                <DataTableCell className="text-muted-foreground">
                  {c.deviceTokens.length > 0
                    ? t('cohortSizeDevices', { count: c.deviceTokens.length })
                    : t('cohortSizePercent', { percentage: c.percentage })}
                </DataTableCell>
                <DataTableCell className="text-muted-foreground">
                  {c.manualPromotion ? t('cohortPromotionManual') : t('cohortPromotionAuto')}
                </DataTableCell>
                <DataTableCell>
                  <Badge variant={status === 'done' ? 'success' : status === 'current' ? 'secondary' : 'outline'}>
                    {t(`cohortStatus_${status}`)}
                  </Badge>
                </DataTableCell>
              </DataTableRow>
            );
          })}
        </DataTableBody>
      </DataTable>
      )}
      {cohorts.length > 0 && (
        <HintText size="md" className="mt-3">
          {t('cohortProgressHint')}
        </HintText>
      )}

      {canWrite && paused && reason === 'COHORT_READY' && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button onClick={promote} loading={busy} disabled={disabled}>
            <ChevronsRight size={14} /> {t('cohortPromote')}
          </Button>
          <HintText size="md">{isLast ? t('cohortReadyLastHint') : t('cohortReadyHint', { name: current?.name ?? '' })}</HintText>
        </div>
      )}
      {canWrite && paused && reason === 'COHORT_FAILED' && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button variant="destructive" onClick={promoteOverride} loading={busy} disabled={disabled}>
            <ChevronsRight size={14} /> {t('cohortPromoteAnyway')}
          </Button>
          <HintText size="md">{t('cohortFailedHint')}</HintText>
        </div>
      )}
      {canWrite && paused && reason === 'FAILURE_RATE' && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button variant="destructive" onClick={overrideHalt} loading={busy} disabled={disabled}>
            <Play size={14} /> {t('cohortHaltOverride')}
          </Button>
          <HintText size="md">
            {t('cohortHaltHint', {
              failures: rollout.haltFailures,
              samples: rollout.haltSamples,
              percentage: rollout.failureRate?.percentage ?? 0,
            })}
          </HintText>
        </div>
      )}
    </SectionPanel>
  );
}

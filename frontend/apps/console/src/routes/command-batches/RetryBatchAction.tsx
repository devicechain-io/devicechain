// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// RETRY: send the same command again to the devices of a finished fleet write that did not
// get it done, as a NEW batch linked to the original.
//
// 🔴 THE PANEL SORTS TARGETS BY WHAT PHYSICALLY HAPPENED, not by status:
//   • never delivered — the platform proved the command did not reach the device. Safe.
//   • device rejected — the device received it and said no. A deliberate re-ask.
//   • refused when the batch fired — offered only when the batch record holds that refusal
//     in full (it keeps a bounded sample per code), and not preselected.
//   • outcome unknown — the command may already have reached the device and acted. Never
//     preselected, and not selectable at all until the operator ticks an explicit
//     acknowledgement; the server independently refuses these without `includeUnknown`.
// In-flight commands are counted but cannot be selected: retrying one would send it twice.
//
// The selection rules live in ./retrySelection.ts and are unit-tested; this file renders them.

import { useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { RotateCw } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { SectionPanel } from '@/components/ui/section-panel';
import { ErrorBanner } from '@/components/ui/error-banner';
import { HintText } from '@/components/ui/hint-text';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import {
  countBatchCommands,
  listUnsuccessfulBatchCommands,
  retryCommandBatch,
  type CommandBatch,
  type CommandBatchRetryRejection,
} from '@/lib/api/command-delivery';
import {
  RETRY_CLASSES,
  buildCandidates,
  defaultSelection,
  planSubmission,
  type RetryCandidate,
  type RetryClass,
} from './retrySelection';

// The states in which a command is still in flight: counted, never selectable.
const IN_FLIGHT = ['QUEUED', 'HELD', 'SENT', 'PARKED'];

// Rows shown per class before "and N more". Selection by class covers every row either way.
const ROWS_SHOWN = 100;

export interface BatchRetryState {
  canRetry: boolean;
  open: boolean;
  toggle: () => void;
}

export function useBatchRetry(): BatchRetryState {
  const { claims } = useAuth();
  const [open, setOpen] = useState(false);
  return {
    canRetry: hasAuthority(claims, 'command:write'),
    open,
    toggle: () => setOpen((o) => !o),
  };
}

export function RetryBatchButton({ state }: { state: BatchRetryState }) {
  const { t } = useTranslation('commandBatches');
  if (!state.canRetry) return null;
  return (
    <Button variant="outline" onClick={state.toggle} data-shot="batch-retry">
      <RotateCw size={14} /> {t('retryAction')}
    </Button>
  );
}

export function RetryBatchPanel({
  batch,
  state,
  reloadKey,
}: {
  batch: CommandBatch;
  state: BatchRetryState;
  reloadKey: number;
}): ReactNode {
  if (!state.canRetry || !state.open) return null;
  return <RetryBatchPanelBody batch={batch} reloadKey={reloadKey} />;
}

function RetryBatchPanelBody({ batch, reloadKey }: { batch: CommandBatch; reloadKey: number }) {
  const { t } = useTranslation('commandBatches');
  const navigate = useNavigate();
  const unsuccessful = useQuery(
    () => listUnsuccessfulBatchCommands(batch.token),
    [batch.token, reloadKey],
  );
  const inFlight = useQuery(
    () => countBatchCommands({ batchToken: batch.token, statuses: IN_FLIGHT }),
    [batch.token, reloadKey],
  );

  // `current`, not `data`: the retry is built FROM these rows, so after a reload (a cancel
  // changes which commands are unsuccessful) the form must not offer the previous answer, nor
  // keep offering it after the reload failed.
  const candidates = useMemo(
    () =>
      unsuccessful.current
        ? buildCandidates(unsuccessful.current.commands, batch.refusals, batch.refusalCounts)
        : [],
    [unsuccessful.current, batch.refusals, batch.refusalCounts],
  );

  if (unsuccessful.error) {
    return (
      <SectionPanel title={t('retryTitle')}>
        <ErrorState description={unsuccessful.error} />
      </SectionPanel>
    );
  }
  if (!unsuccessful.current) {
    return (
      <SectionPanel title={t('retryTitle')}>
        <LoadingState description={t('retryLoading')} />
      </SectionPanel>
    );
  }
  return (
    <RetryForm
      // Re-keyed on the candidate set so a refresh resets selection to its defaults rather
      // than carrying choices over a list that has changed under them.
      key={candidates.map((c) => c.deviceToken).join('|')}
      batch={batch}
      candidates={candidates}
      truncated={unsuccessful.current.truncated}
      inFlight={inFlight.current ?? null}
      onRetried={(token) => navigate(`/command-batches/${encodeURIComponent(token)}`)}
    />
  );
}

function RetryForm({
  batch,
  candidates,
  truncated,
  inFlight,
  onRetried,
}: {
  batch: CommandBatch;
  candidates: RetryCandidate[];
  truncated: boolean;
  inFlight: number | null;
  onRetried: (token: string) => void;
}) {
  const { t } = useTranslation('commandBatches');
  const [selected, setSelected] = useState<Set<string>>(() => defaultSelection(candidates));
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [rejection, setRejection] = useState<CommandBatchRetryRejection | null>(null);
  const inFlightGuard = useRef(false);
  // One token per distinct plan: re-submitting the SAME plan after a lost answer replays the
  // retry instead of sending twice, while a changed plan is a different request and gets its
  // own token.
  const tokenFor = useRef<{ key: string; token: string } | null>(null);

  const plan = planSubmission(candidates, selected, acknowledged);
  const byClass = (cls: RetryClass) => candidates.filter((c) => c.cls === cls);

  const toggleDevice = (device: string, on: boolean) =>
    setSelected((s) => {
      const next = new Set(s);
      if (on) next.add(device);
      else next.delete(device);
      return next;
    });
  const toggleClass = (cls: RetryClass, on: boolean) =>
    setSelected((s) => {
      const next = new Set(s);
      for (const c of byClass(cls)) {
        if (on) next.add(c.deviceToken);
        else next.delete(c.deviceToken);
      }
      return next;
    });

  const submit = async () => {
    if (inFlightGuard.current || plan.deviceTokens.length === 0) return;
    inFlightGuard.current = true;
    setBusy(true);
    setError(null);
    setRejection(null);
    const key = JSON.stringify(plan);
    if (tokenFor.current?.key !== key) {
      tokenFor.current = { key, token: crypto.randomUUID() };
    }
    try {
      const result = await retryCommandBatch({
        token: tokenFor.current.token,
        of: batch.token,
        deviceTokens: plan.deviceTokens,
        includeUnknown: plan.includeUnknown,
        // Devices that cannot receive the command now (disabled since, or out of the
        // vocabulary) are skipped and recorded on the retry rather than failing the rest.
        allowPartial: true,
      });
      if (result.rejection) {
        setRejection(result.rejection);
      } else if (result.batch) {
        onRetried(result.batch.token);
      } else {
        // Neither arm: whether a retry exists is not known. The token is kept, so pressing
        // again replays rather than duplicates.
        setError(t('retryUnreadable'));
      }
    } catch (err) {
      setError(errMessage(err));
    } finally {
      inFlightGuard.current = false;
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('retryTitle')} description={t('retryDescription', { name: batch.name })}>
      {candidates.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('retryNothing')}</p>
      ) : (
        <div className="space-y-5">
          {RETRY_CLASSES.map((cls) => {
            const rows = byClass(cls);
            if (rows.length === 0) return null;
            const unknown = cls === 'UNKNOWN';
            const locked = unknown && !acknowledged;
            const allOn = rows.every((c) => selected.has(c.deviceToken));
            return (
              <fieldset key={cls} className="space-y-2" data-retry-class={cls}>
                <legend className="text-sm font-medium text-foreground">
                  {t(`retryClass_${cls}`, { count: rows.length })}
                </legend>
                <HintText size="md">{t(`retryClassHint_${cls}`)}</HintText>
                {unknown && (
                  <label className="flex items-start gap-2 text-sm text-destructive">
                    <Checkbox
                      checked={acknowledged}
                      onCheckedChange={(v) => setAcknowledged(v === true)}
                      aria-label={t('retryAcknowledge')}
                      className="mt-0.5"
                    />
                    <span>{t('retryAcknowledge')}</span>
                  </label>
                )}
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={allOn && !locked}
                    disabled={locked}
                    onCheckedChange={(v) => toggleClass(cls, v === true)}
                    aria-label={t('retrySelectAll', { count: rows.length })}
                  />
                  <span>{t('retrySelectAll', { count: rows.length })}</span>
                </label>
                <ul className="max-h-56 space-y-1 overflow-auto pl-6">
                  {rows.slice(0, ROWS_SHOWN).map((c) => (
                    <li key={c.deviceToken}>
                      <label className="flex items-center gap-2 text-sm">
                        <Checkbox
                          checked={selected.has(c.deviceToken) && !locked}
                          disabled={locked}
                          onCheckedChange={(v) => toggleDevice(c.deviceToken, v === true)}
                          aria-label={c.deviceToken}
                        />
                        <span className="font-mono text-foreground">{c.deviceToken}</span>
                        {c.detail && (
                          <span className="truncate text-muted-foreground">{c.detail}</span>
                        )}
                      </label>
                    </li>
                  ))}
                  {rows.length > ROWS_SHOWN && (
                    <li className="text-xs text-muted-foreground">
                      {t('retryMoreRows', { count: rows.length - ROWS_SHOWN })}
                    </li>
                  )}
                </ul>
              </fieldset>
            );
          })}
        </div>
      )}

      {inFlight != null && inFlight > 0 && (
        <HintText size="md" className="mt-3">
          {t('retryInFlight', { count: inFlight })}
        </HintText>
      )}
      {truncated && <HintText size="md" className="mt-1">{t('retryTruncated')}</HintText>}

      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      {rejection && (
        <div className="mt-3 space-y-1" role="alert">
          <p className="text-sm text-destructive">
            {t('retryRefused', { code: rejection.code, reason: rejection.reason })}
          </p>
          {rejection.refusals.length > 0 && (
            <ul className="pl-4 text-sm text-muted-foreground">
              {rejection.refusals.slice(0, ROWS_SHOWN).map((r) => (
                <li key={r.deviceToken}>
                  <span className="font-mono">{r.deviceToken}</span> — {r.code}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div className="mt-4 flex items-center gap-3">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || plan.deviceTokens.length === 0}
          data-shot="batch-retry-submit"
        >
          <RotateCw size={14} /> {t('retrySubmit', { count: plan.deviceTokens.length })}
        </Button>
        <HintText size="md">{t('retrySubmitHint')}</HintText>
      </div>
    </SectionPanel>
  );
}

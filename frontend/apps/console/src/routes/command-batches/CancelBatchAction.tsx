// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// CANCEL: call off a fleet write, and report honestly what that did.
//
// 🔴 THE REPORT IS THE FEATURE. `cancelCommandBatch` never refuses — a brake that declined
// to engage because part of the fleet had already moved would leave the rest of the fleet
// commanded — so there is no error path to render and no verdict to relay. There are four
// numbers, and the only way this screen can be wrong is by rendering them in a way that
// reads better than the truth:
//
//   • `alreadySent` MUST NEVER READ AS "STOPPED". Those commands are at their devices and
//     THOSE DEVICES WILL STILL ACT ON THEM. A screen that adds them into one "1,000
//     cancelled" headline tells an operator the fleet was stopped while 300 of it is about
//     to actuate hardware. That is the worst thing this file could do, so `alreadySent` is
//     labelled by what it is and gets its own sentence saying the devices will still act.
//
//   • A SHORTFALL AGAINST `matched` IS AN INSTRUCTION, NOT A DISCREPANCY. Commands that
//     went back into the delivery queue between the write and the count are in none of the
//     three buckets — this cancel did not stop them, and nothing retries on the operator's
//     behalf. So the panel says "cancel again" and puts the button to do it right there.
//
//   • IT IS A PANEL, NOT A TOAST. Four numbers that need not sum are a report someone may
//     need to read twice, quote to a colleague, or act on. A container that vanishes on a
//     timer is the wrong one.
//
// The rules that read the four numbers live in ./cancelOutcome.ts and are unit-tested;
// this file is the renderer.

import { useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Ban } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { SectionPanel } from '@/components/ui/section-panel';
import { ErrorBanner } from '@/components/ui/error-banner';
import { HintText } from '@/components/ui/hint-text';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import {
  cancelCommandBatch,
  countBatchCommands,
  type CancelCommandBatchResult,
  type CommandBatch,
} from '@/lib/api/command-delivery';
import { Fact } from './Fact';
import { STILL_HELD_STATUSES, needsSecondCancel, unaccounted } from './cancelOutcome';

export interface BatchCancelState {
  /** The primary action: offered on a batch nobody has called off yet. */
  canCancel: boolean;
  /** The remedial action: offered whenever the operator may write commands. */
  canWrite: boolean;
  busy: boolean;
  cancel: () => void;
  outcome: CancelCommandBatchResult | null;
  error: string | null;
  dismissError: () => void;
}

// useBatchCancel owns the cancel action and its outcome. It is a hook rather than a
// component because the two halves render in different places — the button belongs in the
// page header's action slot (a detail page's header carries the entity's OWN actions) and
// the report belongs in the page body, where it can be read.
export function useBatchCancel({
  batch,
  reloadKey,
  onCancelled,
}: {
  batch: CommandBatch;
  reloadKey: number;
  onCancelled: () => void;
}): BatchCancelState {
  const { t } = useTranslation('commandBatches');
  const { claims } = useAuth();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<CancelCommandBatchResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef(false);

  const canWrite = hasAuthority(claims, 'command:write');
  // 🔴 BOTH GATES, AND THEY ARE DIFFERENT KINDS OF GATE. The authority decides whether the
  // operator may write commands at all; `cancelledAt` decides whether this batch has
  // already been called off. A batch that has been cancelled keeps the FIRST
  // cancellation's stamp forever (first-wins), so re-offering the primary action on it
  // would invite a write whose only visible effect is a second report of the same event.
  const canCancel = canWrite && batch.cancelledAt == null;

  // How much this cancel could actually stop, read live. The batch record's `accepted` is
  // a stored fact about the moment it fired and is the wrong number here — commands have
  // moved since. This asks the command rows instead, for the three states in which the
  // platform still HOLDS the command.
  //
  // 🔴 A NULL HERE IS "NOT KNOWN", NOT ZERO. Three different things produce it — the read
  // is still in flight, the read failed, or the service answered with a null
  // `totalRecords` (the field is a nullable Int) — and all three mean the same thing to an
  // operator about to press a brake: nobody said how much is left to stop. The dialog says
  // that, rather than printing a confident 0 that reads as "there is nothing to stop".
  //
  // The button is deliberately NOT disabled while this loads. An informational read must
  // never be allowed to hold up a brake.
  const { data: heldCount } = useQuery(
    () => countBatchCommands({ batchToken: batch.token, statuses: STILL_HELD_STATUSES }),
    [batch.token, reloadKey],
  );

  const cancel = async () => {
    // A duplicate cancel is harmless where a duplicate FIRE is not — no token is spent and
    // the batch's recorded cancellation is first-wins — so this guard is only about not
    // stacking two requests and two reports on top of each other.
    if (inFlight.current) return;

    const confirmed = await confirm({
      // The batch's name is the command key every targeted device received; it is customer
      // data, interpolated rather than translated.
      title: t('cancelConfirmTitle', { name: batch.name }),
      description: (
        // Spans, not paragraphs: this lands inside the dialog's own <p> description.
        <>
          <span className="block">
            {/* Zero is a DIFFERENT STATEMENT, not a plural form of the same one: "holding 0
                commands" and "holding 2 commands" lead to opposite expectations of what the
                button will do, so they are separate sentences rather than an ICU category. */}
            {heldCount == null
              ? t('cancelConfirmHeldUnknown')
              : heldCount === 0
                ? t('cancelConfirmHeldNone')
                : t('cancelConfirmHeld', { count: heldCount })}
          </span>
          <span className="mt-2 block">{t('cancelConfirmSent')}</span>
        </>
      ),
      confirmLabel: t('cancelConfirmAction'),
      // 🔴 NOT the default "Cancel". On this dialog alone, "Cancel" is the name of the
      // thing being confirmed, so a dismiss button reading "Cancel" is genuinely ambiguous
      // — half the readers would press it to cancel the batch.
      cancelLabel: t('cancelConfirmDismiss'),
    });
    if (!confirmed) return;

    inFlight.current = true;
    setBusy(true);
    setError(null);
    try {
      const result = await cancelCommandBatch(batch.token);
      // 🔴 THE ANSWER IS READ, NOT ASSUMED. The mutation resolving means the service
      // decided — it says nothing about how much was stopped, and `cancelled` can be 0
      // while `alreadySent` is the whole fleet. The panel renders what came back.
      setOutcome(result);
      // The batch record and its command rows are both stale now: the stamp, the count,
      // and every row's status.
      onCancelled();
    } catch (err) {
      // The server's own message. Unlike the fire path there is no rejection channel to
      // separate a verdict from an outage, because this mutation has no verdict — anything
      // arriving here is a failure to decide, and its text (an authorization refusal
      // naming device:read, say) is the only thing the operator has to go on.
      setError(errMessage(err));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  return { canCancel, canWrite, busy, cancel, outcome, error, dismissError: () => setError(null) };
}

// The header button. Rendered only where pressing it can do something.
export function CancelBatchButton({ state }: { state: BatchCancelState }) {
  const { t } = useTranslation('commandBatches');
  if (!state.canCancel) return null;
  return (
    <Button variant="destructive" onClick={state.cancel} loading={state.busy} disabled={state.busy}>
      <Ban size={14} /> {t('cancelBatch')}
    </Button>
  );
}

// The report, and the failure that means there is no report.
export function CancelBatchReport({ state }: { state: BatchCancelState }): ReactNode {
  const { t } = useTranslation('commandBatches');
  if (state.error) {
    return (
      <div>
        <ErrorBanner message={state.error} onDismiss={state.dismissError} />
        {/* Retrying IS safe here, and saying so is the useful part: a cancel spends no
            token and a batch's recorded cancellation is first-wins, so a second attempt
            cannot double-actuate anything the way a second FIRE could. */}
        <HintText size="md">{t('cancelFailedHint')}</HintText>
      </div>
    );
  }
  if (!state.outcome) return null;
  return <CancelOutcomePanel outcome={state.outcome} state={state} />;
}

function CancelOutcomePanel({
  outcome,
  state,
}: {
  outcome: CancelCommandBatchResult;
  state: BatchCancelState;
}) {
  const { t } = useTranslation('commandBatches');
  const missed = unaccounted(outcome);
  return (
    <SectionPanel title={t('cancelOutcomeTitle')}>
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Fact label={t('factStopped')}>{outcome.cancelled}</Fact>
        {/* 🔴 LABELLED BY WHERE THE COMMANDS ARE, not by what happened to them. "Already
            sent" invites the reading "sent, so done"; these are the ones still travelling
            toward hardware. */}
        <Fact label={t('factAlreadySent')}>{outcome.alreadySent}</Fact>
        <Fact label={t('factAlreadyFinished')}>{outcome.alreadyFinished}</Fact>
        <Fact label={t('factMatched')}>{outcome.matched}</Fact>
      </dl>

      <p className="mt-3 text-sm text-foreground">
        {outcome.cancelled === 0
          ? t('cancelStoppedNone')
          : t('cancelStopped', { count: outcome.cancelled })}
      </p>

      {/* 🔴 THE SENTENCE THIS SLICE EXISTS FOR. It is `text-destructive` rather than muted
          because it is the one part of the report that says the fleet was NOT stopped. */}
      {outcome.alreadySent > 0 ? (
        <p className="mt-1 text-sm text-destructive">
          {t('cancelStillActing', { count: outcome.alreadySent })}
        </p>
      ) : (
        <p className="mt-1 text-sm text-muted-foreground">{t('cancelNoneInFlight')}</p>
      )}

      {needsSecondCancel(outcome) ? (
        <div className="mt-4 space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-4">
          <p className="text-sm text-foreground">{t('cancelAgainNeeded', { count: missed })}</p>
          <HintText size="md">{t('cancelAgainHow')}</HintText>
          <HintText size="md">{t('cancelAgainFirstWins')}</HintText>
          {/* 🔴 NOT GATED ON `cancelledAt`. By the time this renders the batch IS cancelled,
              and cancelling again is exactly the fix the service prescribes — gating the
              remedy on the condition it exists to remedy would leave live commands running
              with the screen telling the operator to stop them and no way to do it. */}
          {state.canWrite && (
            <Button
              variant="destructive"
              size="sm"
              onClick={state.cancel}
              loading={state.busy}
              disabled={state.busy}
            >
              {t('cancelAgain')}
            </Button>
          )}
        </div>
      ) : (
        <HintText size="md" className="mt-3">{t('cancelSettled')}</HintText>
      )}

      <HintText size="md" className="mt-3">{t('cancelMatchedHint')}</HintText>
    </SectionPanel>
  );
}

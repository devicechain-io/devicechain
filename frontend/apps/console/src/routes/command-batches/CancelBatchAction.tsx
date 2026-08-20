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
//
// 🔴 IT ALSO OWNS WHEN THE BRAKE IS OFFERED AT ALL, and that gate reads WHAT IS LEFT rather
// than WHAT WAS CALLED. Withdrawing the action the moment a batch carries a cancellation
// stamp is the obvious rule and the wrong one: command-delivery can leave a command live
// inside a cancelled batch, the report that would have named it has already been rendered,
// and the stamp then hides the only control that could stop it. See `canCancel` below.

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
import { STILL_HELD_STATUSES, cancelMotion, needsSecondCancel, offerCancel, unaccounted } from './cancelOutcome';

// The count of the batch's commands the platform is STILL HOLDING — the three states a
// cancel can actually stop — as a three-valued answer whose null means "not known", plus
// whether a read is in flight right now.
//
// 🔴🔴 `data` ALONE IS NOT THE ANSWER, AND NEITHER IS `data` PLUS `error`. `useQuery` keeps
// the previous data across a re-read in BOTH of its unfinished states, and the two need
// separate handling because they are reached differently:
//
//   • A re-read that FAILED leaves `data` at the previous generation with `error` set.
//   • A re-read that is STILL IN FLIGHT leaves `data` at the previous generation with
//     `error` CLEARED — the deps-change effect sets `{...s, loading: true, error: null}`.
//     So for the whole in-flight window, and indefinitely for a read that hangs without
//     erroring, a stale count is indistinguishable from a fresh one by `error` alone.
//
// Both are "nobody has answered THIS question yet". The value this screen most often holds
// from before is 0, which is the one value that withdraws the brake — so admitting either
// stale reading would withhold a brake on the strength of a read that never landed, which is
// exactly the failure the gate was rewritten to prevent. Hence `!loading && error == null`.
//
// `enabled: false` answers "not known" without asking. It is not an optimization detail: a
// viewer without command:write is never offered the action and never opens the dialog, so
// the read would be a round trip taken to feed a decision already made.
//
// ⚠️ IT IS READ, NOT WATCHED. One read at mount and one per `reloadKey` bump — there is no
// poll and no timer. See `offerCancel` for what that leaves open.
function useStillHeld(
  batchToken: string,
  { enabled = true, reloadKey = 0 }: { enabled?: boolean; reloadKey?: number } = {},
): { held: number | null; checking: boolean } {
  const { data, loading, error } = useQuery(
    () =>
      enabled
        ? countBatchCommands({ batchToken, statuses: STILL_HELD_STATUSES })
        : Promise.resolve(null),
    [batchToken, enabled, reloadKey],
  );
  return { held: !loading && error == null ? data : null, checking: loading };
}

export interface BatchCancelState {
  /** The header action: offered while pressing it could still change something. */
  canCancel: boolean;
  /** Whether the batch already carries a cancellation stamp — the action's WORDING, not its gate. */
  alreadyCancelled: boolean;
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

  // How much this cancel could actually stop. The batch record's `accepted` is a stored fact
  // about the moment it fired and is the wrong number here — commands have moved since.
  //
  // The button is neither hidden nor disabled while this loads. An informational read must
  // never be allowed to hold up a brake, and a read in flight is "not known" — which is the
  // reading `offerCancel` keeps the action on offer for.
  const alreadyCancelled = batch.cancelledAt != null;
  const { held } = useStillHeld(batch.token, { enabled: canWrite, reloadKey });
  const canCancel = offerCancel({ canWrite, alreadyCancelled, held });

  const cancel = async () => {
    // A duplicate cancel is harmless where a duplicate FIRE is not — no token is spent and
    // the batch's recorded cancellation is first-wins — so this guard is only about not
    // stacking two requests and two reports on top of each other.
    //
    // 🔴 IT IS ALSO NOT WHAT STOPS TWO BUTTONS FROM BOTH FIRING, and saying so matters
    // because there are now two: the header's and the report's. This flag is set AFTER the
    // confirmation resolves, so it is blind to the window between press and confirm. What
    // actually holds that window is ConfirmProvider — its overlay makes the page inert, and
    // a second confirm() SUPERSEDES the first by resolving it false, so at most one awaiter
    // ever proceeds. If that ever becomes a queue instead of a supersede, two sequential
    // mutations and two stacked reports become reachable with nothing here going red.
    if (inFlight.current) return;

    const confirmed = await confirm({
      // The batch's name is the command key every targeted device received; it is customer
      // data, interpolated rather than translated.
      //
      // A second cancel gets its own question. "Call off X?" asked about a batch already
      // called off invites the answer "it already is" — and the operator would be right,
      // which is the wrong thing to be thinking about while deciding whether to stop
      // commands that are still live.
      title: alreadyCancelled
        ? t('cancelAgainConfirmTitle', { name: batch.name })
        : t('cancelConfirmTitle', { name: batch.name }),
      description: (
        // Spans, not paragraphs: this lands inside the dialog's own <p> description.
        <>
          <StillHeldSentence batchToken={batch.token} />
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

  return {
    canCancel,
    alreadyCancelled,
    canWrite,
    busy,
    cancel,
    outcome,
    error,
    dismissError: () => setError(null),
  };
}

// What the platform is still holding, stated inside the confirmation — read when the dialog
// OPENS and corrected in place while it is open.
//
// 🔴 IT CANNOT BE HANDED THE PAGE'S COUNT, and the reason is structural rather than
// stylistic. `confirm()` stores its `description` in the provider's state, so what is frozen
// is every VALUE interpolated into it at the instant of the call. Handing over the page's
// count therefore produced a sentence wrong in both directions: on a page open for a while
// it stated a minutes-old number in the present tense, and on a page just opened it said the
// count "could not be read" and never corrected itself when the read landed a moment later.
//
// 🔴 A RENDER FUNCTION WOULD NOT HAVE FIXED IT EITHER — its closure is captured at the same
// instant. What escapes the freeze is an ELEMENT that queries for itself: the element is
// stored, but it mounts and renders inside the dialog, so its answer is taken when the
// operator opens the dialog rather than when the page was loaded.
//
// ⚠️ THAT MOVES THE SNAPSHOT, IT DOES NOT ABOLISH IT. This reads once, on mount, and does not
// poll — so it is right as of the moment the question was asked, and a dialog left open for
// ten minutes states a ten-minute-old number just as the old one did. Moving the instant from
// page-load to decision-time is the whole of what this buys; anything more would need a poll,
// which is a lot of machinery for a dialog nobody holds open.
//
// The page's count is a different question and stays where it is: that one decides whether
// to OFFER the brake at all, this one tells the operator what pressing it will stop.
function StillHeldSentence({ batchToken }: { batchToken: string }) {
  const { t } = useTranslation('commandBatches');
  const { held, checking } = useStillHeld(batchToken);
  return (
    <span className="block">
      {/* FOUR statements, not one with substitutions. Zero is a different statement from a
          count — "holding 0 commands" and "holding 2 commands" lead to opposite expectations
          of what the button will do — and 🔴 A READ STILL IN FLIGHT IS DIFFERENT AGAIN from
          one that came back empty. Both leave the gate at "not known", but only one of them
          justifies telling the operator the count "could not be read": saying that during the
          ordinary loading beat of every dialog open is a failure claimed about a read nobody
          has failed yet. */}
      {checking
        ? t('cancelConfirmHeldChecking')
        : held == null
          ? t('cancelConfirmHeldUnknown')
          : held === 0
            ? t('cancelConfirmHeldNone')
            : t('cancelConfirmHeld', { count: held })}
    </span>
  );
}

// The header button. Rendered only where pressing it can do something.
export function CancelBatchButton({ state }: { state: BatchCancelState }) {
  const { t } = useTranslation('commandBatches');
  if (!state.canCancel) return null;
  return (
    <Button variant="destructive" onClick={state.cancel} loading={state.busy} disabled={state.busy}>
      {/* Named for what pressing it does NOW. On a stamped batch this is not the first
          cancellation and must not offer itself as one — "Cancel batch" there reads as an
          action with no effect, which is exactly the reading that got the header hidden in
          the first place. It deliberately shares its wording with the report's remedial
          button below: same action, two entry points. */}
      <Ban size={14} /> {state.alreadyCancelled ? t('cancelAgain') : t('cancelBatch')}
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
  const motion = cancelMotion(outcome);
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

      {/* 🔴 THE SENTENCE THIS SLICE EXISTS FOR. `stillActing` is `text-destructive` rather
          than muted because it is the one part of the report that says the fleet was NOT
          stopped — and `stillQueued` renders NOTHING here on purpose: the block below already
          states what is back in the queue, and the only sentence this line could add would be
          a reassurance that contradicts it. Silence is the honest option. */}
      {motion === 'stillActing' && (
        <p className="mt-1 text-sm text-destructive">
          {t('cancelStillActing', { count: outcome.alreadySent })}
        </p>
      )}
      {motion === 'settledNothingReached' && (
        <p className="mt-1 text-sm text-muted-foreground">{t('cancelNoneInFlight')}</p>
      )}
      {motion === 'settledSomeFinished' && (
        <p className="mt-1 text-sm text-muted-foreground">
          {t('cancelNoneInFlightSomeFinished', { count: outcome.alreadyFinished })}
        </p>
      )}

      {needsSecondCancel(outcome) ? (
        <div className="mt-4 space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-4">
          <p className="text-sm text-foreground">{t('cancelAgainNeeded', { count: missed })}</p>
          <HintText size="md">{t('cancelAgainHow')}</HintText>
          <HintText size="md">{t('cancelAgainFirstWins')}</HintText>
          {/* 🔴 NOT GATED ON `cancelledAt`. By the time this renders the batch IS cancelled,
              and cancelling again is exactly the fix the service prescribes — gating the
              remedy on the condition it exists to remedy would leave live commands running
              with the screen telling the operator to stop them and no way to do it.
              🔴 NOR IS IT MADE REDUNDANT by the header now offering the same action, and it
              is not kept merely for convenience: the two are gated on DIFFERENT EVIDENCE.
              This one reads what THIS cancel returned; the header reads what a LATER count
              found. A count taken while the missed commands are still settling can come back
              0 and withdraw the header button — leaving this the only way to act on a
              shortfall the operator is being told about right now. */}
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { approveAgentProposal, rejectAgentProposal, type AgentProposal, type ProposalArea } from '@/lib/api/agent-proposals';
import { serverErrorCode } from '@/i18n/signInError';
import { errMessage } from '@/routes/common';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { refusalReasonKey } from './shared';

export function useProposalDecision(area: ProposalArea, token: string, current: AgentProposal | undefined, reload: () => void) {
  const { t } = useTranslation('approvals');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [stepUp, setStepUp] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [note, setNote] = useState('');
  const [info, setInfo] = useState<string | null>(null);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const command = area === 'command-delivery';
  const noteTooLong = new TextEncoder().encode(note).length > 512;
  const canAct = !!current && current.state === 'PROPOSED' && !busy;
  const act = async (approve: boolean) => {
    if (!current || current.state !== 'PROPOSED' || inFlight.current || (!approve && noteTooLong)) return;
    // Freeze the same displayed snapshot through confirmation and step-up failures.
    const displayed = current;
    const final = displayed.approvals.length + 1 >= displayed.requiredApprovals;
    inFlight.current = true;
    setBusy(true);
    try {
      // A DESTRUCTIVE command gets the destructive confirmation on EVERY approval, not only the
      // one counted as final here: another person may approve while this dialog is open, and
      // then this approval is the one that queues the command.
      const destructiveCommand = command && displayed.impact === 'DESTRUCTIVE';
      const description = final ? `approveFinalConfirm_${displayed.kind}` : command ? 'approveCommandConfirm' : 'approveConfirm';
      if (approve && !(await confirm({ title: t('approve'),
        description: t(description, { count: displayed.targetCount, impact: displayed.impact }),
        confirmLabel: t('approve'), destructive: destructiveCommand }))) return;
      setMutationError(null); setStepUp(false); setInfo(null);
      if (approve) {
        const outcome = command ? await approveAgentProposal(area, token, displayed.digest) : await approveAgentProposal(token, displayed.digest);
        if (outcome.state === 'REFUSED') {
          const message = [t(command ? 'commandRefused' : 'refused'), ...outcome.reasons.map(reason => t(refusalReasonKey(reason, command), { reason }))].join(' ');
          setInfo(message); toast(message, 'info');
        } else toast(t(outcome.state === 'EXECUTED' ? command ? 'commandQueued' : 'executed' : 'approved'));
      } else {
        if (command) await rejectAgentProposal(area, token, note || undefined);
        else await rejectAgentProposal(token, note || undefined);
        toast(t('rejected'));
      }
      setRejecting(false); reload();
    } catch (err) {
      const code = serverErrorCode(err);
      if (code === 'STEP_UP_REQUIRED') setStepUp(true);
      else if (code === 'EXECUTION_NOT_AVAILABLE' || code === 'CONCURRENT_APPROVAL') {
        const message = t(code === 'CONCURRENT_APPROVAL' ? 'concurrentApproval' : command ? 'commandExecutionUnavailable' : 'executionUnavailable');
        setInfo(message); toast(message, 'info'); reload();
      } else if (code && refusalReasonKey(code, command) !== 'refusalReason') {
        setMutationError(t(refusalReasonKey(code, command), { reason: code }));
      } else setMutationError(errMessage(err));
    } finally { inFlight.current = false; setBusy(false); }
  };
  return { act, busy, canAct, stepUp, rejecting, setRejecting, note, setNote, noteTooLong, info, mutationError };
}

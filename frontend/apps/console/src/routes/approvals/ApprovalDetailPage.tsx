// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { approveAgentProposal, getAgentProposal, rejectAgentProposal } from '@/lib/api/agent-proposals';
import { useQuery } from '@/lib/hooks/use-query';
import { serverErrorCode } from '@/i18n/signInError';
import { formatTime } from '@/lib/utils';
import { errMessage, useReload } from '@/routes/common';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { CopyToken } from '@/components/ui/copy-token';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { ApprovalsGate, ProposalResult, refusalReasonKey, StepUpPrompt } from './shared';

export default function ApprovalDetailPage() {
  const { token = '' } = useParams();
  return <ApprovalsGate><Detail key={token} token={token} /></ApprovalsGate>;
}
function Detail({ token }: { token: string }) {
  const { t } = useTranslation('approvals');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  const { data: proposal, current, loading, error } = useQuery(() => getAgentProposal(token), [token, version]);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [stepUp, setStepUp] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [note, setNote] = useState('');
  const [info, setInfo] = useState<string | null>(null);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const noteTooLong = new TextEncoder().encode(note).length > 512;
  const canAct = !!current && current.state === 'PROPOSED' && !busy;
  const act = async (approve: boolean) => {
    if (!current || current.state !== 'PROPOSED' || inFlight.current || (!approve && noteTooLong)) return;
    // This snapshot stays displayed while the confirm dialog is open. No read is started
    // until the mutation settles, so the dialog describes exactly the digest being sent.
    const displayed = current;
    inFlight.current = true;
    setBusy(true);
    try {
      if (approve && !(await confirm({ title: t('approve'),
        description: t(displayed.approvals.length + 1 >= displayed.requiredApprovals ? `approveFinalConfirm_${displayed.kind}` : 'approveConfirm',
          { count: displayed.targetCount, impact: displayed.impact }),
        confirmLabel: t('approve'), destructive: false }))) return;
      setMutationError(null); setStepUp(false); setInfo(null);
      if (approve) {
        const outcome = await approveAgentProposal(token, displayed.digest);
        if (outcome.state === 'REFUSED') {
          const message = [t('refused'), ...outcome.reasons.map(reason => t(refusalReasonKey(reason), { reason }))].join(' ');
          setInfo(message); toast(message, 'info');
        } else toast(t(outcome.state === 'EXECUTED' ? 'executed' : 'approved'));
      } else { await rejectAgentProposal(token, note || undefined); toast(t('rejected')); }
      setRejecting(false); reload();
    } catch (err) {
      const code = serverErrorCode(err);
      if (code === 'STEP_UP_REQUIRED') setStepUp(true);
      else if (code === 'EXECUTION_NOT_AVAILABLE' || code === 'CONCURRENT_APPROVAL') {
        const message = t(code === 'CONCURRENT_APPROVAL' ? 'concurrentApproval' : 'executionUnavailable');
        setInfo(message); toast(message, 'info'); reload();
      }
      else setMutationError(errMessage(err));
    } finally { inFlight.current = false; setBusy(false); }
  };
  if (!proposal) return <PageShell title={t('detailTitle')}>{loading ? <LoadingState description={t('loading')} /> : <ErrorState description={error!} />}</PageShell>;
  return <PageShell title={t('detailTitle')} titleAdornment={<Badge variant="secondary">{t('preview')}</Badge>}
    description={token} action={<>
      <Button disabled={!canAct} onClick={() => void act(true)}>{t('approve')}</Button>
      <Button variant="destructive" disabled={!canAct} onClick={() => setRejecting(true)}>{t('reject')}</Button>
    </>}>
    <div className="space-y-4">
      {error && <ErrorState description={error} />}
      {mutationError && <ErrorState description={mutationError} />}
      {stepUp && <StepUpPrompt path={`/approvals/${encodeURIComponent(token)}`} />}
      {info && <p role="status">{info}</p>}
      {rejecting && <SectionPanel title={t('reject')}><form className="space-y-3" onSubmit={e => { e.preventDefault(); void act(false); }}>
        <label htmlFor="reject-note">{t('note')}</label>
        <Input id="reject-note" value={note} onChange={e => setNote(e.target.value)} />
        {noteTooLong && <p role="alert">{t('noteTooLong')}</p>}
        <Button type="submit" disabled={!canAct || noteTooLong}>{t('submitReject')}</Button>
        <Button variant="outline" disabled={busy} onClick={() => setRejecting(false)}>{t('common:cancel')}</Button>
      </form></SectionPanel>}
      <SectionPanel title={t('action')}><dl className="space-y-3">
        <dt>{t('kind')}</dt><dd>{t(proposal.kind)}</dd>
        <dt>{t('targets')}</dt><dd>{proposal.targetCount}<ul>{proposal.targets.map(target => <li className="font-mono break-all" key={target}>{target}</li>)}</ul></dd>
        <dt>{t('impact')}</dt><dd><Badge variant="outline">{proposal.impact}</Badge></dd>
        <dt>{t('payload')}</dt><dd><pre className="whitespace-pre-wrap break-all">{proposal.payload}</pre></dd>
        <dt>{t('digest')}</dt><dd><p className="font-mono break-all">{proposal.digest}</p><CopyToken value={proposal.digest} /></dd>
        <dt>{t('policyVersion')}</dt><dd>{proposal.policyVersion}</dd>
        <dt>{t('reasons')}</dt><dd><ul>{proposal.reasons.map(reason => <li key={reason}>{reason}</li>)}</ul></dd>
      </dl></SectionPanel>
      <SectionPanel title={t('proposer')}>
        <p>{proposal.proposerClient}</p><p>{t('actingFor', { principal: proposal.proposerPrincipal })}</p>
        {!proposal.clientAttested && <Badge variant="warning">{t('unverified')}</Badge>}
        <h3 className="mt-3 font-medium">{t('agentRationale')}</h3><p className="whitespace-pre-wrap break-words">{proposal.rationale}</p>
      </SectionPanel>
      <SectionPanel title={t('decision')}><dl className="space-y-3">
        <dt>{t('state')}</dt><dd>{t(proposal.state)}</dd>
        {(proposal.state === 'EXECUTED' || proposal.state === 'REFUSED') && <>
          <dt>{t('result')}</dt><dd><ProposalResult proposal={proposal} /></dd>
        </>}
        <dt>{t('created')}</dt><dd>{formatTime(proposal.createdAt)}</dd>
        <dt>{t('expires')}</dt><dd>{formatTime(proposal.expiresAt)}</dd>
        <dt>{t('decided')}</dt><dd>{formatTime(proposal.decidedAt)}</dd>
        <dt>{t('decidedBy')}</dt><dd>{proposal.decidedBy}</dd>
        <dt>{t('note')}</dt><dd className="whitespace-pre-wrap">{proposal.note}</dd>
        <dt>{t('approvals')}</dt><dd>{t('approvalCount', { done: proposal.approvals.length, required: proposal.requiredApprovals })}
          <ul>{proposal.approvals.map(a => <li key={a.principal}>{t('recordedApproval', { principal: a.principal, when: formatTime(a.approvedAt) })}</li>)}</ul>
        </dd>
      </dl></SectionPanel>
    </div>
  </PageShell>;
}

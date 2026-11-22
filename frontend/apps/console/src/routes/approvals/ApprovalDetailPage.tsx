// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { getAgentProposal, proposalPath, type ProposalArea } from '@/lib/api/agent-proposals';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { useReload } from '@/routes/common';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { CopyToken } from '@/components/ui/copy-token';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { ApprovalsGate, ProposalResult, StepUpPrompt } from './shared';

import { useProposalDecision } from './useProposalDecision';
import { CommandProposalReview, commandSnapshot } from './CommandProposalReview';

export default function ApprovalDetailPage({ area: selectedArea }: { area?: ProposalArea }) {
  const { token = '', area: routeArea } = useParams();
  const area = selectedArea ?? (routeArea === 'command-delivery' ? 'command-delivery' : 'device-management');
  return <ApprovalsGate><Detail key={JSON.stringify([area, token])} area={area} token={token} /></ApprovalsGate>;
}
function Detail({ area, token }: { area: ProposalArea; token: string }) {
  const { t } = useTranslation('approvals');
  const { claims } = useAuth();
  const command = area === 'command-delivery';
  const [version, reload] = useReload();
  const { data: proposal, current, loading, error } = useQuery(() => command ? getAgentProposal(area, token) : getAgentProposal(token), [area, token, version], [area, token]);
  const snapshot = proposal?.kind === 'COMMAND_SEND' ? commandSnapshot(proposal) : null;
  const decision = command && (!snapshot || !hasAuthority(claims, 'command:write')) ? undefined : current;
  const { act, busy, canAct: ready, stepUp, rejecting, setRejecting, note, setNote, noteTooLong, info, mutationError } = useProposalDecision(area, token, decision, reload);
  const canAct = ready && (!command || snapshot !== null);
  if (!proposal) return <PageShell title={t('detailTitle')}>{loading ? <LoadingState description={t('loading')} /> : <ErrorState description={error!} />}</PageShell>;
  return <PageShell title={t('detailTitle')} titleAdornment={<Badge variant="secondary">{t('preview')}</Badge>}
    description={token} action={<>
      <Button disabled={!canAct} onClick={() => void act(true)}>{t('approve')}</Button>
      <Button variant="destructive" disabled={!canAct} onClick={() => setRejecting(true)}>{t('reject')}</Button>
    </>}>
    <div className="space-y-4">
      {error && <ErrorState description={error} />}
      {mutationError && <ErrorState description={mutationError} />}
      {stepUp && <StepUpPrompt path={proposalPath(area, token)} />}
      {info && <p role="status">{info}</p>}
      {rejecting && <SectionPanel title={t('reject')}><form className="space-y-3" onSubmit={e => { e.preventDefault(); void act(false); }}>
        <label htmlFor="reject-note">{t('note')}</label>
        <Input id="reject-note" value={note} onChange={e => setNote(e.target.value)} />
        {noteTooLong && <p role="alert">{t('noteTooLong')}</p>}
        <Button type="submit" disabled={!canAct || noteTooLong}>{t('submitReject')}</Button>
        <Button variant="outline" disabled={busy} onClick={() => setRejecting(false)}>{t('common:cancel')}</Button>
      </form></SectionPanel>}
      {command && proposal.kind === 'COMMAND_SEND' ? <CommandProposalReview proposal={proposal} snapshot={snapshot} /> : <SectionPanel title={t('action')}><dl className="space-y-3">
        <dt>{t('kind')}</dt><dd>{t(proposal.kind)}</dd>
        <dt>{t('targets')}</dt><dd>{proposal.targetCount}<ul>{proposal.targets.map(target => <li className="font-mono break-all" key={target}>{target}</li>)}</ul></dd>
        <dt>{t('impact')}</dt><dd><Badge variant="outline">{proposal.impact}</Badge></dd>
        <dt>{t('payload')}</dt><dd><pre className="whitespace-pre-wrap break-all">{proposal.payload}</pre></dd>
        <dt>{t('digest')}</dt><dd><p className="font-mono break-all">{proposal.digest}</p><CopyToken value={proposal.digest} /></dd>
        <dt>{t('policyVersion')}</dt><dd>{proposal.policyVersion}</dd>
        <dt>{t('reasons')}</dt><dd><ul>{proposal.reasons.map(reason => <li key={reason}>{reason}</li>)}</ul></dd>
      </dl></SectionPanel>
      }
      <SectionPanel title={t('proposer')}>
        <p>{proposal.proposerClient}</p><p>{t('actingFor', { principal: proposal.proposerPrincipal })}</p>
        {(command || !proposal.clientAttested) && <Badge variant={proposal.clientAttested ? 'secondary' : 'warning'}>{t(proposal.clientAttested ? 'attested' : 'unverified')}</Badge>}
        {command && <p>{t('policyVersion')}: {proposal.policyVersion}</p>}
        {!command && <><h3 className="mt-3 font-medium">{t('agentRationale')}</h3><p className="whitespace-pre-wrap break-words">{proposal.rationale}</p></>}
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { COMMAND_PROPOSAL_AREA, PROPOSAL_AREAS, listAgentProposals, proposalPath, type AgentProposalPage, type ProposalArea } from '@/lib/api/agent-proposals';
import { getAgentPolicy, setAgentActuation } from '@/lib/api/agent-policy';
import type { AgentProposalState } from '@/gql/device-management/graphql';
import { useQuery } from '@/lib/hooks/use-query';
import { serverErrorCode } from '@/i18n/signInError';
import { formatTime } from '@/lib/utils';
import { errMessage, useReload } from '@/routes/common';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/ui/combobox';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Pagination } from '@/components/ui/pagination';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { DataTable, DataTableBody, DataTableCell, DataTableHead, DataTableHeaderCell, DataTableRow } from '@/components/ui/data-table';
import { ApprovalsGate, ProposalResult, StepUpPrompt } from './shared';

const STATES: AgentProposalState[] = ['PROPOSED', 'EXECUTED', 'REJECTED', 'EXPIRED', 'REFUSED', 'CANCELLED', 'KILLED'];
const pageSize = 25;
const COLUMNS = ['proposer', 'kind', 'targets', 'expires', 'approvals', 'state', 'result', 'rationale'];
const relativeExpiry = (value: string, language: string) =>
  new Intl.RelativeTimeFormat(language, { numeric: 'auto' }).format(Math.round((Date.parse(value) - Date.now()) / 60000), 'minute');

export default function ApprovalsPage() {
  return <ApprovalsGate><Inbox /></ApprovalsGate>;
}
function KillSwitch({ onChanged }: { onChanged: () => void }) {
  const { t } = useTranslation('approvals');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  const { data, current, error } = useQuery(getAgentPolicy, [version]);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [stepUp, setStepUp] = useState(false);
  const change = async () => {
    if (!current || inFlight.current) return;
    const enabled = current.actuationOff;
    inFlight.current = true;
    setBusy(true);
    try {
      if (!enabled && !(await confirm({ title: t('turnOff'), description: t('offConfirm'), confirmLabel: t('turnOff') }))) return;
      setStepUp(false);
      await setAgentActuation(enabled);
      toast(t('switchSaved'));
      reload();
      onChanged();
    } catch (err) {
      if (enabled && serverErrorCode(err) === 'STEP_UP_REQUIRED') setStepUp(true);
      else toast(errMessage(err), 'error');
    } finally { inFlight.current = false; setBusy(false); }
  };
  if (!data) return error ? <ErrorState description={error} /> : <LoadingState description={t('loading')} />;
  const status = !data?.instanceEnabled ? 'notEnabled' : data.operatorSuspended ? 'suspended'
    : data.actuationOff ? 'off' : data.actuationInEffect ? 'inEffect' : 'policyOff';
  return <SectionPanel title={t('agentActions')}>
    {error && <ErrorState description={error} />}
    <p className="mb-3">{t(status)}</p>
    <Button variant={data?.actuationOff ? 'outline' : 'destructive'} disabled={!current || busy} loading={busy} onClick={change}>
      {t(data?.actuationOff ? 'turnOn' : 'turnOff')}
    </Button>
    {stepUp && <StepUpPrompt path="/approvals" />}
  </SectionPanel>;
}
// A command proposal's row says why the proposal needs deciding the way it does; the detail page has them
// all, so the row keeps two and counts the rest. Alarm rows show none.
const ROW_REASON_LIMIT = 2;
function RowReasons({ reasons }: { reasons: string[] }) {
  const { t } = useTranslation('approvals');
  if (!reasons.length) return null;
  const more = reasons.length - ROW_REASON_LIMIT;
  return <div className="mt-1 text-sm text-muted-foreground">
    <ul aria-label={t('reasons')}>{reasons.slice(0, ROW_REASON_LIMIT).map(reason => <li key={reason}>{reason}</li>)}</ul>
    {more > 0 && <p>{t('moreReasons', { count: more })}</p>}
  </div>;
}
function Inbox() {
  const { claims } = useAuth();
  const isAdmin = hasAuthority(claims, 'agent:admin');
  const { t, i18n } = useTranslation('approvals');
  const [state, setState] = useState<AgentProposalState>('PROPOSED');
  // Open on a tab this person can READ (what they may decide does not decide what they may
  // read): Commands for a command reader without alarm:read, otherwise Alarms.
  const [area, setArea] = useState<ProposalArea>(!hasAuthority(claims, 'alarm:read') && hasAuthority(claims, 'command:read') ? COMMAND_PROPOSAL_AREA : 'device-management');
  const [pages, setPages] = useState<Record<ProposalArea, number>>({ 'device-management': 1, 'command-delivery': 1 });
  const pageNumber = pages[area];
  const setPageNumber = (page: number) => setPages(current => ({ ...current, [area]: page }));
  const [version, reload] = useReload();
  // The rows are the answer to THIS filter and page only: `current`, never the sticky `data`, so a
  // failed or slow re-read can not leave another state's proposals on screen.
  const { current: data, loading, error } = useQuery<AgentProposalPage>(() => area === 'command-delivery' ? listAgentProposals(area, { pageNumber, pageSize, state }) : listAgentProposals({ pageNumber, pageSize, state }), [area, pageNumber, state, version], [area]);
  return <PageShell title={t('title')} titleAdornment={<Badge variant="secondary">{t('preview')}</Badge>}>
    <div className="space-y-4">
      {isAdmin && <KillSwitch onChanged={reload} />}
      <div role="tablist" aria-label={t('area')} className="flex gap-2">
        {PROPOSAL_AREAS.map(value => <Button key={value} role="tab" aria-selected={area === value}
          variant={area === value ? 'default' : 'outline'} onClick={() => setArea(value)}>{t(value === COMMAND_PROPOSAL_AREA ? 'commandsTab' : 'alarmsTab')}</Button>)}
      </div>
      <Combobox value={state} allowClear={false} aria-label={t('state')} options={STATES.map(value => ({ value, label: t(value) }))}
        onChange={value => { setState(value as AgentProposalState); setPageNumber(1); }} />
      {loading ? <LoadingState description={t('loading')} /> : error ? <ErrorState description={error} />
        : !data?.results.length ? <EmptyState description={t('empty')} /> : <>
          <DataTable><DataTableHead>
            {COLUMNS.map(key => <DataTableHeaderCell key={key}>{t(key)}</DataTableHeaderCell>)}
          </DataTableHead><DataTableBody>{data.results.map(p => <DataTableRow key={`${area}:${p.token}`}>
            <DataTableCell><Link to={proposalPath(area, p.token)} className="text-primary underline">{p.proposerClient}</Link>
              <div>{t('actingFor', { principal: p.proposerPrincipal })}</div>
              {!p.clientAttested && <Badge variant="warning">{t('unverified')}</Badge>}
            </DataTableCell>
            <DataTableCell>{t(p.kind)}</DataTableCell>
            <DataTableCell>{p.targetCount} <Badge variant={p.impact === 'DESTRUCTIVE' ? 'destructive' : p.impact === 'DISRUPTIVE' ? 'warning' : 'outline'}>{p.impact}</Badge></DataTableCell>
            <DataTableCell><div>{relativeExpiry(p.expiresAt, i18n.language)}</div>{formatTime(p.expiresAt)}</DataTableCell>
            <DataTableCell>{t('approvalCount', { done: p.approvals.length, required: p.requiredApprovals })}
              <ul>{p.approvals.map(a => <li key={a.principal}>{t('recordedApproval', { principal: a.principal, when: formatTime(a.approvedAt) })}</li>)}</ul>
            </DataTableCell>
            <DataTableCell>{t(p.state)}{p.decidedBy && <div>{t('decisionBy', { principal: p.decidedBy })}</div>}
              {area === COMMAND_PROPOSAL_AREA && <RowReasons reasons={p.reasons} />}
            </DataTableCell>
            <DataTableCell><ProposalResult proposal={p} /></DataTableCell>
            <DataTableCell><p className="max-w-xs truncate">{'rationale' in p ? p.rationale : null}</p></DataTableCell>
          </DataTableRow>)}</DataTableBody></DataTable>
          <Pagination pageNumber={pageNumber} pageSize={pageSize} pagination={data.pagination} onPageChange={setPageNumber} />
        </>}
    </div>
  </PageShell>;
}

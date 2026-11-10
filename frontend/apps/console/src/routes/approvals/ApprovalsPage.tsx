// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { listAgentProposals } from '@/lib/api/agent-proposals';
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
import { ApprovalsGate, StepUpPrompt } from './shared';

const STATES: AgentProposalState[] = ['PROPOSED', 'EXECUTED', 'REJECTED', 'EXPIRED', 'REFUSED', 'CANCELLED', 'KILLED'];
const pageSize = 25;
const COLUMNS = ['proposer', 'kind', 'targets', 'expires', 'approvals', 'rationale'];
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
function Inbox() {
  const { t, i18n } = useTranslation('approvals');
  const [state, setState] = useState<AgentProposalState>('PROPOSED');
  const [pageNumber, setPageNumber] = useState(1);
  const [version, reload] = useReload();
  // The rows are the answer to THIS filter and page only: `current`, never the sticky `data`, so a
  // failed or slow re-read can not leave another state's proposals on screen.
  const { current: data, loading, error } = useQuery(() => listAgentProposals({ pageNumber, pageSize, state }), [pageNumber, state, version]);
  return <PageShell title={t('title')} titleAdornment={<Badge variant="secondary">{t('preview')}</Badge>}>
    <div className="space-y-4">
      <KillSwitch onChanged={reload} />
      <Combobox value={state} allowClear={false} aria-label={t('state')} options={STATES.map(value => ({ value, label: t(value) }))}
        onChange={value => { setState(value as AgentProposalState); setPageNumber(1); }} />
      {loading ? <LoadingState description={t('loading')} /> : error ? <ErrorState description={error} />
        : !data?.results.length ? <EmptyState description={t('empty')} /> : <>
          <DataTable><DataTableHead>
            {COLUMNS.map(key => <DataTableHeaderCell key={key}>{t(key)}</DataTableHeaderCell>)}
          </DataTableHead><DataTableBody>{data.results.map(p => <DataTableRow key={p.token}>
            <DataTableCell><Link to={`/approvals/${encodeURIComponent(p.token)}`} className="text-primary underline">{p.proposerClient}</Link>
              <div>{t('actingFor', { principal: p.proposerPrincipal })}</div>
              {!p.clientAttested && <Badge variant="warning">{t('unverified')}</Badge>}
            </DataTableCell>
            <DataTableCell>{t(p.kind)}</DataTableCell>
            <DataTableCell>{p.targetCount} <Badge variant="outline">{p.impact}</Badge></DataTableCell>
            <DataTableCell><div>{relativeExpiry(p.expiresAt, i18n.language)}</div>{formatTime(p.expiresAt)}</DataTableCell>
            <DataTableCell>{t('approvalCount', { done: p.approvals.length, required: p.requiredApprovals })}</DataTableCell>
            <DataTableCell><p className="max-w-xs truncate">{p.rationale}</p></DataTableCell>
          </DataTableRow>)}</DataTableBody></DataTable>
          <Pagination pageNumber={pageNumber} pageSize={pageSize} pagination={data.pagination} onPageChange={setPageNumber} />
        </>}
    </div>
  </PageShell>;
}

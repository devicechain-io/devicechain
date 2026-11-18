// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/lazy/automationReview';
import { useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { listAutomationReviews, matchingDraftReviewTokens, runAutomationReview } from '@/lib/api/automation-reviews';
import { listDetectionRules } from '@/lib/api/device-management';
import type { RunAutomationReviewInput } from '@/gql/device-management/graphql';
import { useQuery } from '@/lib/hooks/use-query';
import { serverErrorCode } from '@/i18n/signInError';
import { formatTime } from '@/lib/utils';
import { errMessage } from '@/routes/common';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectItem } from '@/components/ui/select';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { buildExpectation, parseDevices, type ExpectationRow, type ExpectationShape } from './expectations';
import { ProtectionSection } from './ProtectionSection';
import { Outcome, ReviewTable, reviewErrorKey, reviewPath } from './shared';

const ROLES = ['INCIDENT', 'NORMAL'] as const;
const SHAPES: ExpectationShape[] = ['raised', 'neverRaised', 'intent', 'neverIntent'];
const pageSize = 25;
const REVIEW_COLUMNS = ['review', 'outcome', 'state', 'rule', 'created', 'draftMatch'];
const BOUNDS = ['start', 'end'] as const;
const newExpectation = (rule: string): ExpectationRow => ({
  id: crypto.randomUUID(), window: 'INCIDENT', shape: 'raised', name: '', rule, device: '', command: '', minutes: '5',
});

// A number input that accepts fractional minutes.
const ANY_STEP = 'any';

export function AutomationReviewPanel({ profileToken }: { profileToken: string }) {
  const { claims, identityToken } = useAuth();
  return <Panel key={JSON.stringify([profileToken, claims?.tenant, claims?.username, identityToken])} profileToken={profileToken} />;
}

function Panel({ profileToken }: { profileToken: string }) {
  const { t } = useTranslation('automationReview');
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const [pageNumber, setPageNumber] = useState(1);
  const { current: data, loading, error } = useQuery(async () => {
    const [page, matchingTokens] = await Promise.all([
      listAutomationReviews({ profileToken, pageNumber, pageSize }), matchingDraftReviewTokens(profileToken),
    ]);
    return page && { ...page, results: page.results.map(row => ({ ...row, matchesCurrentDraft: matchingTokens.has(row.token) })) };
  }, [profileToken, pageNumber]);
  const reviews = [...(data?.results ?? [])].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
  return <div className="space-y-4">
    <ProtectionSection profileToken={profileToken} />
    <SectionPanel title={t('tab')}><Badge variant="secondary">{t('preview')}</Badge>
      {loading ? <LoadingState description={t('loading')} /> : error ? <ErrorState description={error} />
        : !reviews.length ? <EmptyState description={t('empty')} /> : <>
          <ReviewTable headings={REVIEW_COLUMNS.map(k => t(k))}>
            {reviews.map(r => <tr key={r.token}><td><Link className="text-primary underline" to={reviewPath(r.token)}>{r.token}</Link></td>
              <td><Outcome outcome={r.outcome} /></td><td>{t(r.state)}</td><td>{r.ruleToken}</td>
              <td>{r.createdBy} · {formatTime(r.createdAt)}</td><td>{r.matchesCurrentDraft === true && <Badge variant="outline">{t('matches')}</Badge>}
                {r.matchesCurrentDraft === false && t('changed')}</td></tr>)}
          </ReviewTable>
          {data && <Pagination pageNumber={pageNumber} pageSize={pageSize} pagination={data.pagination} onPageChange={setPageNumber} />}
        </>}
    </SectionPanel>
    {canWrite && <RunForm profileToken={profileToken} />}
  </div>;
}

function RunForm({ profileToken }: { profileToken: string }) {
  const { t } = useTranslation('automationReview');
  const navigate = useNavigate();
  const { current: rules, error: ruleError } = useQuery(() => listDetectionRules(profileToken), [profileToken]);
  const [rule, setRule] = useState('');
  const [devices, setDevices] = useState('');
  const [windows, setWindows] = useState({ INCIDENT: { start: '', end: '' }, NORMAL: { start: '', end: '' } });
  const [rows, setRows] = useState<ExpectationRow[]>([]);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [error, setError] = useState<string | null>(null);
  const tokens = parseDevices(devices);
  const tooMany = tokens.length > 50;
  const update = (id: string, patch: Partial<ExpectationRow>) => setRows(old => old.map(r => r.id === id ? { ...r, ...patch } : r));
  const submit = async () => {
    if (inFlight.current) return;
    setError(null);
    if (tooMany || !tokens.length) { setError(t(tooMany ? 'tooManyDevices' : 'devicesRequired')); return; }
    if (rows.length > 10) { setError(t('tooManyExpectations')); return; }
    const validWindows = ROLES.every(role => {
      const w = windows[role];
      return Number.isFinite(Date.parse(w.start)) && Number.isFinite(Date.parse(w.end)) && Date.parse(w.start) < Date.parse(w.end);
    });
    if (!validWindows) { setError(t('invalidWindows')); return; }
    let expectations: ReturnType<typeof buildExpectation>[];
    try { expectations = rows.map(buildExpectation); }
    catch { setError(t('invalidExpectation')); return; }
    const input: RunAutomationReviewInput = {
      profileToken, ruleToken: rule || undefined, deviceTokens: tokens, requestId: crypto.randomUUID(), expectations,
      windows: ROLES.map(role => ({ role, start: new Date(windows[role].start).toISOString(), end: new Date(windows[role].end).toISOString() })),
    };
    inFlight.current = true; setBusy(true);
    try {
      const result = await runAutomationReview(input);
      if (!result) { setError(t('noResult')); return; }
      navigate(reviewPath(result.token));
    } catch (err) { const key = reviewErrorKey(serverErrorCode(err)); setError(key ? t(key) : errMessage(err)); }
    finally { inFlight.current = false; setBusy(false); }
  };
  return <SectionPanel title={t('run')}><form className="space-y-4" onSubmit={e => { e.preventDefault(); void submit(); }}>
    {error && <ErrorState description={error} />}{ruleError && <ErrorState description={ruleError} />}
    <FormField label={t('rule')} htmlFor="review-rule"><Select id="review-rule" value={rule} onChange={setRule}>
      <SelectItem value="">{t('automaticRule')}</SelectItem>{rules?.map(r => <SelectItem key={r.token} value={r.token}>{r.name || r.token}</SelectItem>)}
    </Select></FormField>
    <div className="grid gap-4 md:grid-cols-2">{ROLES.map(role => <fieldset key={role} className="space-y-2 rounded-md border p-3"><legend>{t(role)}</legend>
      {BOUNDS.map(bound => <FormField key={bound} label={t(bound)} htmlFor={`review-${role}-${bound}`}>
        <Input id={`review-${role}-${bound}`} type="datetime-local" required value={windows[role][bound]}
          onChange={e => setWindows(old => ({ ...old, [role]: { ...old[role], [bound]: e.target.value } }))} />
      </FormField>)}
    </fieldset>)}</div><p className="text-sm text-muted-foreground">{t('localTime')}</p>
    <FormField label={t('devices')} htmlFor="review-devices" description={t('devicesHint')}>
      <Textarea id="review-devices" value={devices} onChange={e => setDevices(e.target.value)} />
    </FormField>{tooMany && <p role="alert">{t('tooManyDevices')}</p>}
    <fieldset className="space-y-3"><legend>{t('expectations')}</legend>{rows.map((row, i) => {
      const prefix = `expect-${row.id}`;
      const command = row.shape === 'intent' || row.shape === 'neverIntent';
      const positive = row.shape === 'raised' || row.shape === 'intent';
      return <fieldset key={row.id} className="grid gap-3 rounded-md border p-3 sm:grid-cols-2"><legend>{t('expectationNumber', { count: i + 1 })}</legend>
        <FormField label={t('name')} htmlFor={`${prefix}-name`}><Input id={`${prefix}-name`} value={row.name} onChange={e => update(row.id, { name: e.target.value })} /></FormField>
        <FormField label={t('shape')} htmlFor={`${prefix}-shape`}><Select id={`${prefix}-shape`} value={row.shape} onChange={shape => update(row.id, { shape: shape as ExpectationShape })}>
          {SHAPES.map(s => <SelectItem key={s} value={s}>{t(s)}</SelectItem>)}</Select></FormField>
        <FormField label={t('window')} htmlFor={`${prefix}-window`}><Select id={`${prefix}-window`} value={row.window} onChange={window => update(row.id, { window: window as ExpectationRow['window'] })}>
          {ROLES.map(r => <SelectItem key={r} value={r}>{t(r)}</SelectItem>)}</Select></FormField>
        <FormField label={t('device')} htmlFor={`${prefix}-device`}><Input id={`${prefix}-device`} required value={row.device} onChange={e => update(row.id, { device: e.target.value })} /></FormField>
        {command ? <FormField label={t('command')} htmlFor={`${prefix}-command`}><Input id={`${prefix}-command`} required value={row.command} onChange={e => update(row.id, { command: e.target.value })} /></FormField>
          : <FormField label={t('expectationRule')} htmlFor={`${prefix}-rule`}><Input id={`${prefix}-rule`} value={row.rule} onChange={e => update(row.id, { rule: e.target.value })} /></FormField>}
        {positive && <FormField label={t('minutes')} htmlFor={`${prefix}-minutes`}><Input id={`${prefix}-minutes`} type="number" min="0.01" step={ANY_STEP} required value={row.minutes} onChange={e => update(row.id, { minutes: e.target.value })} /></FormField>}
        <Button type="button" variant="outline" onClick={() => setRows(old => old.filter(r => r.id !== row.id))}>{t('removeExpectation')}</Button>
      </fieldset>;
    })}<Button type="button" variant="outline" disabled={rows.length >= 10 || busy} onClick={() => setRows(old => [...old, newExpectation(rule)])}>{t('addExpectation')}</Button></fieldset>
    <Button type="submit" disabled={busy || tooMany} loading={busy}>{t('run')}</Button>
  </form></SectionPanel>;
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/lazy/automationReview';
import { useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { approveAutomationReview, getAutomationReview, rejectAutomationReview, type AutomationReview } from '@/lib/api/automation-reviews';
import { useQuery } from '@/lib/hooks/use-query';
import { serverErrorCode } from '@/i18n/signInError';
import { formatTime } from '@/lib/utils';
import { errMessage, useReload } from '@/routes/common';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { useToast } from '@/components/ui/toast';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { StepUpPrompt } from '@/routes/approvals/shared';
import { Outcome, ReviewTable, reviewErrorKey, reviewPath } from './shared';

const EXPECTATION_COLUMNS = ['name', 'window', 'verdict', 'reason', 'evidence'];
const DEVICE_COLUMNS = ['device', 'readings', 'startState', 'divergence', 'lateDeclines'];
const CHANGE_COLUMNS = ['device', 'kind', 'subject', 'baseTime', 'draftTime', 'shift', 'content', 'afterDivergence', 'reading'];

export default function AutomationReviewPage() {
  const { token = '' } = useParams();
  const { claims, identityToken } = useAuth();
  return <Detail key={JSON.stringify([token, claims?.tenant, claims?.username, identityToken])} token={token} />;
}

function Detail({ token }: { token: string }) {
  const { t } = useTranslation('automationReview');
  const { claims } = useAuth();
  const write = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const [version, reload] = useReload();
  const { data: review, current, loading, error } = useQuery(() => getAutomationReview(token), [token, version]);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [stepUp, setStepUp] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [note, setNote] = useState('');
  const [mutationError, setMutationError] = useState<string | null>(null);
  const approvable = (r: AutomationReview) => r.state === 'RAN' && (r.outcome === 'PASSED' || r.outcome === 'NO_AUTOMATION_CHANGE');
  const rejectable = (r: AutomationReview) => r.state === 'RAN' || r.state === 'APPROVED';
  const act = async (approve: boolean) => {
    if (!write || !current || inFlight.current || !(approve ? approvable(current) : rejectable(current))) return;
    const displayed = current;
    inFlight.current = true; setBusy(true); setMutationError(null); setStepUp(false);
    try {
      const result = approve ? await approveAutomationReview(token, displayed.reviewDigest)
        : await rejectAutomationReview(token, note || undefined);
      if (!result) { setMutationError(t('noResult')); return; }
      toast(t(approve ? 'approved' : 'rejected')); setRejecting(false); reload();
    } catch (err) {
      const code = serverErrorCode(err);
      if (code === 'STEP_UP_REQUIRED') setStepUp(true);
      else { const key = reviewErrorKey(code); setMutationError(key ? t(key) : errMessage(err)); }
    } finally { inFlight.current = false; setBusy(false); }
  };
  const copy = async () => {
    try { await navigator.clipboard.writeText(window.location.origin + reviewPath(token)); toast(t('copied')); }
    catch { setMutationError(t('copyFailed')); }
  };
  if (!review) return <PageShell title={t('title')}>{loading ? <LoadingState description={t('loading')} />
    : <ErrorState description={error || t('notFound')} />}</PageShell>;
  const outcomeStyle = review.outcome === 'PASSED' ? 'border-success bg-success/10' : review.outcome === 'FAILED'
    ? 'border-destructive bg-destructive/10' : review.outcome === 'INCONCLUSIVE' ? 'border-warning bg-warning/10' : 'border-border bg-muted';
  return <PageShell title={t('title')} description={token} titleAdornment={<Badge variant="secondary">{t('preview')}</Badge>}
    action={<><Button variant="outline" onClick={() => void copy()}>{t('copyLink')}</Button>
      {write && approvable(review) && <Button disabled={!current || busy} onClick={() => void act(true)}>{t('approve')}</Button>}
      {write && rejectable(review) && <Button variant="destructive" disabled={!current || busy} onClick={() => setRejecting(true)}>{t('reject')}</Button>}</>}>
    <div className="space-y-4">
      {error && <ErrorState description={error} />}{mutationError && <ErrorState description={mutationError} />}
      {stepUp && <StepUpPrompt path={reviewPath(token)} />}
      <div className={`rounded-md border p-4 ${outcomeStyle}`} role="status"><Outcome outcome={review.outcome} /></div>
      {review.consumedVersion != null && <p>{t('consumedVersion', { version: review.consumedVersion })}</p>}
      {review.matchesCurrentDraft === false && <div role="status" className="rounded-md border border-warning p-4">{t('stale')}</div>}
      <SectionPanel title={t('summary')}><dl className="grid gap-2 sm:grid-cols-2">
        <dt>{t('profile')}</dt><dd>{review.profileToken && <Link className="text-primary underline" to={`/device-profiles/${encodeURIComponent(review.profileToken)}`}>{review.profileToken}</Link>}</dd>
        <dt>{t('rule')}</dt><dd>{review.ruleToken}</dd><dt>{t('kind')}</dt><dd>{t(review.kind)}</dd>
        <dt>{t('state')}</dt><dd>{t(review.state)}</dd>
      </dl>
        {review.kind === 'UNREVIEWABLE' && <ul>{review.reasons?.map(r => <li key={r}>{r}</li>)}</ul>}
        <p className="mt-3">{t('fidelity')}</p>
        {review.fidelity?.status === 'UNSUPPORTED' && <><Badge variant="warning">{t('unsupported')}</Badge><ul>{review.fidelity.reasons.map(r => <li key={r}>{r}</li>)}</ul></>}
      </SectionPanel>
      {write && rejecting && <SectionPanel title={t('reject')}><form className="space-y-3" onSubmit={e => { e.preventDefault(); void act(false); }}>
        <label htmlFor="review-note">{t('note')}</label><Input id="review-note" value={note} onChange={e => setNote(e.target.value)} />
        <Button type="submit" disabled={!current || busy}>{t('submitReject')}</Button>
        <Button type="button" variant="outline" onClick={() => setRejecting(false)}>{t('cancel')}</Button>
      </form></SectionPanel>}
      <SectionPanel title={t('expectations')}><ReviewTable headings={EXPECTATION_COLUMNS.map(k => t(k))}>
        {review.expectations?.map(e => <tr key={e.index}><td>{e.name}</td><td>{t(e.window)}</td>
          <td>{t('comparison', { was: t(e.baseline), now: t(e.verdict) })}</td><td>{e.reason}</td>
          <td>{e.evidence.map((r, i) => <p key={i}>{r.device} · {formatTime(r.at)} · {r.value ?? '—'}</p>)}</td></tr>)}
      </ReviewTable></SectionPanel>
      {review.windowResults?.map(w => <WindowResult key={w.role} window={w} />)}
      <SectionPanel title={t('provenance')}><dl className="grid gap-2 break-all sm:grid-cols-2">
        <dt>{t('draftDigest')}</dt><dd>{review.draftDigest}</dd><dt>{t('baselineVersion')}</dt><dd>{review.baselineVersion}</dd>
        <dt>{t('baselineDigest')}</dt><dd>{review.baselineDigest}</dd><dt>{t('reviewDigest')}</dt><dd>{review.reviewDigest}</dd>
        <dt>{t('inputDigest')}</dt><dd>{review.inputDigest}</dd><dt>{t('engineBuild')}</dt><dd>{review.engineBuild}</dd>
        <dt>{t('created')}</dt><dd>{review.createdBy} · {formatTime(review.createdAt)}</dd>
        <dt>{t('authors')}</dt><dd>{review.draftAuthors?.join(', ')}</dd>
      </dl><h3 className="mt-4 font-medium">{t('profileChanges')}</h3><ul>{review.profileChanges?.map((c, i) =>
        <li key={i}>{t(c.area)} · {c.key} · {t(c.change)} {!c.automation && <Badge variant="outline">{t('nonAutomation')}</Badge>}</li>)}</ul>
      </SectionPanel>
      <SectionPanel title={t('approvals')}><p>{t('approvalCount', { done: review.approvals?.length ?? 0, required: review.requiredApprovals })}</p>
        <ul>{review.approvals?.map(a => <li key={a.principal}>{a.principal} · {formatTime(a.approvedAt)}</li>)}</ul>
        <p>{review.decidedBy} {formatTime(review.decidedAt)} {review.note}</p>
      </SectionPanel>
    </div>
  </PageShell>;
}

function WindowResult({ window: w }: { window: NonNullable<AutomationReview['windowResults']>[number] }) {
  const { t } = useTranslation('automationReview');
  const content = (c: (typeof w.changes)[number]['baseContent']) => c ? <dl className="space-y-1">
    {c.payload != null && <><dt className="font-medium">{t('payload')}</dt><dd><pre className="whitespace-pre-wrap">{c.payload}</pre></dd></>}
    {c.severity != null && <><dt className="font-medium">{t('severity')}</dt><dd>{c.severity}</dd></>}
    {c.relativeExpirySeconds != null && <><dt>{t('relativeExpiry')}</dt><dd>{t('seconds', { count: c.relativeExpirySeconds })}</dd></>}
    {c.expiryLabel && <><dt>{t('expiry')}</dt><dd>{c.expiryLabel}</dd></>}
    {c.configDigest && <><dt>{t('configDigest')}</dt><dd>{c.configDigest}</dd></>}
    {c.bodyDigest && <><dt>{t('bodyDigest')}</dt><dd>{c.bodyDigest}</dd></>}
    {c.method && <><dt>{t('method')}</dt><dd>{c.method}</dd></>}
  </dl> : '—';
  return <SectionPanel title={t(w.role)}><div className="space-y-3">
    <p>{t('bounds', { start: formatTime(w.start), end: formatTime(w.end), lead: formatTime(w.leadInStart) })}</p>
    <ul>{w.degraded.map(r => <li key={r}>{r}</li>)}</ul>
    <p>{t('counts', w.counts)}</p>
    <ReviewTable headings={DEVICE_COLUMNS.map(k => t(k))}>
      {w.devices.map(d => <tr key={d.device}><td>{d.device}</td><td>{d.readings.map(r => <p key={r.key}>{r.key}: {r.count}</p>)}</td>
        <td>{t('baseDraft', { base: t(d.stateBase), draft: t(d.stateDraft) })}</td><td>{formatTime(d.divergence)}</td>
        <td>{t('baseDraft', { base: d.lateBase, draft: d.lateDraft })}</td></tr>)}
    </ReviewTable>
    {w.changesTruncated && <p role="status" className="text-warning">{t('truncated')}</p>}
    <ReviewTable headings={CHANGE_COLUMNS.map(k => t(k))}>
      {w.changes.map((c, i) => <tr key={i}><td>{c.device}</td><td>{t(c.kind)}</td><td>{c.subject} · {c.discriminator}</td>
        <td>{formatTime(c.baseAt)}</td><td>{formatTime(c.draftAt)}</td><td>{t('seconds', { count: c.shiftSeconds })}</td>
        <td className="min-w-64 max-w-lg break-all"><div className="grid gap-3">
          <div><p className="font-medium">{t('base')}</p>{content(c.baseContent)}</div>
          <div><p className="font-medium">{t('draft')}</p>{content(c.draftContent)}</div>
        </div></td>
        <td>{t(c.afterDivergence ? 'yes' : 'no')}</td><td>{c.reading.device} · {c.reading.value ?? '—'} · {formatTime(c.reading.at)}</td></tr>)}
    </ReviewTable>
    <dl className="grid gap-2 break-all sm:grid-cols-2"><dt>{t('stream')}</dt><dd>{w.stream}</dd>
      <dt>{t('streamBounds')}</dt><dd>{w.firstSeq} → {w.lastSeq}</dd><dt>{t('inputDigest')}</dt><dd>{w.inputDigest}</dd></dl>
  </div></SectionPanel>;
}

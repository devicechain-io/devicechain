// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/lazy/automationReview';
import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority, isScoped } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { getAutomationReviewPolicy, setAutomationReviewPolicy, approveAutomationReviewPolicyChange,
  cancelAutomationReviewPolicyChange, listAutomationReviewBreakGlass, type AutomationReviewPolicy } from '@/lib/api/automation-reviews';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { errMessage, useReload } from '@/routes/common';
import { serverErrorCode } from '@/i18n/signInError';
import { StepUpPrompt } from '@/routes/approvals/shared';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Input } from '@/components/ui/input';
import { SectionPanel } from '@/components/ui/section-panel';
import { ErrorState } from '@/components/ui/error-state';
import { reviewErrorKey } from './shared';

const ANY_STEP = 'any';
type Settings = AutomationReviewPolicy['effective'];
export function ProtectionSection({ profileToken }: { profileToken: string }) {
  const { t } = useTranslation('automationReview');
  const { claims } = useAuth();
  const write = hasAuthority(claims, 'device:write') && !isScoped(claims);
  const [revision, reload] = useReload();
  const { current: policy, error } = useQuery(() => getAutomationReviewPolicy(profileToken), [profileToken, revision]);
  const { current: records, error: recordError } = useQuery(() => listAutomationReviewBreakGlass(profileToken), [profileToken, revision]);
  const [editing, setEditing] = useState(false);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [message, setMessage] = useState('');
  const [failure, setFailure] = useState('');
  const [stepUp, setStepUp] = useState(false);
  const pending = policy?.pendingChange?.state === 'PENDING' ? policy.pendingChange : null;
  const notice = (p: AutomationReviewPolicy | null | undefined) => p?.pendingChange?.effectiveAt
    ? t('policyDelayed', { time: formatTime(p.pendingChange.effectiveAt) }) : t('policyWaiting');
  const act = async (action: 'save' | 'approve' | 'cancel') => {
    if (!write || !policy || inFlight.current) return;
    inFlight.current = true; setBusy(true); setFailure(''); setStepUp(false);
    try {
      if (action === 'save' && settings) {
        const result = await setAutomationReviewPolicy(profileToken, settings);
        if (!result?.policy) throw new Error(t('noResult'));
        setMessage(result.applied ? t('policyApplied') : notice(result.policy)); setEditing(false);
      } else if (pending) {
        const result = action === 'approve' ? await approveAutomationReviewPolicyChange(pending.token)
          : await cancelAutomationReviewPolicyChange(pending.token);
        if (!result) throw new Error(t('noResult'));
        setMessage(t(action === 'cancel' ? 'policyCancelled' : 'policyApplied'));
      }
      reload();
    } catch (err) {
      const code = serverErrorCode(err);
      if (code === 'STEP_UP_REQUIRED' || code === 'SECOND_FACTOR_REQUIRED') setStepUp(true);
      else { const key = reviewErrorKey(code); setFailure(key ? t(key) : errMessage(err)); }
    } finally { inFlight.current = false; setBusy(false); }
  };
  const approve = () => act('approve');
  const cancel = () => act('cancel');
  return <SectionPanel title={t('protection')}>
    {error && <ErrorState description={error} />}{failure && <ErrorState description={failure} />}
    {message && <p role="status">{message}</p>}
    {stepUp && <StepUpPrompt path={`/device-profiles/${encodeURIComponent(profileToken)}?tab=change-review`} />}
    {policy && <div className="space-y-3">
      <p>{t(policy.effective.required ? 'protected' : 'notProtected')}</p>
      <SettingsSummary settings={policy.effective} />
      <p>{t('policyRevision', { revision: policy.revision })} · {policy.updatedBy} · {formatTime(policy.updatedAt)}</p>
      {write && <Button disabled={busy} onClick={() => { setSettings({ ...policy.effective }); setEditing(true); }}>{t('editProtection')}</Button>}
      {write && editing && settings && <form className="space-y-3" onSubmit={e => { e.preventDefault(); void act('save'); }}>
        <label className="block"><Checkbox checked={settings.required} onCheckedChange={checked => setSettings({ ...settings, required: checked === true })} /> {t('requireReview')}</label>
        <label className="block">{t('requiredApprovals')}<Input type="number" min={1} max={2} required value={settings.requiredApprovals}
          onChange={e => setSettings({ ...settings, requiredApprovals: Number(e.target.value) })} /></label>
        <label className="block"><Checkbox checked={settings.allowSelfApproval} onCheckedChange={checked => setSettings({ ...settings, allowSelfApproval: checked === true })} /> {t('allowSelfApproval')}</label>
        <label className="block">{t('approvalHours')}<Input type="number" min={0.25} max={168} step={ANY_STEP} required value={settings.approvalTtlSeconds / 3600}
          onChange={e => setSettings({ ...settings, approvalTtlSeconds: Math.round(Number(e.target.value) * 3600) })} /></label>
        <label className="block">{t('factorMinutes')}<Input type="number" min={1} max={60} step={ANY_STEP} required value={settings.stepUpMaxAgeSeconds / 60}
          onChange={e => setSettings({ ...settings, stepUpMaxAgeSeconds: Math.round(Number(e.target.value) * 60) })} /></label>
        <Button type="submit" disabled={busy}>{t('saveProtection')}</Button>
        <Button type="button" variant="outline" disabled={busy} onClick={() => setEditing(false)}>{t('cancel')}</Button>
      </form>}
      {pending && <div className="space-y-2 rounded-md border p-3">
        <h3>{t('pendingProtection')}</h3>{pending.proposed && <SettingsSummary settings={pending.proposed} />}
        <p>{pending.proposedBy} · {formatTime(pending.proposedAt)}</p><p>{notice(policy)}</p>
        {write && <>
          {pending.proposedBy !== claims?.username && <Button disabled={busy} onClick={() => void approve()}>{t('approve')}</Button>}
          <Button variant="outline" disabled={busy} onClick={() => void cancel()}>{t('cancelPolicyChange')}</Button>
        </>}
      </div>}
    </div>}
    <h3 className="mt-4 font-medium">{t('breakGlassRecord')}</h3>
    {recordError && <ErrorState description={recordError} />}
    {records && !records.length && <p>{t('noBreakGlass')}</p>}
    <ul>{records?.slice(0, 10).map((r, i) => <li key={i} className="border-b py-2">
      {t(r.action)} · {r.target} · {r.reason} · {r.actor} · {formatTime(r.at)}
    </li>)}</ul>
  </SectionPanel>;
}
function SettingsSummary({ settings: s }: { settings: Settings }) {
  const { t } = useTranslation('automationReview');
  return <p>{t(s.required ? 'protected' : 'notProtected')} · {t('approvalsSetting', { count: s.requiredApprovals })} · {t(s.allowSelfApproval ? 'selfApprovalAllowed' : 'selfApprovalDisallowed')} · {t('lifetimeSetting', { hours: s.approvalTtlSeconds / 3600 })} · {t('factorSetting', { minutes: s.stepUpMaxAgeSeconds / 60 })}</p>;
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type ReactNode } from 'react';
import { StepUpDialog } from '@/components/StepUpDialog';
import { useTranslation } from 'react-i18next';
import { hasAuthority, isScoped } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { getAgentPolicy } from '@/lib/api/agent-policy';
import { useQuery } from '@/lib/hooks/use-query';
import { setStepUpReturn } from '@/lib/stepUpReturn';
import { Button } from '@/components/ui/button';
import { PageShell } from '@/components/ui/page-shell';
import { EmptyState } from '@/components/ui/empty-state';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';

export function StepUpPrompt({ path }: { path: string }) {
  const { t } = useTranslation('approvals');
  const { logout } = useAuth();
  const [open, setOpen] = useState(false);
  const [fallback, setFallback] = useState(false);
  const [verified, setVerified] = useState(false);
  return <div role="status" className="space-y-2 rounded-md border border-warning p-4">
    <p>{t(verified ? 'stepUpSuccess' : 'stepUp')}</p>
    {fallback ? <><p>{t('stepUpUnavailable')}</p><Button onClick={() => { setStepUpReturn(path); void logout(); }}>{t('signInAgain')}</Button></>
      : !verified && <Button onClick={() => setOpen(true)}>{t('verifyCode')}</Button>}
    <StepUpDialog open={open} onOpenChange={setOpen} onSuccess={() => setVerified(true)} onUnavailable={() => setFallback(true)} />
  </div>;
}

export function ApprovalsGate({ children }: { children: ReactNode }) {
  const { claims, identityToken } = useAuth();
  const { t } = useTranslation('approvals');
  if (!hasAuthority(claims, 'agent:admin') || isScoped(claims)) {
    return <PageShell title={t('title')}><EmptyState description={t('notAuthorized')} /></PageShell>;
  }
  // Drop policy, proposals and local decision state when another tab changes the session.
  const sessionKey = JSON.stringify([claims?.tenant, claims?.username, identityToken]);
  return <InstanceGate key={sessionKey}>{children}</InstanceGate>;
}
function InstanceGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation('approvals');
  const { data, current, loading, error } = useQuery(getAgentPolicy);
  if (!data) return <PageShell title={t('title')}>{loading
    ? <LoadingState description={t('loading')} /> : <ErrorState description={error!} />}</PageShell>;
  if (!current?.instanceEnabled) return <PageShell title={t('title')}>
    {error ? <ErrorState description={error} /> : <EmptyState description={t('notEnabled')} />}
  </PageShell>;
  return children;
}

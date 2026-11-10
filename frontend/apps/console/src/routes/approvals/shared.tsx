// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
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
  return <div role="status" className="space-y-2 rounded-md border border-warning p-4">
    <p>{t('stepUp')}</p><p className="text-sm">{t('stepUpHelp')}</p>
    <Button onClick={() => { setStepUpReturn(path); logout(); }}>{t('signInAgain')}</Button>
  </div>;
}

export function ApprovalsGate({ children }: { children: ReactNode }) {
  const { claims } = useAuth();
  const { t } = useTranslation('approvals');
  if (!hasAuthority(claims, 'agent:admin') || isScoped(claims)) {
    return <PageShell title={t('title')}><EmptyState description={t('notAuthorized')} /></PageShell>;
  }
  return <InstanceGate>{children}</InstanceGate>;
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

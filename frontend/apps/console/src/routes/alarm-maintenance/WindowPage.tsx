// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { CopyToken } from '@/components/ui/copy-token';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { getWindow, listWindows, endWindow, deleteWindow } from '@/lib/api/alarm-maintenance';
import { errMessage } from '@/routes/common';
import { WindowForm } from './WindowForm';
import { isBroad } from './rules';

export default function WindowPage() {
  const { token } = useParams();
  return <WindowDetail key={token ?? 'new'} token={token} />;
}
function WindowDetail({ token }: { token?: string }) {
  const { t } = useTranslation('alarmMaintenance');
  const { claims } = useAuth();
  const read = hasAuthority(claims, 'alarm:read');
  const navigate = useNavigate();
  const confirm = useConfirm();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const query = useQuery(async () => {
    if (!read) return null;
    return token ? getWindow(token) : (await listWindows({ pageNumber: 1, pageSize: 1 })) ? true : null;
  }, [token, read], [token, read]);
  const entity = typeof query.data === 'object' && query.data !== null ? query.data : undefined;
  const canWrite = hasAuthority(claims, 'alarm:write') && (!entity || !isBroad(entity) || hasAuthority(claims, 'notification:write'));
  const act = async (remove: boolean) => {
    if (!entity || !query.current || !canWrite || busy) return;
    if (remove && !await confirm({ title: t('delete'), description: t('deleteConfirm', { name: entity.name || entity.token }) })) return;
    setBusy(true); setError(null);
    try {
      if (remove) { await deleteWindow(entity.token); navigate('/alarm-maintenance'); }
      else { await endWindow(entity.token); query.refetch(); }
    } catch (err) { setError(errMessage(err)); }
    finally { setBusy(false); }
  };
  return <PageShell title={entity?.name || token || t('create')} titleAdornment={entity?.name ? <CopyToken value={entity.token} /> : undefined}
    description={entity ? t(`status${entity.status}`) : undefined}
    action={entity ? <>
      <Button variant="outline" disabled={!canWrite || !query.current || busy || entity.status === 'ENDED'} onClick={() => act(false)}>{t('end')}</Button>
      <Button variant="destructive" disabled={!canWrite || !query.current || busy} onClick={() => act(true)}>{t('delete')}</Button>
    </> : undefined}>
    {error && <ErrorBanner message={error} />}
    {query.loading && !query.data ? <LoadingState /> : query.error && !query.data ? <ErrorState description={query.error} /> : query.data ?
      <SectionPanel><WindowForm entity={entity} onDone={saved => { if (!token) navigate(`/alarm-maintenance/${encodeURIComponent(saved)}`); else query.refetch(); }} /></SectionPanel> : null}
  </PageShell>;
}

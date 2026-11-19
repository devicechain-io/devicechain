// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { lazy, Suspense, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import { deviceWindows, listWindows, endWindow, updateWindow } from '@/lib/api/alarm-maintenance';
import { errMessage } from '@/routes/common';
import { HOUR, isBroad, windowUntil, type Target } from './rules';

const StartDialog = lazy(() => import('./StartMaintenanceDialog'));
function useWindows(target: Target) {
  const { claims } = useAuth();
  const read = hasAuthority(claims, 'alarm:read');
  const query = useQuery(async () => {
    if (!read) return null;
    if (target.type === 'DEVICE') return deviceWindows(target.token!);
    const page = await listWindows({ pageNumber: 1, pageSize: 100, status: 'ACTIVE', targetToken: target.token });
    return page?.results.filter(w => w.targets.some(t => t.type === target.type && t.token === target.token)) ?? null;
  }, [target.type, target.token, read], [target.type, target.token, read]);
  useEffect(() => {
    const timer = window.setInterval(query.refetch, 30000);
    const changed = () => query.refetch();
    window.addEventListener('alarm-maintenance-changed', changed);
    return () => { window.clearInterval(timer); window.removeEventListener('alarm-maintenance-changed', changed); };
  }, [query.refetch]);
  return query;
}
function changed() { window.dispatchEvent(new Event('alarm-maintenance-changed')); }

export function StartMaintenanceAction({ target }: { target: Target }) {
  const { t } = useTranslation('alarmMaintenance');
  const { claims } = useAuth();
  const query = useWindows(target);
  const [open, setOpen] = useState(false);
  if (query.data == null || query.error || !hasAuthority(claims, 'alarm:write')) return null;
  return <>
    <Button size="sm" variant="outline" onClick={() => setOpen(true)} data-shot="start-alarm-maintenance">{t('start')}</Button>
    {open && <Suspense fallback={null}><StartDialog target={target} onClose={() => setOpen(false)} onDone={() => { setOpen(false); changed(); }} /></Suspense>}
  </>;
}
export function MaintenanceBanner({ target }: { target: Target }) {
  const { t, i18n } = useTranslation('alarmMaintenance');
  const { claims } = useAuth();
  const query = useWindows(target);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const alarmWrite = hasAuthority(claims, 'alarm:write');
  const notificationWrite = hasAuthority(claims, 'notification:write');
  if (query.data == null || query.error) return null;
  const act = async (operation: () => Promise<unknown>) => {
    setBusy(true); setError(null);
    try { await operation(); changed(); }
    catch (err) { setError(errMessage(err)); }
    finally { setBusy(false); }
  };
  return <div className="space-y-2">
    {error && <ErrorBanner message={error} />}
    {query.data.filter(w => w.status === 'ACTIVE').map(w => {
      const until = windowUntil(w);
      if (!until || Date.parse(until) <= Date.now()) return null;
      const extension = new Date(Date.parse(until) + HOUR).toISOString();
      // End only restores paging: alarm:write plus seeing the window. Extending widens suppression and still needs reach.
      const canEnd = alarmWrite;
      const canExtend = alarmWrite && (!isBroad({ ...w, endTime: extension }) || notificationWrite);
      const fresh = !!query.current?.some(row => row.token === w.token);
      return <div key={w.token} className="flex flex-wrap items-center gap-3 rounded-md border bg-muted p-3" data-shot="alarm-maintenance-banner">
        <span>{t('banner', { time: new Date(until).toLocaleString(i18n.language), reason: w.reason || w.name })}</span>
        <Button size="sm" variant="outline" disabled={!canEnd || !fresh || busy} onClick={() => act(() => endWindow(w.token))}>{t('end')}</Button>
        {w.kind === 'ONE_SHOT' && <Button size="sm" variant="outline" disabled={!canExtend || !fresh || busy} onClick={() => act(() => updateWindow(w.token, { endTime: extension }))}>{t('extend')}</Button>}
      </div>;
    })}
  </div>;
}

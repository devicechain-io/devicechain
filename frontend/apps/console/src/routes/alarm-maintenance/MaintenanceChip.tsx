// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/lazy/alarmMaintenance';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Badge } from '@/components/ui/badge';
import { useQuery } from '@/lib/hooks/use-query';
import { suppressionStatus, suppressionHistoryMarks, type Suppression } from '@/lib/api/alarm-maintenance';

export function useSuppressions(tokens: string[], revision?: unknown) {
  const key = JSON.stringify(tokens);
  const query = useQuery(async () => {
    const [status, marks] = await Promise.all([suppressionStatus(tokens), suppressionHistoryMarks(tokens)]);
    return { status, marks };
  }, [key, revision], [key]);
  const data = query.error ? null : query.data;
  return {
    suppressions: new Map((data?.status ?? []).map(r => [r.alarmToken, r.suppression])),
    historyMarks: new Map((data?.marks ?? []).map(r => [r.token, r.raisedDuringSuppression])),
  };
}
export function MaintenanceChip({ suppression }: { suppression?: Suppression | null }) {
  const { t, i18n } = useTranslation('alarmMaintenance');
  if (!suppression || Date.parse(suppression.until) <= Date.now()) return null;
  const visible = suppression.windowToken !== 'restricted' && suppression.window != null;
  const title = visible ? suppression.window!.name || suppression.window!.reason : t('restricted');
  const label = t('chip', { time: new Date(suppression.until).toLocaleTimeString(i18n.language, { hour: '2-digit', minute: '2-digit' }) });
  return <Badge variant="secondary" title={title} data-shot="alarm-maintenance-chip">
    {visible ? <Link to={`/alarm-maintenance/${encodeURIComponent(suppression.window!.token)}`}>{label}</Link> : label}
  </Badge>;
}

/** Cycle history remains visible after clearing or the maintenance window ending. */
export function MaintenanceHistoryMark({ stamp }: { stamp?: string | null }) {
  const { t } = useTranslation('alarmMaintenance');
  return stamp == null ? null : <Badge variant="outline">{t('historyMark')}</Badge>;
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { FileWarning } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import { getFailedEventCount } from '@/lib/api/ingest-failures';
import '@/i18n/lazy/ingestFailures';

/**
 * "N events from this device were rejected in the last 24 hours", with a way to the reasons.
 * Silent when there are none, and silent when the count cannot be read: this is a hint added to a
 * page that works without it, and an error banner here would read as the device being broken.
 */
export function DeviceRejectedNotice({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation('ingestFailures');
  const { data } = useQuery(
    () => getFailedEventCount(deviceToken, new Date(Date.now() - 24 * 3600_000).toISOString()),
    [deviceToken],
  );
  if (!data || data <= 0) return null;
  return (
    <div
      role="status"
      className="mb-4 flex items-center gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-foreground"
    >
      <FileWarning size={16} className="shrink-0" aria-hidden />
      <span>{t('deviceCallout', { count: data })}</span>
      <Link
        className="ml-auto underline underline-offset-2"
        to={`/ingest-failures?device=${encodeURIComponent(deviceToken)}`}
      >
        {t('deviceCalloutLink')}
      </Link>
    </div>
  );
}

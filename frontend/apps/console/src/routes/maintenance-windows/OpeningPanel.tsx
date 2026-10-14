// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the SERVER says about a maintenance window right now: open (and until when) or
// next opening. The console decides nothing here and computes no schedule of its own —
// every instant shown comes from the `maintenanceWindowOpening` query and is only
// rendered in the browser's locale.

import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { getMaintenanceWindowOpening } from '@/lib/api/maintenance-windows';

export function OpeningPanel({ token }: { token: string }) {
  const { t } = useTranslation('entities');
  const { data, loading, error } = useQuery(() => getMaintenanceWindowOpening(token), [token]);

  let body: string;
  if (loading && !data) body = t('maintenanceWindowOpeningLoading');
  else if (error && !data) body = t('maintenanceWindowOpeningError');
  else if (!data || !data.found) body = t('maintenanceWindowOpeningNotFound');
  else if (data.openNow) body = t('maintenanceWindowOpeningOpen', { closes: formatTime(data.closes) });
  else body = t('maintenanceWindowOpeningNext', { opens: formatTime(data.dispatchAfter) });

  return (
    <div className="rounded-md border p-3" data-testid="window-opening">
      <p className="text-sm font-medium">{t('maintenanceWindowOpeningTitle')}</p>
      <p className="text-muted-foreground mt-1 text-sm">{body}</p>
    </div>
  );
}

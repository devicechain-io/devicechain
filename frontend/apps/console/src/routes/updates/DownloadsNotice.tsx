// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A banner for the one condition that makes every assignment on the instance silently useless:
// the service has no download base URL, so it can never give a device a link to fetch an image
// from. The assignment is published, the device asks, the request is refused and counted, and
// the attempt sits at its first state. Without this the operator sees a fleet that is merely slow.
//
// It speaks only when the service answered and said NO. A read that is pending or failed is not
// "not configured": stating a fault that was not read would be the louder lie.

import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { getUpdateManagementInfo } from '@/lib/api/update-management';

export function DownloadsNotice() {
  const { t } = useTranslation('updates');
  const info = useQuery(getUpdateManagementInfo, []);
  if (info.data?.downloadsConfigured !== false) return null;
  return (
    <div
      role="alert"
      data-testid="downloads-notice"
      className="rounded-md border border-warning/40 bg-warning/10 p-4 text-sm text-foreground"
    >
      <p className="font-medium">{t('downloadsNotConfiguredTitle')}</p>
      <p className="mt-1">{t('downloadsNotConfiguredBody')}</p>
    </div>
  );
}

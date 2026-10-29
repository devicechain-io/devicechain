// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A banner for the other condition that makes every artifact action on the instance useless:
// the update service has no object store, so it refuses every artifact declaration and upload.
// The service is in the default profile and a store cannot always be shared with it, so an
// instance can run it without one; without this the operator meets a refusal on each action.
//
// It speaks only when the service answered and said NO. A read that is pending or failed is not
// "no store": stating a fault that was not read would be the louder lie.

import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { getUpdateManagementInfo } from '@/lib/api/update-management';
import '@/i18n/lazy/updates';

export function StoreNotice() {
  const info = useQuery(getUpdateManagementInfo, []);
  return <StoreNoticeView objectStoreConfigured={info.data?.objectStoreConfigured} />;
}

// The banner itself, for a screen that has already read the service info and should not read it twice.
export function StoreNoticeView({ objectStoreConfigured }: { objectStoreConfigured: boolean | undefined }) {
  const { t } = useTranslation('updates');
  if (objectStoreConfigured !== false) return null;
  return (
    <div
      role="alert"
      data-testid="store-notice"
      className="rounded-md border border-warning/40 bg-warning/10 p-4 text-sm text-foreground"
    >
      <p className="font-medium">{t('storeNotConfiguredTitle')}</p>
      <p className="mt-1">{t('storeNotConfiguredBody')}</p>
    </div>
  );
}

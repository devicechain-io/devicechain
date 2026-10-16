// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import {
  tokenColumn,
  nameColumn,
  type RegistryResource,
} from '@/components/registry';
import {
  listNotificationPolicyRecords,
  getNotificationPolicy,
  deleteNotificationPolicy,
  type NotificationPolicyRecord,
} from '@/lib/api/notifications';
import { AREA } from '@/lib/capabilities';
import { NotificationPolicyForm } from './NotificationPolicyForm';

function EnabledCell({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation('common');
  return <span>{enabled ? t('enabled') : t('disabled')}</span>;
}

// The notification-policy registry, described once for the generic list/detail/new pages.
export const notificationPolicyResource: RegistryResource<NotificationPolicyRecord> = {
  basePath: '/notification-policies',
  i18nKey: 'notificationPolicy',
  banner: 'dashboard',
  // notification-management is absent from the telemetry and ingest-only profiles.
  area: AREA.notificationManagement,
  list: listNotificationPolicyRecords,
  load: getNotificationPolicy,
  remove: deleteNotificationPolicy,
  idOf: (p) => p.id,
  tokenOf: (p) => p.token,
  nameOf: (p) => p.name,
  columns: [
    tokenColumn<NotificationPolicyRecord>(),
    nameColumn<NotificationPolicyRecord>(),
    {
      header: 'entities:notificationPolicyColEnabled',
      cell: (p) => <EnabledCell enabled={p.enabled} />,
    },
    {
      header: 'entities:notificationPolicyColRules',
      cell: (p) => <span className="tabular-nums">{p.rules.length}</span>,
    },
    {
      header: 'entities:notificationPolicyColQuietHours',
      // The zone the quiet hours are read in, or an em dash when the policy has none.
      cell: (p) =>
        p.quietHoursTimezone ? (
          <span className="font-mono text-xs">{p.quietHoursTimezone}</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
  ],
  // Keyed on the entity: the form seeds its fields from the entity once.
  renderForm: (p, onDone) => (
    <NotificationPolicyForm key={p?.id ?? 'new'} entity={p} onDone={onDone} />
  ),
};

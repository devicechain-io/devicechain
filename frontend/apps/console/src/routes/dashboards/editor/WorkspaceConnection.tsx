// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { DashboardHub } from '@devicechain/dashboards';
import { HubConnectionBadge, useHubConnectionState } from '@devicechain/widgets';
import { useTranslation } from 'react-i18next';
import '@/i18n/lazy/dashboards';

// The dashboard header's live-data state. Shown only against the live hub: the synthetic
// preview has no connection to report.
export function WorkspaceConnection({ hub }: { hub: DashboardHub }) {
  const { t } = useTranslation('dashboards');
  const state = useHubConnectionState(hub);
  return (
    <span title={t('workspaceConnectionLabel')} className="shrink-0">
      <HubConnectionBadge
        state={state}
        labels={{
          live: t('workspaceConnectionLive'),
          reconnecting: t('workspaceConnectionReconnecting'),
          offline: t('workspaceConnectionOffline'),
        }}
      />
    </span>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The header's live-data state: live, reconnecting or offline. Kept apart from App.tsx so the
// header gains one element and nothing else.

import type { DashboardHub } from '@devicechain/dashboards';
import { HubConnectionBadge, useHubConnectionState } from '@devicechain/widgets';
import { useTranslation } from 'react-i18next';

export function ConnectionStatus({ hub }: { hub: DashboardHub }) {
  const { t } = useTranslation('view');
  const state = useHubConnectionState(hub);
  return (
    <span title={t('connectionLabel')} style={{ flex: '0 0 auto' }}>
      <HubConnectionBadge
        state={state}
        labels={{
          live: t('connectionLive'),
          reconnecting: t('connectionReconnecting'),
          offline: t('connectionOffline'),
        }}
      />
    </span>
  );
}

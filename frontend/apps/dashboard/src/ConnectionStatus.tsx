// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The header's live-data state: live, reconnecting or offline. Kept apart from App.tsx so the
// header gains one element and nothing else.

import type { DashboardHub } from '@devicechain/dashboards';
import { HubConnectionBadge, useHubConnectionState } from '@devicechain/widgets';
import { useTranslation } from 'react-i18next';

// `onlyWhenDown` (a kiosk with ?chrome=0) leaves the badge out while the board is LIVE, and
// only then: a capture wants no badge on a healthy board, but a board whose socket dropped,
// or could not re-authenticate, must never look live.
export function ConnectionStatus({ hub, onlyWhenDown = false }: { hub: DashboardHub; onlyWhenDown?: boolean }) {
  const { t } = useTranslation('view');
  const state = useHubConnectionState(hub);
  if (onlyWhenDown && state === 'live') return null;
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

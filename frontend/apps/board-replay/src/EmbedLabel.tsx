// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';

// The embed's label: the fixed phrase that this is a replay and, re-timed, that its times were
// moved to now. The embed draws it in every state, and on the canvas with the board, so no
// scale or state of the embed shows the board without it.
export function EmbedLabel({ retimed }: { retimed: boolean }) {
  const { t } = useTranslation();
  return (
    <header
      data-testid="replay-label"
      style={{ flex: '0 0 auto', padding: '6px 12px', fontSize: 13, fontWeight: 600, color: 'hsl(var(--muted-foreground))' }}
    >
      {retimed ? t('embedLabelRetimed') : t('replayBadge')}
    </header>
  );
}

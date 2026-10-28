// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import type { TrustAnchor } from '@/lib/api/trust-anchors';
import { isAnchorExpired } from './anchorRules';

/** Why an anchor would or would not vouch for a chain right now, as badges. */
export function AnchorBadges({ anchor }: { anchor: Pick<TrustAnchor, 'enabled' | 'notAfter'> }) {
  const { t } = useTranslation('trustAnchors');
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {anchor.enabled ? (
        <Badge variant="success">{t('enabledBadge')}</Badge>
      ) : (
        <Badge variant="warning">{t('disabledBadge')}</Badge>
      )}
      {isAnchorExpired(anchor.notAfter) && <Badge variant="destructive">{t('expiredBadge')}</Badge>}
    </span>
  );
}

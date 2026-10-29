// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import type { Integration } from '@/lib/api/integrations';
import '@/i18n/lazy/integrations';

/**
 * Why an integration would or would not be taking traffic right now, as badges: switched off, a
 * sender that is reaching it with a wrong token, and uplinks that arrived with nothing to read.
 */
export function IntegrationBadges({
  integration,
}: {
  integration: Pick<Integration, 'enabled' | 'undecodedCount' | 'lastAuthFailureAt'>;
}) {
  const { t } = useTranslation('integrations');
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {!integration.enabled && <Badge variant="warning">{t('disabledBadge')}</Badge>}
      {integration.lastAuthFailureAt && (
        <Badge variant="destructive" title={t('authFailureHint')}>
          {t('authFailureBadge')}
        </Badge>
      )}
      {integration.undecodedCount > 0 && (
        <Badge variant="warning" title={t('undecodedHint')}>
          {t('undecodedBadge', { count: integration.undecodedCount })}
        </Badge>
      )}
    </span>
  );
}

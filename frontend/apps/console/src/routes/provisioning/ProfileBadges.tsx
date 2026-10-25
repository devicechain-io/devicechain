// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import type { ProvisioningProfile } from '@/lib/api/provisioning';
import { isExpired } from './profileForm';

/**
 * Why a profile would or would not take a registration right now, as badges. "Rotate required"
 * is the loudest: a profile whose secret was chosen by an operator (every profile from before
 * the server generated secrets) is refused at the endpoint with the same generic answer as a
 * wrong secret until it is rotated, so the operator would see devices fail and no reason.
 */
export function ProfileBadges({ profile }: { profile: Pick<ProvisioningProfile, 'enabled' | 'expiresAt' | 'secretIssued'> }) {
  const { t } = useTranslation('provisioning');
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {!profile.secretIssued && (
        <Badge variant="destructive" title={t('rotateRequiredHint')}>
          {t('rotateRequiredBadge')}
        </Badge>
      )}
      {!profile.enabled && <Badge variant="warning">{t('disabledBadge')}</Badge>}
      {isExpired(profile.expiresAt) && <Badge variant="warning">{t('expiredBadge')}</Badge>}
    </span>
  );
}

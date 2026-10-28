// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/routes/trust-anchors/catalog';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import type { ProvisioningProfile } from '@/lib/api/provisioning';
import { isExpired } from './profileForm';

/**
 * Why a profile would or would not take a registration right now, as badges. "Rotate required"
 * is the loudest: a profile whose secret was chosen by an operator (every profile from before
 * the server generated secrets) is refused at the endpoint with the same generic answer as a
 * wrong secret until it is rotated, so the operator would see devices fail and no reason.
 * A certificate profile has no secret to rotate and is badged as such instead.
 */
export function ProfileBadges({
  profile,
}: {
  profile: Pick<ProvisioningProfile, 'enabled' | 'expiresAt' | 'secretIssued' | 'trustAnchor'>;
}) {
  const { t } = useTranslation('provisioning');
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {profile.trustAnchor && <Badge variant="secondary">{t('trustAnchors:certificateBadge')}</Badge>}
      {/* A certificate profile never has a secret, so "rotate required" would be a false alarm. */}
      {!profile.secretIssued && !profile.trustAnchor && (
        <Badge variant="destructive" title={t('rotateRequiredHint')}>
          {t('rotateRequiredBadge')}
        </Badge>
      )}
      {!profile.enabled && <Badge variant="warning">{t('disabledBadge')}</Badge>}
      {isExpired(profile.expiresAt) && <Badge variant="warning">{t('expiredBadge')}</Badge>}
    </span>
  );
}

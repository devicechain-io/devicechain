// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { formatTime } from '@/lib/utils';
import type { DeviceCredential } from '@/lib/api/credentials';
import { EXPIRY_WARN_DAYS, daysUntil } from '@/routes/trust-anchors/anchorRules';

/**
 * What the credential list shows for an X509_CERTIFICATE row: the subject CN, the pin the server
 * computed, the trust anchor its chain must reach (and whether that anchor is withdrawn), and the
 * expiry. The pin is public, so unlike a secret it is shown in full.
 */
export function X509CredentialDetails({ credential: c }: { credential: DeviceCredential }) {
  const { t } = useTranslation('trustAnchors');
  const cert = c.certificate;
  if (!cert) {
    // A row from before certificate credentials were chain-validated has no anchor and never
    // authenticates; say so rather than render an empty cell.
    return <div className="text-xs text-muted-foreground">{t('credentialLegacy')}</div>;
  }
  const anchor = cert.trustAnchor;
  return (
    <div className="space-y-1">
      <div className="text-xs text-foreground">{t('credentialCn', { cn: cert.subjectCn })}</div>
      <div className="break-all font-mono text-xs text-muted-foreground" title={t('pinLabel')}>
        {cert.fingerprintSha256}
      </div>
      {anchor && (
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <span>{t('credentialAnchor')}</span>
          <Link
            className="text-primary underline-offset-2 hover:underline"
            to={`/trust-anchors/${encodeURIComponent(anchor.token)}`}
          >
            {anchor.name || anchor.token}
          </Link>
          {!anchor.enabled && <Badge variant="warning">{t('anchorDisabledBadge')}</Badge>}
        </div>
      )}
    </div>
  );
}

/** The expiry cell of a certificate row: the chain's earliest expiry, called out when near. */
export function X509Expiry({ expiresAt }: { expiresAt: string | null | undefined }) {
  const { t } = useTranslation('trustAnchors');
  if (!expiresAt) return <>—</>;
  const days = daysUntil(expiresAt);
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <span title={t('expiryChainHint')}>{formatTime(expiresAt)}</span>
      {days !== null && days < 0 && <Badge variant="destructive">{t('expiredBadge')}</Badge>}
      {days !== null && days >= 0 && days <= EXPIRY_WARN_DAYS && (
        <Badge variant="warning">{t('expiresSoonBadge', { days })}</Badge>
      )}
    </span>
  );
}

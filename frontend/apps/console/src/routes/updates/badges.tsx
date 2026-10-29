// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The small pieces every artifact surface shares: the trust badge, the lifecycle badge and
// the shortened-but-copyable digest. Kept in one place so a list row and a detail header
// can never disagree about what SIGNED looks like.
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { CopyToken } from '@/components/ui/copy-token';
import type { ArtifactState, ArtifactTrustStatus, SignerState } from '@/lib/api/update-management';
import { artifactStateLabel, signerStateLabel } from '@/lib/enumLabel';
import '@/i18n/lazy/updates';

export function TrustBadge({ status }: { status: ArtifactTrustStatus }) {
  const { t } = useTranslation('updates');
  // UNVERIFIED is `warning`, not `destructive`: it is unresolved rather than bad. The bytes
  // are the declared bytes; nobody has vouched for who made them.
  return status === 'SIGNED' ? (
    <Badge variant="success">{t('trustSigned')}</Badge>
  ) : (
    <Badge variant="warning">{t('trustUnverified')}</Badge>
  );
}

export function ArtifactStateBadge({ state }: { state: ArtifactState }) {
  const { t } = useTranslation('updates');
  switch (state) {
    case 'AVAILABLE':
      return <Badge variant="success">{artifactStateLabel(t, state)}</Badge>;
    case 'UPLOADING':
      return <Badge variant="warning">{artifactStateLabel(t, state)}</Badge>;
    default:
      return <Badge variant="outline">{artifactStateLabel(t, state)}</Badge>;
  }
}

export function SignerStateBadge({ state }: { state: SignerState }) {
  const { t } = useTranslation('updates');
  return state === 'ACTIVE' ? (
    <Badge variant="success">{signerStateLabel(t, state)}</Badge>
  ) : (
    <Badge variant="outline">{signerStateLabel(t, state)}</Badge>
  );
}

/** The scheme plus the first 12 hex digits. The whole digest is what a click copies. */
export function shortDigest(digest: string): string {
  const i = digest.indexOf(':');
  const hex = i >= 0 ? digest.slice(i + 1) : digest;
  const prefix = i >= 0 ? digest.slice(0, i + 1) : '';
  return hex.length > 12 ? `${prefix}${hex.slice(0, 12)}…` : digest;
}

export function DigestChip({ digest }: { digest: string }) {
  return <CopyToken value={digest} display={shortDigest(digest)} />;
}

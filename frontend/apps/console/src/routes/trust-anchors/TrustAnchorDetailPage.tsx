// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A trust anchor: its certificate (fingerprint, subject, validity), what relies on it, and the
// operator controls: disable or enable it, and delete it once nothing relies on it.
//
// Two guards live here and both are load-bearing:
//   * DISABLING withdraws the CA at once: every device whose certificate chains to it is refused
//     at its next connect and its live session is ended. The confirmation says so.
//   * DELETING is refused while a credential or a provisioning profile names the anchor. The
//     page says why (and how many) instead of offering a delete the server will refuse.

import '@/routes/trust-anchors/catalog';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Power, Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { CopyToken } from '@/components/ui/copy-token';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { useAuth } from '@/auth/AuthProvider';
import { mayWrite } from '@/lib/scope';
import { formatTime } from '@/lib/utils';
import { useReload } from '@/routes/common';
import {
  deleteTrustAnchor,
  getTrustAnchor,
  setTrustAnchorEnabled,
  type TrustAnchor,
} from '@/lib/api/trust-anchors';
import { AnchorBadges } from './AnchorBadges';
import { deleteBlockers } from './anchorRules';
import { x509ErrorText } from './anchorErrors';

export default function TrustAnchorDetailPage() {
  const { t } = useTranslation('trustAnchors');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const [version, reload] = useReload();
  const { data: anchor, loading, error } = useQuery(() => getTrustAnchor(token), [token, version]);

  // Only the first load takes the page over; a reload after an action keeps the page in place.
  if (loading && !anchor) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description={t('loadingAnchor')} />
      </PageShell>
    );
  }
  if (error && !anchor) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!anchor) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={t('notFound', { token })} />
      </PageShell>
    );
  }
  return <AnchorDetail key={token} anchor={anchor} reload={reload} />;
}

function AnchorDetail({ anchor, reload }: { anchor: TrustAnchor; reload: () => void }) {
  const { t } = useTranslation('trustAnchors');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const blockers = deleteBlockers(anchor);

  const toggle = async () => {
    setActionError(null);
    const enabling = !anchor.enabled;
    if (
      !enabling &&
      !(await confirm({
        title: t('disableTitle'),
        description: t('disableConfirm', {
          name: anchor.name || anchor.token,
          credentials: anchor.credentialCount,
          profiles: anchor.profileCount,
        }),
        confirmLabel: t('disableAction'),
      }))
    ) {
      return;
    }
    setBusy(true);
    try {
      await setTrustAnchorEnabled(anchor.token, enabling);
      toast(enabling ? t('enabledToast', { token: anchor.token }) : t('disabledToast', { token: anchor.token }));
    } catch (err) {
      // A disable whose live sessions could not be confirmed ended is SAVED and the server says
      // so (repeating it asks again), so the page reloads to show the stored state either way.
      setActionError(x509ErrorText(err, t));
    } finally {
      setBusy(false);
      reload();
    }
  };

  const remove = async () => {
    setActionError(null);
    // Refused before any round trip: the page already knows the anchor is in use, and the server
    // would refuse it. The explanation is the point; the count is the one just read.
    if (blockers) {
      setActionError(t('deleteBlocked', { credentials: blockers.credentials, profiles: blockers.profiles }));
      return;
    }
    if (
      !(await confirm({
        title: t('deleteTitle'),
        description: t('deleteConfirm', { token: anchor.token }),
        confirmLabel: t('delete'),
      }))
    ) {
      return;
    }
    try {
      await deleteTrustAnchor(anchor.token);
      toast(t('deletedToast', { token: anchor.token }));
      navigate('/trust-anchors');
    } catch (err) {
      // The counts on screen may be stale (something bound to the anchor since it loaded): the
      // server's refusal is the authority and gets the same explanation.
      setActionError(x509ErrorText(err, t));
      reload();
    }
  };

  return (
    <PageShell
      title={anchor.name || anchor.token}
      titleAdornment={anchor.name ? <CopyToken value={anchor.token} /> : undefined}
      banner="devices"
      description={<AnchorBadges anchor={anchor} />}
      action={
        canWrite ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={toggle} disabled={busy}>
              <Power size={14} /> {anchor.enabled ? t('disableAction') : t('enableAction')}
            </Button>
            <Button variant="destructive" size="sm" onClick={remove} disabled={busy}>
              <Trash2 size={14} /> {t('delete')}
            </Button>
          </div>
        ) : undefined
      }
    >
      <div className="max-w-3xl space-y-4">
        {actionError && <ErrorBanner message={actionError} onDismiss={() => setActionError(null)} />}
        {!anchor.enabled && (
          <p className="rounded-md border border-warning/40 bg-warning/5 p-3 text-sm text-foreground">
            {t('disabledNotice')}
          </p>
        )}
        <SectionPanel title={t('certificateTitle')}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted-foreground">{t('fingerprintLabel')}</dt>
            <dd className="break-all font-mono text-xs text-foreground">{anchor.fingerprintSha256}</dd>
            <dt className="text-muted-foreground">{t('subjectLabel')}</dt>
            <dd className="break-all text-foreground">{anchor.subject}</dd>
            <dt className="text-muted-foreground">{t('issuerLabel')}</dt>
            <dd className="break-all text-foreground">{anchor.issuer}</dd>
            <dt className="text-muted-foreground">{t('validFromLabel')}</dt>
            <dd className="text-foreground">{formatTime(anchor.notBefore)}</dd>
            <dt className="text-muted-foreground">{t('validUntilLabel')}</dt>
            <dd className="text-foreground">{formatTime(anchor.notAfter)}</dd>
          </dl>
        </SectionPanel>
        <SectionPanel title={t('inUseTitle')}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted-foreground">{t('credentialsLabel')}</dt>
            <dd className="text-foreground tabular-nums">{anchor.credentialCount}</dd>
            <dt className="text-muted-foreground">{t('profilesLabel')}</dt>
            <dd className="text-foreground tabular-nums">{anchor.profileCount}</dd>
          </dl>
          <p className="mt-3 text-xs text-muted-foreground">{t('inUseNote')}</p>
        </SectionPanel>
        <SectionPanel title={t('pemTitle')}>
          <pre className="max-h-64 overflow-auto rounded-md bg-muted/40 p-3 font-mono text-xs text-foreground">
            {anchor.certificatePem}
          </pre>
        </SectionPanel>
      </div>
    </PageShell>
  );
}

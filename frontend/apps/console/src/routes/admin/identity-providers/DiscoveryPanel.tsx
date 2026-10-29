// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The discovery snapshot: the provider's authorization, token and key endpoints as last
// fetched. Sign-ins use ONLY the endpoints approved here, so a person has to look at them
// and approve them before a connection can be enabled — and again whenever a refresh
// finds that they moved.
//
// 🔴 APPROVAL NAMES THE SNAPSHOT BY ITS FINGERPRINT. The fingerprint sent is the one on
// screen, not one read at click time, so the server refuses (rather than approving
// something else) if the stored endpoints changed between the operator's look and their
// click. The acknowledgement is keyed on the fingerprint too: a refresh that swaps in a
// different snapshot unticks it, so an old "I reviewed this" can never carry over to
// endpoints the operator has not seen.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RefreshCw, ShieldCheck } from 'lucide-react';
import {
  approveIdpDiscovery,
  refreshIdpDiscovery,
  type IdpConnection,
} from '@/lib/api/identity-providers';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { formatTime } from '@/lib/utils';
import { errMessage } from '@/routes/common';
import { needsReapproval } from './idpForm';
import '@/i18n/lazy/identityProviders';

export function DiscoveryPanel({
  connection: c,
  reading,
  onChanged,
  onApprovalRefused,
}: {
  connection: IdpConnection;
  // True while a re-read of the connection is in flight; the rows on screen may be stale.
  reading: boolean;
  onChanged: () => void;
  // A refused approval most likely means the snapshot moved under the operator. The page
  // must show the refusal AND re-read, or the screen keeps offering the stale snapshot and
  // every retry sends the same stale fingerprint. The message is handed up because this
  // panel remounts when the re-read lands, which would drop its own error state.
  onApprovalRefused: (message: string) => void;
}) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const d = c.discovery;
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const reapproval = needsReapproval(c);

  const run = async (
    fn: () => Promise<void>,
    doneToast: string,
    onFail: (message: string) => void = setError,
  ) => {
    setError(null);
    setBusy(true);
    try {
      await fn();
      toast(doneToast);
      onChanged();
    } catch (err) {
      onFail(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel
      title={t('discoveryTitle')}
      description={t('discoveryDescription')}
      action={
        <Button
          variant="outline"
          size="sm"
          disabled={reading || busy}
          onClick={() => void run(() => refreshIdpDiscovery(c.token), t('discoveryRefreshedToast'))}
        >
          <RefreshCw size={14} /> {t('refreshButton')}
        </Button>
      }
    >
      <div className="space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

        {reapproval && (
          <div
            role="alert"
            className="rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
          >
            <p className="font-medium">{t('reapprovalTitle')}</p>
            <p>{t('reapprovalBody')}</p>
          </div>
        )}

        <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[12rem_1fr]">
          <Row label={t('authorizationEndpointLabel')} value={d.authorizationEndpoint} />
          <Row label={t('tokenEndpointLabel')} value={d.tokenEndpoint} />
          <Row label={t('jwksUriLabel')} value={d.jwksUri} />
          <Row label={t('endSessionEndpointLabel')} value={d.endSessionEndpoint ?? t('none')} />
          <Row label={t('fetchedAtLabel')} value={d.fetchedAt ? formatTime(d.fetchedAt) : t('none')} />
          <Row label={t('fingerprintLabel')} value={d.fingerprint} mono />
          <dt className="text-muted-foreground">{t('approvalLabel')}</dt>
          <dd>
            {d.approved ? (
              <span className="flex flex-wrap items-center gap-2">
                <Badge variant="success">{t('discoveryApprovedBadge')}</Badge>
                <span className="text-muted-foreground">
                  {t('approvedBy', { by: d.approvedBy ?? t('none'), at: d.approvedAt ? formatTime(d.approvedAt) : t('none') })}
                </span>
              </span>
            ) : (
              <Badge variant="warning">{t('discoveryPendingBadge')}</Badge>
            )}
          </dd>
        </dl>

        {!d.approved && (
          // Keyed on the fingerprint: a different snapshot is a different review.
          <ApproveBlock
            key={d.fingerprint}
            fingerprint={d.fingerprint}
            reapproval={reapproval}
            disabled={reading || busy}
            onApprove={() =>
              run(
                () => approveIdpDiscovery(c.token, d.fingerprint),
                t('discoveryApprovedToast'),
                (message) => {
                  onApprovalRefused(message);
                  onChanged();
                },
              )
            }
          />
        )}
      </div>
    </SectionPanel>
  );
}

function Row({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={mono ? 'break-all font-mono text-xs text-foreground' : 'break-all text-foreground'}
      >
        {value}
      </dd>
    </>
  );
}

function ApproveBlock({
  fingerprint,
  reapproval,
  disabled,
  onApprove,
}: {
  fingerprint: string;
  reapproval: boolean;
  disabled: boolean;
  onApprove: () => Promise<void>;
}) {
  const { t } = useTranslation('identityProviders');
  const [reviewed, setReviewed] = useState(false);
  return (
    <div className="space-y-3 rounded-md border border-border p-4">
      <label className="flex items-start gap-2 text-sm">
        <Checkbox
          checked={reviewed}
          onCheckedChange={(v) => setReviewed(v === true)}
          aria-label={t('reviewedLabel', { fingerprint })}
        />
        <span>{t('reviewedLabel', { fingerprint })}</span>
      </label>
      <Button disabled={disabled || !reviewed} onClick={() => void onApprove()}>
        <ShieldCheck size={16} /> {reapproval ? t('reapproveButton') : t('approveDiscoveryButton')}
      </Button>
    </div>
  );
}

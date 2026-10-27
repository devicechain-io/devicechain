// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An integration: how to connect the sender, whether traffic is arriving and being read, the
// identifiers it has seen that no device carries, and the operator controls (rotate the token,
// delete). The edit form sends only the fields that changed.

import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { KeyRound, Trash2 } from 'lucide-react';
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
import { errMessage, useReload } from '@/routes/common';
import {
  deleteIntegration,
  getIntegration,
  rotateIntegrationToken,
  type Integration,
} from '@/lib/api/integrations';
import { IntegrationForm } from './IntegrationForm';
import { IntegrationBadges } from './IntegrationBadges';
import { IntegrationTokenReveal, type RevealedToken } from './IntegrationTokenReveal';
import { ConnectSnippet } from './ConnectSnippet';
import { UnregisteredDevicesPanel } from './UnregisteredDevicesPanel';

const ID_PREFIX = 'ig-detail';

export default function IntegrationDetailPage() {
  const { t } = useTranslation('integrations');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => getIntegration(token), [token, version]);
  // useQuery keeps the previous data while a new read is in flight, so after an in-place move to
  // another :token it still holds the OLD integration. Show a record only when it is this one.
  const integration = data && data.token === token ? data : null;

  // Only the first load takes the page over; a reload after a rotate keeps the page (and the
  // open reveal dialog) in place.
  if (loading && !integration) {
    return (
      <PageShell title={token} banner="devices">
        <LoadingState description={t('loadingIntegration')} />
      </PageShell>
    );
  }
  if (error && !integration) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!integration) {
    return (
      <PageShell title={token} banner="devices">
        <ErrorState description={t('notFound', { token })} />
      </PageShell>
    );
  }
  // Keyed by token so no state survives a move to another integration.
  return <IntegrationDetail key={token} integration={integration} reload={reload} refreshError={error} />;
}

function IntegrationDetail({
  integration,
  reload,
  refreshError,
}: {
  integration: Integration;
  reload: () => void;
  /** A failed re-read after the page had loaded (a rotate or clear): the data shown may be stale. */
  refreshError: string | null;
}) {
  const { t } = useTranslation('integrations');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const [revealed, setRevealed] = useState<RevealedToken | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const rotate = async () => {
    setActionError(null);
    if (
      !(await confirm({
        title: t('rotateTitle'),
        description: t('rotateConfirm', { token: integration.token }),
        confirmLabel: t('rotateAction'),
      }))
    )
      return;
    setBusy(true);
    try {
      const { integration: updated, issuedToken } = await rotateIntegrationToken(integration.token);
      setRevealed({ integration: updated, token: issuedToken, rotated: true });
      reload();
    } catch (err) {
      setActionError(errMessage(err));
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setActionError(null);
    if (
      !(await confirm({
        title: t('deleteTitle'),
        description: t('deleteConfirm', { token: integration.token }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      await deleteIntegration(integration.token);
      toast(t('deletedToast', { token: integration.token }));
      navigate('/integrations');
    } catch (err) {
      setActionError(errMessage(err));
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={integration.name || integration.token}
      titleAdornment={integration.name ? <CopyToken value={integration.token} /> : undefined}
      banner="devices"
      description={<IntegrationBadges integration={integration} />}
      action={
        canWrite ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={rotate} disabled={busy}>
              <KeyRound size={14} /> {t('rotateAction')}
            </Button>
            <Button variant="destructive" size="sm" onClick={remove} disabled={busy}>
              <Trash2 size={14} /> {t('delete')}
            </Button>
          </div>
        ) : undefined
      }
    >
      <IntegrationTokenReveal revealed={revealed} onClose={() => setRevealed(null)} />
      <div className="max-w-3xl space-y-4">
        {refreshError && <ErrorBanner message={refreshError} />}
        {actionError && <ErrorBanner message={actionError} onDismiss={() => setActionError(null)} />}
        <SectionPanel title={t('healthTitle')} description={t('healthDescription')}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted-foreground">{t('healthUndecoded')}</dt>
            <dd className="tabular-nums text-foreground">{integration.undecodedCount}</dd>
            <dt className="text-muted-foreground">{t('healthLastUndecoded')}</dt>
            <dd className="text-foreground">{formatTime(integration.lastUndecodedAt)}</dd>
            {integration.lastUndecodedSample && (
              <>
                <dt className="text-muted-foreground">{t('healthSample')}</dt>
                <dd className="break-all font-mono text-xs text-foreground" data-testid="undecoded-sample" data-shot-mask>
                  {integration.lastUndecodedSample}
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">{t('healthLastAuthFailure')}</dt>
            <dd className="text-foreground">{formatTime(integration.lastAuthFailureAt)}</dd>
            <dt className="text-muted-foreground">{t('healthTokenIssued')}</dt>
            <dd className="text-foreground">{formatTime(integration.tokenIssuedAt)}</dd>
          </dl>
          {integration.undecodedCount > 0 && <p className="mt-3 text-xs text-muted-foreground">{t('healthUndecodedNote')}</p>}
        </SectionPanel>
        <SectionPanel title={t('connectTitle')} description={t('connectDescription')}>
          <ConnectSnippet
            idPrefix={ID_PREFIX}
            integration={integration}
            input={{ tenant: claims?.tenant ?? '', integrationToken: integration.token }}
          />
        </SectionPanel>
        <UnregisteredDevicesPanel integration={integration} canWrite={canWrite} />
        <SectionPanel>
          <IntegrationForm
            // Re-seeded when the stored integration changes (a save, a rotate), so the form is
            // measured against what the server now holds.
            key={`${integration.updatedAt}|${integration.tokenIssuedAt}`}
            entity={integration}
            readOnly={!canWrite}
            onSaved={(m) => {
              toast(m);
              reload();
            }}
          />
        </SectionPanel>
      </div>
    </PageShell>
  );
}

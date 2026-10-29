// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// One identity-provider connection: discovery approval, settings and the write-only
// secret, the tenants and email domains it serves, and enable / disable / delete.
//
// An ADMIN-console screen behind AdminProtectedRoute; see IdentityProvidersPage.
//
// 🔴 EVERY WRITE IS DISABLED WHILE A RE-READ IS IN FLIGHT. useQuery keeps the previous
// data on screen while it refetches, so right after a write the page shows the OLD
// connection for a moment — an Enable button that is already stale, a snapshot the
// server has since replaced. Gating the first load on `!data` keeps the tabs mounted
// across a save (so the operator is not thrown back to the first tab), and `reading`
// closes the window that opens as a result.

import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Power, PowerOff, Trash2 } from 'lucide-react';
import {
  deleteIdpConnection,
  getIdpConnection,
  updateIdpConnection,
  type IdpConnection,
} from '@/lib/api/identity-providers';
import { useQuery } from '@/lib/hooks/use-query';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { CopyToken } from '@/components/ui/copy-token';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { PageShell } from '@/components/ui/page-shell';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { useToast } from '@/components/ui/toast';
import { StatusBadge, useReload } from '@/routes/common';
import { idpErrorMessage } from './idpError';
import { DiscoveryPanel } from './DiscoveryPanel';
import { DomainsPanel, TenantsPanel } from './ServedPanels';
import { SettingsPanel } from './SettingsPanel';
import { needsReapproval } from './idpForm';
import { IDENTITY_PROVIDERS_BASE } from './paths';
import '@/i18n/lazy/identityProviders';

export default function IdentityProviderDetailPage() {
  const { t } = useTranslation('identityProviders');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => getIdpConnection(token), [token, version]);

  if (loading && !data) {
    return (
      <PageShell title={token} banner="dashboard">
        <LoadingState description={t('loadingConnection')} />
      </PageShell>
    );
  }
  if (error && !data) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!data) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={t('connectionNotFound', { token })} />
      </PageShell>
    );
  }
  return <Editor connection={data} reading={loading} onChanged={reload} />;
}

function Editor({
  connection: c,
  reading,
  onChanged,
}: {
  connection: IdpConnection;
  reading: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const confirm = useConfirm();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // The panels seed their drafts from the connection, so they remount whenever what the
  // server holds changes: a draft is never left describing a connection that moved on.
  const contentKey = JSON.stringify(c);
  const reapproval = needsReapproval(c);

  const setEnabled = async (enabled: boolean) => {
    if (!enabled) {
      const ok = await confirm({
        title: t('disableTitle'),
        description: t('disableDescription', { name: c.name || c.token }),
        confirmLabel: t('disableButton'),
      });
      if (!ok) return;
    }
    setError(null);
    setBusy(true);
    try {
      await updateIdpConnection(c.token, { enabled });
      toast(enabled ? t('enabledToast') : t('disabledToast'));
      onChanged();
    } catch (err) {
      setError(idpErrorMessage(err, t));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    const ok = await confirm({
      title: t('deleteTitle'),
      description: t('deleteDescription', { name: c.name || c.token }),
      confirmLabel: t('common:delete'),
    });
    if (!ok) return;
    setError(null);
    setBusy(true);
    try {
      await deleteIdpConnection(c.token);
      toast(t('deletedToast', { token: c.token }));
      navigate(IDENTITY_PROVIDERS_BASE);
    } catch (err) {
      setError(idpErrorMessage(err, t));
      setBusy(false);
    }
  };

  const locked = busy || reading;
  // Enabling needs approved endpoints; the server refuses otherwise, and saying so up
  // front is kinder than a refusal.
  const canEnable = c.discovery.approved;

  return (
    <PageShell
      title={c.name || c.token}
      titleAdornment={c.name ? <CopyToken value={c.token} /> : undefined}
      description={
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge enabled={c.enabled} />
          <Badge variant="secondary">{t(`kind_${c.kind === 'instance' ? 'instance' : 'tenant'}`)}</Badge>
          {reapproval && <Badge variant="destructive">{t('discoveryReapprovalBadge')}</Badge>}
        </div>
      }
      action={
        <>
          {c.enabled ? (
            <Button variant="outline" size="sm" disabled={locked} onClick={() => void setEnabled(false)}>
              <PowerOff size={14} /> {t('disableButton')}
            </Button>
          ) : (
            <Button
              variant="outline"
              size="sm"
              disabled={locked || !canEnable}
              title={canEnable ? undefined : t('enableNeedsApproval')}
              onClick={() => void setEnabled(true)}
            >
              <Power size={14} /> {t('enableButton')}
            </Button>
          )}
          <Button variant="destructive" size="sm" disabled={locked} onClick={() => void remove()}>
            <Trash2 size={14} /> {t('common:delete')}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        {!c.enabled && !canEnable && (
          <p className="text-sm text-muted-foreground">{t('enableNeedsApproval')}</p>
        )}
        <Tabs defaultValue="discovery">
          <TabsList>
            <TabsTrigger value="discovery">{t('discoveryTab')}</TabsTrigger>
            <TabsTrigger value="settings">{t('settingsTab')}</TabsTrigger>
            <TabsTrigger value="tenants">{t('tenantsTab')}</TabsTrigger>
            <TabsTrigger value="domains">{t('domainsTab')}</TabsTrigger>
          </TabsList>
          <TabsContent value="discovery" className="pt-4">
            <DiscoveryPanel
              key={contentKey}
              connection={c}
              reading={reading}
              onChanged={onChanged}
              onApprovalRefused={setError}
            />
          </TabsContent>
          <TabsContent value="settings" className="pt-4">
            <SettingsPanel key={contentKey} connection={c} reading={reading} onChanged={onChanged} />
          </TabsContent>
          <TabsContent value="tenants" className="pt-4">
            <TenantsPanel key={contentKey} connection={c} reading={reading} onChanged={onChanged} />
          </TabsContent>
          <TabsContent value="domains" className="pt-4">
            <DomainsPanel key={contentKey} connection={c} reading={reading} onChanged={onChanged} />
          </TabsContent>
        </Tabs>
      </div>
    </PageShell>
  );
}

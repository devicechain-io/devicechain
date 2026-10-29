// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Identity providers list: the external OpenID Connect providers people sign in
// through, plus the queue of sign-ins waiting for a link decision.
//
// An ADMIN-console screen: it sits behind AdminProtectedRoute (a superuser identity
// session) and calls the identity-token admin API, which authorizes every operation
// server-side. Like every other admin screen it carries no per-page authority check of
// its own, and it must not read `useAuth().claims` (the TENANT token's claims).

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { listIdpConnections, type IdpConnectionListItem } from '@/lib/api/identity-providers';
import { useQuery } from '@/lib/hooks/use-query';
import { FormDrawer } from '@/components/registry';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { PageShell } from '@/components/ui/page-shell';
import { useToast } from '@/components/ui/toast';
import { rowLinkProps, StatusBadge, useReload } from '@/routes/common';
import { IdpCreateForm } from './IdpCreateForm';
import { LinkRequestsPanel } from './LinkRequestsPanel';
import { needsReapproval } from './idpForm';
import { identityProviderPath } from './paths';
import '@/i18n/lazy/identityProviders';

export default function IdentityProvidersPage() {
  const { t } = useTranslation('identityProviders');
  const navigate = useNavigate();
  const { toast } = useToast();
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(listIdpConnections, [version]);

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      banner="dashboard"
      action={
        <Button onClick={() => setCreating(true)} data-shot="new-button">
          <Plus size={16} /> {t('newButton')}
        </Button>
      }
    >
      <FormDrawer shot="identity-provider-new" open={creating} onOpenChange={setCreating} title={t('newDrawerTitle')}>
        <IdpCreateForm
          onDone={(token) => {
            toast(t('createdToast', { token }));
            setCreating(false);
            reload();
            navigate(identityProviderPath(token));
          }}
        />
      </FormDrawer>

      <div className="space-y-6">
        {loading && !data ? (
          <LoadingState description={t('loadingConnections')} />
        ) : error && !data ? (
          <ErrorState description={error} />
        ) : !data || data.length === 0 ? (
          <EmptyState description={t('emptyConnections')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('kindLabel')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('issuerLabel')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colDomains')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colDiscovery')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {data.map((c) => (
                <DataTableRow
                  key={c.token}
                  {...rowLinkProps(() => navigate(identityProviderPath(c.token)))}
                >
                  <DataTableCell className="font-medium text-foreground">
                    {c.name || c.token}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {t(`kind_${c.kind === 'instance' ? 'instance' : 'tenant'}`)}
                  </DataTableCell>
                  <DataTableCell className="break-all text-muted-foreground">{c.issuer}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {c.domains.map((d) => d.domain).join(', ')}
                  </DataTableCell>
                  <DataTableCell>
                    <DiscoveryBadge connection={c} />
                  </DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={c.enabled} />
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}

        <LinkRequestsPanel />
      </div>
    </PageShell>
  );
}

function DiscoveryBadge({ connection }: { connection: IdpConnectionListItem }) {
  const { t } = useTranslation('identityProviders');
  if (connection.discovery.approved) {
    return <Badge variant="secondary">{t('discoveryApprovedBadge')}</Badge>;
  }
  return needsReapproval(connection) ? (
    <Badge variant="destructive">{t('discoveryReapprovalBadge')}</Badge>
  ) : (
    <Badge variant="warning">{t('discoveryPendingBadge')}</Badge>
  );
}

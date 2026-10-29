// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The integrations list. An integration is an authenticated sender of device data over HTTP, such
// as a LoRaWAN network server or a webhook. This page lists them and hosts the create drawer;
// creating one shows the server-generated bearer token exactly once.

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { listIntegrations } from '@/lib/api/integrations';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { rowLinkProps, useReload } from '@/routes/common';
import { mayWrite } from '@/lib/scope';
import { formatTime } from '@/lib/utils';
import { FormDrawer } from '@/components/registry';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { IntegrationForm } from './IntegrationForm';
import { IntegrationBadges } from './IntegrationBadges';
import { IntegrationTokenReveal, type RevealedToken } from './IntegrationTokenReveal';
import '@/i18n/lazy/integrations';

const pageSize = 20;

export default function IntegrationsPage() {
  const { t } = useTranslation('integrations');
  const navigate = useNavigate();
  const { toast } = useToast();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  // The one-time token, held only while its dialog is open. Component state, so leaving the page
  // drops it too; nothing else reads it and nothing persists it.
  const [revealed, setRevealed] = useState<RevealedToken | null>(null);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listIntegrations({ pageNumber, pageSize }),
    [pageNumber, version],
  );
  const results = data?.results ?? [];

  return (
    <PageShell
      title={t('title')}
      description={t('listDescription')}
      banner="devices"
      action={
        canWrite ? (
          <Button onClick={() => setCreating(true)} data-shot="new-button">
            <Plus size={16} /> {t('newIntegration')}
          </Button>
        ) : undefined
      }
    >
      <FormDrawer shot="integration-new" open={creating} onOpenChange={setCreating} title={t('newIntegration')}>
        <IntegrationForm
          onCreated={({ integration, issuedToken }) => {
            toast(t('createdToast', { token: integration.token }));
            setCreating(false);
            reload();
            setRevealed({ integration, token: issuedToken, rotated: false });
          }}
        />
      </FormDrawer>
      <IntegrationTokenReveal revealed={revealed} onClose={() => setRevealed(null)} />
      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('listEmpty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colScope')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colLastAuthFailure')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((i) => (
                <DataTableRow
                  key={i.id}
                  {...rowLinkProps(() => navigate(`/integrations/${encodeURIComponent(i.token)}`))}
                >
                  <DataTableCell className="font-medium text-foreground">{i.name || i.token}</DataTableCell>
                  <DataTableCell>
                    <span className="font-mono text-xs text-foreground">{i.token}</span>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{t(`type_${i.type}`, { defaultValue: i.type })}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {i.deviceTypes.map((d) => d.name || d.token).join(', ')}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{formatTime(i.lastAuthFailureAt)}</DataTableCell>
                  <DataTableCell>
                    <IntegrationBadges integration={i} />
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={pageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
            className="mt-4"
          />
        </>
      )}
    </PageShell>
  );
}

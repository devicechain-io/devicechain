// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's trust anchors: the CA certificates it trusts to vouch for its devices' client
// certificates. This page lists them and hosts the create drawer.

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { listTrustAnchors } from '@/lib/api/trust-anchors';
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
import { TrustAnchorForm } from './TrustAnchorForm';
import { AnchorBadges } from './AnchorBadges';

const pageSize = 20;

export default function TrustAnchorsPage() {
  const { t } = useTranslation('trustAnchors');
  const navigate = useNavigate();
  const { toast } = useToast();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => listTrustAnchors({ pageNumber, pageSize }), [pageNumber, version]);
  const results = data?.results ?? [];

  return (
    <PageShell
      title={t('title')}
      description={t('listDescription')}
      banner="devices"
      action={
        canWrite ? (
          <Button onClick={() => setCreating(true)} data-shot="new-button">
            <Plus size={16} /> {t('newAnchor')}
          </Button>
        ) : undefined
      }
    >
      <FormDrawer shot="trust-anchor-new" open={creating} onOpenChange={setCreating} title={t('newAnchor')}>
        <TrustAnchorForm
          onCreated={(token) => {
            toast(t('createdToast', { token }));
            setCreating(false);
            reload();
          }}
        />
      </FormDrawer>
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
              <DataTableHeaderCell>{t('colSubject')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colExpires')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colInUse')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((a) => (
                <DataTableRow
                  key={a.id}
                  {...rowLinkProps(() => navigate(`/trust-anchors/${encodeURIComponent(a.token)}`))}
                >
                  <DataTableCell className="font-medium text-foreground">{a.name || a.token}</DataTableCell>
                  <DataTableCell>
                    <span className="font-mono text-xs text-foreground">{a.token}</span>
                  </DataTableCell>
                  <DataTableCell className="max-w-64 truncate text-muted-foreground">
                    <span title={a.subject}>{a.subject}</span>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{formatTime(a.notAfter)}</DataTableCell>
                  <DataTableCell className="tabular-nums text-muted-foreground">
                    {t('inUseCounts', { credentials: a.credentialCount, profiles: a.profileCount })}
                  </DataTableCell>
                  <DataTableCell>
                    <AnchorBadges anchor={a} />
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

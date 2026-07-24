// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenants } from '@/lib/api/admin';
import { TierPill } from '@/components/tiers/TierPill';
import { StatusBadge, rowLinkProps } from '@/routes/common';

export default function TenantsPage() {
  const { t } = useTranslation('tenants');
  const navigate = useNavigate();
  const { data: tenants, loading, error } = useQuery(listTenants, []);

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        <Button onClick={() => navigate('/admin/tenants/new')}>
          <Plus size={16} /> {t('newTenant')}
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description={t('loading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : !tenants || tenants.length === 0 ? (
          <EmptyState description={t('empty')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colTier')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colConfig')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {tenants.map((tr) => (
                <DataTableRow
                  key={tr.id}
                  {...rowLinkProps(() => navigate(`/admin/tenants/${encodeURIComponent(tr.token)}`))}
                >
                  <DataTableCell className="font-medium">{tr.token}</DataTableCell>
                  <DataTableCell>{tr.name ?? '—'}</DataTableCell>
                  {/* Never blank: the tier is a required FK (ADR-065). The pill carries
                      the tier token in the tier's color (S5c) — presentation, so a tenant
                      reads at a glance as its packaging. */}
                  <DataTableCell>
                    <TierPill label={tr.tier.token} color={tr.tier.color} />
                  </DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={tr.enabled} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate font-mono text-xs text-muted-foreground">
                    {tr.config ?? '—'}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>
    </PageShell>
  );
}

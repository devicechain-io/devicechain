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
  const { t: translate } = useTranslation('tenants');
  const navigate = useNavigate();
  const { data: tenants, loading, error } = useQuery(listTenants, []);

  return (
    <PageShell
      title={translate('title')}
      description={translate('description')}
      action={
        <Button onClick={() => navigate('/admin/tenants/new')}>
          <Plus size={16} /> {translate('newTenant')}
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description={translate('loading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : !tenants || tenants.length === 0 ? (
          <EmptyState description={translate('empty')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{translate('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{translate('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{translate('colTier')}</DataTableHeaderCell>
              <DataTableHeaderCell>{translate('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{translate('colConfig')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {tenants.map((t) => (
                <DataTableRow
                  key={t.id}
                  {...rowLinkProps(() => navigate(`/admin/tenants/${encodeURIComponent(t.token)}`))}
                >
                  <DataTableCell className="font-medium">{t.token}</DataTableCell>
                  <DataTableCell>{t.name ?? '—'}</DataTableCell>
                  {/* Never blank: the tier is a required FK (ADR-065). The pill carries
                      the tier token in the tier's color (S5c) — presentation, so a tenant
                      reads at a glance as its packaging. */}
                  <DataTableCell>
                    <TierPill label={t.tier.token} color={t.tier.color} />
                  </DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={t.enabled} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate font-mono text-xs text-muted-foreground">
                    {t.config ?? '—'}
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

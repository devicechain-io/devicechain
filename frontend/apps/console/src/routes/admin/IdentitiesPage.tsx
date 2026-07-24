// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
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
import { listIdentities } from '@/lib/api/admin';
import { StatusBadge, rowLinkProps } from '@/routes/common';

export default function IdentitiesPage() {
  const { t } = useTranslation('identities');
  const navigate = useNavigate();
  const { data: identities, loading, error } = useQuery(listIdentities, []);

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        <Button onClick={() => navigate('/admin/identities/new')}>
          <Plus size={16} /> {t('newIdentity')}
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description={t('loading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : !identities || identities.length === 0 ? (
          <EmptyState description={t('empty')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colEmail')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('systemRoles')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colTenants')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {identities.map((i) => (
                <DataTableRow
                  key={i.id}
                  {...rowLinkProps(() => navigate('/admin/identities/' + encodeURIComponent(i.email)))}
                >
                  <DataTableCell className="font-medium">{i.email}</DataTableCell>
                  <DataTableCell>{[i.firstName, i.lastName].filter(Boolean).join(' ') || '—'}</DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={i.enabled} />
                  </DataTableCell>
                  <DataTableCell>
                    <div className="flex flex-wrap gap-1">
                      {i.systemRoles.length === 0
                        ? '—'
                        : i.systemRoles.map((r) => (
                            <Badge key={r} variant="secondary">
                              {r}
                            </Badge>
                          ))}
                    </div>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{i.memberships.length}</DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>
    </PageShell>
  );
}

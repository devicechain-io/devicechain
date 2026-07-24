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
import { listRoles } from '@/lib/api/admin';
import { rowLinkProps } from '@/routes/common';

// SCOPE_LABEL_KEY maps the role scope enum to its localized human-readable label.
// The raw values ('system'/'tenant') are also the wire tokens used in the route
// (/admin/roles/:scope/:token) and API calls, so only the RENDERED text goes
// through the map — the values themselves are never translated.
const SCOPE_LABEL_KEY: Record<'system' | 'tenant', string> = {
  system: 'scopeSystem',
  tenant: 'scopeTenant',
};

export default function RolesPage() {
  const { t } = useTranslation('roles');
  const navigate = useNavigate();
  const { data: roles, loading, error } = useQuery(listRoles, []);

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        <Button onClick={() => navigate('/admin/roles/new')}>
          <Plus size={16} /> {t('newRole')}
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description={t('loading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : !roles || roles.length === 0 ? (
          <EmptyState description={t('empty')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colScope')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colAuthorities')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {roles.map((r) => (
                <DataTableRow
                  key={r.id}
                  {...rowLinkProps(() => navigate(`/admin/roles/${r.scope}/${encodeURIComponent(r.token)}`))}
                >
                  <DataTableCell>
                    <Badge variant="secondary">{t(SCOPE_LABEL_KEY[r.scope as 'system' | 'tenant'])}</Badge>
                  </DataTableCell>
                  <DataTableCell className="font-medium">{r.token}</DataTableCell>
                  <DataTableCell>{r.name ?? '—'}</DataTableCell>
                  <DataTableCell>
                    <div className="flex flex-wrap gap-1">
                      {r.authorities.map((a) => (
                        <Badge key={a} variant="outline" className="font-mono text-xs">
                          {a}
                        </Badge>
                      ))}
                    </div>
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

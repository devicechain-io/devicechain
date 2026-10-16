// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { mayWrite } from '@/lib/scope';
import { listTenantRoles } from '@/lib/api/members';
import { rowLinkProps } from '@/routes/common';

// The roles that can be given to this tenant's members: the ones the instance operator
// defined (shown and assignable, but changed only by the operator) and the ones this
// tenant defined for itself. The operator's sort first, then the tenant's, each by name.
export default function RolesPage() {
  const { t } = useTranslation('members');
  const navigate = useNavigate();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'role:write');
  const { data, loading, error } = useQuery(listTenantRoles, []);
  const roles = [...(data ?? [])].sort(
    (a, b) => a.owner.localeCompare(b.owner) || (a.name || a.token).localeCompare(b.name || b.token),
  );

  return (
    <PageShell
      title={t('rolesTitle')}
      description={t('rolesPageDescription')}
      action={
        canWrite && (
          <Button onClick={() => navigate('/roles/new')} data-shot="new-button">
            <Plus size={16} /> {t('newRole')}
          </Button>
        )
      }
    >
      {loading ? (
        <LoadingState description={t('rolesLoading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : roles.length === 0 ? (
        <EmptyState description={t('rolesEmpty')} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colDefinedBy')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colAuthorities')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {roles.map((r) => (
              <DataTableRow key={r.token} {...rowLinkProps(() => navigate(`/roles/${encodeURIComponent(r.token)}`))}>
                <DataTableCell>
                  <div className="flex flex-col">
                    <span className="font-medium text-foreground">{r.name || r.token}</span>
                    {r.name && <span className="font-mono text-xs text-muted-foreground">{r.token}</span>}
                  </div>
                </DataTableCell>
                <DataTableCell>
                  <Badge variant={r.owner === 'INSTANCE' ? 'secondary' : 'outline'}>
                    {r.owner === 'INSTANCE' ? t('roleOwnerInstance') : t('roleOwnerTenant')}
                  </Badge>
                </DataTableCell>
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
    </PageShell>
  );
}

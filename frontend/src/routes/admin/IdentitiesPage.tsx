// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
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
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listIdentities,
  setIdentityEnabled,
  deleteIdentity,
  type AdminIdentity,
} from '@/lib/api/admin';
import { StatusBadge, errMessage, useReload } from '@/routes/admin/common';

export default function IdentitiesPage() {
  const navigate = useNavigate();
  const [version, reload] = useReload();
  const { data: identities, loading, error } = useQuery(listIdentities, [version]);
  const { toast } = useToast();

  const remove = async (i: AdminIdentity) => {
    if (!window.confirm(`Delete identity “${i.email}” and all its memberships?`)) return;
    try {
      await deleteIdentity(i.email);
      toast(`Identity “${i.email}” deleted`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const toggleEnabled = async (i: AdminIdentity) => {
    try {
      await setIdentityEnabled(i.email, !i.enabled);
      toast(`Identity “${i.email}” ${i.enabled ? 'disabled' : 'enabled'}`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Identities"
      description="The global identity directory. A person is one identity that can hold memberships in many tenants."
      action={
        <Button onClick={() => navigate('/admin/identities/new')}>
          <Plus size={16} /> New identity
        </Button>
      }
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description="Loading identities…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !identities || identities.length === 0 ? (
          <EmptyState description="No identities yet. Create the first one." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Email</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Status</DataTableHeaderCell>
              <DataTableHeaderCell>System roles</DataTableHeaderCell>
              <DataTableHeaderCell>Tenants</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">Actions</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {identities.map((i) => (
                <DataTableRow key={i.id}>
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
                  <DataTableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => navigate('/admin/identities/' + encodeURIComponent(i.email))}
                      >
                        Manage
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => toggleEnabled(i)}>
                        {i.enabled ? 'Disable' : 'Enable'}
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => remove(i)}>
                        Delete
                      </Button>
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

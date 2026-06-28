// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { listRoles, deleteRole } from '@/lib/api/admin';
import { AdminCard, BackLink, errMessage, useReload } from '@/routes/admin/common';
import { RoleForm } from '@/routes/admin/roles/RoleForm';

export default function RoleDetailPage() {
  const { scope: rawScope, token: rawToken } = useParams<{ scope: string; token: string }>();
  const scope = (rawScope ?? '') as 'system' | 'tenant';
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: roles, loading, error } = useQuery(() => listRoles(scope), [version]);

  const role = roles?.find((r) => r.scope === scope && r.token === token) ?? null;

  if (loading) {
    return (
      <PageShell title={token} action={<BackLink to="/admin/roles">Roles</BackLink>}>
        <LoadingState description="Loading role…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} action={<BackLink to="/admin/roles">Roles</BackLink>}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!role) {
    return (
      <PageShell title={token} action={<BackLink to="/admin/roles">Roles</BackLink>}>
        <ErrorState description={`Role “${token}” not found.`} />
      </PageShell>
    );
  }

  const remove = async () => {
    if (!window.confirm(`Delete the ${role.scope} role “${role.token}”? It will be removed from all assignees.`)) return;
    try {
      const ok = await deleteRole(role.scope, role.token);
      toast(ok ? `Role “${role.token}” deleted` : `Role “${role.token}” not found`);
      navigate('/admin/roles');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={token}
      description={`${scope} role`}
      action={<BackLink to="/admin/roles">Roles</BackLink>}
    >
      <AdminCard title={`Edit ${scope} role “${token}”`}>
        <div className="space-y-6">
          <RoleForm
            role={role}
            onDone={(m) => {
              toast(m);
              reload();
            }}
          />
          <div className="flex gap-2 border-t border-border pt-4">
            <Button variant="destructive" onClick={remove}>
              Delete role
            </Button>
          </div>
        </div>
      </AdminCard>
    </PageShell>
  );
}

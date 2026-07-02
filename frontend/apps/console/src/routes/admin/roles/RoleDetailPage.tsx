// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listRoles, deleteRole } from '@/lib/api/admin';
import { BackLink, errMessage, useReload } from '@/routes/common';
import { RoleForm } from '@/routes/admin/roles/RoleForm';

export default function RoleDetailPage() {
  const { scope: rawScope, token: rawToken } = useParams<{ scope: string; token: string }>();
  const scope = (rawScope ?? '') as 'system' | 'tenant';
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: roles, loading, error } = useQuery(() => listRoles(scope), [version]);

  const role = roles?.find((r) => r.scope === scope && r.token === token) ?? null;

  const back = <BackLink to="/admin/roles">Roles</BackLink>;

  if (loading) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description="Loading role…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!role) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={`Role “${token}” not found.`} />
      </PageShell>
    );
  }

  const remove = async () => {
    if (
      !(await confirm({
        title: 'Delete role',
        description: `Delete the ${role.scope} role “${role.token}”? It will be removed from all assignees.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
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
      action={
        <div className="flex items-center gap-2">
          {back}
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
      }
    >
      <SectionPanel>
        <RoleForm
          role={role}
          onDone={(m) => {
            toast(m);
            reload();
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}

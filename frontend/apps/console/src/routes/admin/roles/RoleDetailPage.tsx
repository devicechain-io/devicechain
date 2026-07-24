// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listRoles, deleteRole } from '@/lib/api/admin';
import { errMessage, useReload } from '@/routes/common';
import { RoleForm } from '@/routes/admin/roles/RoleForm';

// SCOPE_LABEL_KEY maps the role scope enum to its localized human-readable label.
// The raw value is also the wire token used in the route and API calls, so only
// the RENDERED text goes through the map.
const SCOPE_LABEL_KEY: Record<'system' | 'tenant', string> = {
  system: 'scopeSystem',
  tenant: 'scopeTenant',
};

export default function RoleDetailPage() {
  const { t } = useTranslation('roles');
  const { scope: rawScope, token: rawToken } = useParams<{ scope: string; token: string }>();
  const scope = (rawScope ?? '') as 'system' | 'tenant';
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: roles, loading, error } = useQuery(() => listRoles(scope), [version]);

  const role = roles?.find((r) => r.scope === scope && r.token === token) ?? null;


  if (loading) {
    return (
      <PageShell title={token}>
        <LoadingState description={t('loadingRole')} />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!role) {
    return (
      <PageShell title={token}>
        <ErrorState description={t('notFound', { token })} />
      </PageShell>
    );
  }

  const remove = async () => {
    if (
      !(await confirm({
        title: t('deleteRoleTitle'),
        description: t('deleteRoleConfirm', { scope: t(SCOPE_LABEL_KEY[role.scope as 'system' | 'tenant']), token: role.token }),
        confirmLabel: t('common:delete'),
      }))
    )
      return;
    try {
      const ok = await deleteRole(role.scope, role.token);
      toast(ok ? t('roleDeletedToast', { token: role.token }) : t('roleNotFoundToast', { token: role.token }));
      navigate('/admin/roles');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      // A role's identity IS its token (there is no separate human name), so the token is
      // the title and no copy chip is added — the chip is for entities whose title is a
      // human name distinct from the token.
      title={token}
      description={t('roleScopeDescription', { scope: t(SCOPE_LABEL_KEY[scope]) })}
      action={
        <Button variant="destructive" size="sm" onClick={remove}>
          <Trash2 size={14} /> {t('common:delete')}
        </Button>
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

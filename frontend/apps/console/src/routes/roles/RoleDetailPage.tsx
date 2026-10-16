// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { AuthorityChecklist } from '@/components/AuthorityChecklist';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { mayWrite } from '@/lib/scope';
import {
  createTenantRole,
  deleteTenantRole,
  listTenantAuthorities,
  listTenantRoles,
  updateTenantRole,
  type TenantRole,
} from '@/lib/api/members';
import { memberErrorMessage } from '@/routes/members/memberErrors';

// RoleDetailPage defines a new tenant role (/roles/new) or shows one (/roles/:token). A
// role this tenant defined is edited here; an operator's role is shown read-only, because
// only the operator can change it.
export default function RoleDetailPage() {
  const { t } = useTranslation('members');
  const { token } = useParams<{ token: string }>();
  const creating = token === undefined;
  const { data: roles, loading, error } = useQuery(listTenantRoles, []);
  const { data: vocabulary } = useQuery(listTenantAuthorities, []);
  const role = creating ? null : ((roles ?? []).find((r) => r.token === token) ?? null);

  const title = creating ? t('newRole') : role ? role.name || role.token : (token ?? '');
  return (
    <PageShell title={title}>
      {!creating && loading && !roles ? (
        <LoadingState description={t('rolesLoading')} />
      ) : !creating && error ? (
        <ErrorState description={error} />
      ) : !creating && !role ? (
        <ErrorState description={t('roleNotFound', { token })} />
      ) : (
        <RoleForm key={role?.token ?? 'new'} role={role} vocabulary={vocabulary ?? []} />
      )}
    </PageShell>
  );
}

function RoleForm({ role, vocabulary }: { role: TenantRole | null; vocabulary: string[] }) {
  const { t } = useTranslation('members');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const held = claims?.authorities ?? [];
  const instanceRole = role?.owner === 'INSTANCE';
  const editable = mayWrite(claims, 'role:write') && !instanceRole;

  const [token, setToken] = useState(role?.token ?? '');
  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [authorities, setAuthorities] = useState<string[]>(role ? [...role.authorities] : []);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const fail = (err: unknown) => setFormError(memberErrorMessage(err, t, { CONFLICT: 'members:errorRoleTokenTaken' }));

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (role) {
        // The form shows all three fields, so it states the full end state: a cleared box
        // clears the field rather than leaving it alone.
        await updateTenantRole(role.token, {
          name: name.trim() || null,
          description: description.trim() || null,
          authorities,
        });
        toast(t('roleSavedToast', { token: role.token }));
      } else {
        const created = token.trim();
        await createTenantRole({
          token: created,
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          authorities,
        });
        toast(t('roleCreatedToast', { token: created }));
        navigate(`/roles/${encodeURIComponent(created)}`, { replace: true });
      }
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!role) return;
    if (
      !(await confirm({
        title: t('deleteRoleTitle'),
        description: t('deleteRoleConfirm', { token: role.token }),
        confirmLabel: t('deleteRole'),
        destructive: true,
      }))
    )
      return;
    try {
      await deleteTenantRole(role.token);
      toast(t('roleDeletedToast', { token: role.token }));
      navigate('/roles');
    } catch (err) {
      fail(err);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {instanceRole && <p className="text-sm text-muted-foreground">{t('instanceRoleReadOnly')}</p>}
      <SectionPanel>
        <div className="space-y-4">
          <FormField label={t('common:colToken')} htmlFor="tr-token">
            {role ? (
              <Input id="tr-token" value={token} disabled />
            ) : (
              <TokenField
                id="tr-token"
                entityType="role"
                value={token}
                onChange={setToken}
                seed={name}
                placeholder={t('roleTokenPlaceholder')}
              />
            )}
          </FormField>
          <FormField label={t('common:colName')} htmlFor="tr-name">
            <Input id="tr-name" value={name} disabled={!editable} onChange={(e) => setName(e.target.value)} />
          </FormField>
          <FormField label={t('common:colDescription')} htmlFor="tr-desc">
            <Input
              id="tr-desc"
              value={description}
              disabled={!editable}
              onChange={(e) => setDescription(e.target.value)}
            />
          </FormField>
        </div>
      </SectionPanel>
      <SectionPanel title={t('colAuthorities')} description={editable ? t('authoritiesHint') : undefined}>
        <AuthorityChecklist
          // Before the vocabulary arrives (or for a reader who cannot fetch it) the role's
          // own authorities still render, so a read-only view is never blank.
          vocabulary={vocabulary.length > 0 ? vocabulary : authorities}
          value={authorities}
          onChange={setAuthorities}
          held={held}
          readOnly={!editable}
        />
      </SectionPanel>
      {editable && (
        <div className="flex gap-2">
          <Button onClick={submit} loading={busy} disabled={busy || (!role && !token.trim())}>
            {role ? t('common:saveChanges') : t('createRole')}
          </Button>
          {role && (
            <Button variant="destructive" onClick={remove}>
              <Trash2 size={14} /> {t('deleteRole')}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

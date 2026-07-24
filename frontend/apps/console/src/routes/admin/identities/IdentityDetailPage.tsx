// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Ban, Power, Trash2, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { useAuth } from '@/auth/AuthProvider';
import {
  listIdentities,
  listTenants,
  listRoles,
  setIdentityEnabled,
  setSystemRoles,
  setPassword,
  deleteIdentity,
  addMembership,
  setMembershipRoles,
  setMembershipEnabled,
  removeMembership,
  type AdminIdentity,
} from '@/lib/api/admin';
import { StatusBadge, errMessage, useReload } from '@/routes/common';
import { CopyToken } from '@/components/ui/copy-token';

// toOptions turns a token+name record (tenant or role) into combobox options:
// the token is the value, a friendlier name is the label, and the raw token is
// shown as a secondary line so the exact value is still visible.
function toOptions(items: { token: string; name?: string | null }[] | null | undefined): ComboboxOption[] {
  return (items ?? []).map((i) => ({
    value: i.token,
    label: i.name || i.token,
    description: i.name ? i.token : undefined,
  }));
}

export default function IdentityDetailPage() {
  const { t } = useTranslation('identities');
  const { email: rawEmail } = useParams<{ email: string }>();
  const email = decodeURIComponent(rawEmail ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const { refreshMemberships } = useAuth();
  const [version, reload] = useReload();
  const { data: identities, loading, error } = useQuery(listIdentities, [version]);

  // After a membership change, also refresh the signed-in identity's own
  // memberships so a change to itself (e.g. a superuser granting itself a tenant)
  // shows in the tenant picker immediately, without a re-login. A change to
  // another identity re-reads own memberships harmlessly.
  const onChanged = useCallback(() => {
    reload();
    void refreshMemberships();
  }, [reload, refreshMemberships]);
  const { data: tenants } = useQuery(listTenants, [version]);
  const { data: systemRoles } = useQuery(() => listRoles('system'), [version]);
  const { data: tenantRoles } = useQuery(() => listRoles('tenant'), [version]);

  const tenantOptions = useMemo(() => toOptions(tenants), [tenants]);
  const systemRoleOptions = useMemo(() => toOptions(systemRoles), [systemRoles]);
  const tenantRoleOptions = useMemo(() => toOptions(tenantRoles), [tenantRoles]);

  const identity = identities?.find((i) => i.email === email) ?? null;


  if (loading) {
    return (
      <PageShell title={email}>
        <LoadingState description={t('loadingIdentity')} />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={email}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!identity) {
    return (
      <PageShell title={email}>
        <ErrorState description={t('notFound', { email })} />
      </PageShell>
    );
  }

  const toggleEnabled = async () => {
    try {
      await setIdentityEnabled(identity.email, !identity.enabled);
      toast(
        identity.enabled
          ? t('identityDisabledToast', { email: identity.email })
          : t('identityEnabledToast', { email: identity.email }),
      );
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const remove = async () => {
    if (
      !(await confirm({
        title: t('deleteIdentityTitle'),
        description: t('deleteIdentityConfirm', { email: identity.email }),
        confirmLabel: t('common:delete'),
      }))
    )
      return;
    try {
      await deleteIdentity(identity.email);
      toast(t('identityDeletedToast', { email: identity.email }));
      navigate('/admin/identities');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const fullName = [identity.firstName, identity.lastName].filter(Boolean).join(' ');

  return (
    <PageShell
      // The full name is the title, with the email as the copyable id chip beside it. An
      // identity may have no name entered, in which case the email IS the title and no chip
      // is shown (the chip carries the id only when the title is a human name).
      title={fullName || identity.email}
      titleAdornment={fullName ? <CopyToken value={identity.email} /> : undefined}
      description={<StatusBadge enabled={identity.enabled} />}
      action={
        <>
          <Button variant="outline" size="sm" onClick={toggleEnabled}>
            {identity.enabled ? <Ban size={14} /> : <Power size={14} />}
            {identity.enabled ? t('disable') : t('enable')}
          </Button>
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> {t('common:delete')}
          </Button>
        </>
      }
    >
      <IdentityDetail
        key={identity.email}
        identity={identity}
        tenantOptions={tenantOptions}
        systemRoleOptions={systemRoleOptions}
        tenantRoleOptions={tenantRoleOptions}
        onChanged={onChanged}
      />
    </PageShell>
  );
}

// ── Identity detail (system roles, password, memberships) ────────────────

function IdentityDetail({
  identity,
  tenantOptions,
  systemRoleOptions,
  tenantRoleOptions,
  onChanged,
}: {
  identity: AdminIdentity;
  tenantOptions: ComboboxOption[];
  systemRoleOptions: ComboboxOption[];
  tenantRoleOptions: ComboboxOption[];
  onChanged: () => void;
}) {
  const { t } = useTranslation('identities');
  const { toast } = useToast();
  const [sysRoles, setSysRoles] = useState<string[]>(identity.systemRoles);
  const [newPassword, setNewPassword] = useState('');
  const [memberError, setMemberError] = useState<string | null>(null);

  const run = async (fn: () => Promise<unknown>, ok: string) => {
    setMemberError(null);
    try {
      await fn();
      toast(ok);
      onChanged();
    } catch (err) {
      setMemberError(errMessage(err));
    }
  };

  return (
    <div className="space-y-6">
      {memberError && <ErrorBanner message={memberError} onDismiss={() => setMemberError(null)} />}

      <SectionPanel title={t('accessSectionTitle')}>
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label={t('systemRoles')} description={t('systemRolesDescription')}>
            <div className="flex gap-2">
              <MultiSelect
                className="flex-1"
                options={systemRoleOptions}
                value={sysRoles}
                onChange={setSysRoles}
                placeholder={t('selectSystemRolesPlaceholder')}
                searchPlaceholder={t('filterRolesPlaceholder')}
                emptyMessage={t('noSystemRolesMessage')}
              />
              <Button
                variant="outline"
                onClick={() => run(() => setSystemRoles(identity.email, sysRoles), t('systemRolesUpdatedToast'))}
              >
                {t('save')}
              </Button>
            </div>
          </FormField>
          <FormField label={t('setPasswordLabel')} description={t('setPasswordDescription')}>
            <div className="flex gap-2">
              <Input
                type="password"
                value={newPassword}
                placeholder={t('newPasswordPlaceholder')}
                onChange={(e) => setNewPassword(e.target.value)}
              />
              <Button
                variant="outline"
                disabled={!newPassword}
                onClick={() =>
                  run(async () => {
                    await setPassword(identity.email, newPassword);
                    setNewPassword('');
                  }, t('passwordUpdatedToast'))
                }
              >
                {t('setButton')}
              </Button>
            </div>
          </FormField>
        </div>
      </SectionPanel>

      <SectionPanel title={t('membershipsSectionTitle')}>
        {identity.memberships.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('noMemberships')}</p>
        ) : (
          <div className="space-y-2">
            {identity.memberships.map((m) => (
              <MembershipRow
                key={m.tenant}
                email={identity.email}
                membership={m}
                roleOptions={tenantRoleOptions}
                onChanged={onChanged}
              />
            ))}
          </div>
        )}
        <AddMembershipRow
          email={identity.email}
          tenantOptions={tenantOptions}
          roleOptions={tenantRoleOptions}
          existingTenants={identity.memberships.map((m) => m.tenant)}
          onChanged={onChanged}
        />
      </SectionPanel>
    </div>
  );
}

function MembershipRow({
  email,
  membership,
  roleOptions,
  onChanged,
}: {
  email: string;
  membership: AdminIdentity['memberships'][number];
  roleOptions: ComboboxOption[];
  onChanged: () => void;
}) {
  const { t } = useTranslation('identities');
  const { toast } = useToast();
  const [roles, setRoles] = useState<string[]>(membership.roles);

  const run = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      toast(ok);
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-background px-3 py-2">
      <span className="min-w-24 font-medium">{membership.tenant}</span>
      <StatusBadge enabled={membership.enabled} />
      <MultiSelect
        className="max-w-xs flex-1"
        options={roleOptions}
        value={roles}
        onChange={setRoles}
        placeholder={t('selectRolesPlaceholder')}
        searchPlaceholder={t('filterRolesPlaceholder')}
        emptyMessage={t('noTenantRolesMessage')}
      />
      <Button
        variant="outline"
        size="sm"
        onClick={() => run(() => setMembershipRoles(email, membership.tenant, roles), t('rolesUpdatedToast'))}
      >
        {t('saveRolesButton')}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() =>
          run(
            () => setMembershipEnabled(email, membership.tenant, !membership.enabled),
            membership.enabled ? t('membershipDisabledToast') : t('membershipEnabledToast'),
          )
        }
      >
        {membership.enabled ? t('disable') : t('enable')}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => run(() => removeMembership(email, membership.tenant), t('membershipRemovedToast'))}
      >
        <X size={14} /> {t('removeButton')}
      </Button>
    </div>
  );
}

function AddMembershipRow({
  email,
  tenantOptions,
  roleOptions,
  existingTenants,
  onChanged,
}: {
  email: string;
  tenantOptions: ComboboxOption[];
  roleOptions: ComboboxOption[];
  existingTenants: string[];
  onChanged: () => void;
}) {
  const { t } = useTranslation('identities');
  const { toast } = useToast();
  const [tenant, setTenant] = useState('');
  const [roles, setRoles] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  // Only offer tenants the identity isn't already a member of.
  const available = useMemo(
    () => tenantOptions.filter((o) => !existingTenants.includes(o.value)),
    [tenantOptions, existingTenants],
  );

  const add = async () => {
    setBusy(true);
    try {
      await addMembership(email, tenant, roles);
      toast(t('addedToTenantToast', { tenant }));
      setTenant('');
      setRoles([]);
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-3 flex flex-wrap items-end gap-2">
      <FormField label={t('addToTenantLabel')}>
        <Combobox
          className="w-56"
          options={available}
          value={tenant}
          onChange={setTenant}
          placeholder={t('selectTenantPlaceholder')}
          searchPlaceholder={t('filterTenantsPlaceholder')}
          emptyMessage={t('noTenantsAvailableMessage')}
        />
      </FormField>
      <FormField label={t('rolesLabel')}>
        <MultiSelect
          className="w-64"
          options={roleOptions}
          value={roles}
          onChange={setRoles}
          placeholder={t('selectRolesPlaceholder')}
          searchPlaceholder={t('filterRolesPlaceholder')}
          emptyMessage={t('noTenantRolesMessage')}
        />
      </FormField>
      <Button variant="outline" loading={busy} disabled={busy || !tenant} onClick={add}>
        {t('addMembershipButton')}
      </Button>
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Ban, Power, Trash2, X } from 'lucide-react';
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
import { useQuery } from '@/lib/hooks/use-query';
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
import { BackLink, StatusBadge, errMessage, useReload } from '@/routes/common';

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
  const { email: rawEmail } = useParams<{ email: string }>();
  const email = decodeURIComponent(rawEmail ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: identities, loading, error } = useQuery(listIdentities, [version]);
  const { data: tenants } = useQuery(listTenants, [version]);
  const { data: systemRoles } = useQuery(() => listRoles('system'), [version]);
  const { data: tenantRoles } = useQuery(() => listRoles('tenant'), [version]);

  const tenantOptions = useMemo(() => toOptions(tenants), [tenants]);
  const systemRoleOptions = useMemo(() => toOptions(systemRoles), [systemRoles]);
  const tenantRoleOptions = useMemo(() => toOptions(tenantRoles), [tenantRoles]);

  const identity = identities?.find((i) => i.email === email) ?? null;

  const back = <BackLink to="/admin/identities">Identities</BackLink>;

  if (loading) {
    return (
      <PageShell title={email} action={back}>
        <LoadingState description="Loading identity…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={email} action={back}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!identity) {
    return (
      <PageShell title={email} action={back}>
        <ErrorState description={`Identity “${email}” not found.`} />
      </PageShell>
    );
  }

  const toggleEnabled = async () => {
    try {
      await setIdentityEnabled(identity.email, !identity.enabled);
      toast(`Identity “${identity.email}” ${identity.enabled ? 'disabled' : 'enabled'}`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const remove = async () => {
    if (!window.confirm(`Delete identity “${identity.email}” and all its memberships?`)) return;
    try {
      await deleteIdentity(identity.email);
      toast(`Identity “${identity.email}” deleted`);
      navigate('/admin/identities');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const fullName = [identity.firstName, identity.lastName].filter(Boolean).join(' ');

  return (
    <PageShell
      title={identity.email}
      description={
        <div className="mt-1 flex items-center gap-2">
          <StatusBadge enabled={identity.enabled} />
          {fullName && <span className="text-sm text-muted-foreground">{fullName}</span>}
        </div>
      }
      action={
        <div className="flex items-center gap-2">
          {back}
          <Button variant="outline" size="sm" onClick={toggleEnabled}>
            {identity.enabled ? <Ban size={14} /> : <Power size={14} />}
            {identity.enabled ? 'Disable' : 'Enable'}
          </Button>
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
      }
    >
      <IdentityDetail
        key={identity.email}
        identity={identity}
        tenantOptions={tenantOptions}
        systemRoleOptions={systemRoleOptions}
        tenantRoleOptions={tenantRoleOptions}
        onChanged={reload}
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

      <SectionPanel title="Access">
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label="System roles" description="Roles that gate the admin API.">
            <div className="flex gap-2">
              <MultiSelect
                className="flex-1"
                options={systemRoleOptions}
                value={sysRoles}
                onChange={setSysRoles}
                placeholder="Select system roles…"
                searchPlaceholder="Filter roles…"
                emptyMessage="No system roles defined."
              />
              <Button
                variant="outline"
                onClick={() => run(() => setSystemRoles(identity.email, sysRoles), 'System roles updated')}
              >
                Save
              </Button>
            </div>
          </FormField>
          <FormField label="Set password" description="Replaces the identity's password.">
            <div className="flex gap-2">
              <Input
                type="password"
                value={newPassword}
                placeholder="New password"
                onChange={(e) => setNewPassword(e.target.value)}
              />
              <Button
                variant="outline"
                disabled={!newPassword}
                onClick={() =>
                  run(async () => {
                    await setPassword(identity.email, newPassword);
                    setNewPassword('');
                  }, 'Password updated')
                }
              >
                Set
              </Button>
            </div>
          </FormField>
        </div>
      </SectionPanel>

      <SectionPanel title="Memberships">
        {identity.memberships.length === 0 ? (
          <p className="text-sm text-muted-foreground">No tenant memberships.</p>
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
        placeholder="Select roles…"
        searchPlaceholder="Filter roles…"
        emptyMessage="No tenant roles defined."
      />
      <Button
        variant="outline"
        size="sm"
        onClick={() => run(() => setMembershipRoles(email, membership.tenant, roles), 'Roles updated')}
      >
        Save roles
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() =>
          run(
            () => setMembershipEnabled(email, membership.tenant, !membership.enabled),
            `Membership ${membership.enabled ? 'disabled' : 'enabled'}`,
          )
        }
      >
        {membership.enabled ? 'Disable' : 'Enable'}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => run(() => removeMembership(email, membership.tenant), 'Membership removed')}
      >
        <X size={14} /> Remove
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
      toast(`Added to “${tenant}”`);
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
      <FormField label="Add to tenant">
        <Combobox
          className="w-56"
          options={available}
          value={tenant}
          onChange={setTenant}
          placeholder="Select a tenant…"
          searchPlaceholder="Filter tenants…"
          emptyMessage="No tenants available."
        />
      </FormField>
      <FormField label="Roles">
        <MultiSelect
          className="w-64"
          options={roleOptions}
          value={roles}
          onChange={setRoles}
          placeholder="Select roles…"
          searchPlaceholder="Filter roles…"
          emptyMessage="No tenant roles defined."
        />
      </FormField>
      <Button variant="outline" loading={busy} disabled={busy || !tenant} onClick={add}>
        Add membership
      </Button>
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { Plus, X } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { ErrorBanner } from '@/components/ui/error-banner';
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
  listTenants,
  listRoles,
  createIdentity,
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
import { AdminCard, StatusBadge, errMessage, useReload } from '@/routes/admin/common';

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

export default function IdentitiesPage() {
  const [version, reload] = useReload();
  const { data: identities, loading, error } = useQuery(listIdentities, [version]);
  // Lists that back the tenant + role selectors, loaded once for the page.
  const { data: tenants } = useQuery(listTenants, [version]);
  const { data: systemRoles } = useQuery(() => listRoles('system'), [version]);
  const { data: tenantRoles } = useQuery(() => listRoles('tenant'), [version]);
  const { toast } = useToast();

  const tenantOptions = useMemo(() => toOptions(tenants), [tenants]);
  const systemRoleOptions = useMemo(() => toOptions(systemRoles), [systemRoles]);
  const tenantRoleOptions = useMemo(() => toOptions(tenantRoles), [tenantRoles]);

  const [creating, setCreating] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);

  const selectedIdentity = identities?.find((i) => i.email === selected) ?? null;

  const remove = async (i: AdminIdentity) => {
    if (!window.confirm(`Delete identity “${i.email}” and all its memberships?`)) return;
    try {
      await deleteIdentity(i.email);
      toast(`Identity “${i.email}” deleted`);
      if (selected === i.email) setSelected(null);
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
        <Button onClick={() => setCreating((v) => !v)}>
          <Plus size={16} /> New identity
        </Button>
      }
    >
      <div className="space-y-6">
        {creating && (
          <CreateIdentityForm
            roleOptions={systemRoleOptions}
            onClose={() => setCreating(false)}
            oncreated={(email) => {
              setCreating(false);
              setSelected(email);
              reload();
            }}
          />
        )}

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
                        onClick={() => setSelected(selected === i.email ? null : i.email)}
                      >
                        {selected === i.email ? 'Close' : 'Manage'}
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

        {selectedIdentity && (
          <IdentityDetail
            key={selectedIdentity.email}
            identity={selectedIdentity}
            tenantOptions={tenantOptions}
            systemRoleOptions={systemRoleOptions}
            tenantRoleOptions={tenantRoleOptions}
            onChanged={reload}
          />
        )}
      </div>
    </PageShell>
  );
}

// ── Create identity ─────────────────────────────────────────────────────

function CreateIdentityForm({
  roleOptions,
  onClose,
  oncreated,
}: {
  roleOptions: ComboboxOption[];
  onClose: () => void;
  oncreated: (email: string) => void;
}) {
  const { toast } = useToast();
  const [email, setEmail] = useState('');
  const [password, setPasswordValue] = useState('');
  const [firstName, setFirstName] = useState('');
  const [lastName, setLastName] = useState('');
  const [systemRoles, setSystemRolesValue] = useState<string[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      await createIdentity({
        email: email.trim(),
        password,
        firstName: firstName.trim() || undefined,
        lastName: lastName.trim() || undefined,
        enabled: true,
        systemRoles,
      });
      toast(`Identity “${email.trim()}” created`);
      oncreated(email.trim().toLowerCase());
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AdminCard title="New identity" description="An email-keyed global principal. Add tenant memberships after creating it.">
      <div className="space-y-4">
        {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label="Email" htmlFor="i-email">
            <Input id="i-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </FormField>
          <FormField label="Password" htmlFor="i-pw">
            <Input id="i-pw" type="password" value={password} onChange={(e) => setPasswordValue(e.target.value)} />
          </FormField>
          <FormField label="First name" htmlFor="i-fn">
            <Input id="i-fn" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
          </FormField>
          <FormField label="Last name" htmlFor="i-ln">
            <Input id="i-ln" value={lastName} onChange={(e) => setLastName(e.target.value)} />
          </FormField>
        </div>
        <FormField
          label="System roles"
          htmlFor="i-sys"
          description="System roles gate the admin API (e.g. superuser). Leave empty for none."
        >
          <MultiSelect
            id="i-sys"
            options={roleOptions}
            value={systemRoles}
            onChange={setSystemRolesValue}
            placeholder="Select system roles…"
            searchPlaceholder="Filter roles…"
            emptyMessage="No system roles defined."
          />
        </FormField>
        <div className="flex gap-2">
          <Button onClick={submit} loading={busy} disabled={busy || !email.trim() || !password}>
            Create identity
          </Button>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
        </div>
      </div>
    </AdminCard>
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
    <AdminCard title={`Manage “${identity.email}”`}>
      <div className="space-y-6">
        {memberError && <ErrorBanner message={memberError} onDismiss={() => setMemberError(null)} />}

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

        <div>
          <h4 className="mb-2 text-sm font-semibold text-foreground">Memberships</h4>
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
        </div>
      </div>
    </AdminCard>
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

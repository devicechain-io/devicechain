// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Plus, X } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
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
import { AdminCard, StatusBadge, errMessage, parseTokens, useReload } from '@/routes/admin/common';

export default function IdentitiesPage() {
  const [version, reload] = useReload();
  const { data: identities, loading, error } = useQuery(listIdentities, [version]);
  const { toast } = useToast();

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
          <IdentityDetail key={selectedIdentity.email} identity={selectedIdentity} onChanged={reload} />
        )}
      </div>
    </PageShell>
  );
}

// ── Create identity ─────────────────────────────────────────────────────

function CreateIdentityForm({
  onClose,
  oncreated,
}: {
  onClose: () => void;
  oncreated: (email: string) => void;
}) {
  const { toast } = useToast();
  const [email, setEmail] = useState('');
  const [password, setPasswordValue] = useState('');
  const [firstName, setFirstName] = useState('');
  const [lastName, setLastName] = useState('');
  const [systemRoles, setSystemRolesValue] = useState('');
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
        systemRoles: parseTokens(systemRoles),
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
          description='Space-separated system role tokens, e.g. "superuser". Leave blank for none.'
        >
          <Input id="i-sys" value={systemRoles} onChange={(e) => setSystemRolesValue(e.target.value)} />
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

function IdentityDetail({ identity, onChanged }: { identity: AdminIdentity; onChanged: () => void }) {
  const { toast } = useToast();
  const [sysRoles, setSysRoles] = useState(identity.systemRoles.join(' '));
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
          <FormField label="System roles" description="Space-separated system role tokens.">
            <div className="flex gap-2">
              <Input value={sysRoles} onChange={(e) => setSysRoles(e.target.value)} />
              <Button
                variant="outline"
                onClick={() => run(() => setSystemRoles(identity.email, parseTokens(sysRoles)), 'System roles updated')}
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
                <MembershipRow key={m.tenant} email={identity.email} membership={m} onChanged={onChanged} />
              ))}
            </div>
          )}
          <AddMembershipRow email={identity.email} onChanged={onChanged} />
        </div>
      </div>
    </AdminCard>
  );
}

function MembershipRow({
  email,
  membership,
  onChanged,
}: {
  email: string;
  membership: AdminIdentity['memberships'][number];
  onChanged: () => void;
}) {
  const { toast } = useToast();
  const [roles, setRoles] = useState(membership.roles.join(' '));

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
      <Input
        className="h-8 max-w-xs"
        value={roles}
        placeholder="role tokens"
        onChange={(e) => setRoles(e.target.value)}
      />
      <Button
        variant="outline"
        size="sm"
        onClick={() => run(() => setMembershipRoles(email, membership.tenant, parseTokens(roles)), 'Roles updated')}
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

function AddMembershipRow({ email, onChanged }: { email: string; onChanged: () => void }) {
  const { toast } = useToast();
  const [tenant, setTenant] = useState('');
  const [roles, setRoles] = useState('');
  const [busy, setBusy] = useState(false);

  const add = async () => {
    setBusy(true);
    try {
      await addMembership(email, tenant.trim(), parseTokens(roles));
      toast(`Added to “${tenant.trim()}”`);
      setTenant('');
      setRoles('');
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
        <Input className="h-9 w-40" value={tenant} placeholder="tenant token" onChange={(e) => setTenant(e.target.value)} />
      </FormField>
      <FormField label="Roles">
        <Input className="h-9 w-56" value={roles} placeholder="role tokens" onChange={(e) => setRoles(e.target.value)} />
      </FormField>
      <Button variant="outline" loading={busy} disabled={busy || !tenant.trim()} onClick={add}>
        Add membership
      </Button>
    </div>
  );
}

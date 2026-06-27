// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Plus } from 'lucide-react';
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
  listRoles,
  createRole,
  updateRole,
  deleteRole,
  type AdminRole,
} from '@/lib/api/admin';
import { AdminCard, Textarea, errMessage, parseTokens, useReload } from '@/routes/admin/common';

type Scope = 'system' | 'tenant';

export default function RolesPage() {
  const [version, reload] = useReload();
  const { data: roles, loading, error } = useQuery(listRoles, [version]);
  const { toast } = useToast();

  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<AdminRole | null>(null);
  const [scope, setScope] = useState<Scope>('tenant');
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [authorities, setAuthorities] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const startCreate = () => {
    setEditing(null);
    setScope('tenant');
    setToken('');
    setName('');
    setDescription('');
    setAuthorities('');
    setFormError(null);
    setOpen(true);
  };

  const startEdit = (r: AdminRole) => {
    setEditing(r);
    setScope(r.scope as Scope);
    setToken(r.token);
    setName(r.name ?? '');
    setDescription(r.description ?? '');
    setAuthorities(r.authorities.join(' '));
    setFormError(null);
    setOpen(true);
  };

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const auths = parseTokens(authorities);
      if (editing) {
        await updateRole(editing.scope, editing.token, {
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          authorities: auths,
        });
        toast(`Role “${editing.token}” updated`);
      } else {
        await createRole({
          scope,
          token: token.trim(),
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          authorities: auths,
        });
        toast(`Role “${token.trim()}” created`);
      }
      setOpen(false);
      reload();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (r: AdminRole) => {
    if (!window.confirm(`Delete the ${r.scope} role “${r.token}”? It will be removed from all assignees.`)) return;
    try {
      const ok = await deleteRole(r.scope, r.token);
      toast(ok ? `Role “${r.token}” deleted` : `Role “${r.token}” not found`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Roles"
      description="The global role catalog. System roles gate the admin API; tenant roles gate the data plane."
      action={
        <Button onClick={startCreate}>
          <Plus size={16} /> New role
        </Button>
      }
    >
      <div className="space-y-6">
        {open && (
          <AdminCard title={editing ? `Edit ${editing.scope} role “${editing.token}”` : 'New role'}>
            <div className="space-y-4">
              {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
              <FormField label="Scope">
                <div className="flex gap-2">
                  {(['tenant', 'system'] as Scope[]).map((s) => (
                    <Button
                      key={s}
                      type="button"
                      variant={scope === s ? 'default' : 'outline'}
                      size="sm"
                      disabled={editing !== null}
                      onClick={() => setScope(s)}
                    >
                      {s}
                    </Button>
                  ))}
                </div>
              </FormField>
              <FormField label="Token" htmlFor="r-token">
                <Input
                  id="r-token"
                  value={token}
                  disabled={editing !== null}
                  placeholder="operator"
                  onChange={(e) => setToken(e.target.value)}
                />
              </FormField>
              <FormField label="Name" htmlFor="r-name">
                <Input id="r-name" value={name} placeholder="Operator" onChange={(e) => setName(e.target.value)} />
              </FormField>
              <FormField label="Description" htmlFor="r-desc">
                <Input
                  id="r-desc"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </FormField>
              <FormField
                label="Authorities"
                htmlFor="r-auths"
                description='Space-separated, e.g. "device:read command:write". Use "*" for full access.'
              >
                <Textarea
                  id="r-auths"
                  value={authorities}
                  placeholder="device:read device:write"
                  onChange={(e) => setAuthorities(e.target.value)}
                />
              </FormField>
              <div className="flex gap-2">
                <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
                  {editing ? 'Save changes' : 'Create role'}
                </Button>
                <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
                  Cancel
                </Button>
              </div>
            </div>
          </AdminCard>
        )}

        {loading ? (
          <LoadingState description="Loading roles…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !roles || roles.length === 0 ? (
          <EmptyState description="No roles defined yet." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Scope</DataTableHeaderCell>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Authorities</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">Actions</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {roles.map((r) => (
                <DataTableRow key={r.id}>
                  <DataTableCell>
                    <Badge variant="secondary">{r.scope}</Badge>
                  </DataTableCell>
                  <DataTableCell className="font-medium">{r.token}</DataTableCell>
                  <DataTableCell>{r.name ?? '—'}</DataTableCell>
                  <DataTableCell>
                    <div className="flex flex-wrap gap-1">
                      {r.authorities.map((a) => (
                        <Badge key={a} variant="outline" className="font-mono text-xs">
                          {a}
                        </Badge>
                      ))}
                    </div>
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" size="sm" onClick={() => startEdit(r)}>
                        Edit
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => remove(r)}>
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

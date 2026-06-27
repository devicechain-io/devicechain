// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Plus } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
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
  listTenants,
  createTenant,
  updateTenant,
  setTenantEnabled,
  deleteTenant,
  type AdminTenant,
} from '@/lib/api/admin';
import { AdminCard, StatusBadge, Textarea, errMessage, useReload } from '@/routes/admin/common';

export default function TenantsPage() {
  const [version, reload] = useReload();
  const { data: tenants, loading, error } = useQuery(listTenants, [version]);
  const { toast } = useToast();

  // The create/edit form. editing holds the token being edited (null = create).
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [config, setConfig] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const startCreate = () => {
    setEditing(null);
    setToken('');
    setName('');
    setConfig('');
    setFormError(null);
    setOpen(true);
  };

  const startEdit = (t: AdminTenant) => {
    setEditing(t.token);
    setToken(t.token);
    setName(t.name ?? '');
    setConfig(t.config ?? '');
    setFormError(null);
    setOpen(true);
  };

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const cfg = config.trim() === '' ? undefined : config;
      if (editing) {
        await updateTenant(editing, { name: name.trim() || undefined, config: cfg });
        toast(`Tenant “${editing}” updated`);
      } else {
        await createTenant({ token: token.trim(), name: name.trim() || undefined, config: cfg });
        toast(`Tenant “${token.trim()}” created`);
      }
      setOpen(false);
      reload();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const toggleEnabled = async (t: AdminTenant) => {
    try {
      await setTenantEnabled(t.token, !t.enabled);
      toast(`Tenant “${t.token}” ${t.enabled ? 'disabled' : 'enabled'}`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const remove = async (t: AdminTenant) => {
    if (!window.confirm(`Delete tenant “${t.token}”? This cannot be undone.`)) return;
    try {
      const ok = await deleteTenant(t.token);
      toast(ok ? `Tenant “${t.token}” deleted` : `Tenant “${t.token}” not found`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Tenants"
      description="The instance's tenant registry. A tenant is a control-plane record, not a provisioned resource."
      action={
        <Button onClick={startCreate}>
          <Plus size={16} /> New tenant
        </Button>
      }
    >
      <div className="space-y-6">
        {open && (
          <AdminCard
            title={editing ? `Edit tenant “${editing}”` : 'New tenant'}
            description={editing ? undefined : 'The token is the tenant id used across the platform; it cannot change later.'}
          >
            <div className="space-y-4">
              {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
              <FormField label="Token" htmlFor="t-token">
                <Input
                  id="t-token"
                  value={token}
                  disabled={editing !== null}
                  placeholder="acme"
                  onChange={(e) => setToken(e.target.value)}
                />
              </FormField>
              <FormField label="Name" htmlFor="t-name">
                <Input id="t-name" value={name} placeholder="Acme Corp" onChange={(e) => setName(e.target.value)} />
              </FormField>
              <FormField label="Config (JSON)" htmlFor="t-config" description="Optional freeform JSON object.">
                <Textarea
                  id="t-config"
                  value={config}
                  placeholder='{ "tier": "gold" }'
                  onChange={(e) => setConfig(e.target.value)}
                />
              </FormField>
              <div className="flex gap-2">
                <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
                  {editing ? 'Save changes' : 'Create tenant'}
                </Button>
                <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
                  Cancel
                </Button>
              </div>
            </div>
          </AdminCard>
        )}

        {loading ? (
          <LoadingState description="Loading tenants…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !tenants || tenants.length === 0 ? (
          <EmptyState description="No tenants yet. Create the first one to get started." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Token</DataTableHeaderCell>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Status</DataTableHeaderCell>
              <DataTableHeaderCell>Config</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">Actions</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {tenants.map((t) => (
                <DataTableRow key={t.id}>
                  <DataTableCell className="font-medium">{t.token}</DataTableCell>
                  <DataTableCell>{t.name ?? '—'}</DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={t.enabled} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate font-mono text-xs text-muted-foreground">
                    {t.config ?? '—'}
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" size="sm" onClick={() => startEdit(t)}>
                        Edit
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => toggleEnabled(t)}>
                        {t.enabled ? 'Disable' : 'Enable'}
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => remove(t)}>
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

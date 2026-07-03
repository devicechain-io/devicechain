// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { createTenant, updateTenant, type AdminTenant } from '@/lib/api/admin';
import { Textarea, errMessage } from '@/routes/common';

// TenantForm creates a tenant (tenant absent) or edits one (tenant present, with
// its token fixed). Shared by the new + detail pages.
export function TenantForm({ tenant, onDone }: { tenant?: AdminTenant; onDone: (message: string) => void }) {
  const editing = tenant != null;
  const [token, setToken] = useState(tenant?.token ?? '');
  const [name, setName] = useState(tenant?.name ?? '');
  const [config, setConfig] = useState(tenant?.config ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const cfg = config.trim() === '' ? undefined : config;
      if (editing) {
        await updateTenant(tenant.token, { name: name.trim() || undefined, config: cfg });
        onDone(`Tenant “${tenant.token}” updated`);
      } else {
        await createTenant({ token: token.trim(), name: name.trim() || undefined, config: cfg });
        onDone(`Tenant “${token.trim()}” created`);
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label="Token"
        htmlFor="t-token"
        description={editing ? 'The tenant id used across the platform; it cannot change.' : undefined}
      >
        {editing ? (
          <Input id="t-token" value={token} disabled />
        ) : (
          <TokenField
            id="t-token"
            entityType="tenant"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder="acme"
          />
        )}
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
      </div>
    </div>
  );
}

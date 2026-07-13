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
  const [ingestRate, setIngestRate] = useState(tenant?.ingestMessagesPerSecond?.toString() ?? '');
  const [ingestBurst, setIngestBurst] = useState(tenant?.ingestBurst?.toString() ?? '');
  const [outboundRate, setOutboundRate] = useState(tenant?.outboundMessagesPerSecond?.toString() ?? '');
  const [outboundBurst, setOutboundBurst] = useState(tenant?.outboundBurst?.toString() ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // An empty override field submits undefined, so the request omits it and the
  // tenant inherits the platform default (clearing any existing override).
  const optNum = (s: string): number | undefined => {
    const n = Number(s.trim());
    return s.trim() === '' || !Number.isFinite(n) ? undefined : n;
  };

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const cfg = config.trim() === '' ? undefined : config;
      const gov = {
        ingestMessagesPerSecond: optNum(ingestRate),
        ingestBurst: optNum(ingestBurst),
        outboundMessagesPerSecond: optNum(outboundRate),
        outboundBurst: optNum(outboundBurst),
      };
      if (editing) {
        await updateTenant(tenant.token, { name: name.trim() || undefined, config: cfg, ...gov });
        onDone(`Tenant “${tenant.token}” updated`);
      } else {
        await createTenant({ token: token.trim(), name: name.trim() || undefined, config: cfg, ...gov });
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
      <div className="grid grid-cols-2 gap-2">
        <FormField
          label="Ingest rate (events/sec)"
          htmlFor="t-ingest-rate"
          description="Leave blank to inherit the platform default."
        >
          <Input
            id="t-ingest-rate"
            type="number"
            min="0"
            value={ingestRate}
            placeholder="default"
            onChange={(e) => setIngestRate(e.target.value)}
          />
        </FormField>
        <FormField
          label="Ingest burst"
          htmlFor="t-ingest-burst"
          description="Leave blank to inherit the platform default."
        >
          <Input
            id="t-ingest-burst"
            type="number"
            min="0"
            step="1"
            value={ingestBurst}
            placeholder="default"
            onChange={(e) => setIngestBurst(e.target.value)}
          />
        </FormField>
        <FormField
          label="Outbound rate (calls/sec)"
          htmlFor="t-outbound-rate"
          description="Rate ceiling for outbound connector actions. Leave blank to inherit the platform default."
        >
          <Input
            id="t-outbound-rate"
            type="number"
            min="0"
            value={outboundRate}
            placeholder="default"
            onChange={(e) => setOutboundRate(e.target.value)}
          />
        </FormField>
        <FormField
          label="Outbound burst"
          htmlFor="t-outbound-burst"
          description="Leave blank to inherit the platform default."
        >
          <Input
            id="t-outbound-burst"
            type="number"
            min="0"
            step="1"
            value={outboundBurst}
            placeholder="default"
            onChange={(e) => setOutboundBurst(e.target.value)}
          />
        </FormField>
      </div>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? 'Save changes' : 'Create tenant'}
        </Button>
      </div>
    </div>
  );
}

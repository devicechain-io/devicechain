// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import { createTenant, updateTenant, listTenantTiers, type AdminTenant } from '@/lib/api/admin';
import { Textarea, errMessage } from '@/routes/common';

// TenantForm creates a tenant (tenant absent) or edits one (tenant present, with
// its token fixed). Shared by the new + detail pages.
export function TenantForm({ tenant, onDone }: { tenant?: AdminTenant; onDone: (message: string) => void }) {
  const editing = tenant != null;
  const [token, setToken] = useState(tenant?.token ?? '');
  const [name, setName] = useState(tenant?.name ?? '');
  // The packaging tier (ADR-065). Required — every tenant has one, so a new tenant
  // starts with NO tier selected and the operator must choose. Deliberately not
  // pre-filled with a "sensible default": defaulting it would quietly recreate the
  // un-tiered tenant the required FK exists to rule out, and which tier a customer
  // is on is a commercial decision, not a form convenience.
  const [tierToken, setTierToken] = useState(tenant?.tier.token ?? '');
  const [config, setConfig] = useState(tenant?.config ?? '');
  const [ingestRate, setIngestRate] = useState(tenant?.ingestMessagesPerSecond?.toString() ?? '');
  const [ingestBurst, setIngestBurst] = useState(tenant?.ingestBurst?.toString() ?? '');
  const [outboundRate, setOutboundRate] = useState(tenant?.outboundMessagesPerSecond?.toString() ?? '');
  const [outboundBurst, setOutboundBurst] = useState(tenant?.outboundBurst?.toString() ?? '');
  // Per-tenant consent to route NL→rule authoring to an external AI model (ADR-056 §6).
  // Default off (fail-closed): a null/false flag means the tenant has not opted in.
  const [aiExternalEnabled, setAiExternalEnabled] = useState(tenant?.aiExternalEnabled === true);
  // How fast an opted-in tenant may spend inference budget. A separate dimension from
  // the consent flag above — that gates whether the tenant may route externally at all,
  // this gates how often — and declared per minute, since drafting is human-paced.
  const [aiRate, setAiRate] = useState(tenant?.aiInferenceRequestsPerMinute?.toString() ?? '');
  const [aiBurst, setAiBurst] = useState(tenant?.aiInferenceBurst?.toString() ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { data: tiers, error: tiersError } = useQuery(() => listTenantTiers(), []);

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
        // Sent explicitly (true/false) — consent is a deliberate operator decision, so
        // an unchecked box records "not opted in" rather than leaving it ambiguous.
        aiExternalEnabled,
        aiInferenceRequestsPerMinute: optNum(aiRate),
        aiInferenceBurst: optNum(aiBurst),
      };
      if (editing) {
        await updateTenant(tenant.token, { name: name.trim() || undefined, tierToken, config: cfg, ...gov });
        onDone(`Tenant “${tenant.token}” updated`);
      } else {
        await createTenant({ token: token.trim(), name: name.trim() || undefined, tierToken, config: cfg, ...gov });
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
      {/* The tenant's packaging tier (ADR-065) — one fact that several subsystems
          read: how the tenant is treated under contention, which AI models it may
          choose from, its default ceilings. Changing it takes effect within a
          minute and needs no restart. */}
      <FormField
        label="Tier"
        htmlFor="t-tier"
        description={
          editing
            ? 'The packaging this tenant is held to. Changing it applies within a minute.'
            : 'The packaging this tenant is held to. Sets its defaults across the platform; individual settings below can still override them.'
        }
      >
        <select
          id="t-tier"
          value={tierToken}
          onChange={(e) => setTierToken(e.target.value)}
          className="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          <option value="">Select a tier…</option>
          {(tiers ?? []).map((t) => (
            <option key={t.token} value={t.token}>
              {t.name || t.token}
            </option>
          ))}
        </select>
        {tiersError && <p className="mt-1 text-sm text-destructive">Tiers unavailable ({tiersError}).</p>}
      </FormField>
      <FormField label="Config (JSON)" htmlFor="t-config" description="Optional freeform JSON object.">
        <Textarea
          id="t-config"
          value={config}
          placeholder='{ "region": "us-east" }'
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
      {/* The per-tenant external-model consent gate (ADR-056 §6). */}
      <FormField
        label="External AI routing"
        htmlFor="t-ai-external"
        description="Consent to send this tenant's rule descriptions to an external AI model for drafting. Off by default; rule drafting stays unavailable for the tenant until this is granted."
      >
        <label className="flex items-center gap-2 text-sm">
          <input
            id="t-ai-external"
            type="checkbox"
            checked={aiExternalEnabled}
            onChange={(e) => setAiExternalEnabled(e.target.checked)}
            className="h-4 w-4 rounded border-input"
          />
          <span>Allow external AI routing for NL→rule authoring</span>
        </label>
      </FormField>
      <div className="grid grid-cols-2 gap-2">
        <FormField
          label="AI drafting rate (requests/min)"
          htmlFor="t-ai-rate"
          description="Rate ceiling for AI inference requests. Leave blank to inherit the platform default."
        >
          <Input
            id="t-ai-rate"
            type="number"
            min="0"
            value={aiRate}
            placeholder="default"
            onChange={(e) => setAiRate(e.target.value)}
          />
        </FormField>
        <FormField
          label="AI drafting burst"
          htmlFor="t-ai-burst"
          description="Leave blank to inherit the platform default."
        >
          <Input
            id="t-ai-burst"
            type="number"
            min="0"
            step="1"
            value={aiBurst}
            placeholder="default"
            onChange={(e) => setAiBurst(e.target.value)}
          />
        </FormField>
      </div>
      <div className="flex gap-2">
        {/* A tenant is never un-tiered, so submit stays disabled until one is
            picked — the client-side half of the required FK behind it. */}
        <Button onClick={submit} loading={busy} disabled={busy || !tierToken || (!editing && !token.trim())}>
          {editing ? 'Save changes' : 'Create tenant'}
        </Button>
      </div>
    </div>
  );
}

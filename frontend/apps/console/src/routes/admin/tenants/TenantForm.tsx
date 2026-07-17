// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState, type ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { Combobox } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { useQuery } from '@/lib/hooks/use-query';
import { createTenant, updateTenant, listTenantTiers, type AdminTenant } from '@/lib/api/admin';
import { Textarea, errMessage } from '@/routes/common';

// TenantForm creates a tenant (tenant absent) or edits one (tenant present, with
// its token fixed). Shared by the new + detail pages.
//
// LAYOUT. Create is a single flat form. Edit is TABBED: Basic (token/name/tier/config)
// and Settings (the governance ceilings + AI consent), plus two caller-supplied tabs that
// are NOT part of the tenant save — Effective settings (effectiveSettingsPanel, a read-only
// view of the result) and AI models (aiModelsPanel, the operator's per-function model
// assignment, ADR-065 S5c′, with its own mutations). Basic and Settings are two VIEWS OF ONE SAVE,
// not two independent forms: a tenant update sends name/tier/config and the ceilings
// together, so both tabs share this component's state and its single `submit`, and each
// renders the same Save button. Splitting them into separate submits could let one tab's
// save omit — and so silently reset — a field the other tab owns (the AI-consent flag is
// sent explicitly on every save, so a partial payload is not merely stale, it flips it).
export function TenantForm({
  tenant,
  onDone,
  effectiveSettingsPanel,
  aiModelsPanel,
}: {
  tenant?: AdminTenant;
  onDone: (message: string) => void;
  // When provided (edit only), the form renders tabbed and this is the read-only
  // Effective-settings tab.
  effectiveSettingsPanel?: ReactNode;
  // The AI-models tab (edit only): the operator's per-function model assignment for this
  // tenant (ADR-065 S5c′). Its own mutations, not part of the tenant save — like the
  // Effective tab, the caller passes it in.
  aiModelsPanel?: ReactNode;
}) {
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

  // The identity fields (Basic tab / top of the create form): token, name, tier, config.
  // Held as a fragment so create renders it inline and edit renders it in a tab.
  const identityFields = (
    <>
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
        {/* Tiers arrive in the operator's display order; the picker preserves it.
            A tier's name is the label, its token the muted second line — the same
            two facts, in the same order, an operator sees everywhere else. */}
        <Combobox
          id="t-tier"
          value={tierToken}
          onChange={setTierToken}
          placeholder="Select a tier…"
          searchPlaceholder="Search tiers…"
          emptyMessage="No tiers."
          allowClear={false}
          options={(tiers ?? []).map((t) => ({
            value: t.token,
            label: t.name || t.token,
            description: t.name ? t.token : undefined,
          }))}
        />
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
    </>
  );

  // The governance ceilings + AI consent (Settings tab / lower half of the create form).
  const settingsFields = (
    <>
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
    </>
  );

  // One handler, so the button is the same act wherever it is rendered (both edit tabs).
  // A tenant is never un-tiered, so submit stays disabled until one is picked — the
  // client-side half of the required FK behind it.
  const saveButton = (
    <div className="flex gap-2">
      <Button onClick={submit} loading={busy} disabled={busy || !tierToken || (!editing && !token.trim())}>
        {editing ? 'Save changes' : 'Create tenant'}
      </Button>
    </div>
  );

  const errorBanner = formError && (
    <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />
  );

  // Create: one flat form. The caller (NewTenantPage) supplies the SectionPanel wrapper.
  if (!editing || effectiveSettingsPanel === undefined) {
    return (
      <div className="space-y-4">
        {errorBanner}
        {identityFields}
        {settingsFields}
        {saveButton}
      </div>
    );
  }

  // Edit: tabbed. Basic and Settings are two views of one save (see the component doc), so
  // each tab renders the same Save button and both persist the whole tenant. Each tab owns
  // its own SectionPanel, so the detail page renders this form WITHOUT wrapping it.
  return (
    <div className="space-y-4">
      {errorBanner}
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">Basic</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="effective">Effective settings</TabsTrigger>
          {aiModelsPanel !== undefined && <TabsTrigger value="ai-models">AI models</TabsTrigger>}
        </TabsList>
        <TabsContent value="basic">
          <SectionPanel>
            <div className="space-y-4">
              {identityFields}
              {saveButton}
            </div>
          </SectionPanel>
        </TabsContent>
        <TabsContent value="settings">
          <SectionPanel>
            <div className="space-y-4">
              {settingsFields}
              {saveButton}
            </div>
          </SectionPanel>
        </TabsContent>
        {/* Read-only view of what the Settings tab resolves to, folded onto the tier. */}
        <TabsContent value="effective">
          <SectionPanel title="Effective settings">{effectiveSettingsPanel}</SectionPanel>
        </TabsContent>
        {/* The operator's per-function AI model assignment (ADR-065 S5c′) — its own
            mutations, not part of the tenant save. */}
        {aiModelsPanel !== undefined && (
          <TabsContent value="ai-models">
            <SectionPanel title="AI models">{aiModelsPanel}</SectionPanel>
          </TabsContent>
        )}
      </Tabs>
    </div>
  );
}

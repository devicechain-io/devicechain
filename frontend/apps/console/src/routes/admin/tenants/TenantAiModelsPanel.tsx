// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "AI Models" tab of a single tenant's detail page (ADR-065 S5c′). The operator
// assigns which model this tenant uses for each AI function — a per-tenant packaging
// decision, set here alongside the tenant's tier, NOT a tenant self-service toggle.
//
// It reads the ai-inference admin plane (the function vocabulary, this tenant's
// assignments, the tier grants, and this tenant's additive grants) and composes the menu
// and each function's effective model with buildTenantAiModels — the same union the server
// resolves, from grant facts the operator already sees. There is no new tenant-plane read:
// which model a tenant uses is operator config, decided on the operator's plane.
//
// The tenant's own facts (token, tier) arrive as props from the detail page, so this panel
// never re-fetches the tenant. Assigning or clearing calls the admin plane and reloads.

import { useMemo, useState } from 'react';
import { Combobox } from '@/components/ui/combobox';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { useReload, errMessage } from '@/routes/common';
import type { AdminTenant } from '@/lib/api/admin';
import {
  listAiFunctions,
  listAiFunctionAssignments,
  listAiProviderTierGrants,
  listAiProviderTenantGrants,
  setAiFunctionModel,
  clearAiFunctionModel,
} from '@/lib/api/ai-inference-admin';
import { buildTenantAiModels, modelLabel, type FunctionRow } from './tenantAiModels';

export function TenantAiModelsPanel({ tenant }: { tenant: AdminTenant }) {
  const { toast } = useToast();
  const [version, reload] = useReload();
  // The function token currently being mutated, so only its controls freeze.
  const [busy, setBusy] = useState<string | null>(null);
  const tierToken = tenant.tier.token;
  const tierLabel = tenant.tier.name || tenant.tier.token;

  const functions = useQuery(listAiFunctions, [version]);
  const assignments = useQuery(() => listAiFunctionAssignments(tenant.token), [version, tenant.token]);
  const tierGrants = useQuery(listAiProviderTierGrants, [version]);
  const tenantGrants = useQuery(() => listAiProviderTenantGrants(tenant.token), [version, tenant.token]);

  // First load only (no data yet): a reload after a save re-enters loading while keeping
  // prior data, and gating on bare loading would flap the tab to a spinner on every
  // assignment. busy already freezes the row's controls.
  const loading =
    (functions.loading && !functions.data) ||
    (assignments.loading && !assignments.data) ||
    (tierGrants.loading && !tierGrants.data) ||
    (tenantGrants.loading && !tenantGrants.data);
  const error = functions.error || assignments.error || tierGrants.error || tenantGrants.error;

  const model = useMemo(() => {
    if (!functions.data || !assignments.data || !tierGrants.data || !tenantGrants.data) return null;
    return buildTenantAiModels(
      functions.data,
      assignments.data,
      tierGrants.data,
      tenantGrants.data,
      tierToken,
    );
  }, [functions.data, assignments.data, tierGrants.data, tenantGrants.data, tierToken]);

  const assign = async (fn: string, provider: string) => {
    setBusy(fn);
    try {
      await setAiFunctionModel(tenant.token, fn, provider);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
    }
  };

  const clear = async (fn: string) => {
    setBusy(fn);
    try {
      await clearAiFunctionModel(tenant.token, fn);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
    }
  };

  if (loading) return <LoadingState description="Loading AI models…" />;
  if (error) return <ErrorState description={error} />;
  if (!model) return <ErrorState description="Could not load this tenant’s AI models." />;

  const menuEmpty = model.menu.length === 0;

  return (
    <div className="space-y-6">
      {!tenant.aiExternalEnabled && (
        <p className="rounded-md border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
          External AI routing is off for this tenant (Settings tab). Assignments here are saved,
          but the tenant cannot draft rules until it is enabled.
        </p>
      )}
      {menuEmpty && (
        <p className="rounded-md border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
          This tenant has no AI models available: the{' '}
          <span className="font-medium text-foreground">{tierLabel}</span> tier grants none (or the
          ones it grants are disabled), and this tenant has no additional grants. Package or re-enable
          a model on the tier (its AI Models tab), or grant one to this tenant, before assigning a
          model here.
        </p>
      )}
      <div className="space-y-4">
        {model.rows.map((row) => (
          <FunctionAssignmentRow
            key={row.token}
            row={row}
            menu={model.menu}
            tierDefault={model.tierDefault}
            tierLabel={tierLabel}
            busy={busy === row.token}
            onAssign={(provider) => assign(row.token, provider)}
            onClear={() => clear(row.token)}
          />
        ))}
      </div>
    </div>
  );
}

function FunctionAssignmentRow({
  row,
  menu,
  tierDefault,
  tierLabel,
  busy,
  onAssign,
  onClear,
}: {
  row: FunctionRow;
  menu: { token: string; name: string | null }[];
  tierDefault: { token: string; name: string | null; enabled: boolean } | null;
  tierLabel: string;
  busy: boolean;
  onAssign: (provider: string) => void;
  onClear: () => void;
}) {
  // The picker shows the assignment only when it is actually on the menu; a stale
  // assignment (off-menu) leaves the picker empty and is surfaced in the status line, so
  // the operator never sees a model selected that in fact resolves to nothing.
  const pickerValue = row.assigned && row.assignedOnMenu ? row.assigned.token : '';
  // The placeholder (shown whenever pickerValue is '') must state what the EMPTY picker
  // actually resolves to, not assume the tier default takes over. It only names the tier
  // default in the one state where the tenant really gets it — no assignment and an
  // ENABLED default. A stale assignment or a disabled default resolves to no model, and
  // the placeholder must not read "Tier default — X" there, or the picker face would
  // assert the very substitution the server refuses.
  const placeholder =
    row.effective.kind === 'tier-default' && tierDefault
      ? `Tier default — ${modelLabel(tierDefault)}`
      : 'No model — assign one';

  return (
    <div className="rounded-md border border-border p-4">
      <div className="mb-3">
        <p className="text-sm font-medium">{row.name}</p>
        <p className="text-sm text-muted-foreground">{row.description}</p>
      </div>
      <div className="flex items-center gap-2">
        <div className="flex-1">
          <Combobox
            id={`ai-fn-${row.token}`}
            value={pickerValue}
            onChange={onAssign}
            placeholder={placeholder}
            searchPlaceholder="Search models…"
            emptyMessage="No models on this tenant’s menu."
            allowClear={false}
            disabled={busy || menu.length === 0}
            options={menu.map((m) => ({
              value: m.token,
              label: m.name || m.token,
              description: m.name ? m.token : undefined,
            }))}
          />
        </div>
        {/* One explicit Clear, shown whenever a choice is stored (on- or off-menu), so a
            stale assignment the picker cannot display is still removable. Clearing falls
            the tenant back to its tier default. */}
        {row.assigned && (
          <Button variant="outline" size="sm" onClick={onClear} loading={busy} disabled={busy}>
            Clear
          </Button>
        )}
      </div>
      <EffectiveLine row={row} tierLabel={tierLabel} />
    </div>
  );
}

// EffectiveLine states what the function resolves to today, mirroring the server. The
// three NONE cases read as warnings because each leaves the tenant with no model; the two
// resolved cases are muted context.
function EffectiveLine({ row, tierLabel }: { row: FunctionRow; tierLabel: string }) {
  const e = row.effective;
  switch (e.kind) {
    case 'assigned':
      return (
        <p className="mt-2 text-sm text-muted-foreground">
          Uses {modelLabel({ token: e.token, name: e.name })}.
        </p>
      );
    case 'tier-default':
      return (
        <p className="mt-2 text-sm text-muted-foreground">
          No model assigned — falls back to the tier default,{' '}
          {modelLabel({ token: e.token, name: e.name })}.
        </p>
      );
    case 'none-assignment-off-menu':
      return (
        <p className="mt-2 text-sm text-destructive">
          Assigned {modelLabel({ token: e.token, name: e.name })}, which is no longer on this
          tenant’s menu — it resolves to no model until you pick an available one or the model is
          granted again.
        </p>
      );
    case 'none-default-disabled':
      return (
        <p className="mt-2 text-sm text-destructive">
          No model assigned. The tier’s default ({modelLabel({ token: e.token, name: e.name })}) is
          disabled, so nothing takes the call — this function is unavailable for the tenant.
        </p>
      );
    case 'none-no-default':
      return (
        <p className="mt-2 text-sm text-destructive">
          No model assigned and the {tierLabel} tier marks no default — this function is unavailable
          for the tenant. Assign a model above, or set a default on the tier.
        </p>
      );
  }
}

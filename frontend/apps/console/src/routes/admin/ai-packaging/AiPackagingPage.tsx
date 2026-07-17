// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI packaging screen (ADR-065 decision 10, slice S5b): which models each tier
// offers, and which one it defaults to. One panel per tier; inside it, one row per
// provider and two columns — grant and default.
//
// WHY THIS SCREEN EXISTS. The server deliberately never infers a default: a tier that
// grants models and marks none resolves to NO model, even when it grants exactly one.
// That is the correct answer — every alternative reads the size of a set an operator can
// change, and that shape shipped as a bug five times (model/grant.go carries the
// history). But correct and discoverable are different things, and until this screen
// existed an operator could grant a tier a model, stop, and find every tenant on it
// resolving to nothing with no indication why. THIS SCREEN IS THAT BEHAVIOUR'S ONLY
// MITIGATION: it shows the grant and the default as the two separate facts they are, and
// says plainly when a tier will resolve to none.
//
// It reads BOTH admin planes — the tier catalog is user-management's, the providers and
// grants are ai-inference's — which is why the join is rendered here rather than served
// by either. Neither service can see the other's half: ai-inference holds a service
// token and the tier catalog is on an identity-only plane, so it cannot even validate a
// tier token at write. Both planes are identity-lane and superuser-gated by
// AdminProtectedRoute, so this page performs no authority check of its own.
//
// It issues grant and default as SEPARATE mutations, and must keep doing so. Pre-selecting
// a default in the UI and having the operator confirm it is a choice; the same
// pre-selection made server-side is an inference. That distinction is the whole design.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { Checkbox } from '@/components/ui/checkbox';
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group';
import { SectionPanel } from '@/components/ui/section-panel';
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
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listAiProviders,
  listAiProviderTierGrants,
  grantAiProviderToTier,
  revokeAiProviderFromTier,
  setAiTierDefault,
  clearAiTierDefault,
  type AiProviderListItem,
} from '@/lib/api/ai-inference-admin';
import { listTenantTierCatalog } from '@/lib/api/admin';
import { useReload, errMessage } from '@/routes/common';
import { buildPackagingTiers, tierWarning, warningText, type PackagingTier } from './aiPackaging';

// Providers are instance config an operator hand-registers, so the realistic count is a
// handful and one page holds them all. Every provider needs a row (an ungranted one still
// has to be grantable), and the list API is the paginated one — so ask for more than
// anyone will have, and say so plainly if that ever stops being true rather than silently
// rendering a partial matrix.
const PROVIDER_PAGE_SIZE = 200;

// The radio value standing for "this tier marks no default". A provider token can never
// collide with it: core.ValidateToken's grammar is ^[A-Za-z0-9][A-Za-z0-9_-]*$, so a real
// token cannot begin with an underscore. The sentinel is safe by the grammar, not by
// convention.
const NO_DEFAULT = '__no_default__';

export default function AiPackagingPage() {
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  // One in-flight mutation at a time. The controls are cheap and each panel is small, so
  // freezing them all is simpler than per-cell pending state — and it removes the race
  // where two fast clicks on the same tier's default interleave.
  const [busy, setBusy] = useState(false);

  const { data: catalog, loading: catalogLoading, error: catalogError } = useQuery(
    listTenantTierCatalog,
    [],
  );
  const { data: providerResults, loading: providersLoading, error: providersError } = useQuery(
    () => listAiProviders({ pageNumber: 1, pageSize: PROVIDER_PAGE_SIZE }),
    [version],
  );
  const { data: grants, loading: grantsLoading, error: grantsError } = useQuery(
    listAiProviderTierGrants,
    [version],
  );

  const providers = useMemo(() => providerResults?.results ?? [], [providerResults]);
  const tiers = useMemo(
    () => (catalog && grants ? buildPackagingTiers(catalog, grants) : []),
    [catalog, grants],
  );

  const loading = catalogLoading || providersLoading || grantsLoading;
  const error = catalogError || providersError || grantsError;
  const truncated =
    providerResults != null && (providerResults.pagination.totalRecords ?? 0) > providers.length;

  const run = async (action: () => Promise<unknown>, ok: string) => {
    setBusy(true);
    try {
      await action();
      toast(ok);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
      // Reload on failure too: every control is rendered from server data, so refetching
      // is what snaps an optimistically-flipped checkbox back to the truth rather than
      // leaving the operator looking at a state the server rejected.
      reload();
    } finally {
      setBusy(false);
    }
  };

  const toggleGrant = async (tier: PackagingTier, provider: string, granted: boolean) => {
    if (!granted) {
      await run(
        () => grantAiProviderToTier(tier.token, provider),
        `“${provider}” is on the ${tier.token} menu`,
      );
      return;
    }
    // Revoking the tier's default is the one click here that silently changes what live
    // tenants resolve to: the mark rides the row being deleted, and the server promotes
    // nothing in its place. Say so before it happens.
    if (tier.defaultProvider === provider) {
      const ok = await confirm({
        title: `Revoke ${tier.token}'s default model`,
        description: `“${provider}” is ${tier.token}'s default. Revoking it leaves ${tier.token} with no default — no other model is promoted in its place, so every tenant at ${tier.token} that has not chosen a model itself will resolve to no model.`,
        confirmLabel: 'Revoke',
      });
      if (!ok) return;
    }
    await run(
      () => revokeAiProviderFromTier(tier.token, provider),
      `“${provider}” is off the ${tier.token} menu`,
    );
  };

  const chooseDefault = async (tier: PackagingTier, value: string) => {
    if (value === NO_DEFAULT) {
      await run(() => clearAiTierDefault(tier.token), `${tier.token} has no default model`);
      return;
    }
    await run(() => setAiTierDefault(tier.token, value), `${tier.token} defaults to “${value}”`);
  };

  return (
    <PageShell
      title="AI packaging"
      description="Which AI models each tier offers, and which one it falls back to. Granting a model puts it on the menu of every tenant at that tier; the default is what those tenants get for a function they have not assigned a model to themselves. They are separate decisions — a tier can offer models and default to none, in which case its tenants must each choose."
    >
      <div className="space-y-6">
        {loading ? (
          <LoadingState description="Loading packaging…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : providers.length === 0 ? (
          <EmptyState description="No AI providers registered yet. Register one before packaging it." />
        ) : tiers.length === 0 ? (
          <EmptyState description="No tiers defined yet. Tiers are what models are packaged onto." />
        ) : (
          <>
            {truncated && (
              <SectionPanel>
                <p className="text-sm text-muted-foreground">
                  Showing the first {providers.length} of {providerResults?.pagination.totalRecords}{' '}
                  providers. The panels below are not complete — a tier may grant a provider that
                  is not listed here.
                </p>
              </SectionPanel>
            )}

            {tiers.map((tier) => (
              <TierPanel
                key={tier.token}
                tier={tier}
                providers={providers}
                busy={busy}
                onToggleGrant={toggleGrant}
                onChooseDefault={chooseDefault}
              />
            ))}
          </>
        )}
      </div>
    </PageShell>
  );
}

function TierPanel({
  tier,
  providers,
  busy,
  onToggleGrant,
  onChooseDefault,
}: {
  tier: PackagingTier;
  providers: AiProviderListItem[];
  busy: boolean;
  onToggleGrant: (tier: PackagingTier, provider: string, granted: boolean) => void;
  onChooseDefault: (tier: PackagingTier, value: string) => void;
}) {
  const warning = tierWarning(tier, providers);

  return (
    <SectionPanel
      title={tier.token}
      description={tier.name ?? undefined}
      action={
        tier.known ? (
          <Badge variant="secondary">
            {tier.tenantCount} tenant{tier.tenantCount === 1 ? '' : 's'}
          </Badge>
        ) : (
          <Badge variant="outline">unknown tier</Badge>
        )
      }
    >
      <div className="space-y-4">
        {!tier.known && (
          <p className="text-sm text-muted-foreground">
            No tier with this token exists, so nothing resolves through these grants — no tenant
            can report a tier the catalog does not have. It is shown because this is the only
            screen that can reveal it: ai-inference cannot check a tier token when a grant is
            written, since the catalog lives on a plane its credential cannot reach. Untick every
            model below to clear it.
          </p>
        )}

        {warning && (
          <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
            <AlertTriangle size={16} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
            <span>{warningText(warning, tier)}</span>
          </div>
        )}

        {/* One RadioGroup per tier: Radix gives it "exactly one selected", arrow-key
            movement and role="radiogroup" for free, which is the same invariant
            uix_ai_tier_grant_default enforces in the database. Items need only be
            descendants, so the group can wrap the table and its items sit in cells. */}
        <RadioGroup
          value={tier.defaultProvider ?? NO_DEFAULT}
          onValueChange={(v) => onChooseDefault(tier, v)}
          disabled={busy}
          className="gap-0"
          aria-label={`Default model for ${tier.token}`}
        >
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Provider</DataTableHeaderCell>
              <DataTableHeaderCell className="w-24 text-center">Grant</DataTableHeaderCell>
              <DataTableHeaderCell className="w-24 text-center">Default</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {providers.map((p) => {
                const granted = tier.granted.has(p.token);
                return (
                  <DataTableRow key={p.token}>
                    <DataTableCell>
                      <div className="flex items-center gap-2">
                        <Link
                          to={`/admin/ai-providers/${encodeURIComponent(p.token)}`}
                          className="font-medium hover:underline"
                        >
                          {p.token}
                        </Link>
                        {!p.enabled && <Badge variant="outline">disabled</Badge>}
                      </div>
                      <span className="text-xs text-muted-foreground">{p.model}</span>
                    </DataTableCell>
                    <DataTableCell className="text-center">
                      <div className="flex justify-center">
                        <Checkbox
                          checked={granted}
                          disabled={busy}
                          aria-label={`Grant ${p.token} to ${tier.token}`}
                          onCheckedChange={() => onToggleGrant(tier, p.token, granted)}
                        />
                      </div>
                    </DataTableCell>
                    <DataTableCell className="text-center">
                      <div className="flex justify-center">
                        {/* An ungranted provider cannot be a default — the server refuses
                            it rather than granting as a side effect — so the control is
                            disabled rather than hidden, which shows the operator the order
                            of operations instead of concealing it. */}
                        <RadioGroupItem
                          value={p.token}
                          disabled={busy || !granted}
                          aria-label={`Make ${p.token} the default for ${tier.token}`}
                          title={granted ? undefined : 'Grant this model first'}
                        />
                      </div>
                    </DataTableCell>
                  </DataTableRow>
                );
              })}

              {/* The explicit "no default" option. Without a row to select, clearing a
                  default is an act with no control, and "this tier deliberately has no
                  default" becomes a state an operator can only fall into rather than
                  choose. It is the same group, so choosing it visibly deselects whichever
                  model was marked. */}
              <DataTableRow className="bg-muted/30">
                <DataTableCell>
                  <span className="font-medium">No default</span>
                  <span className="block text-xs text-muted-foreground">
                    Tenants at this tier must each choose a model
                  </span>
                </DataTableCell>
                <DataTableCell className="text-center text-muted-foreground">—</DataTableCell>
                <DataTableCell className="text-center">
                  <div className="flex justify-center">
                    <RadioGroupItem
                      value={NO_DEFAULT}
                      disabled={busy}
                      aria-label={`${tier.token} has no default model`}
                    />
                  </div>
                </DataTableCell>
              </DataTableRow>
            </DataTableBody>
          </DataTable>
        </RadioGroup>
      </div>
    </SectionPanel>
  );
}

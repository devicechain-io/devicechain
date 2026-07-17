// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI packaging matrix (ADR-065 decision 10, slice S5b): which models each tier
// offers, and which one it defaults to. One row per provider; each tier contributes two
// controls, grant and default.
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
} from '@/lib/api/ai-inference-admin';
import { listTenantTierCatalog } from '@/lib/api/admin';
import { useReload, errMessage } from '@/routes/common';
import { buildPackagingTiers, tierWarning, warningText, type PackagingTier } from './aiPackaging';

// Providers are instance config an operator hand-registers, so the realistic count is a
// handful and one page holds them all. The matrix needs every provider (an ungranted one
// still needs a row to be grantable), and the list API is the paginated one — so ask for
// more than anyone will have and say so plainly if that ever stops being true, rather
// than silently rendering a partial matrix.
const PROVIDER_PAGE_SIZE = 200;

// The grant/default pair shares one grid in the header and in every cell, so the two
// controls line up down the column without the table needing a second header row.
const PAIR_GRID = 'grid grid-cols-2 gap-3 w-20 place-items-center';

export default function AiPackagingPage() {
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  // One in-flight mutation at a time. The controls are cheap and the table is small, so
  // freezing all of them is simpler than per-cell pending state — and it removes the
  // race where two fast clicks on the same tier's default interleave.
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
  const warnings = useMemo(
    () => tiers.map((t) => ({ tier: t, warning: tierWarning(t, providers) })).filter((w) => w.warning),
    [tiers, providers],
  );
  const unknownTiers = useMemo(() => tiers.filter((t) => !t.known), [tiers]);

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
                  providers. This matrix is not complete — grants on the providers below the cut are
                  not shown here.
                </p>
              </SectionPanel>
            )}

            {warnings.length > 0 && (
              <SectionPanel
                title="Tiers that resolve to no model"
                description="These tiers offer models but will not serve one to a tenant that has not chosen for itself. That can be deliberate — it is what a tier with no default means — so nothing here is corrected automatically."
              >
                <ul className="space-y-2">
                  {warnings.map(({ tier, warning }) => (
                    <li key={tier.token} className="flex items-start gap-2 text-sm">
                      <AlertTriangle
                        size={16}
                        className="mt-0.5 shrink-0 text-amber-500"
                        aria-hidden
                      />
                      <span>
                        <span className="font-medium">{tier.token}</span> — {warningText(warning!, tier)}
                      </span>
                    </li>
                  ))}
                </ul>
              </SectionPanel>
            )}

            <DataTable className="overflow-x-auto">
              <DataTableHead>
                <DataTableHeaderCell>Provider</DataTableHeaderCell>
                {tiers.map((t) => (
                  <DataTableHeaderCell key={t.token}>
                    <div className="flex flex-col gap-1.5">
                      <span className="flex items-center gap-1.5 normal-case">
                        <span className="text-sm font-medium text-foreground">{t.token}</span>
                        {t.known ? (
                          <Badge variant="secondary">{t.tenantCount}</Badge>
                        ) : (
                          <Badge variant="outline">unknown</Badge>
                        )}
                      </span>
                      <span className={PAIR_GRID}>
                        <span>Grant</span>
                        <span>Default</span>
                      </span>
                    </div>
                  </DataTableHeaderCell>
                ))}
              </DataTableHead>
              <DataTableBody>
                {providers.map((p) => (
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
                    {tiers.map((t) => {
                      const granted = t.granted.has(p.token);
                      return (
                        <DataTableCell key={t.token}>
                          <div className={PAIR_GRID}>
                            <input
                              type="checkbox"
                              className="size-4 cursor-pointer accent-primary disabled:cursor-not-allowed"
                              checked={granted}
                              disabled={busy}
                              aria-label={`Grant ${p.token} to ${t.token}`}
                              onChange={() => void toggleGrant(t, p.token, granted)}
                            />
                            {/* A native radio group per tier: the browser enforces "at most
                                one default per tier" for free, which is the same invariant
                                uix_ai_tier_grant_default enforces in the database. An
                                ungranted provider cannot be a default — the server refuses
                                it rather than granting as a side effect — so the control is
                                disabled rather than hidden, which shows the operator the
                                order of operations instead of hiding it. */}
                            <input
                              type="radio"
                              name={`ai-default-${t.token}`}
                              className="size-4 cursor-pointer accent-primary disabled:cursor-not-allowed disabled:opacity-30"
                              checked={t.defaultProvider === p.token}
                              disabled={busy || !granted}
                              aria-label={`Make ${p.token} the default for ${t.token}`}
                              title={granted ? undefined : 'Grant this model first'}
                              onChange={() =>
                                void run(
                                  () => setAiTierDefault(t.token, p.token),
                                  `${t.token} defaults to “${p.token}”`,
                                )
                              }
                            />
                          </div>
                        </DataTableCell>
                      );
                    })}
                  </DataTableRow>
                ))}

                {/* The explicit "no default" option. Without a row to select, clearing a
                    default is an act with no control, and "this tier deliberately has no
                    default" becomes a state an operator can only fall into rather than
                    choose. It is the same radio group, so choosing it visibly deselects
                    whichever model was marked. */}
                <DataTableRow className="bg-muted/30">
                  <DataTableCell>
                    <span className="font-medium">No default</span>
                    <span className="block text-xs text-muted-foreground">
                      Tenants at this tier must each choose a model
                    </span>
                  </DataTableCell>
                  {tiers.map((t) => (
                    <DataTableCell key={t.token}>
                      <div className={PAIR_GRID}>
                        <span aria-hidden />
                        <input
                          type="radio"
                          name={`ai-default-${t.token}`}
                          className="size-4 cursor-pointer accent-primary disabled:cursor-not-allowed"
                          checked={t.defaultProvider === null}
                          disabled={busy}
                          aria-label={`${t.token} has no default model`}
                          onChange={() =>
                            void run(
                              () => clearAiTierDefault(t.token),
                              `${t.token} has no default model`,
                            )
                          }
                        />
                      </div>
                    </DataTableCell>
                  ))}
                </DataTableRow>
              </DataTableBody>
            </DataTable>

            {unknownTiers.length > 0 && (
              <SectionPanel
                title="Grants naming tiers that no longer exist"
                description="These grants name a tier the catalog does not have, so nothing resolves through them — no tenant can report a tier that is not there. They are shown because this is the only screen that can reveal them: ai-inference cannot check a tier token when a grant is written, since the catalog lives on a plane its credential cannot reach. Untick a column above to clear one."
              >
                <div className="flex flex-wrap gap-2">
                  {unknownTiers.map((t) => (
                    <Badge key={t.token} variant="outline">
                      {t.token}
                    </Badge>
                  ))}
                </div>
              </SectionPanel>
            )}
          </>
        )}
      </div>
    </PageShell>
  );
}

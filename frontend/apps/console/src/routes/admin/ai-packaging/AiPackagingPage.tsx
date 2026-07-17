// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI packaging screen (ADR-065 decision 10, slice S5b): which models each tier
// offers, and which one it defaults to. One panel per tier; inside it, one row per
// provider and two columns — grant and default. The panel and its mutations live in
// aiPackagingPanel.tsx, shared with a single tier's detail tab (TierAiModelsPanel); this
// screen is the cross-tier view — every tier at once, side by side.
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

import { useMemo } from 'react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useQuery } from '@/lib/hooks/use-query';
import { listAiProviders, listAiProviderTierGrants } from '@/lib/api/ai-inference-admin';
import { listTenantTierCatalog } from '@/lib/api/admin';
import { useReload } from '@/routes/common';
import { buildPackagingTiers } from './aiPackaging';
import { PROVIDER_PAGE_SIZE, TierPanel, useTierPackaging } from './aiPackagingPanel';

export default function AiPackagingPage() {
  const [version, reload] = useReload();
  const { busy, toggleGrant, chooseDefault } = useTierPackaging(reload);

  // Keyed on [version] like the other two: the tier catalog is half of this join, and a
  // tier created or deleted on the Tiers screen has to reach this one. Refreshing only
  // the ai-inference half left a deleted tier rendering as known and a warning's tenant
  // count arbitrarily stale.
  const { data: catalog, loading: catalogLoading, error: catalogError } = useQuery(
    listTenantTierCatalog,
    [version],
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

  // FIRST load only. `reload()` puts every query back into `loading`, so gating the whole
  // tree on it replaced the screen with a spinner after every single toggle — packaging
  // one tier is five gestures and was five full-screen flaps, each destroying focus and
  // scroll position. useQuery keeps the previous data across a refetch, and `busy`
  // already freezes the controls, so a refetch can repaint in place.
  const loading = (catalogLoading && !catalog) || (providersLoading && !providerResults) ||
    (grantsLoading && !grants);
  const error = catalogError || providersError || grantsError;
  const truncated =
    providerResults != null && (providerResults.pagination.totalRecords ?? 0) > providers.length;

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

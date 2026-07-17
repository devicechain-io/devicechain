// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI-models tab of a single tier's detail page (ADR-065 decision 10). It is the same
// grant/default control the cross-tier AI-packaging matrix uses (aiPackagingPanel.tsx),
// scoped to one tier: which models THIS tier offers, and which one it defaults to.
//
// It reads the ai-inference admin plane (providers + grants) itself rather than taking
// them as props, because the tier detail page lives on user-management's plane and knows
// nothing of providers. The tier's own facts (token, name, tenant count) come in as props
// — the detail page already has them — so this panel never re-fetches the tier catalog
// just to draw one row. It builds the one PackagingTier from those facts plus this tier's
// grants; a grant naming some OTHER tier is filtered out before the fold, so the "unknown
// tier" branch buildPackagingTiers has for orphan grants is never reachable here (a tier
// whose own detail page you are on is, by definition, known).

import { useMemo } from 'react';
import { SectionPanel } from '@/components/ui/section-panel';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useQuery } from '@/lib/hooks/use-query';
import { listAiProviders, listAiProviderTierGrants } from '@/lib/api/ai-inference-admin';
import { useReload } from '@/routes/common';
import { buildPackagingTiers } from '@/routes/admin/ai-packaging/aiPackaging';
import {
  PROVIDER_PAGE_SIZE,
  TierPanel,
  useTierPackaging,
} from '@/routes/admin/ai-packaging/aiPackagingPanel';

export function TierAiModelsPanel({
  token,
  name,
  tenantCount,
}: {
  token: string;
  name: string | null;
  tenantCount: number;
}) {
  const [version, reload] = useReload();
  const { busy, toggleGrant, chooseDefault } = useTierPackaging(reload);

  const { data: providerResults, loading: providersLoading, error: providersError } = useQuery(
    () => listAiProviders({ pageNumber: 1, pageSize: PROVIDER_PAGE_SIZE }),
    [version],
  );
  const { data: grants, loading: grantsLoading, error: grantsError } = useQuery(
    listAiProviderTierGrants,
    [version],
  );

  const providers = useMemo(() => providerResults?.results ?? [], [providerResults]);
  // Only this tier's grants feed the fold, so buildPackagingTiers returns exactly one
  // (known) tier — the token is in the input catalog, so it is never treated as orphan.
  const tier = useMemo(() => {
    if (!grants) return null;
    const mine = grants.filter((g) => g.tier === token);
    return buildPackagingTiers([{ token, name, tenantCount }], mine)[0] ?? null;
  }, [grants, token, name, tenantCount]);

  // First load only — see AiPackagingPage: reload() re-enters loading, and gating on it
  // would flap the whole tab to a spinner on every toggle. busy already freezes controls.
  const loading = (providersLoading && !providerResults) || (grantsLoading && !grants);
  const error = providersError || grantsError;
  const truncated =
    providerResults != null && (providerResults.pagination.totalRecords ?? 0) > providers.length;

  if (loading) return <LoadingState description="Loading models…" />;
  if (error) return <ErrorState description={error} />;
  if (providers.length === 0) {
    return (
      <EmptyState description="No AI providers registered yet. Register one on the AI providers screen before packaging it onto a tier." />
    );
  }
  if (!tier) return <ErrorState description="Could not load this tier’s AI models." />;

  return (
    <div className="space-y-6">
      {truncated && (
        <SectionPanel>
          <p className="text-sm text-muted-foreground">
            Showing the first {providers.length} of {providerResults?.pagination.totalRecords}{' '}
            providers. This tier may grant a provider that is not listed here.
          </p>
        </SectionPanel>
      )}
      <TierPanel
        tier={tier}
        providers={providers}
        busy={busy}
        onToggleGrant={toggleGrant}
        onChooseDefault={chooseDefault}
      />
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2, Users, Package, ChevronRight } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenantTierCatalog, deleteTenantTier } from '@/lib/api/admin';
import { BackLink, errMessage, useReload } from '@/routes/common';
import { TierForm } from '@/routes/admin/tiers/TierForm';
import { TierPill } from '@/components/tiers/TierPill';

export default function TierDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: tiers, loading, error } = useQuery(listTenantTierCatalog, [version]);

  const tier = tiers?.find((t) => t.token === token) ?? null;

  const back = <BackLink to="/admin/tiers">Tiers</BackLink>;

  if (loading) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description="Loading tier…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!tier) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={`Tier “${token}” not found.`} />
      </PageShell>
    );
  }

  const inUse = tier.tenantCount > 0;

  const remove = async () => {
    // The server refuses this while the tier has tenants (their tier is a required
    // FK, so there is nowhere to strand them). Asking here is a courtesy that saves a
    // round trip — it is NOT the enforcement, which is why the error below is still
    // surfaced rather than assumed unreachable.
    if (
      !(await confirm({
        title: 'Delete tier',
        description: `Delete “${tier.token}”? This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      const ok = await deleteTenantTier(tier.token);
      toast(ok ? `Tier “${tier.token}” deleted` : `Tier “${tier.token}” not found`);
      navigate('/admin/tiers');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={token}
      description={
        <div className="mt-1 flex items-center gap-2">
          <TierPill label={tier.token} color={tier.color} />
          {tier.name && <span className="text-sm font-medium">{tier.name}</span>}
          <Badge variant="secondary">
            <Users size={12} /> {tier.tenantCount} {tier.tenantCount === 1 ? 'tenant' : 'tenants'}
          </Badge>
        </div>
      }
      action={
        <div className="flex items-center gap-2">
          {back}
          <Button variant="destructive" size="sm" onClick={remove} disabled={inUse}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
      }
    >
      <div className="space-y-6">
        {inUse && (
          <p className="text-sm text-muted-foreground">
            {tier.tenantCount} {tier.tenantCount === 1 ? 'tenant is' : 'tenants are'} packaged at this
            tier. Saving a change here re-prices {tier.tenantCount === 1 ? 'it' : 'them'} within a
            minute — no restart, and no effect on any tenant given its own override. The tier cannot
            be deleted until {tier.tenantCount === 1 ? 'it is' : 'they are'} moved elsewhere.
          </p>
        )}
        <SectionPanel>
          <TierForm
            tier={tier}
            onDone={(m) => {
              toast(m);
              reload();
            }}
          />
        </SectionPanel>

        {/* AI model packaging is configuration OF the tiers (which models each grants),
            so it is reached from here rather than a top-level nav entry. The destination
            is the cross-tier matrix — the operator's side-by-side comparison — not a
            single-tier view, which is why the copy names all tiers. */}
        <SectionPanel title="AI models">
          <button
            type="button"
            onClick={() => navigate('/admin/ai-packaging')}
            className="group flex w-full items-center gap-3 rounded-md border border-border px-4 py-3 text-left transition hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
          >
            <Package size={18} className="shrink-0 text-muted-foreground" />
            <span className="flex-1">
              <span className="block text-sm font-medium">AI model packaging</span>
              <span className="block text-sm text-muted-foreground">
                Which AI models this tier grants, and its default — set side by side with the
                other tiers.
              </span>
            </span>
            <ChevronRight
              size={16}
              className="text-muted-foreground/50 transition group-hover:text-muted-foreground"
            />
          </button>
        </SectionPanel>
      </div>
    </PageShell>
  );
}

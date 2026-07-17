// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2, Users } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { listTenantTierCatalog, deleteTenantTier } from '@/lib/api/admin';
import { errMessage, useReload } from '@/routes/common';
import { TierForm } from '@/routes/admin/tiers/TierForm';
import { TierAiModelsPanel } from '@/routes/admin/tiers/TierAiModelsPanel';
import { TierPill } from '@/components/tiers/TierPill';
import { CopyToken } from '@/components/ui/copy-token';

export default function TierDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: tiers, loading, error } = useQuery(listTenantTierCatalog, [version]);

  const tier = tiers?.find((t) => t.token === token) ?? null;


  // Gate the spinner and error on the FIRST load only (no data yet). Saving reloads this
  // query, and useQuery re-enters `loading` on every refetch while keeping the prior
  // `tiers` — so a bare `if (loading)` unmounted the whole page (and TierForm) to a
  // spinner after every save, remounting the form on the default Basic tab and dumping an
  // operator who saved from Settings back to Basic. With `!tiers` the form stays mounted
  // and the active tab is preserved; the refetch repaints in place. (Same first-load
  // gating the matrix page and TierAiModelsPanel use.)
  if (loading && !tiers) {
    return (
      <PageShell title={token}>
        <LoadingState description="Loading tier…" />
      </PageShell>
    );
  }
  if (error && !tiers) {
    return (
      <PageShell title={token}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!tier) {
    return (
      <PageShell title={token}>
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
      // The display name is the title; its token rides on the same line as a copyable
      // chip. Below sit the colored tier pill (the color is the tier's identity) and the
      // tenant count (a tier can be unnamed, so fall back to the token as the title too).
      title={tier.name || tier.token}
      titleAdornment={tier.name ? <CopyToken value={tier.token} /> : undefined}
      description={
        <div className="flex items-center gap-2">
          <TierPill label={tier.token} color={tier.color} />
          <Badge variant="secondary">
            <Users size={12} /> {tier.tenantCount} {tier.tenantCount === 1 ? 'tenant' : 'tenants'}
          </Badge>
        </div>
      }
      action={
        <>
          <Button variant="destructive" size="sm" onClick={remove} disabled={inUse}>
            <Trash2 size={14} /> Delete
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {inUse && (
          <p className="text-sm text-muted-foreground">
            {tier.tenantCount} {tier.tenantCount === 1 ? 'tenant is' : 'tenants are'} packaged at this
            tier. Saving a change here re-prices {tier.tenantCount === 1 ? 'it' : 'them'} within a
            minute — no restart, and no effect on any tenant given its own override. The tier cannot
            be deleted until {tier.tenantCount === 1 ? 'it is' : 'they are'} moved elsewhere.
          </p>
        )}
        {/* Tabbed: Basic + Settings edit the tier (one atomic save), and the AI-models
            tab configures which models THIS tier grants — the same control the cross-tier
            packaging matrix uses, scoped to one tier. TierForm renders its own per-tab
            SectionPanels, so it is not wrapped here. */}
        <TierForm
          tier={tier}
          onDone={(m) => {
            toast(m);
            reload();
          }}
          aiModelsPanel={
            <TierAiModelsPanel token={tier.token} name={tier.name} tenantCount={tier.tenantCount} />
          }
        />
      </div>
    </PageShell>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2, Users } from 'lucide-react';
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
          <Badge variant="secondary">
            <Users size={12} /> {tier.tenantCount} {tier.tenantCount === 1 ? 'tenant' : 'tenants'}
          </Badge>
          {tier.name && <span className="text-sm text-muted-foreground">{tier.name}</span>}
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
      </div>
    </PageShell>
  );
}

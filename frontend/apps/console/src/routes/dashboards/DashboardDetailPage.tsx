// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { DashboardRenderer } from '@devicechain/widgets';
import {
  DashboardHub,
  createDeviceResolver,
  parseDashboardDefinition,
  type DashboardDefinition,
} from '@devicechain/dashboards';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useQuery } from '@/lib/hooks/use-query';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { getDashboard, deleteDashboard } from '@/lib/api/dashboards';
import { BackLink, errMessage } from '@/routes/common';

export default function DashboardDetailPage() {
  // useParams returns already-decoded values (React Router v6); the nav side
  // encodes, so a second decode here would double-decode (e.g. a "%" token throws).
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const { data, loading, error } = useQuery(() => getDashboard(token), [token]);

  // The runtime: one resolver (token/anchor → device ids) backing one hub that
  // multiplexes every widget's live stream. Torn down on unmount so the socket
  // subscriptions don't leak when leaving the page.
  const resolver = useMemo(() => createDeviceResolver(), []);
  const hub = useMemo(() => new DashboardHub({ resolver }), [resolver]);
  useEffect(() => () => hub.disposeAll(), [hub]);

  // Parse the stored JSON into a DashboardDefinition. A malformed definition must
  // surface as an error state, not a white screen — hence the guarded parse.
  const parsed = useMemo<
    { definition: DashboardDefinition } | { error: string } | null
  >(() => {
    if (!data) return null;
    try {
      return { definition: parseDashboardDefinition(JSON.parse(data.definition)) };
    } catch (err) {
      return { error: err instanceof Error ? err.message : 'Invalid dashboard definition.' };
    }
  }, [data]);

  const remove = async () => {
    if (!data) return;
    if (
      !(await confirm({
        title: 'Delete dashboard',
        description: `Delete “${data.token}”? This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      await deleteDashboard(data.token);
      toast(`Dashboard “${data.token}” deleted`);
      navigate('/dashboards');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const heading = data?.name || token;

  if (loading) {
    return (
      <PageShell title={token} banner="dashboard">
        <LoadingState description="Loading dashboard…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!data) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={`Dashboard “${token}” not found.`} />
      </PageShell>
    );
  }

  return (
    <PageShell
      title={heading}
      banner="dashboard"
      description={
        <div className="mt-1">
          <BackLink to="/dashboards">Dashboards</BackLink>
        </div>
      }
      action={
        <div className="flex items-center gap-2">
          {/* The canvas editor is PR F; the button is present but disabled so the
              placement is visible without implying it works yet. */}
          <Button variant="outline" size="sm" disabled>
            Edit
          </Button>
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
      }
    >
      {parsed && 'error' in parsed ? (
        <ErrorState description={`Could not render this dashboard: ${parsed.error}`} />
      ) : parsed ? (
        // DashboardRenderer's root fills 100% width/height, so it needs a bounded
        // container to give it real height inside the page flow.
        <div style={{ position: 'relative', height: 'calc(100vh - 180px)', minHeight: 400 }}>
          <DashboardRenderer definition={parsed.definition} hub={hub} resolver={resolver} />
        </div>
      ) : null}
    </PageShell>
  );
}

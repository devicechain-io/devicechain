// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo } from 'react';
import { useParams } from 'react-router-dom';
import {
  createDeviceResolver,
  migrateToSlots,
  parseDashboardDefinition,
  setTitle,
  type DashboardDefinition,
} from '@devicechain/dashboards';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useQuery } from '@/lib/hooks/use-query';
import { getDashboard } from '@/lib/api/dashboards';
import { DashboardWorkspace } from './editor/DashboardWorkspace';

export default function DashboardDetailPage() {
  // useParams returns already-decoded values (React Router v6); the nav side
  // encodes, so a second decode here would double-decode (e.g. a "%" token throws).
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';

  const { data, loading, error } = useQuery(() => getDashboard(token), [token]);

  // The resolver (token/anchor → device ids) that backs both the hub's stream
  // resolution and the renderer's history seeding. The hub itself is created by the
  // workspace, keyed on the slot manifest so a rebind gets a hub that already carries
  // the new bindings (constructing-with-bindings avoids an effect-ordering race where
  // widgets would subscribe before setBindings ran).
  const resolver = useMemo(() => createDeviceResolver(), []);

  // Parse the stored JSON into a DashboardDefinition. A malformed definition must
  // surface as an error state, not a white screen — hence the guarded parse. The
  // title is seeded from the dashboard's name when the definition carries none, so
  // BOTH the workspace's working and saved baselines start seeded (otherwise the
  // seed would read as a spurious unsaved edit).
  const parsed = useMemo<
    { definition: DashboardDefinition } | { error: string } | null
  >(() => {
    if (!data) return null;
    try {
      let definition = parseDashboardDefinition(JSON.parse(data.definition));
      if (definition.title === '' && data.name) definition = setTitle(definition, data.name);
      // Decisive pre-GA cutover: rewrite any concrete device/anchor selectors into
      // default-bound slots on load (idempotent), so authoring is slot-based and the
      // dashboard is export-ready. Persisted on the next save; renders identically.
      definition = migrateToSlots(definition);
      return { definition };
    } catch (err) {
      return { error: err instanceof Error ? err.message : 'Invalid dashboard definition.' };
    }
  }, [data]);

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
  if (parsed && 'error' in parsed) {
    return (
      <PageShell title={data.name || token} banner="dashboard">
        <ErrorState description={`Could not render this dashboard: ${parsed.error}`} />
      </PageShell>
    );
  }
  if (!parsed) return null;

  return (
    <DashboardWorkspace
      // Remount on token change so the workspace re-seeds working/saved from the
      // newly loaded definition (its state is initialized once from `loaded`).
      key={token}
      token={token}
      name={data.name ?? null}
      description={data.description ?? null}
      updatedAt={data.updatedAt ?? null}
      loaded={parsed.definition}
      resolver={resolver}
    />
  );
}

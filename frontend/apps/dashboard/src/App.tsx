// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboard app shell. Resolves the dashboard token from the path (/dash/<token>),
// gates on a reused console session, loads the definition from dashboard-management,
// parses it, and hands it to the renderer with a hub + concrete DeviceResolver.

import { gql } from '@devicechain/client';
import {
  DashboardHub,
  parseDashboardDefinition,
  type DashboardDefinition,
  type DeviceResolver,
} from '@devicechain/dashboards';
import { useEffect, useMemo, useState } from 'react';

import { hasValidSession } from './auth';
import { DashboardRenderer } from './DashboardRenderer';
import { DASHBOARD_BY_TOKEN } from './queries';
import { createDeviceResolver } from './resolver';

// The dashboard token is the path segment after the /dash/ base.
function dashboardTokenFromPath(): string {
  const path = window.location.pathname.replace(/^\/dash\/?/, '');
  return decodeURIComponent(path.split('/')[0] ?? '');
}

type LoadState =
  | { status: 'loading' }
  | { status: 'ready'; definition: DashboardDefinition; title: string }
  | { status: 'error'; message: string };

export default function App() {
  const token = useMemo(dashboardTokenFromPath, []);
  // One resolver shared by the hub (stream resolution) and the renderer (history
  // seeding) so token→id / anchor→devices lookups are cached once, and one hub for
  // the app's lifetime, torn down on unmount.
  const resolver = useMemo(() => createDeviceResolver(), []);
  const hub = useMemo(() => new DashboardHub({ resolver }), [resolver]);
  useEffect(() => () => hub.disposeAll(), [hub]);

  if (!hasValidSession()) return <SignInPrompt />;
  if (!token) return <Message title="No dashboard selected" detail="Open a dashboard from the console." />;
  return <DashboardView token={token} hub={hub} resolver={resolver} />;
}

function DashboardView({
  token,
  hub,
  resolver,
}: {
  token: string;
  hub: DashboardHub;
  resolver: DeviceResolver;
}) {
  const [state, setState] = useState<LoadState>({ status: 'loading' });

  useEffect(() => {
    let cancelled = false;
    setState({ status: 'loading' });
    gql('dashboard-management', DASHBOARD_BY_TOKEN, { token })
      .then((r) => {
        if (cancelled) return;
        if (!r.dashboard) {
          setState({ status: 'error', message: `Dashboard "${token}" not found.` });
          return;
        }
        const definition = parseDashboardDefinition(JSON.parse(r.dashboard.definition));
        setState({ status: 'ready', definition, title: r.dashboard.name ?? token });
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ status: 'error', message: errorMessage(err) });
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  if (state.status === 'loading') return <Message title="Loading…" />;
  if (state.status === 'error') return <Message title="Couldn’t load dashboard" detail={state.message} />;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <header
        style={{
          padding: '10px 16px',
          borderBottom: '1px solid hsl(var(--border))',
          fontWeight: 600,
          flex: '0 0 auto',
        }}
      >
        {state.title}
      </header>
      <main style={{ flex: '1 1 auto', minHeight: 0 }}>
        <DashboardRenderer definition={state.definition} hub={hub} resolver={resolver} />
      </main>
    </div>
  );
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'Unexpected error';
}

function SignInPrompt() {
  return (
    <Message
      title="Sign in required"
      detail="Your session has expired or you’re not signed in. Sign in through the console, then reopen this dashboard."
    />
  );
}

function Message({ title, detail }: { title: string; detail?: string }) {
  return (
    <div
      style={{
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 8,
        padding: 24,
        textAlign: 'center',
      }}
    >
      <div style={{ fontSize: 18, fontWeight: 600 }}>{title}</div>
      {detail && <div style={{ color: 'hsl(var(--muted-foreground))', maxWidth: 420 }}>{detail}</div>}
    </div>
  );
}

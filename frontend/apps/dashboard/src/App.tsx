// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboard app shell. Resolves the dashboard token from the path (/dash/<token>),
// gates on a reused console session, loads the definition from dashboard-management,
// parses it, and hands it to the renderer with a hub + concrete DeviceResolver.

import { gql } from '@devicechain/client';
import {
  createDeviceResolver,
  DashboardHub,
  isDirty,
  parseDashboardDefinition,
  serializeDefinition,
  WIDGET_TYPES,
  type DashboardDefinition,
  type DeviceResolver,
  type WidgetType,
} from '@devicechain/dashboards';
import { DashboardRenderer } from '@devicechain/widgets';
import { useEffect, useMemo, useState } from 'react';

import { hasValidSession } from './auth';
import { DashboardEditor } from './DashboardEditor';
import { addWidget, setTitle, updateWidget } from './editor-model';
import { DASHBOARD_BY_TOKEN, UPDATE_DASHBOARD } from './queries';
import { WidgetConfigPanel } from './WidgetConfigPanel';

// The dashboard token is the path segment after the /dash/ base.
function dashboardTokenFromPath(): string {
  const path = window.location.pathname.replace(/^\/dash\/?/, '');
  const segment = path.split('/')[0] ?? '';
  try {
    return decodeURIComponent(segment);
  } catch {
    // A malformed %-escape (URIError) → treat as no token rather than throwing.
    return '';
  }
}

type LoadState =
  | { status: 'loading' }
  | { status: 'ready'; definition: DashboardDefinition; description: string | null }
  | { status: 'error'; message: string };

type SaveState = { kind: 'clean' } | { kind: 'saving' } | { kind: 'error'; message: string };

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
        let definition = parseDashboardDefinition(JSON.parse(r.dashboard.definition));
        // If the stored definition has no title, seed it from the dashboard's name
        // so BOTH the working and saved baselines start seeded — otherwise the seed
        // would only land on the working copy and read as a spurious dirty edit.
        if (definition.title === '' && r.dashboard.name) {
          definition = setTitle(definition, r.dashboard.name);
        }
        setState({ status: 'ready', definition, description: r.dashboard.description ?? null });
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
    <DashboardWorkspace
      key={token}
      token={token}
      loaded={state.definition}
      description={state.description}
      hub={hub}
      resolver={resolver}
    />
  );
}

// DashboardWorkspace holds the view/edit mode and the working copy of the
// definition. View renders the read-only DashboardRenderer; edit renders the
// react-rnd canvas. Save persists the working copy via updateDashboard and
// re-baselines the dirty check.
function DashboardWorkspace({
  token,
  loaded,
  description,
  hub,
  resolver,
}: {
  token: string;
  loaded: DashboardDefinition;
  description: string | null;
  hub: DashboardHub;
  resolver: DeviceResolver;
}) {
  const [mode, setMode] = useState<'view' | 'edit'>('view');
  const [working, setWorking] = useState<DashboardDefinition>(loaded);
  const [saved, setSaved] = useState<DashboardDefinition>(loaded);
  // One save state, not scattered saving/error booleans — can't be both at once.
  const [saveState, setSaveState] = useState<SaveState>({ kind: 'clean' });
  // Selection is owned here (not in the editor) so the config panel and the
  // editor stay in sync; leaving edit mode clears it.
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const dirty = isDirty(working, saved);
  const title = working.title || token;
  const selected = working.widgets.find((w) => w.id === selectedId) ?? null;

  // Warn before a tab close/reload discards unsaved edits (the browser shows its
  // own generic prompt when a beforeunload handler cancels). Only armed while dirty.
  useEffect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [dirty]);

  const addWidgetOfType = (type: WidgetType) => {
    const { definition, id } = addWidget(working, type);
    setWorking(definition);
    setSelectedId(id); // open the new widget's config panel
  };

  const toggleMode = () => {
    setSelectedId(null);
    setMode(mode === 'edit' ? 'view' : 'edit');
  };

  const save = () => {
    const snapshot = working; // persist exactly what we serialize; later edits stay dirty
    setSaveState({ kind: 'saving' });
    gql('dashboard-management', UPDATE_DASHBOARD, {
      token,
      // description is preserved verbatim (the editor doesn't edit it); name tracks
      // the (now-seeded) title so a save never wipes either field.
      request: {
        token,
        name: snapshot.title || null,
        description,
        definition: serializeDefinition(snapshot),
      },
    })
      .then(() => {
        setSaved(snapshot);
        setSaveState({ kind: 'clean' });
      })
      .catch((err: unknown) => setSaveState({ kind: 'error', message: errorMessage(err) }));
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '8px 16px',
          borderBottom: '1px solid hsl(var(--border))',
          flex: '0 0 auto',
        }}
      >
        {mode === 'edit' ? (
          <input
            value={working.title}
            onChange={(e) => setWorking(setTitle(working, e.target.value))}
            placeholder="Dashboard title"
            style={{
              flex: '1 1 auto',
              fontSize: 16,
              fontWeight: 600,
              padding: '4px 8px',
              borderRadius: 6,
              border: '1px solid hsl(var(--border))',
              background: 'hsl(var(--card))',
              color: 'hsl(var(--foreground))',
            }}
          />
        ) : (
          <div style={{ flex: '1 1 auto', fontWeight: 600 }}>{title}</div>
        )}

        {saveState.kind === 'error' && (
          <span style={{ color: 'hsl(var(--destructive))', fontSize: 13 }}>{saveState.message}</span>
        )}
        {mode === 'edit' && dirty && saveState.kind !== 'error' && (
          <span style={{ color: 'hsl(var(--muted-foreground))', fontSize: 13 }}>Unsaved changes</span>
        )}

        {mode === 'edit' && <AddWidgetMenu onAdd={addWidgetOfType} />}
        {mode === 'edit' && (
          <HeaderButton onClick={save} disabled={!dirty || saveState.kind === 'saving'} primary>
            {saveState.kind === 'saving' ? 'Saving…' : 'Save'}
          </HeaderButton>
        )}
        <HeaderButton onClick={toggleMode}>{mode === 'edit' ? 'Done' : 'Edit'}</HeaderButton>
      </header>

      <main style={{ flex: '1 1 auto', minHeight: 0 }}>
        {mode === 'edit' ? (
          <div style={{ display: 'flex', height: '100%' }}>
            <div style={{ flex: '1 1 auto', minWidth: 0 }}>
              <DashboardEditor
                definition={working}
                onChange={setWorking}
                hub={hub}
                selectedId={selectedId}
                onSelect={setSelectedId}
              />
            </div>
            {selected && (
              <WidgetConfigPanel
                widget={selected}
                onChange={(next) => setWorking(updateWidget(working, next.id, next))}
                onClose={() => setSelectedId(null)}
              />
            )}
          </div>
        ) : (
          <DashboardRenderer definition={working} hub={hub} resolver={resolver} />
        )}
      </main>
    </div>
  );
}

// AddWidgetMenu is a native select that adds a widget of the chosen type, then
// snaps back to its placeholder so it reads as an action, not a stored value.
function AddWidgetMenu({ onAdd }: { onAdd: (type: WidgetType) => void }) {
  return (
    <select
      value=""
      onChange={(e) => {
        const type = e.target.value as WidgetType;
        if (type) onAdd(type);
        e.target.value = '';
      }}
      style={{
        fontSize: 14,
        padding: '6px 10px',
        borderRadius: 6,
        border: '1px solid hsl(var(--border))',
        cursor: 'pointer',
        color: 'hsl(var(--foreground))',
        background: 'hsl(var(--card))',
        flex: '0 0 auto',
      }}
    >
      <option value="" disabled>
        + Add widget
      </option>
      {WIDGET_TYPES.map((type) => (
        <option key={type} value={type}>
          {type}
        </option>
      ))}
    </select>
  );
}

function HeaderButton({
  children,
  onClick,
  disabled,
  primary,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
  primary?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      style={{
        fontSize: 14,
        padding: '6px 14px',
        borderRadius: 6,
        border: '1px solid hsl(var(--border))',
        cursor: disabled ? 'default' : 'pointer',
        opacity: disabled ? 0.5 : 1,
        color: primary ? 'hsl(var(--primary-foreground))' : 'hsl(var(--foreground))',
        background: primary ? 'hsl(var(--primary))' : 'hsl(var(--card))',
        flex: '0 0 auto',
      }}
    >
      {children}
    </button>
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

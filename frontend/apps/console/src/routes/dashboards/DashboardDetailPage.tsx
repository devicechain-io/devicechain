// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  createDeviceResolver,
  migrateToSlots,
  parseDashboardDefinition,
  type DashboardDefinitionError,
  setTitle,
  type DashboardDefinition,
} from '@devicechain/dashboards';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useQuery } from '@/lib/hooks/use-query';
import { mayWrite } from '@/lib/scope';
import { useAuth } from '@/auth/AuthProvider';
import {
  getDashboard,
  getPublishedDashboard,
  isNotPublishedError,
  type Dashboard,
} from '@/lib/api/dashboards';
import { preloadWidgets } from '@devicechain/widgets';
import { DashboardWorkspace } from './editor/DashboardWorkspace';
import { defaultWidgetRegistry } from './editor/widgetRegistry';
import '@/i18n/lazy/dashboards';

// The marker loadPublished answers for a dashboard that exists but was never published.
const NOT_PUBLISHED = Symbol('not-published');

// Adapts the published snapshot to the shape the page renders from the draft. The
// snapshot has no updatedAt: a viewer never saves, so there is no baseline to carry.
async function loadPublished(token: string): Promise<Dashboard | typeof NOT_PUBLISHED | null> {
  try {
    const published = await getPublishedDashboard(token);
    if (!published) return null;
    return {
      token: published.token,
      name: published.name,
      description: published.description,
      definition: published.definition,
      updatedAt: null,
      publishedVersion: published.version,
    };
  } catch (err) {
    if (isNotPublishedError(err)) return NOT_PUBLISHED;
    throw err;
  }
}

// The widget types a stored definition names, read leniently: this only decides which widget code
// to fetch ahead of the first frame. A definition that does not parse yields none, and the guarded
// parse below reports it.
function widgetTypesOf(definition: string): string[] {
  try {
    const widgets: unknown = (JSON.parse(definition) as { widgets?: unknown }).widgets;
    if (!Array.isArray(widgets)) return [];
    return widgets.flatMap((w: unknown) =>
      w !== null && typeof w === 'object' && typeof (w as { type?: unknown }).type === 'string'
        ? [(w as { type: string }).type]
        : [],
    );
  } catch {
    return [];
  }
}

export default function DashboardDetailPage() {
  const { t } = useTranslation('dashboards');
  // useParams returns already-decoded values (React Router v6); the nav side
  // encodes, so a second decode here would double-decode (e.g. a "%" token throws).
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';

  // The draft is author-only: a member without dashboard:write reads the PUBLISHED
  // snapshot instead, shown read-only (the workspace derives canEdit from the same
  // authority). They see nothing until someone publishes.
  const { claims } = useAuth();
  // A member limited to part of the tenant (a customer user) is served only the published
  // versions shared with their customers, whatever their role says about writing.
  const canEdit = mayWrite(claims, 'dashboard:write');
  // The one widget registry this deployment composed (shared with /dash and the replay
  // player): parse, edit, validate, preview and render all go through it.
  const registry = defaultWidgetRegistry();
  const { data, loading, error } = useQuery(
    async () => {
      const loaded = await (canEdit ? getDashboard(token) : loadPublished(token));
      // The page stays on its loading state until the code of the board's lazily loaded widgets
      // has arrived too, so the board's first frame is the whole board rather than empty boxes
      // still filling in. Never rejects: a chunk that fails still fails in its widget's boundary.
      if (loaded && loaded !== NOT_PUBLISHED) await preloadWidgets(registry, widgetTypesOf(loaded.definition));
      return loaded;
    },
    [token, canEdit],
  );

  // The resolver (anchor → device tokens) that backs the hub's stream resolution.
  // The hub itself is created by the workspace, once per (resolver, authorities).
  const resolver = useMemo(() => createDeviceResolver(), []);

  // Parse the stored JSON into a DashboardDefinition. A malformed definition must
  // surface as an error state, not a white screen — hence the guarded parse. The
  // title is seeded from the dashboard's name when the definition carries none, so
  // BOTH the workspace's working and saved baselines start seeded (otherwise the
  // seed would read as a spurious unsaved edit).
  const parsed = useMemo<
    { definition: DashboardDefinition } | { error: string } | { tooNew: true } | null
  >(() => {
    if (!data || data === NOT_PUBLISHED) return null;
    try {
      // Parsed WITH the deployment's catalog: an installed extension widget is recognized
      // (not read as a placeholder) and a stored widget's settings version is checked
      // against the installed one. The workspace is handed the same registry below.
      let definition = parseDashboardDefinition(JSON.parse(data.definition), { catalog: registry.catalog });
      if (definition.title === '' && data.name) definition = setTitle(definition, data.name);
      // Decisive pre-GA cutover: rewrite any concrete device/anchor selectors into
      // default-bound slots on load (idempotent), so authoring is slot-based and the
      // dashboard is export-ready. Persisted on the next save; renders identically.
      definition = migrateToSlots(definition);
      return { definition };
    } catch (err) {
      // Refused whole -- for an author too: the editor is never mounted over a board it cannot
      // read, so there is no save to write a lossy copy over the newer one.
      if ((err as DashboardDefinitionError | null)?.code === 'schemaTooNew') return { tooNew: true };
      return { error: err instanceof Error ? err.message : t('detailInvalidDefinition') };
    }
  }, [data, t, registry]);

  if (loading) {
    return (
      <PageShell title={token} banner="dashboard">
        <LoadingState description={t('detailLoading')} />
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
  if (data === NOT_PUBLISHED) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={t('detailNotPublished', { token })} />
      </PageShell>
    );
  }
  if (!data) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={t('detailNotFound', { token })} />
      </PageShell>
    );
  }
  if (parsed && 'error' in parsed) {
    return (
      <PageShell title={data.name || token} banner="dashboard">
        <ErrorState description={t('detailParseError', { error: parsed.error })} />
      </PageShell>
    );
  }
  if (parsed && 'tooNew' in parsed) {
    return (
      <PageShell title={data.name || token} banner="dashboard">
        <ErrorState description={t('schemaTooNew')} />
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
      updatedAt={data.updatedAt ?? null}
      loaded={parsed.definition}
      resolver={resolver}
      registry={registry}
    />
  );
}

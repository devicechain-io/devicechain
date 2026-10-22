// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console layout gate's page: one dashboard-editor surface, drawn by the console's own
// components at whatever viewport the browser has, in light or dark, in any shipped locale.
//
//   layout.html?surface=versions           the dashboard Version history drawer, with a
//                                          history in which every row but the live one
//                                          offers both Make live and Roll back
//   layout.html?surface=canvas&selected=ID the editor canvas with widget ID selected, so its
//                                          toolbar (Grid, Relative, Duplicate, ...) is shown
//   layout.html?surface=settings           the widget settings panel for an alarm table whose pinned
//                                          device-type list holds a type the catalogue lacks
//   &labels=none                           (versions) a history in which no version has a label
//   &lang=es                               the locale (default en)
//   &theme=light                           light; anything else is dark, the console default
//
// DATA. The drawer reads its history over GraphQL; this page answers those requests itself
// (a fetch stub keyed by operation name), so it needs no backend and the rows are the same on
// every run. The history is written to be WIDE: a long label, a long unbroken publisher
// address, the live badge — the shapes that push the action buttons toward the drawer's edge.
//
// The page reports through attributes on <html>, never text: data-gate="ready", or
// data-gate="error" with data-gate-error.

import '@/i18n/config';
import '@fontsource-variable/inter';
import '@/index.css';

import { parseDashboardDefinition, SyntheticDataSource } from '@devicechain/dashboards';
import i18n from 'i18next';
import { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';
import { WidgetConfigPanel } from '@/routes/dashboards/editor/WidgetConfigPanel';
import { DashboardCanvas } from '@/routes/dashboards/editor/DashboardCanvas';
import { VersionHistorySheet } from '@/routes/dashboards/editor/VersionHistorySheet';

const LIVE_VERSION = 3;

// &labels=none drops every label: the first column then holds only a number and, on the live
// row, the badge, so it is the column a table short of room squeezes hardest. That is the
// history in which the badge once broke inside its word.
const UNLABELLED = new URLSearchParams(window.location.search).get('labels') === 'none';

const VERSIONS = [
  { version: 4, label: 'Night shift tuning', publishedBy: 'operations.engineering.lead@plant-north.example.com' },
  { version: 3, label: 'Quarterly review layout', publishedBy: 'operations.engineering.lead@plant-north.example.com' },
  { version: 2, label: null, publishedBy: 'a@example.com' },
  { version: 1, label: 'Initial', publishedBy: null },
].map((v) => ({ ...v, label: UNLABELLED ? null : v.label, description: null, publishedAt: '2026-12-28T22:58:58Z' }));

// The canned answers, by GraphQL operation name. An operation not listed is an error, so a new
// request the drawer starts making shows up here rather than rendering as an empty state.
const ANSWERS: Record<string, unknown> = {
  DashboardVersions: { dashboardVersions: VERSIONS },
  DashboardPublishedVersion: { dashboard: { publishedVersion: LIVE_VERSION } },
  // The device-type catalogue the settings panel's pickers read: one type, so the other pinned
  // row's token is "unknown", and long enough to need more than a phone row's width.
  DeviceTypes: {
    deviceTypes: { results: [{ token: 'thermostat', name: 'Thermostat' }], pagination: { totalRecords: 1 } },
  },
};

const realFetch = window.fetch.bind(window);
window.fetch = async (input, init) => {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
  if (!url.includes('/graphql')) return realFetch(input, init);
  const body = JSON.parse(String(init?.body ?? '{}')) as { query?: string };
  const op = /(?:query|mutation)\s+(\w+)/.exec(body.query ?? '')?.[1] ?? '?';
  const data = ANSWERS[op];
  const payload = data === undefined ? { errors: [{ message: `layout gate: no answer for ${op}` }] } : { data };
  return new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } });
};

const gauge = (id: string, title: string, col: number, row: number, cols: number, rows: number) => ({
  id,
  type: 'gauge',
  layout: { base: { mode: 'grid' as const, col, row, cols, rows } },
  options: { title, min: 0, max: 100 },
});

// A board with widgets at the top edge (row 0, where nothing sits above them), beside each
// other, and below another, each with a title long enough to reach under a toolbar.
const BOARD = parseDashboardDefinition({
  title: 'Layout gate',
  widgets: [
    gauge('top-left', 'Boiler outlet temperature', 0, 0, 3, 3),
    gauge('top-right', 'Condenser return pressure', 9, 0, 3, 3),
    gauge('below', 'Feedwater pump discharge flow', 0, 3, 4, 3),
    gauge('narrow', 'Stack oxygen', 4, 3, 2, 2),
  ],
});

function Ready({ children }: { children: React.ReactNode }) {
  useEffect(() => {
    document.documentElement.dataset.gate = 'ready';
  }, []);
  return <>{children}</>;
}

function Canvas({ initial }: { initial: string | null }) {
  const [definition, setDefinition] = useState(BOARD);
  const [selected, setSelected] = useState<string | null>(initial);
  const [hub] = useState(() => new SyntheticDataSource({ generator: 'sine', min: 42, max: 42 }));
  return (
    <div style={{ position: 'fixed', inset: 0 }} data-testid="canvas-host">
      <DashboardCanvas
        definition={definition}
        onChange={(next) => setDefinition(next)}
        onDuplicate={() => {}}
        onCopy={() => {}}
        hub={hub}
        bindings={{}}
        selectedId={selected}
        onSelect={setSelected}
      />
    </div>
  );
}

async function main() {
  const params = new URLSearchParams(window.location.search);
  document.documentElement.classList.toggle('dark', params.get('theme') !== 'light');
  await i18n.changeLanguage(params.get('lang') ?? 'en');
  const surface = params.get('surface');
  let page: React.ReactNode;
  if (surface === 'versions') {
    page = (
      <VersionHistorySheet
        token="layout-gate"
        open
        onOpenChange={() => {}}
        saved={BOARD}
        dirty={false}
        saving={false}
        canWrite
        expectedUpdatedAt={null}
        onRolledBack={() => {}}
        onStripUnknownOptions={() => {}}
        onFitToLimits={() => {}}
      />
    );
  } else if (surface === 'canvas') {
    page = <Canvas initial={params.get('selected')} />;
  } else if (surface === 'settings') {
    page = (
      <WidgetConfigPanel
        widget={{
          id: 'alarms',
          type: 'alarm-table',
          layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 6, rows: 4 } },
          options: {
            groupBy: 'deviceType',
            deviceTypes: [{ token: 'thermostat' }, { token: 'retired-boiler-controller-v2-legacy' }],
          },
        }}
        datasource={undefined}
        slots={{}}
        onChange={() => {}}
        onDatasource={() => {}}
        onScope={() => {}}
        onClose={() => {}}
      />
    );
  } else {
    throw new Error(`surface must be versions, canvas or settings, got '${surface}'`);
  }
  createRoot(document.getElementById('root')!).render(
    <ToastProvider>
      <ConfirmProvider>
        <Ready>{page}</Ready>
      </ConfirmProvider>
    </ToastProvider>,
  );
}

main().catch((err: unknown) => {
  const root = document.documentElement;
  root.dataset.gateError = err instanceof Error ? err.message : String(err);
  root.dataset.gate = 'error';
});

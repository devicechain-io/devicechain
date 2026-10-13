// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { Page } from '@playwright/test';

// A recipe is data plus, at most, a short `setup`. Selectors are data-shot* anchors
// (or the few data-testid already on the right element), never CSS classes or visible
// text: text is translated, so a text selector would break the `es` run. The static
// anchor check (check-anchors.mjs) IMPORTS this module (so it must stay free of runtime
// imports: `import type` only) and verifies every anchor a recipe names still exists in
// the console source. Convention: frontend/apps/console/CONVENTIONS.md.

export interface Recipe {
  /** Stable kebab-case id: the image name (`{id}.{locale}.webp`) and the manifest key. */
  id: string;
  /** Which signed-in identity shoots it: `tenant` (hero tenant admin), `widgets` (the widgetlab tenant's admin, dashboards chapter), `superuser` (skipped, loudly, without $DC_ADMIN_PASSWORD), or `none` (the sign-in screens: the real form, no storage state). */
  session: 'tenant' | 'superuser' | 'widgets' | 'none';
  route: string;
  /** Drives the page into the state to shoot (open a drawer, pick a tab). Throwing fails the recipe. */
  setup?: (page: Page) => Promise<void>;
  /** Must be visible once setup has run, or the recipe fails (fail closed). */
  anchor: string;
  /** Crop to this element instead of the whole viewport. */
  clip?: string;
  /** Paint over every [data-shot-mask] region (live times, live values, status). */
  mask?: boolean;
  /** Freeze `Date` at this instant, for pages where "now" shows. Not for pages with live data. */
  clock?: string;
  /** Serve map tiles from the committed HAR (see tiles.har / GUIDE_HAR_RECORD). */
  tiles?: boolean;
  /** Rows the screen must show; fewer fails the recipe. */
  minRows?: { selector: string; count: number };
  /** The screen legitimately shows an EMPTY state (the default is to fail on one). An error state always fails. */
  allowEmpty?: boolean;
  /**
   * Console source files (relative to frontend/apps/console/src) that must carry this
   * recipe's page-specific anchors: tab keys, drawer ids, page buttons, test ids. Pins an
   * anchor to the screen that owns it, so another file's identical value cannot satisfy it.
   * Anchors of the shared primitives (page, drawer, confirm, sheet, empty/error/loading) are
   * checked globally and by the primitives' own unit tests.
   */
  sources?: string[];
}

const PAGE = '[data-shot="page"]';
const ROWS = `${PAGE} table tbody tr`;
const DRAWER = '[data-shot="drawer"]';

// Where the console's own client-side router can go but a fresh page load cannot: the
// instance serves the console behind a static `/assets/` mount, so `/assets/<token>` (an
// asset's detail page) is a 404 on a direct load while the same URL works from inside the
// app. Recipes reach such pages by navigating the way a user does, through the router.
async function spa(page: Page, path: string) {
  await page.evaluate((p) => {
    window.history.pushState({}, '', p);
    window.dispatchEvent(new PopStateEvent('popstate'));
  }, path);
}

/** Open a combobox inside `scope` (a selector) and pick the option whose value is `value`. */
async function pick(page: Page, scope: string, value: string) {
  await page.locator(`${scope} [data-shot="combobox"]`).first().click();
  await page.locator(`[data-shot="combobox-option"][data-shot-value="${value}"]`).first().click();
}

const DEVICE_DETAIL = 'routes/devices/DeviceDetailPage.tsx';
const DEVICES_PAGE = 'routes/devices/DevicesPage.tsx';

/** A tab of a device's detail page, opened by its tab key. */
function deviceTab(
  id: string,
  token: string,
  tab: string,
  opts: Partial<Recipe> & { then?: (page: Page) => Promise<void> } = {},
): Recipe {
  const { then, ...rest } = opts;
  return {
    id,
    session: 'tenant',
    route: `/devices/${token}`,
    setup: async (page) => {
      await page.locator(`[data-shot-tab="${tab}"]`).click();
      await then?.(page);
    },
    anchor: `[data-shot-tab="${tab}"][data-state="active"]`,
    sources: [DEVICE_DETAIL],
    ...rest,
  };
}

/** A tab of a registry detail page (device type, profile, asset, area...). */
function resourceTab(
  id: string,
  route: string,
  tab: string,
  source: string,
  opts: Partial<Recipe> & { then?: (page: Page) => Promise<void>; spa?: boolean } = {},
): Recipe {
  const { then, spa: viaRouter, ...rest } = opts;
  return {
    id,
    session: 'tenant',
    route: viaRouter ? '/' : route,
    setup: async (page) => {
      if (viaRouter) {
        await spa(page, route);
        await page.locator(`[data-shot-tab]`).first().waitFor({ timeout: 30_000 });
      }
      await page.locator(`[data-shot-tab="${tab}"]`).click();
      await then?.(page);
    },
    anchor: `[data-shot-tab="${tab}"][data-state="active"]`,
    sources: [source],
    ...rest,
  };
}

/** A list page that opens its New drawer from the page's New button. */
function newDrawer(id: string, route: string, source: string, session: Recipe['session'] = 'tenant'): Recipe {
  return {
    id,
    session,
    route,
    setup: async (page) => {
      await page.locator('[data-shot="new-button"]').click();
    },
    anchor: DRAWER,
    sources: [source],
  };
}

const PROFILE = 'routes/device-profiles/resource.tsx';
const PROFILE_ROUTE = '/device-profiles/bp-thermostat-profile';

export const recipes: Recipe[] = [
  // ---- Chapter 1: Signing in ---------------------------------------------------------
  // The REAL form, with no storage state. Tenant chooser and the SSO screens need a
  // second membership and an identity provider (slice S3).
  {
    id: 'signin-identify',
    session: 'none',
    route: '/login',
    anchor: '[data-shot="login-step-identify"]',
    sources: ['routes/Login.tsx'],
  },
  {
    id: 'signin-password',
    session: 'none',
    route: '/login',
    setup: async (page) => {
      await page.locator('[data-shot="login-email"]').fill('dana.okafor@acme-properties.example');
      await page.locator('[data-shot="login-continue"]').click();
    },
    anchor: '[data-shot="login-step-password"]',
    sources: ['routes/Login.tsx'],
  },
  {
    id: 'signin-session-expired',
    session: 'none',
    route: '/login',
    setup: async (page) => {
      // The console flags a lapsed session in sessionStorage and the login screen reads it once.
      await page.evaluate(() => window.sessionStorage.setItem('dc-session-expired', '1'));
      await page.reload();
    },
    anchor: '[data-shot="login-session-expired"]',
    sources: ['routes/Login.tsx'],
  },
  {
    id: 'user-menu',
    session: 'tenant',
    route: '/',
    setup: async (page) => {
      await page.locator('[data-shot="user-menu"]').click();
    },
    anchor: '[data-shot="user-menu-content"]',
    sources: ['routes/NavUser.tsx'],
  },
  {
    id: 'user-profile-drawer',
    session: 'tenant',
    route: '/',
    setup: async (page) => {
      await page.locator('[data-shot="user-menu"]').click();
      await page.locator('[data-shot="user-menu-profile"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="edit-profile"]`,
    sources: ['routes/NavUser.tsx'],
  },

  // ---- Chapter 2: the console at a glance --------------------------------------------
  {
    id: 'console-home',
    session: 'tenant',
    route: '/',
    anchor: `${PAGE} a[href="/devices"]`,
  },
  {
    id: 'console-sidebar',
    session: 'tenant',
    route: '/',
    anchor: '[data-shot="sidebar"]',
    clip: '[data-shot="sidebar"]',
    sources: ['routes/AppSidebar.tsx'],
  },
  {
    id: 'console-tenant-chip',
    session: 'tenant',
    route: '/',
    anchor: '[data-shot="tenant-chip"]',
    clip: '[data-shot="tenant-chip"]',
    sources: ['routes/TenantChip.tsx'],
  },
  {
    // An area this instance does not run. The Connectors page used to be the example, but
    // the guide's Connectors chapter needs outbound-connectors DEPLOYED, so the shot moved to
    // the AI providers page, whose area (ai-inference) the guide instance never deploys. That
    // page is an instance-administration screen, so it is a superuser recipe.
    id: 'console-area-unavailable',
    session: 'superuser',
    route: '/admin/ai-providers',
    anchor: '[data-testid="area-unavailable"]',
    sources: ['components/AreaUnavailable.tsx'],
  },

  // ---- Chapter 3: Devices -------------------------------------------------------------
  {
    id: 'devices-list',
    session: 'tenant',
    route: '/devices',
    anchor: `${ROWS}[role="button"]`,
    minRows: { selector: `${ROWS}[role="button"]`, count: 12 },
  },
  {
    id: 'devices-new',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await page.locator('[data-shot="device-new-button"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="device-new"]`,
    sources: [DEVICES_PAGE],
  },
  {
    id: 'devices-bulk',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await page.locator('[data-shot="device-bulk-button"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="device-bulk"]`,
    sources: [DEVICES_PAGE],
  },
  {
    id: 'devices-export-csv',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      const download = page.waitForEvent('download');
      await page.locator('[data-shot="export-csv"]').click();
      await download;
    },
    // The button returns once the export has finished and the notice has rendered.
    anchor: '[data-shot="export-csv"]',
    sources: ['components/ExportCsvButton.tsx'],
  },
  {
    id: 'device-detail-basic',
    session: 'tenant',
    route: '/devices/bp-therm-001',
    anchor: '[data-shot-tab="basic"][data-state="active"]',
    sources: [DEVICE_DETAIL],
  },
  deviceTab('device-detail-assignment', 'bp-therm-001', 'assignment'),
  deviceTab('device-detail-facets', 'bp-therm-001', 'facets'),
  deviceTab('device-detail-connectivity', 'bp-therm-001', 'connectivity', { mask: true }),
  deviceTab('device-detail-events', 'bp-therm-001', 'events', { mask: true }),
  // The typed command vocabulary and the history of the command issued during seeding.
  deviceTab('device-detail-commands', 'bp-therm-001', 'commands', { mask: true }),
  deviceTab('device-commands-parameter-form', 'bp-therm-001', 'commands', {
    mask: true,
    then: async (page) => {
      await pick(page, '[data-shot="command-picker"]', 'set-setpoint');
      await page.locator('[data-shot="command-parameters"]').waitFor({ timeout: 30_000 });
    },
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceCommandsPanel.tsx', 'routes/devices/CommandParameterForm.tsx'],
  }),
  deviceTab('device-detail-configuration', 'bp-therm-001', 'configuration'),
  // Restoring an earlier revision shows its diff against the current one before anything is applied.
  deviceTab('device-configuration-restore', 'bp-therm-001', 'configuration', {
    then: async (page) => {
      await page.locator('[data-shot="config-restore"]').first().click();
      await page.locator('[data-shot="config-preview"]').waitFor({ timeout: 30_000 });
    },
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceConfigurationPanel.tsx'],
  }),
  deviceTab('device-configuration-copy', 'bp-therm-001', 'configuration', {
    then: async (page) => {
      await pick(page, '[data-shot="config-copy-source"]', 'bp-therm-002');
      await page.locator('[data-shot="config-copy-preview"]').click();
      await page.locator('[data-shot="config-preview"]').waitFor({ timeout: 30_000 });
    },
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceConfigurationPanel.tsx'],
  }),
  // ACCESS_TOKEN ids are masked (a bearer secret); the MQTT credential's password is shown once,
  // here by setting a new one (the same banner a create produces).
  deviceTab('device-detail-credentials', 'bp-therm-003', 'credentials', {
    mask: true,
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceCredentialsPanel.tsx'],
  }),
  deviceTab('device-credentials-shown-once', 'bp-therm-003', 'credentials', {
    mask: true,
    then: async (page) => {
      await page.locator('[data-shot="credential-rotate"]').click();
      await page.locator('[data-shot="credential-new-password"]').click();
      await page.locator('[data-shot="credential-new-password"]').fill('Rooftop-AHU2-r0tated-Kx83');
      await page.locator('[data-shot="credential-rotate-save"]').click();
      await page.locator('[data-testid="issued-password"]').waitFor({ timeout: 30_000 });
    },
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceCredentialsPanel.tsx'],
  }),
  deviceTab('device-detail-replacement', 'bp-therm-005', 'replacement', {
    mask: true,
    then: async (page) => {
      // The inputs are read-only until focused (an autofill guard), so focus them first.
      await page.locator('[data-shot="replacement-reason"]').click();
      await page.locator('[data-shot="replacement-reason"]').fill('Display unreadable after a power surge');
      await page.locator('[data-shot="replacement-unit"]').click();
      await page.locator('[data-shot="replacement-unit"]').fill('SN-5180-C');
    },
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceReplacementPanel.tsx'],
  }),

  // ---- Chapter 4: Device types and profiles --------------------------------------------
  resourceTab('device-type-identity', '/device-types/bp-thermostat', 'identity', 'routes/device-types/resource.tsx'),
  resourceTab('device-type-appearance', '/device-types/bp-thermostat', 'appearance', 'routes/device-types/resource.tsx'),
  resourceTab('device-type-profile', '/device-types/bp-thermostat', 'profile', 'routes/device-types/resource.tsx'),
  resourceTab('profile-metrics', PROFILE_ROUTE, 'metrics', PROFILE),
  resourceTab('profile-commands', PROFILE_ROUTE, 'commands', PROFILE),
  resourceTab('profile-command-new', PROFILE_ROUTE, 'commands', PROFILE, {
    then: async (page) => {
      await page.locator('[data-shot="definition-add"]').click();
    },
    anchor: DRAWER,
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx'],
  }),
  resourceTab('profile-configuration', PROFILE_ROUTE, 'configuration', PROFILE),
  resourceTab('profile-versions', PROFILE_ROUTE, 'versions', PROFILE, {
    sources: [PROFILE, 'routes/device-profiles/ThingModelDownload.tsx'],
    then: async (page) => {
      await page.locator('[data-shot="thing-model-download"]').first().waitFor({ timeout: 30_000 });
    },
  }),
  resourceTab('profile-version-publish', PROFILE_ROUTE, 'versions', PROFILE, {
    then: async (page) => {
      await page.locator('[data-shot="version-publish-button"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="version-publish"]`,
    sources: [PROFILE, 'components/VersionsPanel.tsx'],
  }),
  resourceTab('profile-version-rollback', PROFILE_ROUTE, 'versions', PROFILE, {
    then: async (page) => {
      // The active version's button is disabled; the first enabled one is an earlier version.
      await page.locator('[data-shot="version-rollback"]:not([disabled])').first().click();
    },
    anchor: '[data-shot="confirm"]',
    sources: [PROFILE, 'components/VersionsPanel.tsx'],
  }),

  // ---- Chapter 5: Detection rules --------------------------------------------------------
  resourceTab('rules-list', PROFILE_ROUTE, 'detection-rules', PROFILE),
  resourceTab('rule-form-new', PROFILE_ROUTE, 'detection-rules', PROFILE, {
    then: async (page) => {
      await page.locator('[data-shot="definition-add"]').click();
    },
    anchor: `${DRAWER} [data-shot-option="form"]`,
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx'],
  }),
  resourceTab('rule-canvas', PROFILE_ROUTE, 'detection-rules', PROFILE, {
    then: async (page) => {
      await page.locator('[data-shot="definition-edit"][data-shot-for="bp-early-heat"]').click();
      await page.locator(`${DRAWER} [data-shot="preview-run"]`).waitFor({ timeout: 30_000 });
      // Let the canvas lay its nodes out and the compile badge settle.
      await page.waitForTimeout(1500);
    },
    anchor: `${DRAWER} [data-shot="preview-run"]`,
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx', 'routes/device-profiles/canvas/PreviewPanel.tsx'],
  }),
  resourceTab('rule-replay-preview', PROFILE_ROUTE, 'detection-rules', PROFILE, {
    then: async (page) => {
      await page.locator('[data-shot="definition-edit"][data-shot-for="bp-early-heat"]').click();
      await page.locator(`${DRAWER} [data-shot="preview-run"]`).click();
      await page.waitForTimeout(4000);
      // The panel sits below the canvas in a tall drawer: bring the run (and its trace) into view.
      await page.locator(DRAWER).evaluate((el) => el.scrollTo(0, el.scrollHeight));
      await page.waitForTimeout(500);
    },
    anchor: `${DRAWER} [data-shot="preview-run"]`,
    allowEmpty: true,
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx', 'routes/device-profiles/canvas/PreviewPanel.tsx'],
  }),
  resourceTab('rule-describe', PROFILE_ROUTE, 'detection-rules', PROFILE, {
    then: async (page) => {
      await page.locator('[data-shot="definition-add"]').click();
      await page.locator(`${DRAWER} [data-shot-option="nl"]`).click();
      await page.locator('[data-shot="nl-prompt"]').fill('Raise a warning when the lobby thermostat stays above 28 degrees for ten minutes');
      await page.locator('[data-shot="nl-draft"]').click();
    },
    // This instance does not run ai-inference, so the draft is answered "unavailable".
    anchor: '[data-shot="nl-unavailable"]',
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx', 'routes/device-profiles/DetectionRuleNLDraft.tsx'],
  }),
  resourceTab('rule-health', PROFILE_ROUTE, 'rule-health', PROFILE, { allowEmpty: true }),

  // ---- Chapter 6: Alarms ---------------------------------------------------------------------
  {
    id: 'alarms-list',
    session: 'tenant',
    route: '/alarms',
    anchor: `${ROWS}`,
    minRows: { selector: ROWS, count: 5 },
  },
  {
    id: 'alarms-filter',
    session: 'tenant',
    route: '/alarms',
    setup: async (page) => {
      await page.locator(`${PAGE} [data-shot="combobox"]`).first().click();
    },
    anchor: '[data-shot="combobox-option"]',
  },
  {
    id: 'alarms-acknowledged',
    session: 'tenant',
    route: '/alarms',
    sources: ['routes/alarms/AlarmsPage.tsx'],
    setup: async (page) => {
      // Acknowledge a live alarm the way an operator does, then filter to the acknowledged ones.
      // (The seed cannot hold one: the engine re-opens an alarm, and clears its acknowledgement,
      // whenever its device crosses the threshold again.)
      await page.locator('[data-shot="alarm-acknowledge"]').first().click();
      await page.waitForTimeout(1500);
      // The third filter is acknowledgement; "true" is its Acknowledged option.
      await page.locator(`${PAGE} [data-shot="combobox"]`).nth(2).click();
      await page.locator('[data-shot="combobox-option"][data-shot-value="true"]').click();
    },
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
  },

  // ---- Chapter 7: Commanding a fleet -----------------------------------------------------------
  {
    id: 'batches-list',
    session: 'tenant',
    route: '/command-batches',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 3 },
  },
  newDrawer('batches-new', '/command-batches', 'routes/command-batches/CommandBatchesPage.tsx'),
  {
    id: 'batch-detail-completed',
    session: 'tenant',
    route: '/command-batches/batch-identify-rooftop',
    anchor: '[data-shot="batch-cancel"]',
    allowEmpty: true,
    sources: ['routes/command-batches/CancelBatchAction.tsx'],
  },
  {
    id: 'batch-detail-refusals',
    session: 'tenant',
    route: '/command-batches/batch-night-setback',
    anchor: '[data-shot="page"] h2',
    allowEmpty: true,
  },
  {
    id: 'batch-detail-cancelled',
    session: 'tenant',
    route: '/command-batches/batch-setpoint-cancelled',
    anchor: '[data-shot="batch-cancel"]',
    allowEmpty: true,
    sources: ['routes/command-batches/CancelBatchAction.tsx'],
  },
  {
    id: 'batch-cancel-confirm',
    session: 'tenant',
    route: '/command-batches/batch-identify-rooftop',
    setup: async (page) => {
      // The page re-renders as its counts refresh; click through the DOM node, not the pointer.
      await page.locator('[data-shot="batch-cancel"]').first().evaluate((el) => (el as HTMLElement).click());
    },
    anchor: '[data-shot="confirm"]',
    allowEmpty: true,
    sources: ['routes/command-batches/CancelBatchAction.tsx'],
  },

  // ---- Chapter 8: Dashboards (the widgetlab tenant) ---------------------------------------------
  {
    id: 'dashboards-list',
    session: 'widgets',
    route: '/dashboards',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 2 },
  },
  newDrawer('dashboards-new', '/dashboards', 'routes/dashboards/DashboardsPage.tsx', 'widgets'),
  {
    id: 'dashboards-import',
    session: 'widgets',
    route: '/dashboards',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-import-button"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="dashboard-import"]`,
    sources: ['routes/dashboards/DashboardsPage.tsx', 'routes/dashboards/import/DashboardImportDrawer.tsx'],
  },
  {
    id: 'dashboard-view',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    anchor: '[data-shot="dashboard-edit"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx'],
  },
  {
    id: 'dashboard-edit',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-edit"]').click();
      await page.locator('[data-shot="dashboard-widget"]').first().waitFor({ timeout: 30_000 });
    },
    anchor: '[data-shot="dashboard-add-widget"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx', 'routes/dashboards/editor/AddWidgetMenu.tsx', 'routes/dashboards/editor/DashboardCanvas.tsx'],
  },
  {
    id: 'dashboard-add-widget',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-edit"]').click();
      await page.locator('[data-shot="dashboard-add-widget"]').click();
    },
    anchor: '[data-testid="add-widget-group"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx', 'routes/dashboards/editor/AddWidgetMenu.tsx'],
  },
  {
    id: 'dashboard-widget-config',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-edit"]').click();
      // The widget body is inert while editing (the frame beneath it owns the pointer), so the
      // click lands on the frame at the body's coordinates.
      await page.locator('[data-shot="dashboard-widget"]').nth(1).click({ force: true });
    },
    anchor: '[data-shot="widget-config"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx', 'routes/dashboards/editor/DashboardCanvas.tsx', 'routes/dashboards/editor/WidgetConfigPanel.tsx'],
  },
  {
    id: 'dashboard-preview',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-preview"]').click();
    },
    anchor: '[data-shot="dashboard-preview"][aria-pressed="true"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx'],
  },
  {
    id: 'dashboard-export',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-export"]').click();
    },
    anchor: '[data-shot="dashboard-export"][data-state="open"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx'],
  },
  {
    id: 'dashboard-history',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-history"]').click();
    },
    anchor: '[data-shot="sheet"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx'],
  },

  // ---- Chapter 9: Assets, customers and areas ----------------------------------------------------
  {
    id: 'assets-list',
    session: 'tenant',
    route: '/assets',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 3 },
  },
  newDrawer('assets-new', '/assets', 'components/registry/ResourceListPage.tsx'),
  resourceTab('asset-detail-basic', '/assets/bp-hvac-01', 'basic', 'components/registry/ResourceDetailPage.tsx', { spa: true }),
  resourceTab('asset-detail-hierarchy', '/assets/bp-hvac-01', 'hierarchy', 'routes/assets/resource.tsx', { spa: true, allowEmpty: true }),
  resourceTab('asset-detail-devices', '/assets/bp-hvac-01', 'devices', 'routes/assets/resource.tsx', { spa: true, allowEmpty: true }),
  resourceTab('asset-type-properties', '/asset-types/bp-hvac', 'properties', 'routes/asset-types/resource.tsx', { allowEmpty: true }),
  resourceTab('asset-type-versions', '/asset-types/bp-hvac', 'versions', 'routes/asset-types/resource.tsx', { allowEmpty: true }),
  {
    id: 'customers-list',
    session: 'tenant',
    route: '/customers',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
  },
  resourceTab('customer-detail-facets', '/customers/bp-acme', 'facets', 'routes/customers/resource.tsx', { allowEmpty: true }),
  {
    id: 'areas-list',
    session: 'tenant',
    route: '/areas',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 3 },
  },
  resourceTab('area-detail-facets', '/areas/bp-bldg-01', 'facets', 'routes/areas/resource.tsx', { allowEmpty: true }),
  {
    id: 'device-groups-list',
    session: 'tenant',
    route: '/device-groups',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
  },
  {
    id: 'area-types-list',
    session: 'tenant',
    route: '/area-types',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
  },

  // ---- Chapter 10: Geofences -----------------------------------------------------------------------
  {
    id: 'geofences-list',
    session: 'tenant',
    route: '/geofences',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
  },
  {
    id: 'geofence-detail',
    session: 'tenant',
    route: '/geofences/acme-campus',
    tiles: true,
    anchor: '[data-testid="fence-map"]',
    sources: ['routes/geofences/FenceMap.tsx'],
  },
  resourceTab('geofence-history', '/geofences/acme-campus', 'history', 'routes/geofences/resource.tsx', { allowEmpty: true, tiles: true }),
  newDrawer('geofences-new', '/geofences', 'components/registry/ResourceListPage.tsx'),

  // ---- Chapter 11: Groups, facets and Browse ---------------------------------------------------------
  {
    id: 'facets-list',
    session: 'tenant',
    route: '/facets',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 2 },
  },
  newDrawer('facets-new', '/facets', 'routes/facets/FacetKeysPage.tsx'),
  {
    id: 'browse-filter',
    session: 'tenant',
    route: '/browse',
    setup: async (page) => {
      // Zone is "any of": operator "is", then the rooftop value.
      await page.locator('[data-shot="facet-axis"][data-shot-for="zone"] [data-shot="facet-operator"] [data-shot="combobox"]').click();
      await page.locator('[data-shot="combobox-option"][data-shot-value="eq"]').click();
      await page.locator('[data-shot="facet-axis"][data-shot-for="zone"] [data-shot="facet-value"] [data-shot="combobox"]').click();
      await page.locator('[data-shot="combobox-option"][data-shot-value="rooftop"]').click();
      await page.keyboard.press('Escape');
      await page.locator('[data-shot="save-group-button"]').waitFor({ timeout: 30_000 });
    },
    anchor: '[data-shot="save-group-button"]',
    sources: ['routes/browse/BrowsePage.tsx'],
  },
  {
    id: 'browse-save-group',
    session: 'tenant',
    route: '/browse',
    setup: async (page) => {
      await page.locator('[data-shot="facet-axis"][data-shot-for="zone"] [data-shot="facet-operator"] [data-shot="combobox"]').click();
      await page.locator('[data-shot="combobox-option"][data-shot-value="eq"]').click();
      await page.locator('[data-shot="facet-axis"][data-shot-for="zone"] [data-shot="facet-value"] [data-shot="combobox"]').click();
      await page.locator('[data-shot="combobox-option"][data-shot-value="offices"]').click();
      await page.keyboard.press('Escape');
      await page.locator('[data-shot="save-group-button"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="save-dynamic-group"]`,
    sources: ['routes/browse/BrowsePage.tsx'],
  },

  // ---- Chapter 12: Connectors ------------------------------------------------------------------------
  {
    id: 'connectors-list',
    session: 'tenant',
    route: '/connectors',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
  },
  newDrawer('connectors-new', '/connectors', 'routes/connectors/ConnectorsPage.tsx'),
  {
    id: 'connector-config',
    session: 'tenant',
    route: '/connectors/facilities-broker',
    anchor: '[data-shot-tab="config"][data-state="active"]',
    sources: ['routes/connectors/ConnectorDetailPage.tsx'],
  },
  {
    id: 'connector-versions',
    session: 'tenant',
    route: '/connectors/facilities-broker',
    setup: async (page) => {
      await page.locator('[data-shot-tab="versions"]').click();
    },
    anchor: '[data-shot-tab="versions"][data-state="active"]',
    sources: ['routes/connectors/ConnectorDetailPage.tsx', 'routes/connectors/ConnectorVersionsPanel.tsx'],
  },

  // ---- Chapter 13: Audit ----------------------------------------------------------------------------------
  {
    id: 'audit-journal',
    session: 'tenant',
    route: '/audit',
    setup: async (page) => {
      // The simulator's alarm churn would fill the journal, so show what the operator's own
      // identity changed. The address typed here is masked (data-shot-pii).
      await page.locator('[data-shot="audit-actor"]').fill('guide@sim.devicechain.local');
      await page.waitForTimeout(1500);
    },
    anchor: ROWS,
    sources: ['routes/audit/AuditPage.tsx'],
    minRows: { selector: ROWS, count: 5 },
  },
  {
    id: 'audit-filter',
    session: 'tenant',
    route: '/audit',
    setup: async (page) => {
      await page.locator(`${PAGE} [data-shot="combobox"]`).first().click();
    },
    anchor: '[data-shot="combobox-option"]',
  },

  // ---- Chapter 14: Tenant settings ----------------------------------------------------------------------------
  { id: 'settings-branding', session: 'tenant', route: '/branding', anchor: PAGE },
  { id: 'settings-basemap', session: 'tenant', route: '/basemap', anchor: PAGE },
  { id: 'settings-locale', session: 'tenant', route: '/locale', anchor: PAGE },

  // ---- Superuser plumbing proof (the admin chapter proper is S3) -------------------------------------------------
  {
    id: 'admin-tenants',
    session: 'superuser',
    route: '/admin/tenants',
    anchor: `${PAGE} table tbody tr`,
  },
];

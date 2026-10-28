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
  /** Which signed-in identity shoots it: `tenant` (hero tenant admin), `widgets` (the widgetlab tenant's admin, dashboards chapter), `superuser` (skipped, loudly, without $DC_ADMIN_PASSWORD), `none` (the sign-in screens: the real form, no storage state), or `dash` (the /dash viewer, signed in as the widgetlab admin through its own form). */
  session: 'tenant' | 'superuser' | 'widgets' | 'none' | 'dash';
  /** Which app's source carries the anchors: the console (default) or the /dash viewer (frontend/apps/dashboard/src). */
  app?: 'console' | 'dash';
  /** Keep toasts in the frame (they are hidden by default): for a shot whose subject is the toast. */
  keepToasts?: boolean;
  /** A taller (or wider) frame than the default 1440x900, for a screen that does not fit it. Same for every locale. */
  viewport?: { width: number; height: number };
  route: string;
  /** Drives the page into the state to shoot (open a drawer, pick a tab). Throwing fails the recipe. */
  setup?: (page: Page) => Promise<void>;
  /** The page-specific half of `setup` for tab recipes (kept so the anchor check can read it). */
  then?: (page: Page) => Promise<void>;
  /** Must be visible once setup has run, or the recipe fails (fail closed). */
  anchor: string;
  /** Crop to this element instead of the whole viewport. */
  clip?: string;
  /** The screen shows the operator's address: it must be marked data-shot-pii in `sources`. */
  pii?: boolean;
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
  opts: Partial<Recipe> = {},
): Recipe {
  const { then = async () => {}, ...rest } = opts;
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
    then,
    ...rest,
  };
}

/** A tab of a registry detail page (device type, profile, asset, area...). */
function resourceTab(
  id: string,
  route: string,
  tab: string,
  source: string,
  opts: Partial<Recipe> & { spa?: boolean } = {},
): Recipe {
  const { then = async () => {}, spa: viaRouter, ...rest } = opts;
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
    then,
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

/** A list page whose New button opens the FormDrawer with this `shot` id. */
function newDrawerShot(id: string, route: string, drawer: string, source: string, session: Recipe['session'] = 'tenant'): Recipe {
  return { ...newDrawer(id, route, source, session), anchor: `${DRAWER}[data-shot-drawer="${drawer}"]` };
}

/** A tab of a superuser (/admin) detail page, opened by its tab key. */
function adminTab(id: string, route: string, tab: string, source: string, opts: Partial<Recipe> = {}): Recipe {
  return {
    id,
    session: 'superuser',
    route,
    setup: async (page) => {
      await page.locator(`[data-shot-tab="${tab}"]`).click();
    },
    anchor: `[data-shot-tab="${tab}"][data-state="active"]`,
    sources: [source],
    ...opts,
  };
}

// Chapter 15's subjects (seed.ts, seedAdmin).
const HERO_TENANT = 'sim-acme';
const THROWAWAY_TENANT = 'harbor-view-pilot';
const IDP = 'acme-google';
const TENANT_FORM = 'routes/admin/tenants/TenantForm.tsx';
const TENANT_DETAIL = 'routes/admin/tenants/TenantDetailPage.tsx';
const TIER_FORM = 'routes/admin/tiers/TierForm.tsx';
const IDP_DETAIL = 'routes/admin/identity-providers/IdentityProviderDetailPage.tsx';
// Chapter 17's board: the widgetlab gallery, as published by its simulator.
const DASH_BOARD = 'wl-gallery';

// The gallery's first screen in EDIT mode: the two Temperature cards, the chart and the table
// each draw a freshness footer once their first reading lands. Right after the switch to edit
// mode they can still be waiting, and the shot shows dashes and "No data yet": wait for the
// footers instead of sleeping. Counted over the widgets that START inside the viewport (a
// footer itself can sit below the fold), since only those are in the shot.
const EDIT_LIVE_WIDGETS = 4;
async function waitForEditReadings(page: Page) {
  await page.waitForFunction(
    (want) =>
      [...document.querySelectorAll('[data-shot="dashboard-widget"]')].filter((w) => {
        const r = w.getBoundingClientRect();
        return r.height > 0 && r.top >= 0 && r.top < window.innerHeight && w.querySelector('[data-widget-freshness]');
      }).length >= want,
    EDIT_LIVE_WIDGETS,
    { timeout: 20_000 },
  );
}

// A band of the widgetlab gallery in view mode, scrolled so the widget titled `title` sits at
// the top of the frame. The one text selector in this file, and deliberately so: a widget
// title is text the dashboard's AUTHOR wrote (the simulator's gallery spec), which the console
// never translates, so it reads the same in every locale. The widgets carry no data-shot
// anchor of their own in view mode. The title must be unique on the board: a second widget
// with the same title fails the recipe (strict locator) rather than shooting whichever came first.
//
// `ready`, when given, is a selector the band must show before the shot: the content a reader
// is meant to see, waited for rather than slept for, so a card still waiting on its first
// reading fails the recipe instead of being published as "No data".
function galleryBand(id: string, title: string, ready?: string): Recipe {
  return {
    id,
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      const heading = page.getByText(title, { exact: true });
      await heading.waitFor({ timeout: 30_000 });
      if (ready) await page.locator(ready).waitFor({ state: 'visible', timeout: 20_000 });
      await heading.evaluate((el) => {
        el.scrollIntoView({ block: 'start' });
        // Leave a little room above the widget's frame: walk up to the scrolling ancestor.
        let p: HTMLElement | null = el.parentElement;
        while (p && p.scrollHeight <= p.clientHeight) p = p.parentElement;
        (p ?? document.scrollingElement)?.scrollBy(0, -12);
      });
      await page.waitForTimeout(4000); // chart layout: ECharts gives no settled signal
    },
    anchor: '[data-shot="dashboard-edit"]',
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx'],
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
    pii: true,
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
    pii: true,
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
    id: 'devices-import',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await page.locator('[data-shot="device-import-button"]').click();
    },
    anchor: `${DRAWER}[data-shot-drawer="device-import"]`,
    sources: [DEVICES_PAGE, 'routes/devices/DeviceImportForm.tsx'],
  },
  {
    id: 'devices-export-csv',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      const download = page.waitForEvent('download');
      await page.locator('[data-shot="export-csv"]').click();
      await download;
      // The end state is the "exported N rows" toast. Holding the pointer over it pauses its timer,
      // so it is still there when the frame is taken.
      await page.locator('[data-sonner-toast]').first().waitFor({ timeout: 30_000 });
      await page.locator('[data-sonner-toast]').first().hover();
    },
    // The button returns once the export has finished and the notice has rendered.
    anchor: '[data-shot="export-csv"]',
    keepToasts: true,
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
      await page.locator('[data-shot="config-preview"]').evaluate((el) => el.scrollIntoView({ block: 'center' }));
    },
    pii: true,
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceConfigurationPanel.tsx'],
  }),
  deviceTab('device-configuration-copy', 'bp-therm-001', 'configuration', {
    then: async (page) => {
      await pick(page, '[data-shot="config-copy-source"]', 'bp-therm-002');
      await page.locator('[data-shot="config-copy-preview"]').click();
      await page.locator('[data-shot="config-preview"]').waitFor({ timeout: 30_000 });
      await page.locator('[data-shot="config-preview"]').evaluate((el) => el.scrollIntoView({ block: 'center' }));
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
  deviceTab('device-detail-replacement', 'bp-therm-boiler', 'replacement', {
    mask: true,
    pii: true,
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
    pii: true,
    sources: [PROFILE, 'routes/device-profiles/ThingModelDownload.tsx', 'components/VersionsPanel.tsx'],
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
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx', 'routes/device-profiles/DetectionRuleAuthoring.tsx'],
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
    sources: [PROFILE, 'routes/device-profiles/DefinitionsPanel.tsx', 'routes/device-profiles/DetectionRuleAuthoring.tsx', 'routes/device-profiles/DetectionRuleNLDraft.tsx'],
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
  // The gallery further down, one shot per band: the gauge and indicator widgets, the stat
  // cards over the board's time range, the bar and pie charts, and the fleet table.
  galleryBand('dashboard-gauges', 'Thermometer'),
  // The one card on the latest reading rather than an aggregate: it needs the live reading,
  // which can land after the aggregates do.
  galleryBand('dashboard-stat-cards', 'Average temperature', '[data-stat="last"]'),
  galleryBand('dashboard-bar-pie', 'Latest readings'),
  galleryBand('dashboard-fleet-table', 'Sensors in the zone'),
  {
    id: 'dashboard-edit',
    session: 'widgets',
    route: '/dashboards/wl-gallery',
    setup: async (page) => {
      await page.locator('[data-shot="dashboard-edit"]').click();
      await page.locator('[data-shot="dashboard-widget"]').first().waitFor({ timeout: 30_000 });
      await waitForEditReadings(page);
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
      await waitForEditReadings(page);
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
      await waitForEditReadings(page);
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
    pii: true,
    sources: ['routes/dashboards/editor/DashboardWorkspace.tsx', 'routes/dashboards/editor/VersionHistorySheet.tsx'],
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
  // Taller than the default frame: the Spanish labels wrap the boundary editor's buttons, which
  // pushed the bottom of the drawer out of a 900-pixel frame.
  { ...newDrawer('geofences-new', '/geofences', 'components/registry/ResourceListPage.tsx'), viewport: { width: 1440, height: 1120 } },

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

  // ---- Connect: trust anchors and inbound integrations ------------------------------------------------
  {
    id: 'trust-anchors-list',
    session: 'tenant',
    route: '/trust-anchors',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
    sources: ['routes/trust-anchors/TrustAnchorsPage.tsx'],
  },
  {
    id: 'integration-detail',
    session: 'tenant',
    route: '/integrations/chirpstack-eu868',
    anchor: PAGE,
    sources: ['routes/integrations/IntegrationDetailPage.tsx'],
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
    pii: true,
    sources: ['routes/connectors/ConnectorDetailPage.tsx', 'routes/connectors/ConnectorVersionsPanel.tsx'],
  },

  // ---- Chapter 13: Audit ----------------------------------------------------------------------------------
  {
    id: 'audit-journal',
    session: 'tenant',
    route: '/audit',
    setup: async (page) => {
      // The simulator's alarm churn would fill the journal, and the guide's own runs add UPDATE rows
      // (a rotated password, an acknowledged alarm). So show what the operator's identity CREATED:
      // the seed's own work, which no capture run changes. The address typed here is substituted
      // with the persona (data-shot-pii).
      await page.locator('[data-shot="audit-actor"]').fill(`${process.env.GUIDE_SIM ?? 'acme'}@sim.devicechain.local`);
      await pick(page, PAGE, 'create');
      await page.waitForTimeout(1500);
    },
    anchor: ROWS,
    pii: true,
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

  // ---- Chapter 16: Over-the-air updates ------------------------------------------------------------------------
  // The seed declares three thermostat firmware images (2.3.1 signed and retired, 2.4.0 signed,
  // 2.5.0-rc.1 unverified), registers an active Ed25519 release signer and a revoked P-256 one,
  // and assigns 2.4.0 to the lobby thermostat. The thermostats never report update progress,
  // so that assignment stays where the platform put it.
  {
    id: 'updates-artifacts-list',
    session: 'tenant',
    route: '/updates/artifacts',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 3 },
  },
  newDrawerShot('updates-artifact-new', '/updates/artifacts', 'artifact-new', 'routes/updates/ArtifactsPage.tsx'),
  {
    // A signed artifact: details, its trust, the registered signers and the add-signature form.
    id: 'updates-artifact-signed',
    session: 'tenant',
    route: '/updates/artifacts/thermostat-fw-2-4-0',
    anchor: `${PAGE} table tbody tr`,
    minRows: { selector: `${PAGE} table tbody tr`, count: 1 },
  },
  {
    id: 'updates-artifact-signatures',
    session: 'tenant',
    route: '/updates/artifacts/thermostat-fw-2-4-0',
    setup: async (page) => {
      // The signatures panel sits below the details: bring it into the middle of the frame.
      await page.locator(`${PAGE} table`).last().waitFor({ timeout: 30_000 });
      await page.locator(`${PAGE} table`).last().evaluate((el) => el.scrollIntoView({ block: 'center' }));
    },
    anchor: `${PAGE} table tbody tr`,
    minRows: { selector: `${PAGE} table tbody tr`, count: 1 },
  },
  {
    id: 'updates-artifact-unverified',
    session: 'tenant',
    route: '/updates/artifacts/thermostat-fw-2-5-0-rc1',
    anchor: '[data-testid="unverified-notice"]',
    sources: ['routes/updates/ArtifactDetailPage.tsx'],
  },
  {
    id: 'updates-signers-list',
    session: 'tenant',
    route: '/updates/signers',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 2 },
  },
  newDrawerShot('updates-signer-register', '/updates/signers', 'signer-register', 'routes/updates/SignersPage.tsx'),
  {
    id: 'updates-signer-revoke',
    session: 'tenant',
    route: '/updates/signers',
    setup: async (page) => {
      // Opens the confirmation and stops there: nothing is revoked.
      await page.locator('[data-shot="signer-revoke"]').first().click();
    },
    anchor: '[data-shot="confirm"]',
    sources: ['routes/updates/SignersPage.tsx'],
  },
  deviceTab('updates-device-tab', 'bp-therm-001', 'updates', {
    then: async (page) => {
      await page.locator(`${PAGE} table tbody tr`).first().waitFor({ timeout: 30_000 });
    },
    sources: [DEVICE_DETAIL, 'routes/devices/DeviceUpdatesPanel.tsx'],
  }),

  // ---- Chapter 15: Administering the instance (the superuser's /admin console) -----------------------------------
  // The seed (seedAdmin) adds a Pilot tier and a colour on every tier, three people with memberships
  // in the hero tenant (one disabled), a tenant role, a Google Workspace connection serving the hero
  // tenant, and a throwaway tenant (harbor-view-pilot) that it DELETES, so the deletion screens have a
  // subject. The hero tenant's own deletion stops at the confirmation. AI providers and AI packaging
  // are not here: the guide instance does not run ai-inference, which console-area-unavailable shows.
  {
    id: 'admin-tenants',
    session: 'superuser',
    route: '/admin/tenants',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 2 },
  },
  // A new tenant's form has no tabs until it exists.
  { id: 'admin-tenant-new', session: 'superuser', route: '/admin/tenants/new', anchor: `${PAGE} input` },
  adminTab('admin-tenant-basic', `/admin/tenants/${HERO_TENANT}`, 'basic', TENANT_FORM),
  adminTab('admin-tenant-settings', `/admin/tenants/${HERO_TENANT}`, 'settings', TENANT_FORM),
  adminTab('admin-tenant-effective', `/admin/tenants/${HERO_TENANT}`, 'effective', TENANT_FORM),
  {
    // Opens the confirmation and stops there: the hero tenant is never deleted.
    id: 'admin-tenant-delete-confirm',
    session: 'superuser',
    route: `/admin/tenants/${HERO_TENANT}`,
    setup: async (page) => {
      await page.locator('[data-shot="tenant-delete"]').click();
    },
    anchor: '[data-shot="confirm"]',
    sources: [TENANT_DETAIL],
  },
  // The throwaway tenant, mid-deletion: its page loses the live-tenant actions and gains a Deletion
  // tab. Empty is allowed: the purge coordinator writes its record up to a minute after the delete.
  adminTab('admin-tenant-deletion', `/admin/tenants/${THROWAWAY_TENANT}`, 'deletion', TENANT_FORM, { allowEmpty: true }),
  {
    id: 'admin-deletions',
    session: 'superuser',
    route: '/admin/deletions',
    anchor: PAGE,
    allowEmpty: true,
  },
  {
    id: 'admin-tiers',
    session: 'superuser',
    route: '/admin/tiers',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 4 },
  },
  adminTab('admin-tier-basic', '/admin/tiers/pilot', 'basic', TIER_FORM),
  adminTab('admin-tier-settings', '/admin/tiers/pilot', 'settings', TIER_FORM),
  {
    id: 'admin-identities',
    session: 'superuser',
    route: '/admin/identities',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 5 },
  },
  {
    id: 'admin-identity-detail',
    session: 'superuser',
    route: '/admin/identities/maria.chen%40acme-properties.example',
    anchor: `${PAGE} h2`,
  },
  { id: 'admin-identity-new', session: 'superuser', route: '/admin/identities/new', anchor: `${PAGE} input` },
  {
    id: 'admin-roles',
    session: 'superuser',
    route: '/admin/roles',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 4 },
  },
  { id: 'admin-role-detail', session: 'superuser', route: '/admin/roles/tenant/facilities-viewer', anchor: `${PAGE} input` },
  {
    id: 'admin-identity-providers',
    session: 'superuser',
    route: '/admin/identity-providers',
    anchor: ROWS,
    minRows: { selector: ROWS, count: 1 },
    // The page's second panel, link requests, is empty: nobody has been refused for want of a link.
    allowEmpty: true,
  },
  {
    ...newDrawerShot('admin-identity-provider-new', '/admin/identity-providers', 'identity-provider-new', 'routes/admin/identity-providers/IdentityProvidersPage.tsx', 'superuser'),
    allowEmpty: true,
  },
  adminTab('admin-identity-provider-discovery', `/admin/identity-providers/${IDP}`, 'discovery', IDP_DETAIL),
  adminTab('admin-identity-provider-settings', `/admin/identity-providers/${IDP}`, 'settings', IDP_DETAIL),
  adminTab('admin-identity-provider-tenants', `/admin/identity-providers/${IDP}`, 'tenants', IDP_DETAIL),
  adminTab('admin-identity-provider-domains', `/admin/identity-providers/${IDP}`, 'domains', IDP_DETAIL),
  {
    // The superuser's own administration: the seed's tier, role, people, connection and deletion.
    // Unfiltered, the journal is all sign-ins (every capture signs in) and the deletion sweep's rows.
    id: 'admin-audit',
    session: 'superuser',
    route: '/admin/audit',
    setup: async (page) => {
      await page.locator('[data-shot="admin-audit-actor"]').fill('superuser@devicechain.local');
      await page.locator(`${PAGE} [data-shot="combobox"]`).nth(1).click();
      await page.locator('[data-shot="combobox-option"][data-shot-value="mutation"]').click();
      await page.waitForTimeout(1500);
    },
    anchor: ROWS,
    minRows: { selector: ROWS, count: 5 },
    sources: ['routes/admin/AuditPage.tsx'],
  },
  // Settings tabs are keyed by the setting's key, which each editor declares (`key: '...'`).
  adminTab('admin-settings', '/admin/settings', 'branding.default', 'routes/admin/settings/BrandingDefaultEditor.tsx'),
  adminTab('admin-settings-token-masks', '/admin/settings', 'entity.token_masks', 'routes/admin/settings/TokenMasksEditor.tsx'),

  // ---- Chapter 17: Viewing dashboards in /dash ----------------------------------------------------------------------
  // The viewer the instance serves at /dash/, signed in as the widgetlab tenant's admin through its
  // own form (session `dash`). It holds its session in memory only, so every shot signs in again.
  // Its anchors live in frontend/apps/dashboard/src (app: 'dash').
  {
    // The real form, nothing signed in. Only the address is typed: the password field stays empty.
    id: 'dash-signin',
    session: 'none',
    app: 'dash',
    route: '/dash/',
    setup: async (page) => {
      await page.locator('[data-shot="dash-email"]').fill('sam.rivera@widgetlab.example');
    },
    anchor: '[data-shot="dash-signin"]',
    sources: ['App.tsx'],
  },
  {
    id: 'dash-load',
    session: 'dash',
    app: 'dash',
    route: '/dash/',
    setup: async (page) => {
      await page.locator('[data-shot="dash-token"]').fill(DASH_BOARD);
    },
    anchor: '[data-shot="dash-load-published"]:not([disabled])',
    sources: ['App.tsx'],
  },
  {
    // Loaded by token through the form, the way a viewer first meets a board.
    id: 'dash-view',
    session: 'dash',
    app: 'dash',
    route: '/dash/',
    setup: async (page) => {
      await page.locator('[data-shot="dash-token"]').fill(DASH_BOARD);
      await page.locator('[data-shot="dash-load-published"]').click();
      await page.locator('[data-shot="dash-view"]').waitFor({ timeout: 30_000 });
      await page.waitForTimeout(4000); // chart layout: ECharts gives no settled signal
    },
    anchor: '[data-shot="dash-theme-toggle"]',
    sources: ['App.tsx'],
  },
  {
    // A deep link opens the board straight after sign-in; kiosk=1 drops the header for a wall display.
    id: 'dash-kiosk',
    session: 'dash',
    app: 'dash',
    route: `/dash/?dashboard=${DASH_BOARD}&kiosk=1`,
    setup: async (page) => {
      await page.locator('[data-shot="dash-view"]').waitFor({ timeout: 30_000 });
      await page.waitForTimeout(4000);
    },
    anchor: '[data-shot="dash-view"]',
    sources: ['App.tsx'],
  },
  {
    // The viewer's own theme toggle, switched to dark: the one deliberately dark shot in the guide.
    id: 'dash-dark',
    session: 'dash',
    app: 'dash',
    route: `/dash/?dashboard=${DASH_BOARD}`,
    setup: async (page) => {
      await page.locator('[data-shot="dash-theme-toggle"]').click();
      await page.waitForTimeout(4000);
    },
    anchor: 'html.dark [data-shot="dash-view"]',
    sources: ['App.tsx'],
  },
];

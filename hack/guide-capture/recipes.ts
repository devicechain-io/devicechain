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
  /** Which signed-in identity shoots it. A superuser recipe is skipped, loudly, without $DC_ADMIN_PASSWORD. */
  session: 'tenant' | 'superuser';
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

const ROWS = '[data-shot="page"] table tbody tr';

/** Open the first row of the devices list. Row order is stable for a fixed sim population. */
async function openFirstDevice(page: Page) {
  const row = page.locator(`${ROWS}[role="button"]`).first();
  await row.waitFor({ timeout: 30_000 });
  await row.click();
  await page.locator('[data-shot-tab="basic"]').waitFor({ timeout: 30_000 });
}

const DEVICE_DETAIL = 'routes/devices/DeviceDetailPage.tsx';
const DEVICES_PAGE = 'routes/devices/DevicesPage.tsx';

function deviceTab(tab: string, opts: Pick<Recipe, 'allowEmpty' | 'mask'> = {}): Recipe {
  return {
    id: `device-detail-${tab}`,
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await openFirstDevice(page);
      await page.locator(`[data-shot-tab="${tab}"]`).click();
    },
    anchor: `[data-shot-tab="${tab}"][data-state="active"]`,
    sources: [DEVICE_DETAIL],
    ...opts,
  };
}

export const recipes: Recipe[] = [
  // ---- Chapter 2: the console at a glance ------------------------------------------
  {
    id: 'console-home',
    session: 'tenant',
    route: '/',
    anchor: '[data-shot="page"] a[href="/devices"]',
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
    // Connectors needs the outbound-connectors area, which a default install does not deploy.
    id: 'console-area-unavailable',
    session: 'tenant',
    route: '/connectors',
    anchor: '[data-testid="area-unavailable"]',
    sources: ['components/AreaUnavailable.tsx'],
  },

  // ---- Chapter 3: Devices -----------------------------------------------------------
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
    anchor: '[data-shot="drawer"][data-shot-drawer="device-new"]',
    sources: [DEVICES_PAGE],
  },
  {
    id: 'devices-bulk',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await page.locator('[data-shot="device-bulk-button"]').click();
    },
    anchor: '[data-shot="drawer"][data-shot-drawer="device-bulk"]',
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
  deviceTab('basic'),
  deviceTab('connectivity', { mask: true }),
  deviceTab('events', { mask: true }),
  // The sim's profile declares no commands and none has been issued, so the panel shows its
  // free-form form and an empty history. S2 seeds a typed command and an issued one.
  deviceTab('commands', { allowEmpty: true }),
  // Not captured yet (they need S2 seed objects to say anything): assignment (history
  // needs reassignments), facets (needs facet keys), configuration diff/restore (needs a
  // declared configuration), credentials (the shown-once secret needs a created
  // credential), replacement (needs a second device).

  // ---- Superuser plumbing proof (the admin chapter proper is S3) ----------------------
  {
    id: 'admin-tenants',
    session: 'superuser',
    route: '/admin/tenants',
    anchor: '[data-shot="page"] table tbody tr',
  },
];

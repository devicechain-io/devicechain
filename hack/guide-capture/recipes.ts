// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { Page } from '@playwright/test';

// A recipe is data plus, at most, a short `setup`. Selectors are data-shot* anchors
// (or the few data-testid already on the right element), never CSS classes or visible
// text: text is translated, so a text selector would break the `es` run. The static
// anchor check (check-anchors.mjs) reads this file and verifies every anchor named
// here still exists in the console source. Convention: frontend/apps/console/CONVENTIONS.md.

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
  /** The screen legitimately shows an empty/error state (the default is to fail on one). */
  allowEmpty?: boolean;
}

const ROWS = '[data-shot="page"] table tbody tr';

/** Open the first row of the devices list. Row order is stable for a fixed sim population. */
async function openFirstDevice(page: Page) {
  const row = page.locator(`${ROWS}[role="button"]`).first();
  await row.waitFor({ timeout: 30_000 });
  await row.click();
  await page.locator('[data-shot-tab="basic"]').waitFor({ timeout: 30_000 });
}

function deviceTab(tab: string, opts: Pick<Recipe, 'allowEmpty'> = {}): Recipe {
  return {
    id: `device-detail-${tab}`,
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await openFirstDevice(page);
      await page.locator(`[data-shot-tab="${tab}"]`).click();
    },
    anchor: `[data-shot-tab="${tab}"][data-state="active"]`,
    mask: true,
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
  },
  {
    id: 'console-tenant-chip',
    session: 'tenant',
    route: '/',
    anchor: '[data-shot="tenant-chip"]',
    clip: '[data-shot="tenant-chip"]',
  },
  {
    // Connectors needs the outbound-connectors area, which a default install does not deploy.
    id: 'console-area-unavailable',
    session: 'tenant',
    route: '/connectors',
    anchor: '[data-testid="area-unavailable"]',
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
  },
  {
    id: 'devices-bulk',
    session: 'tenant',
    route: '/devices',
    setup: async (page) => {
      await page.locator('[data-shot="device-bulk-button"]').click();
    },
    anchor: '[data-shot="drawer"][data-shot-drawer="device-bulk"]',
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
  },
  deviceTab('basic'),
  deviceTab('connectivity'),
  deviceTab('events'),
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

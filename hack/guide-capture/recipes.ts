// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { Page } from '@playwright/test';

export interface Recipe {
  id: string;
  session: 'tenant';
  route: string;
  /** Resolves when the screen shows the data the recipe expects; throws otherwise (fail closed). */
  ready: (page: Page) => Promise<void>;
}

export const recipes: Recipe[] = [
  {
    id: 'devices-list',
    session: 'tenant',
    route: '/devices',
    // Row anchors only: role/tag, never visible text (translated). An empty state
    // renders no rows, so an unseeded tenant fails here rather than shooting a blank page.
    ready: async (page) => {
      const rows = page.locator('table tbody tr[role="button"]');
      await rows.first().waitFor({ timeout: 30_000 });
      const n = await rows.count();
      if (n < 12) throw new Error(`devices-list: expected the 12 seeded thermostats, saw ${n} rows`);
    },
  },
];

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { test } from '@playwright/test';
import sharp from 'sharp';
import { recipes, type Recipe } from './recipes';
import type { Session } from './session';

const OUT = process.env.GUIDE_OUT ?? 'out';
const MASK_COLOR = '#e2e8f0';

// Map tiles come from a HAR recorded once at the guide's fixed map views, so captures
// are deterministic, offline, and put no load on public tile servers. Record with
// GUIDE_HAR_RECORD=1 (needs network); replay is the default and aborts any tile
// request the HAR does not hold, so a moved view fails loudly instead of fetching.
const TILE_HAR = 'tiles.har';
const TILE_URL = /tile\.openstreetmap\.org|\/\d+\/\d+\/\d+\.(png|pbf|mvt|webp|jpg)(\?|$)/;
const RECORD_TILES = process.env.GUIDE_HAR_RECORD === '1';

function loadSession(name: Recipe['session']): Session | null {
  const file = `.auth/${name}.json`;
  return existsSync(file) ? (JSON.parse(readFileSync(file, 'utf8')) as Session) : null;
}

for (const recipe of recipes) {
  test(`${recipe.id}`, async ({ page, context }, info) => {
    const locale = info.project.name;
    const session = loadSession(recipe.session);
    if (!session) {
      const why = `${recipe.id}: SKIPPED, no ${recipe.session} session (set $DC_ADMIN_PASSWORD for superuser recipes)`;
      console.warn(why);
      test.skip(true, why);
      return;
    }

    // Everything the console reads at boot is pinned before any page script runs.
    // The locale comes from localStorage (the detector ignores ?lng=).
    await context.addInitScript(
      ({ s, loc }) => {
        if (s.auth) localStorage.setItem('dc-auth', JSON.stringify(s.auth));
        localStorage.setItem('dc-identity', JSON.stringify(s.identity));
        localStorage.setItem('dc.locale', loc);
        localStorage.setItem('dc-theme', 'light');
      },
      { s: session, loc: locale },
    );
    await context.addCookies([{ name: 'sidebar_state', value: 'true', url: info.project.use.baseURL! }]);

    if (recipe.clock) await page.clock.setFixedTime(new Date(recipe.clock));
    if (recipe.tiles) {
      if (RECORD_TILES) await page.routeFromHAR(TILE_HAR, { url: TILE_URL, update: true, updateContent: 'embed' });
      else await page.routeFromHAR(TILE_HAR, { url: TILE_URL, notFound: 'abort' });
    }

    await page.goto(recipe.route);
    await page.locator('[data-shot="page"]').waitFor({ timeout: 30_000 });
    await recipe.setup?.(page);
    await page.locator(recipe.anchor).first().waitFor({ state: 'visible', timeout: 30_000 });

    if (recipe.minRows) {
      const n = await page.locator(recipe.minRows.selector).count();
      if (n < recipe.minRows.count) {
        throw new Error(`${recipe.id}: expected at least ${recipe.minRows.count} of ${recipe.minRows.selector}, saw ${n}`);
      }
    }
    // Nothing may still be loading: an empty state is only meaningful once the reads settled,
    // and a shot taken mid-load would document a spinner.
    await page.locator('[data-shot="loading"]').first().waitFor({ state: 'detached', timeout: 30_000 }).catch(() => {
      throw new Error(`${recipe.id}: the screen is still loading after 30s`);
    });
    // Fail closed: a screen that rendered an empty or error state is not the screen
    // the guide means to show.
    if (!recipe.allowEmpty) {
      const bad = await page.locator('[data-shot="empty"], [data-shot="error"]').count();
      if (bad > 0) throw new Error(`${recipe.id}: the screen shows an empty/error state (${bad}); not shooting it`);
    }

    await page.evaluate(() => document.fonts.ready);
    await page.waitForLoadState('networkidle');

    const opts = {
      animations: 'disabled' as const,
      caret: 'hide' as const,
      ...(recipe.mask ? { mask: [page.locator('[data-shot-mask]')], maskColor: MASK_COLOR } : {}),
    };
    const png = recipe.clip
      ? await page.locator(recipe.clip).first().screenshot(opts)
      : await page.screenshot(opts);

    mkdirSync(OUT, { recursive: true });
    const webp = await sharp(png).webp({ quality: 80 }).toBuffer();
    const file = `${OUT}/${recipe.id}.${locale}.webp`;
    writeFileSync(file, webp);
    console.log(`${file}: ${webp.length} bytes (png ${png.length})`);
  });
}

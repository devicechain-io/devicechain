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

// A HAR recorded here held only aborted requests (MapLibre fetches tiles from a worker the
// recorder did not see), so a HAR with no 200 entry is treated as absent rather than replayed
// into a map that would never finish loading.
function hasUsableHar(): boolean {
  if (!existsSync(TILE_HAR)) return false;
  try {
    const har = JSON.parse(readFileSync(TILE_HAR, 'utf8')) as { log: { entries: { response: { status: number } }[] } };
    return har.log.entries.some((e) => e.response.status === 200);
  } catch {
    return false;
  }
}

function loadSession(name: Exclude<Recipe['session'], 'none'>): Session | null {
  const file = `.auth/${name}.json`;
  return existsSync(file) ? (JSON.parse(readFileSync(file, 'utf8')) as Session) : null;
}

for (const recipe of recipes) {
  test(`${recipe.id}`, async ({ page, context }, info) => {
    const locale = info.project.name;
    // `none` is the sign-in screens: the real form, no storage state at all.
    const session = recipe.session === 'none' ? null : loadSession(recipe.session);
    if (!session && recipe.session !== 'none') {
      // Only the superuser session is optional (it needs a password this harness never reads
      // from the cluster). A tenant session that is missing is a broken bring-up, not a skip.
      if (recipe.session !== 'superuser') throw new Error(`${recipe.id}: no ${recipe.session} session was minted; see global-setup`);
      if (process.env.GUIDE_REQUIRE_SUPERUSER === '1') {
        throw new Error(`${recipe.id}: no ${recipe.session} session and GUIDE_REQUIRE_SUPERUSER=1`);
      }
      const why = `${recipe.id}: SKIPPED, no ${recipe.session} session (set $DC_ADMIN_PASSWORD for superuser recipes)`;
      console.warn(why);
      test.skip(true, why);
      return;
    }

    // Everything the console reads at boot is pinned before any page script runs.
    // The locale comes from localStorage (the detector ignores ?lng=).
    await context.addInitScript(
      ({ s, loc }) => {
        if (s) {
          if (s.auth) localStorage.setItem('dc-auth', JSON.stringify(s.auth));
          localStorage.setItem('dc-identity', JSON.stringify(s.identity));
        }
        localStorage.setItem('dc.locale', loc);
        localStorage.setItem('dc-theme', 'light');
      },
      { s: session, loc: locale },
    );
    await context.addCookies([{ name: 'sidebar_state', value: 'true', url: info.project.use.baseURL! }]);

    if (recipe.clock) await page.clock.setFixedTime(new Date(recipe.clock));
    if (recipe.tiles) {
      if (RECORD_TILES) await context.routeFromHAR(TILE_HAR, { url: TILE_URL, update: true, updateContent: 'embed' });
      else if (hasUsableHar()) await context.routeFromHAR(TILE_HAR, { url: TILE_URL, notFound: 'abort' });
      else if (process.env.GUIDE_REQUIRE_HAR === '1') throw new Error(`${recipe.id}: no usable ${TILE_HAR} and GUIDE_REQUIRE_HAR=1`);
      else console.warn(`${recipe.id}: no usable ${TILE_HAR}; map tiles load LIVE from the network (not offline-deterministic)`);
    }

    await page.goto(recipe.route);
    // Signed-out screens have no app shell, so there is no page region to wait for.
    if (recipe.session !== 'none') await page.locator('[data-shot="page"]').waitFor({ timeout: 30_000 });
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
    try {
      await page.locator('[data-shot="loading"]').first().waitFor({ state: 'detached', timeout: 30_000 });
    } catch (err) {
      if (err instanceof Error && err.name === 'TimeoutError') {
        throw new Error(`${recipe.id}: the screen is still loading after 30s`);
      }
      throw err;
    }
    // Fail closed. An error state is never a screenshot; an empty state is one only where
    // the recipe says the screen is meant to be empty.
    const errors = await page.locator('[data-shot="error"]').count();
    if (errors > 0) throw new Error(`${recipe.id}: the screen shows an error state (${errors}); not shooting it`);
    if (!recipe.allowEmpty) {
      const empty = await page.locator('[data-shot="empty"]').count();
      if (empty > 0) throw new Error(`${recipe.id}: the screen shows an empty state (${empty}); not shooting it`);
    }
    // A mask that matches nothing is a recipe that thinks it is hiding live values and is not.
    if (recipe.mask && (await page.locator('[data-shot-mask]').count()) === 0) {
      throw new Error(`${recipe.id}: mask is set but the screen has no [data-shot-mask] region`);
    }

    await page.evaluate(() => document.fonts.ready);
    await page.waitForLoadState('networkidle');

    const opts = {
      animations: 'disabled' as const,
      caret: 'hide' as const,
      // [data-shot-pii] (the signed-in email) is always painted over: it is a property of the
      // seed identity, not of any screen. [data-shot-mask] only where a recipe asks.
      mask: [page.locator('[data-shot-pii]'), ...(recipe.mask ? [page.locator('[data-shot-mask]')] : [])],
      maskColor: MASK_COLOR,
    };
    const png = recipe.clip
      ? await page.locator(recipe.clip).first().screenshot(opts)
      : await page.screenshot(opts);

    mkdirSync(OUT, { recursive: true });
    // GUIDE_PNG=1 keeps the lossless capture beside the WebP, for eyeballing a recipe run.
    if (process.env.GUIDE_PNG === '1') {
      mkdirSync(`${OUT}/png`, { recursive: true });
      writeFileSync(`${OUT}/png/${recipe.id}.${locale}.png`, png);
    }
    const webp = await sharp(png).webp({ quality: 80 }).toBuffer();
    const file = `${OUT}/${recipe.id}.${locale}.webp`;
    writeFileSync(file, webp);
    console.log(`${file}: ${webp.length} bytes (png ${png.length})`);
  });
}

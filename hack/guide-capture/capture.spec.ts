// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { test } from '@playwright/test';
import sharp from 'sharp';
import { recipes } from './recipes';
import type { Session } from './session';

const OUT = process.env.GUIDE_OUT ?? 'out';

for (const recipe of recipes) {
  test(`${recipe.id}`, async ({ page, context }, info) => {
    const locale = info.project.name;
    const session = JSON.parse(readFileSync('.auth/tenant.json', 'utf8')) as Session;

    // Everything the console reads at boot is pinned before any page script runs.
    // The locale comes from localStorage (the detector ignores ?lng=).
    await context.addInitScript(
      ({ s, loc }) => {
        localStorage.setItem('dc-auth', JSON.stringify(s.auth));
        localStorage.setItem('dc-identity', JSON.stringify(s.identity));
        localStorage.setItem('dc.locale', loc);
        localStorage.setItem('dc-theme', 'light');
      },
      { s: session, loc: locale },
    );
    await context.addCookies([{ name: 'sidebar_state', value: 'true', url: info.project.use.baseURL! }]);

    await page.goto(recipe.route);
    await recipe.ready(page);
    await page.evaluate(() => document.fonts.ready);
    await page.waitForLoadState('networkidle');

    const png = await page.screenshot({ animations: 'disabled', caret: 'hide' });
    mkdirSync(OUT, { recursive: true });
    const webp = await sharp(png).webp({ quality: 80 }).toBuffer();
    const file = `${OUT}/${recipe.id}.${locale}.webp`;
    writeFileSync(file, webp);
    console.log(`${file}: ${webp.length} bytes (png ${png.length})`);
  });
}

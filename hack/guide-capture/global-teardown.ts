// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { readdirSync, writeFileSync } from 'node:fs';
import sharp from 'sharp';
import { BASE_URL } from './playwright.config';

const OUT = process.env.GUIDE_OUT ?? 'out';

// The console serves no version endpoint today, so: an explicit override, then a
// /version.json probe (only trusted if it parses as JSON with a string `version`),
// else 'unknown'. Never a guess.
async function consoleVersion(): Promise<string> {
  if (process.env.GUIDE_CONSOLE_VERSION) return process.env.GUIDE_CONSOLE_VERSION;
  try {
    const res = await fetch(`${BASE_URL}/version.json`);
    const body = (await res.json()) as { version?: unknown };
    if (res.ok && typeof body.version === 'string' && body.version) return body.version;
  } catch {
    /* not served, or not JSON */
  }
  return 'unknown';
}

// manifest.json is derived from the images actually on disk, never from the recipe list,
// so it cannot advertise a shot that failed to capture. The shape is a contract with the
// docs build (GuideShot): schemaVersion 1.
export default async function globalTeardown() {
  const shots: Record<string, { locales: string[]; width: number; height: number }> = {};
  for (const f of readdirSync(OUT).sort()) {
    const m = /^(.+)\.([a-zA-Z-]+)\.webp$/.exec(f);
    if (!m) continue;
    const [, id, locale] = m;
    const meta = await sharp(`${OUT}/${f}`).metadata();
    const entry = (shots[id] ??= { locales: [], width: meta.width ?? 0, height: meta.height ?? 0 });
    entry.locales.push(locale);
    if (entry.width !== meta.width || entry.height !== meta.height) {
      console.warn(`${id}: ${locale} is ${meta.width}x${meta.height}, differs from ${entry.width}x${entry.height}`);
    }
  }
  const manifest = { schemaVersion: 1, consoleVersion: await consoleVersion(), shots };
  writeFileSync(`${OUT}/manifest.json`, JSON.stringify(manifest, null, 2) + '\n');
  console.log(`manifest.json: ${Object.keys(shots).length} shots`);
}

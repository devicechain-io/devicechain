// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { BASE_URL } from './playwright.config';
import { readSimHandshake, signIn } from './session';

export const AUTH_DIR = '.auth';
export const OUT_DIR = process.env.GUIDE_OUT ?? 'out';

// Two sessions, both minted over GraphQL (no sign-in form):
//   tenant     the sim's tenant admin (~/.devicechain/sims/<name>.json)
//   superuser  the bootstrap admin, from $DC_ADMIN_PASSWORD. Absent => superuser recipes
//              are SKIPPED, loudly. This harness never reads cluster Secrets.
export default async function globalSetup() {
  mkdirSync(AUTH_DIR, { recursive: true });

  // A stale shot must not outlive the recipe (or failed run) that produced it, or the
  // manifest would advertise an image nothing regenerated. GUIDE_KEEP_OUT=1 keeps the
  // directory, for a single-recipe `--grep` run.
  if (process.env.GUIDE_KEEP_OUT !== '1') {
    mkdirSync(OUT_DIR, { recursive: true });
    for (const f of readdirSync(OUT_DIR)) {
      if (f.endsWith('.webp') || f === 'manifest.json') rmSync(`${OUT_DIR}/${f}`);
    }
  }

  const sim = readSimHandshake(process.env.GUIDE_SIM ?? 'guide');
  const tenant = await signIn(BASE_URL, sim.email, sim.password, sim.tenant);
  writeFileSync(`${AUTH_DIR}/tenant.json`, JSON.stringify(tenant), { mode: 0o600 });
  console.log('tenant session minted');

  const adminPassword = process.env.DC_ADMIN_PASSWORD;
  rmSync(`${AUTH_DIR}/superuser.json`, { force: true });
  if (!adminPassword) {
    console.warn(
      '\n*** SUPERUSER RECIPES WILL BE SKIPPED: $DC_ADMIN_PASSWORD is not set. ' +
        'Export the bootstrap admin password to capture the admin chapter. ***\n',
    );
    return;
  }
  const email = process.env.DC_ADMIN_EMAIL ?? 'superuser@devicechain.local';
  const superuser = await signIn(BASE_URL, email, adminPassword);
  if (!superuser.identity.superuser) throw new Error(`${email} signed in but is not a superuser`);
  writeFileSync(`${AUTH_DIR}/superuser.json`, JSON.stringify(superuser), { mode: 0o600 });
  console.log('superuser session minted');
}

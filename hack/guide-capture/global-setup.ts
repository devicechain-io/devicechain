// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { BASE_URL } from './playwright.config';
import { readSimHandshake, signIn } from './session';
import { resolveAdminPassword } from './superuser';

export const AUTH_DIR = '.auth';
export const OUT_DIR = process.env.GUIDE_OUT ?? 'out';

// Two sessions, both minted over GraphQL (no sign-in form):
//   tenant     the sim's tenant admin (~/.devicechain/sims/<name>.json)
//   superuser  the bootstrap admin: $DC_ADMIN_PASSWORD, else the instance's own Secret on the
//              cluster, as dcctl reads it (superuser.ts). Unreadable => superuser recipes are
//              SKIPPED, loudly, or the run fails with GUIDE_REQUIRE_SUPERUSER=1. The password
//              stays in this process's memory: never printed, logged, or written anywhere.
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

  const sim = readSimHandshake(process.env.GUIDE_SIM ?? 'acme');
  const tenant = await signIn(BASE_URL, sim.email, sim.password, sim.tenant);
  writeFileSync(`${AUTH_DIR}/tenant.json`, JSON.stringify(tenant), { mode: 0o600 });
  console.log('tenant session minted');

  // The widgetlab tenant (dashboards chapter). Absent handshake = no sim created yet; the
  // dashboards recipes then fail loudly (capture.spec) instead of shooting the wrong tenant.
  const widgetsName = process.env.GUIDE_WIDGETS_SIM ?? 'widgets';
  rmSync(`${AUTH_DIR}/widgets.json`, { force: true });
  if (existsSync(join(homedir(), '.devicechain', 'sims', `${widgetsName}.json`))) {
    const w = readSimHandshake(widgetsName);
    writeFileSync(`${AUTH_DIR}/widgets.json`, JSON.stringify(await signIn(BASE_URL, w.email, w.password, w.tenant)), { mode: 0o600 });
    console.log('widgets session minted');
  }

  rmSync(`${AUTH_DIR}/superuser.json`, { force: true });
  const admin = await resolveAdminPassword();
  if (admin.password === null) {
    if (process.env.GUIDE_REQUIRE_SUPERUSER === '1') {
      throw new Error(`GUIDE_REQUIRE_SUPERUSER=1 but no superuser password: ${admin.reason}`);
    }
    console.warn(
      `\n*** SUPERUSER RECIPES WILL BE SKIPPED: ${admin.reason}. ` +
        'Export $DC_ADMIN_PASSWORD, or point GUIDE_INSTANCE / GUIDE_KUBE_CONTEXT at the instance. ***\n',
    );
    return;
  }
  console.log(`superuser password: from ${admin.source === 'env' ? '$DC_ADMIN_PASSWORD' : "the instance's Secret"}`);
  const email = process.env.DC_ADMIN_EMAIL ?? 'superuser@devicechain.local';
  const superuser = await signIn(BASE_URL, email, admin.password);
  if (!superuser.identity.superuser) throw new Error(`${email} signed in but is not a superuser`);
  writeFileSync(`${AUTH_DIR}/superuser.json`, JSON.stringify(superuser), { mode: 0o600 });
  console.log('superuser session minted');
}

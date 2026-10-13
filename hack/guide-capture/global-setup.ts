// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { mkdirSync, writeFileSync } from 'node:fs';
import { BASE_URL } from './playwright.config';
import { readSimHandshake, signIn } from './session';

export const AUTH_DIR = '.auth';

// S0 builds one session: the sim's tenant admin. The superuser session joins in S1.
export default async function globalSetup() {
  const sim = readSimHandshake(process.env.GUIDE_SIM ?? 'guide');
  const session = await signIn(BASE_URL, sim.email, sim.password, sim.tenant);
  mkdirSync(AUTH_DIR, { recursive: true });
  writeFileSync(`${AUTH_DIR}/tenant.json`, JSON.stringify(session), { mode: 0o600 });
  console.log('tenant session minted');
}

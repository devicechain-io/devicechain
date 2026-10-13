// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

// Token injection: sign in over GraphQL (Login, then SelectTenant) and hand the
// console the same localStorage shapes its AuthProvider persists, so no recipe
// depends on the sign-in UI. The sign-in screens get their own recipes.

export interface Session {
  /** localStorage `dc-auth` */
  auth: { accessToken: string; refreshToken: string };
  /** localStorage `dc-identity` */
  identity: {
    identityToken: string;
    superuser: boolean;
    memberships: { tenant: string; roles: string[]; ssoRequired: boolean }[];
    method: 'password';
  };
}

async function gql<T>(base: string, query: string, variables: object): Promise<T> {
  const res = await fetch(`${base}/api/user-management/graphql`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ query, variables }),
  });
  const body = (await res.json()) as { data?: T; errors?: { message: string }[] };
  if (!res.ok || body.errors?.length) {
    throw new Error(`user-management graphql: ${res.status} ${body.errors?.map((e) => e.message).join('; ') ?? ''}`);
  }
  return body.data as T;
}

export function readSimHandshake(name: string): { email: string; password: string; tenant: string } {
  const file = join(homedir(), '.devicechain', 'sims', `${name}.json`);
  const rec = JSON.parse(readFileSync(file, 'utf8')) as {
    simEmail: string;
    simPassword: string;
    tenant: string;
  };
  // The password is never logged; only the fact that the record was read.
  console.log(`read sim handshake ${name}.json (tenant ${rec.tenant})`);
  return { email: rec.simEmail, password: rec.simPassword, tenant: rec.tenant };
}

export async function signIn(base: string, email: string, password: string, tenant: string): Promise<Session> {
  const { login } = await gql<{
    login: { identityToken: string; superuser: boolean; memberships: Session['identity']['memberships'] };
  }>(
    base,
    `mutation Login($email: String!, $password: String!) {
       login(email: $email, password: $password) {
         identityToken superuser memberships { tenant roles ssoRequired }
       }
     }`,
    { email, password },
  );
  const { selectTenant } = await gql<{ selectTenant: Session['auth'] }>(
    base,
    `mutation SelectTenant($identityToken: String!, $tenant: String!) {
       selectTenant(identityToken: $identityToken, tenant: $tenant) { accessToken refreshToken }
     }`,
    { identityToken: login.identityToken, tenant },
  );
  return {
    auth: { accessToken: selectTenant.accessToken, refreshToken: selectTenant.refreshToken },
    identity: {
      identityToken: login.identityToken,
      superuser: login.superuser,
      memberships: login.memberships,
      method: 'password',
    },
  };
}

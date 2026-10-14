// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// node --test hack/guide-capture/superuser.test.ts   (no dependencies, no cluster)
//
// Pins the superuser password's resolution order (the same as dcctl sim's) and that the value
// never reaches the console or an error message.

import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import {
  decodeSecretValue,
  kubectlArgs,
  resolveAdminPassword,
  superuserSecretRef,
  type SecretReader,
  type SecretRef,
} from './superuser.ts';

const SECRET = 'Sup3r-s3cret-seed-Pa55word';

/** Runs fn with every console method captured; returns what fn returned and everything printed. */
async function quietly<T>(fn: () => Promise<T>): Promise<{ value: T; printed: string }> {
  const methods = ['log', 'info', 'warn', 'error', 'debug', 'trace'] as const;
  const saved = methods.map((m) => console[m]);
  let printed = '';
  for (const m of methods) console[m] = (...a: unknown[]) => void (printed += a.map(String).join(' ') + '\n');
  try {
    return { value: await fn(), printed };
  } finally {
    methods.forEach((m, i) => (console[m] = saved[i]));
  }
}

test('the Secret is the one dcctl writes: dci-<instance>/dci-<instance>-superuser, key password', () => {
  assert.deepEqual(superuserSecretRef('guide'), { namespace: 'dci-guide', name: 'dci-guide-superuser', key: 'password' });
  assert.deepEqual(kubectlArgs('kind-devicechain', superuserSecretRef('guide')), [
    '--context', 'kind-devicechain', '-n', 'dci-guide', 'get', 'secret', 'dci-guide-superuser', '-o', 'jsonpath={.data.password}',
  ]);
});

test('an explicit $DC_ADMIN_PASSWORD wins and the cluster is not read', async () => {
  let reads = 0;
  const read: SecretReader = async () => {
    reads++;
    return 'from-cluster';
  };
  const got = await resolveAdminPassword({ DC_ADMIN_PASSWORD: 'from-env' }, read);
  assert.deepEqual(got, { password: 'from-env', source: 'env' });
  assert.equal(reads, 0);
});

test('unset (or empty) $DC_ADMIN_PASSWORD falls back to the Secret, on bringup.sh\'s defaults', async () => {
  const seen: { ctx: string; ref: SecretRef }[] = [];
  const read: SecretReader = async (ctx, ref) => {
    seen.push({ ctx, ref });
    return SECRET;
  };
  for (const env of [{}, { DC_ADMIN_PASSWORD: '' }]) {
    const { value, printed } = await quietly(() => resolveAdminPassword(env, read));
    assert.deepEqual(value, { password: SECRET, source: 'cluster' });
    assert.ok(!printed.includes(SECRET), 'the password was printed');
  }
  assert.deepEqual(seen[0], { ctx: 'kind-devicechain', ref: superuserSecretRef('guide') });
});

test('GUIDE_INSTANCE and GUIDE_KUBE_CONTEXT pick the Secret and the cluster', async () => {
  let seen: [string, SecretRef] | null = null;
  await resolveAdminPassword({ GUIDE_INSTANCE: 'rc', GUIDE_KUBE_CONTEXT: 'gke_x' }, async (ctx, ref) => {
    seen = [ctx, ref];
    return SECRET;
  });
  assert.deepEqual(seen, ['gke_x', superuserSecretRef('rc')]);
});

test('an unreadable Secret is a reason, never a throw, and the reason names the Secret', async () => {
  const got = await resolveAdminPassword({}, async () => {
    throw new Error('kubectl could not read Secret dci-guide/dci-guide-superuser on context kind-devicechain: NotFound');
  });
  assert.equal(got.password, null);
  assert.ok(!got.password && /DC_ADMIN_PASSWORD/.test(got.reason) && /dci-guide-superuser/.test(got.reason));
});

test('decoding: the value comes back; a failure never carries it', () => {
  const ref = superuserSecretRef('guide');
  assert.equal(decodeSecretValue(Buffer.from(SECRET).toString('base64') + '\n', 'ctx', ref), SECRET);
  for (const bad of ['', '   ', `${SECRET}!!`]) {
    assert.throws(
      () => decodeSecretValue(bad, 'ctx', ref),
      (e: Error) => !e.message.includes(SECRET) && e.message.includes('dci-guide-superuser'),
    );
  }
  // Base64 of an empty string is empty, so "decodes to empty" is reached only by padding.
  assert.throws(() => decodeSecretValue('==', 'ctx', ref));
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The bootstrap superuser's password, for the superuser session global-setup mints.
//
// Resolved the way `dcctl sim` resolves it (backend/cli/cmd/sim.go resolveAdminPassword):
// $DC_ADMIN_PASSWORD, else the instance's own Secret on the cluster, which dcctl writes at
// bootstrap as dci-<instance>/dci-<instance>-superuser, key `password`
// (backend/cli/bootstrap/superuser.go). It is the SEED password: the superuser's password
// until somebody changes it.
//
// 🔴 The value is held in memory only. Nothing in this module prints, logs or writes it, and
// no error it raises carries it: a failure names the Secret and the command that reads it,
// never what was read. A unit test (superuser.test.ts) pins both the order and the silence.
//
// No runtime dependencies beyond Node itself, so CI runs its test before any `npm ci`.

import { execFile } from 'node:child_process';

export interface SecretRef {
  namespace: string;
  name: string;
  key: string;
}

/** Where dcctl keeps an instance's superuser seed password (superuserSecretRef in dcctl). */
export function superuserSecretRef(instance: string): SecretRef {
  return { namespace: `dci-${instance}`, name: `dci-${instance}-superuser`, key: 'password' };
}

/** The kubectl arguments that read one key of a Secret, still base64-encoded. */
export function kubectlArgs(kubeContext: string, ref: SecretRef): string[] {
  return ['--context', kubeContext, '-n', ref.namespace, 'get', 'secret', ref.name, '-o', `jsonpath={.data.${ref.key}}`];
}

/** Reads a Secret key and returns it DECODED. Rejects without the value in the message. */
export type SecretReader = (kubeContext: string, ref: SecretRef) => Promise<string>;

const BASE64 = /^[A-Za-z0-9+/]+={0,2}$/;

export const kubectlSecretReader: SecretReader = (kubeContext, ref) =>
  new Promise((resolve, reject) => {
    execFile('kubectl', kubectlArgs(kubeContext, ref), { windowsHide: true, maxBuffer: 64 * 1024 }, (err, stdout, stderr) => {
      // Build the message from kubectl's stderr only. Node's own error text restates the
      // command line, which is harmless, but stdout is never quoted anywhere.
      if (err) {
        const why = String(stderr).trim().split('\n')[0] || (err as NodeJS.ErrnoException).code || 'kubectl failed';
        reject(new Error(`kubectl could not read Secret ${ref.namespace}/${ref.name} on context ${kubeContext}: ${why}`));
        return;
      }
      try {
        resolve(decodeSecretValue(String(stdout), kubeContext, ref));
      } catch (e) {
        reject(e);
      }
    });
  });

/** kubectl's jsonpath output (base64) to the password. Throws, without the value, on anything else. */
export function decodeSecretValue(stdout: string, kubeContext: string, ref: SecretRef): string {
  const b64 = stdout.trim();
  if (!b64) throw new Error(`Secret ${ref.namespace}/${ref.name} on context ${kubeContext} has no "${ref.key}" key`);
  if (!BASE64.test(b64)) throw new Error(`Secret ${ref.namespace}/${ref.name} key "${ref.key}" is not base64 (${b64.length} characters)`);
  const value = Buffer.from(b64, 'base64').toString('utf8');
  if (!value) throw new Error(`Secret ${ref.namespace}/${ref.name} key "${ref.key}" decodes to an empty password`);
  return value;
}

export type AdminPassword =
  | { password: string; source: 'env' | 'cluster' }
  | { password: null; reason: string };

/**
 * $DC_ADMIN_PASSWORD wins; otherwise the instance's Secret, on the same instance and kube
 * context bringup.sh defaults to (GUIDE_INSTANCE=guide, GUIDE_KUBE_CONTEXT=kind-devicechain).
 * Never throws: an unreadable Secret is a `reason`, so the caller decides between skipping the
 * superuser recipes and failing the run (GUIDE_REQUIRE_SUPERUSER=1).
 */
export async function resolveAdminPassword(
  env: Record<string, string | undefined> = process.env,
  read: SecretReader = kubectlSecretReader,
): Promise<AdminPassword> {
  const fromEnv = env.DC_ADMIN_PASSWORD;
  if (fromEnv) return { password: fromEnv, source: 'env' };
  const instance = env.GUIDE_INSTANCE || 'guide';
  const kubeContext = env.GUIDE_KUBE_CONTEXT || 'kind-devicechain';
  const ref = superuserSecretRef(instance);
  try {
    return { password: await read(kubeContext, ref), source: 'cluster' };
  } catch (err) {
    return {
      password: null,
      reason: `$DC_ADMIN_PASSWORD is not set and ${err instanceof Error ? err.message : 'the Secret could not be read'}`,
    };
  }
}

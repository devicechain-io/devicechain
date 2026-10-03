// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Tests for scripts/launch.mjs, the one way frontend scripts start a process.
//
// Run before `npm ci` in CI (`npm run test:scripts`), so nothing here may need a
// dependency: every fixture is built in a temporary directory.

import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import {
  LaunchError,
  describeCommand,
  nodeCommand,
  npmCommand,
  resolveNpmCli,
  runSync,
  tscCommand,
} from './launch.mjs';
import { PackageError } from './packages.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));

// A realpath, because a resolved path is compared against it below and the platform
// temp dir is often reached through a symlink (/var -> /private/var on macOS) or an
// 8.3 short name (C:\Users\RUNNER~1 on the Windows runner).
function tempDir(t) {
  const dir = realpathSync.native(mkdtempSync(path.join(tmpdir(), 'launch-')));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

// ---------------------------------------------------------------------------
// The guard. Spawning a Node CLI by name works on Linux and macOS and fails only on
// Windows, so a new script that does it passes every Linux check there is. This is
// what fails instead: no script but launch.mjs may reach child_process or a .bin shim.
// ---------------------------------------------------------------------------
const CHILD_PROCESS = /\bchild_process\b/;
const BIN_SHIM = /['"]\.bin['"]|node_modules[\\/]\.bin/;

test('no frontend script launches a process except through launch.mjs', () => {
  const scanned = readdirSync(here).filter(
    (f) => f.endsWith('.mjs') && !f.endsWith('.test.mjs') && f !== 'launch.mjs',
  );
  // Reach control: the scan sees the scripts that launch things...
  for (const name of ['build-package.mjs', 'build-packages.mjs', 'publish-packages.mjs', 'verify-packages.mjs']) {
    assert.ok(scanned.includes(name), `the scan did not see ${name}: ${scanned.join(', ')}`);
  }
  // ...and the patterns match where a launch is known to exist.
  assert.match(readFileSync(path.join(here, 'launch.mjs'), 'utf8'), CHILD_PROCESS);
  assert.match("path.join(dir, 'node_modules', '.bin', 'tsc')", BIN_SHIM);
  assert.match("import { spawnSync } from 'child_process';", CHILD_PROCESS);

  const offenders = scanned
    .filter((f) => {
      const source = readFileSync(path.join(here, f), 'utf8');
      return CHILD_PROCESS.test(source) || BIN_SHIM.test(source);
    })
    .sort();
  assert.deepEqual(offenders, []);
});

// ---------------------------------------------------------------------------
// npm
// ---------------------------------------------------------------------------
test('resolveNpmCli prefers npm_execpath when it is npm', () => {
  const cli = '/x/lib/node_modules/npm/bin/npm-cli.js';
  assert.equal(resolveNpmCli({ env: { npm_execpath: cli }, platform: 'linux', exists: () => true }), cli);
});

test('resolveNpmCli ignores an npm_execpath that is another package manager', () => {
  const bundled = '/opt/node/lib/node_modules/npm/bin/npm-cli.js';
  assert.equal(
    resolveNpmCli({
      env: { npm_execpath: '/x/pnpm/bin/pnpm.cjs' },
      platform: 'linux',
      execPath: '/opt/node/bin/node',
      exists: (p) => p === bundled || p === '/x/pnpm/bin/pnpm.cjs',
    }),
    bundled,
  );
});

test('resolveNpmCli finds the npm bundled beside node.exe on Windows', () => {
  const bundled = 'C:\\node\\node_modules\\npm\\bin\\npm-cli.js';
  assert.equal(
    resolveNpmCli({ env: {}, platform: 'win32', execPath: 'C:\\node\\node.exe', exists: (p) => p === bundled }),
    bundled,
  );
});

test('resolveNpmCli accepts a Windows npm_execpath', () => {
  const cli = 'C:\\Program Files\\nodejs\\node_modules\\npm\\bin\\npm-cli.js';
  assert.equal(
    resolveNpmCli({ env: { npm_execpath: cli }, platform: 'win32', execPath: 'C:\\x\\node.exe', exists: (p) => p === cli }),
    cli,
  );
});

test('npmCommand runs the npm CLI under node', () => {
  assert.deepEqual(
    npmCommand(['run', 'x'], { env: { npm_execpath: '/n/npm-cli.js' }, exists: () => true, execPath: '/node' }),
    { file: '/node', args: ['/n/npm-cli.js', 'run', 'x'] },
  );
});

test('npmCommand with no npm CLI to be found: PATH on POSIX, a named error on Windows', () => {
  const none = { env: {}, exists: () => false, execPath: '/opt/node/bin/node' };
  assert.deepEqual(npmCommand(['ci'], { ...none, platform: 'linux' }), { file: 'npm', args: ['ci'] });
  assert.throws(
    () => npmCommand(['ci'], { ...none, platform: 'win32', execPath: 'C:\\node\\node.exe' }),
    (err) =>
      err instanceof LaunchError &&
      err instanceof PackageError &&
      err.message.includes('C:\\node\\node_modules\\npm\\bin\\npm-cli.js') &&
      err.message.includes('npm_execpath is unset'),
  );
});

test('npm launches for real through the resolved CLI', () => {
  const result = runSync(npmCommand(['--version']), { encoding: 'utf8' });
  assert.equal(result.status, 0);
  assert.match(result.stdout.trim(), /^\d+\.\d+\.\d+/);
});

// The branch release.yml takes: it runs `node scripts/...` directly, so npm_execpath
// is unset and the npm bundled with the runner's Node must be found. On a developer's
// box npm may live elsewhere (a distro package), so this is asserted on CI only.
test('with npm_execpath unset, the npm bundled with this Node is found', { skip: process.env.GITHUB_ACTIONS !== 'true' }, () => {
  const command = npmCommand(['--version'], { env: {} });
  assert.equal(command.file, process.execPath);
  assert.match(path.basename(command.args[0]), /^npm-cli\.js$/);
  const result = runSync(command, { encoding: 'utf8' });
  assert.equal(result.status, 0);
  assert.match(result.stdout.trim(), /^\d+\.\d+\.\d+/);
});

// ---------------------------------------------------------------------------
// tsc
// ---------------------------------------------------------------------------
function writeTypescript(root, manifest) {
  const dir = path.join(root, 'node_modules', 'typescript');
  mkdirSync(dir, { recursive: true });
  writeFileSync(path.join(dir, 'package.json'), JSON.stringify(manifest));
  return dir;
}

test('tscCommand runs the manifest bin.tsc found up the node_modules chain', (t) => {
  const root = tempDir(t);
  const ts = writeTypescript(root, {
    name: 'typescript',
    type: 'module',
    bin: { tsc: './bin/tsc' },
    exports: { './package.json': './package.json', '.': './lib/version.cjs' },
  });
  const pkgDir = path.join(root, 'packages', 'a');
  mkdirSync(pkgDir, { recursive: true });
  assert.deepEqual(tscCommand(['-p', 't.json'], { from: pkgDir, execPath: '/node' }), {
    file: '/node',
    args: [path.join(ts, 'bin', 'tsc'), '-p', 't.json'],
  });
});

test('tscCommand takes the nearest install, as Node would', (t) => {
  const root = tempDir(t);
  writeTypescript(root, { bin: { tsc: './bin/tsc' } });
  const pkgDir = path.join(root, 'packages', 'a');
  const nearest = writeTypescript(pkgDir, { bin: { tsc: './bin/near' } });
  assert.equal(tscCommand([], { from: pkgDir, execPath: '/node' }).args[0], path.join(nearest, 'bin', 'near'));
});

test('tscCommand fails by name when there is no TypeScript or no bin.tsc', (t) => {
  const root = tempDir(t);
  const pkgDir = path.join(root, 'a');
  mkdirSync(pkgDir, { recursive: true });
  // No node_modules anywhere above the temp dir is assumed only for the walk this
  // function does — it never consults NODE_PATH or the global folders.
  const hasTypescriptAbove = (() => {
    for (let d = root; ; d = path.dirname(d)) {
      try {
        readFileSync(path.join(d, 'node_modules', 'typescript', 'package.json'));
        return true;
      } catch {
        if (path.dirname(d) === d) return false;
      }
    }
  })();
  if (!hasTypescriptAbove) {
    assert.throws(
      () => tscCommand([], { from: pkgDir }),
      (err) => err instanceof LaunchError && err.message.includes('npm ci'),
    );
  }
  writeTypescript(root, { name: 'typescript', bin: './bin/tsc' });
  assert.throws(
    () => tscCommand([], { from: pkgDir }),
    (err) => err instanceof LaunchError && err.message.includes('bin.tsc'),
  );
});

// ---------------------------------------------------------------------------
// runSync and the rest
// ---------------------------------------------------------------------------
test('runSync throws, with the error code, when the process cannot be started', (t) => {
  const missing = path.join(tempDir(t), 'no-such-program');
  assert.throws(
    () => runSync({ file: missing, args: [] }),
    (err) => err instanceof LaunchError && err.message.includes('ENOENT') && err.message.includes(missing),
  );
});

test('runSync returns a started process with its exit status', () => {
  const result = runSync(nodeCommand('-e', ['process.exit(3)']));
  assert.equal(result.status, 3);
});

test('nodeCommand runs a script under the given node', () => {
  assert.deepEqual(nodeCommand('s.mjs', ['a'], { execPath: '/node' }), { file: '/node', args: ['s.mjs', 'a'] });
});

test('describeCommand names npm as npm, and anything else by its program name', () => {
  assert.equal(describeCommand({ file: '/node', args: ['/x/npm/bin/npm-cli.js', 'run', 'build'] }), 'npm run build');
  assert.equal(describeCommand({ file: 'C:\\node\\node.exe', args: ['C:\\n\\npm-cli.js', 'ci'] }), 'npm ci');
  assert.equal(describeCommand({ file: '/usr/bin/tar', args: ['-xzf', 'a.tgz'] }), 'tar -xzf a.tgz');
});
